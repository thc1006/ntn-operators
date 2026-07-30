/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ephemeris

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
)

// MaxPassWindows is the maximum number of pass windows stored in status
// to stay within etcd object size limits (~1.5 MB).
const MaxPassWindows = 500

// MaxSatelliteNameLen bounds the externally-sourced OMM ObjectName wherever it is persisted or
// surfaced (pass windows, propagated states, condition/error messages). OMM data is CR-controlled
// (the source URL is), so an unbounded name — copied into up to MaxPassWindows windows — could
// amplify a bounded input into a status object past the etcd limit. Measured in runes to match the
// CRD maxLength (code points). The canonical satellite identity is the NORAD ID; the name is a label.
const MaxSatelliteNameLen = 64

// BoundedSatelliteLabel truncates an external satellite name to MaxSatelliteNameLen runes. Rune-safe:
// it never splits a multi-byte rune (byte truncation would, yielding U+FFFD replacement chars on
// JSON marshal). Shared by pass windows, propagated states, and error/condition messages so one
// external name cannot inflate the status object.
func BoundedSatelliteLabel(name string) string {
	if len(name) <= MaxSatelliteNameLen { // byte length <= max ⇒ rune count <= max
		return name
	}
	if r := []rune(name); len(r) > MaxSatelliteNameLen {
		return string(r[:MaxSatelliteNameLen])
	}
	return name
}

// DefaultStepSeconds is the propagation step size for pass prediction.
const DefaultStepSeconds = 60

// GroundStation holds parsed ground station coordinates for pass prediction.
type GroundStation struct {
	Name      string
	Latitude  float64 // degrees
	Longitude float64 // degrees
	Altitude  float64 // meters
}

// PassResult holds a predicted pass window.
type PassResult struct {
	Satellite     string
	NoradID       int
	GroundStation string
	AOS           time.Time
	LOS           time.Time
	MaxElevation  float64 // degrees
}

// PredictPasses computes satellite pass windows for the given OMMs over ground stations.
// minElevation must be a finite value in [0, 90] degrees; otherwise PredictPasses returns an
// error (it does not silently degrade). It filters by minElevation, limits results to
// MaxPassWindows, and uses concurrent workers.
// The startTime parameter sets the prediction window start; pass time.Time{} to use time.Now().
//
// ctx cancellation is honoured BETWEEN work items (each item is one satellite × ground station over
// the whole horizon): a cancelled or timed-out call stops dispatching new items and returns
// ctx.Err(), but an item already running finishes, because the upstream sgp4.GeneratePasses sweep
// takes no context. The caller MUST bound the horizon (see the reconcile's 7-day clamp) so a single
// item stays small — at the 7-day clamp one item is ~20ms (BenchmarkPredictPasses_7d_*), so a
// cancelled call returns within roughly that bound. This is coarse-grained, between-item, not
// mid-sweep cancellation; finer granularity would need a context-aware GeneratePasses upstream.
func PredictPasses(
	ctx context.Context,
	omms []sgp4.OMM,
	stations []GroundStation,
	minElevation float64,
	horizon time.Duration,
	noradFilter []int,
	startTime time.Time,
) ([]PassResult, error) {
	// Enforce the [0,90] finite mask invariant at the exported entry point (defense in
	// depth): the operator path pre-validates via ParseElevation, but a direct caller could
	// otherwise pass NaN/Inf/out-of-range — and because every mask comparison against NaN is
	// false, an unchecked NaN would silently degrade to emitting 0°-horizon passes instead of
	// rejecting the bad mask (#223 review finding 2).
	if math.IsNaN(minElevation) || math.IsInf(minElevation, 0) || minElevation < 0 || minElevation > 90 {
		return nil, fmt.Errorf("minElevation must be a finite value in [0,90], got %v", minElevation)
	}
	if len(omms) == 0 || len(stations) == 0 {
		return nil, nil
	}

	// Apply NORAD ID filter if specified.
	filtered := FilterOMMs(omms, noradFilter)
	if len(filtered) == 0 {
		return nil, nil
	}

	start := startTime
	if start.IsZero() {
		start = time.Now()
	}
	stop := start.Add(horizon)

	// Build work items.
	type workItem struct {
		omm     sgp4.OMM
		station GroundStation
	}
	var work []workItem
	for _, omm := range filtered {
		for _, gs := range stations {
			work = append(work, workItem{omm: omm, station: gs})
		}
	}

	// Run pass predictions concurrently with a bounded worker pool.
	numWorkers := min(8, len(work))

	var mu sync.Mutex
	var allPasses []PassResult
	var firstErr error

	workCh := make(chan workItem, len(work))
	for _, w := range work {
		workCh <- w
	}
	close(workCh)

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Go(func() {
			for item := range workCh {
				if ctx.Err() != nil {
					return // cancelled/timed-out: stop pulling new work items
				}
				passes, err := predictSingleFn(item.omm, item.station, start, stop, minElevation)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				allPasses = append(allPasses, passes...)
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	// A cancelled/timed-out context takes precedence over partial results: the caller
	// (reconcile) will requeue, and partial pass windows would falsely converge status.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if firstErr != nil && len(allPasses) == 0 {
		return nil, firstErr
	}

	// Sort by the total order lessPass (I-15): AOS alone is not a total order and
	// the workers append concurrently, so an AOS-only unstable sort yields a
	// run-to-run-nondeterministic order, defeating idempotent status convergence
	// and any DeepEqual-based reconcile skip (#188).
	sort.Slice(allPasses, func(i, j int) bool { return lessPass(allPasses[i], allPasses[j]) })

	// Cap to MaxPassWindows (etcd object-size limit) while keeping each
	// satellite's EARLIEST windows first. A naive earliest-N-overall truncation
	// drops later satellites' current/next passes once enough passes exist, which
	// blinds NTNSlice.checkSatelliteAvailability into reporting a satellite
	// unavailable while it is actually overhead. capPerSatellite guarantees fair
	// per-satellite representation instead.
	allPasses = capPerSatellite(allPasses, MaxPassWindows)

	return allPasses, nil
}

// capPerSatellite bounds passes to limit while preserving each satellite's
// earliest windows first, so availability checks never lose a satellite's
// current/next pass to a global earliest-N cut. Input must be AOS-sorted; the
// result is returned AOS-sorted. When there are more satellites than the limit,
// it keeps the earliest `limit` passes (one per satellite, by AOS).
func capPerSatellite(passes []PassResult, limit int) []PassResult {
	if len(passes) <= limit {
		return passes
	}
	nSat := 0
	seen := make(map[string]bool)
	for i := range passes {
		if !seen[passes[i].Satellite] {
			seen[passes[i].Satellite] = true
			nSat++
		}
	}
	quota := max(limit/nSat, 1)
	kept := make([]PassResult, 0, limit)
	leftover := make([]PassResult, 0)
	count := make(map[string]int)
	for _, p := range passes {
		if len(kept) < limit && count[p.Satellite] < quota {
			kept = append(kept, p)
			count[p.Satellite]++
		} else {
			leftover = append(leftover, p)
		}
	}
	// Fill any remaining budget with the earliest leftover passes (AOS-sorted).
	for _, p := range leftover {
		if len(kept) >= limit {
			break
		}
		kept = append(kept, p)
	}
	sort.Slice(kept, func(i, j int) bool { return lessPass(kept[i], kept[j]) })
	return kept
}

// lessPass is the total order for pass windows: AOS, then satellite, then ground
// station, then LOS. AOS alone is not a total order (passes from different
// satellites/stations can share an AOS), so this makes PredictPasses output
// deterministic across runs despite concurrent worker appends (findings.md I-15).
func lessPass(a, b PassResult) bool {
	if !a.AOS.Equal(b.AOS) {
		return a.AOS.Before(b.AOS)
	}
	if a.Satellite != b.Satellite {
		return a.Satellite < b.Satellite
	}
	if a.GroundStation != b.GroundStation {
		return a.GroundStation < b.GroundStation
	}
	return a.LOS.Before(b.LOS)
}

// predictSingleFn is the per-work-item predictor the worker pool calls. It is a package var (not a
// direct call) so tests can substitute a counting/blocking implementation and prove that a cancelled
// context stops the pool from dispatching further work items.
var predictSingleFn = predictSingle

// predictSingle computes pass windows for a single satellite over a single ground station.
func predictSingle(
	omm sgp4.OMM,
	gs GroundStation,
	start, stop time.Time,
	minElevation float64,
) ([]PassResult, error) {
	tle, err := omm.ToTLE()
	if err != nil {
		return nil, fmt.Errorf("converting OMM to TLE for %s (NORAD %d): %w",
			BoundedSatelliteLabel(omm.ObjectName), omm.NoradCatID, err)
	}

	passes, err := tle.GeneratePasses(
		gs.Latitude, gs.Longitude, gs.Altitude,
		start, stop, DefaultStepSeconds,
	)
	if err != nil {
		// Propagation errors (e.g., decayed satellite) are non-fatal; skip this satellite.
		return nil, nil
	}

	observer := &sgp4.Location{Latitude: gs.Latitude, Longitude: gs.Longitude, Altitude: gs.Altitude}
	elevFn := func(t time.Time) (float64, error) { return elevationAt(tle, observer, t) }

	var results []PassResult
	for _, p := range passes {
		if p.MaxElevation < minElevation {
			continue
		}
		// The library defines AOS/LOS at the 0° geometric horizon, but a link is
		// only usable at/above minElevation. Trim each window to the interval where
		// elevation >= minElevation (I-22) so availability consumers (e.g. NTNSlice
		// failover) never treat a below-mask, unusable link as available.
		aos, los := trimPassToMask(elevFn, p, minElevation)
		// Conservative whole-second window (#201-P2-1): ceil AOS up and floor LOS down.
		// The bisection resolves the crossing to ~200ms, and metav1.Time serializes at
		// second granularity WITHOUT the fractional second — which would push the stored
		// AOS EARLIER than the computed acquisition (an over-claim). Rounding keeps the
		// persisted window within the window trimPassToMask returned. NOTE that is the
		// usable (>= minElevation) interval only WHEN the mask-boundary evaluation
		// succeeded; on the fail-open elevation-error path trimPassToMask leaves the wider
		// 0°-horizon window (see its doc + the deferred narrow fail-closed), and the
		// rounding only keeps the window within THAT. Round BEFORE the collapse check so a
		// window that shrinks below one second is dropped, not stored with AOS >= LOS.
		aos, los, valid := conservativePassWindow(aos, los)
		if !valid {
			// Grazing pass whose usable window collapsed to (near) zero once the mask and
			// whole-second rounding are applied — the satellite only brushes the mask, so
			// drop it. (The collapse decision is made inside conservativePassWindow so it is
			// locked by that helper's unit test, not just this branch.)
			continue
		}
		results = append(results, PassResult{
			Satellite:     BoundedSatelliteLabel(omm.ObjectName),
			NoradID:       omm.NoradCatID,
			GroundStation: gs.Name,
			AOS:           aos,
			LOS:           los,
			MaxElevation:  p.MaxElevation,
		})
	}
	return results, nil
}

const (
	// maskCrossingIterations bounds the mask-crossing bisection. A LEO half-pass
	// arc is at most a few hundred seconds, so this many halvings resolve the
	// crossing far below one second; the loop also stops early once the bracket
	// is under maskCrossingTol.
	maskCrossingIterations = 40
	// maskCrossingTol is the bracket width at which the bisection is considered
	// converged (sub-second, as libpredict-style AOS/LOS refinement targets).
	maskCrossingTol = 200 * time.Millisecond
)

// elevationAt returns the topocentric elevation (degrees) of the satellite as
// seen from observer at time t. It mirrors the private elevation helper inside
// akhenakh/sgp4's GeneratePasses, which is what lets us re-derive the
// minElevation crossings the library itself only computes at the 0° horizon.
func elevationAt(tle *sgp4.TLE, observer *sgp4.Location, t time.Time) (float64, error) {
	eci, err := tle.FindPositionAtTime(t)
	if err != nil {
		return 0, err
	}
	sv := &sgp4.StateVector{
		X: eci.Position.X, Y: eci.Position.Y, Z: eci.Position.Z,
		VX: eci.Velocity.X, VY: eci.Velocity.Y, VZ: eci.Velocity.Z,
	}
	obs, err := sv.GetLookAngle(observer, t)
	if err != nil {
		return 0, err
	}
	return obs.LookAngles.Elevation, nil
}

// trimPassToMask narrows a 0°-horizon pass to the sub-interval where elevation
// is at or above mask. Elevation over a single pass is unimodal (it rises to one
// peak at p.MaxElevationTime, then falls), so the mask is crossed at most once on
// each side of the peak. Each crossing is refined by bisection. Endpoints are
// only trimmed when the satellite actually starts/ends below the mask, so a pass
// clipped by the prediction window boundary (already above the mask at start or
// stop) is left untouched on that side. mask <= 0 is a no-op (the 0° horizon
// already satisfies it).
func trimPassToMask(elevFn func(time.Time) (float64, error), p sgp4.PassDetails, mask float64) (aos, los time.Time) {
	aos, los = p.AOS, p.LOS
	if mask <= 0 {
		return aos, los
	}
	peak := p.MaxElevationTime

	// AOS side: [aos, peak] straddles the rising crossing when the satellite
	// starts below the mask. (peak must be strictly after aos to be a valid
	// bracket; peak's elevation is >= mask because the pass survived the
	// MaxElevation filter.)
	if peak.After(aos) {
		if elAOS, err := elevFn(aos); err == nil && elAOS < mask {
			if t, err := refineMaskCrossing(elevFn, aos, peak, mask, true); err == nil {
				aos = t
			}
		}
	}
	// LOS side: [peak, los] straddles the falling crossing when the satellite
	// ends below the mask.
	if los.After(peak) {
		if elLOS, err := elevFn(los); err == nil && elLOS < mask {
			if t, err := refineMaskCrossing(elevFn, peak, los, mask, false); err == nil {
				los = t
			}
		}
	}
	return aos, los
}

// refineMaskCrossing bisects [lo, hi] for the instant elevation crosses mask,
// given the endpoints straddle it: for a rising crossing elevation(lo) < mask <=
// elevation(hi); for a falling crossing elevation(lo) >= mask > elevation(hi).
// It returns the boundary on the USABLE side (elevation >= mask) — hi for a
// rising crossing (AOS), lo for a falling one (LOS) — so the trimmed window is a
// subset of {t : elevation(t) >= mask} and never over-claims below-mask time.
func refineMaskCrossing(
	elevFn func(time.Time) (float64, error),
	lo, hi time.Time,
	mask float64,
	rising bool,
) (time.Time, error) {
	for i := 0; i < maskCrossingIterations && hi.Sub(lo) > maskCrossingTol; i++ {
		mid := lo.Add(hi.Sub(lo) / 2)
		el, err := elevFn(mid)
		if err != nil {
			return time.Time{}, err
		}
		above := el >= mask
		// Keep the bracket invariant (lo below/hi above for rising; lo above/hi
		// below for falling) so the returned usable-side boundary stays valid.
		if above == rising {
			hi = mid
		} else {
			lo = mid
		}
	}
	if rising {
		return hi, nil
	}
	return lo, nil
}

// FilterOMMs returns OMMs matching the NORAD ID filter. If filter is empty, returns all.
func FilterOMMs(omms []sgp4.OMM, noradFilter []int) []sgp4.OMM {
	if len(noradFilter) == 0 {
		return omms
	}
	allowed := make(map[int]bool, len(noradFilter))
	for _, id := range noradFilter {
		allowed[id] = true
	}
	var filtered []sgp4.OMM
	for _, omm := range omms {
		if allowed[omm.NoradCatID] {
			filtered = append(filtered, omm)
		}
	}
	return filtered
}

// ceilToSecond rounds t UP to the next whole second (a no-op when t is already on a
// second boundary). Applied to AOS so the acquisition time is never claimed earlier than
// the sub-second computed value, even after metav1.Time truncates on serialization.
func ceilToSecond(t time.Time) time.Time {
	trunc := t.Truncate(time.Second)
	if trunc.Before(t) {
		return trunc.Add(time.Second)
	}
	return trunc
}

// floorToSecond rounds t DOWN to the previous whole second. Applied to LOS so the loss
// time is never claimed later than the computed value.
func floorToSecond(t time.Time) time.Time {
	return t.Truncate(time.Second)
}

// conservativePassWindow rounds a pass window to whole seconds CONSERVATIVELY: AOS up
// (ceil) and LOS down (floor), so the persisted window never extends past the computed
// edges once metav1.Time drops the fractional second. It ALSO returns whether the rounded
// window is still valid (LOS strictly after AOS): a sub-second window straddling a second
// boundary collapses to zero length and the caller must drop it. Both the rounding
// DIRECTION and the collapse decision live here so a single unit test locks them — swapping
// a side, or weakening `After` to `!Before` (which would keep a zero-length window), fails
// that test (#201-P2-1; #223 review finding 1).
func conservativePassWindow(aos, los time.Time) (roundedAOS, roundedLOS time.Time, valid bool) {
	roundedAOS = ceilToSecond(aos)
	roundedLOS = floorToSecond(los)
	return roundedAOS, roundedLOS, roundedLOS.After(roundedAOS)
}

// ParseElevation parses a string elevation value (e.g., "10") to a float64 in [0, 90].
// 0 is the geometric horizon and 90 the zenith; a negative or >90 mask is physically
// meaningless (it would make every / no pass "usable"), so it is rejected. The bound is on
// the parsed float64 VALUE, not the exact decimal literal: the whole pass pipeline is
// float64, so a literal within ~half a ULP above 90 (e.g. "90.000000000000001", which
// rounds to exactly 90.0) is accepted as 90 — while "90.0000000000001" rounds above 90 and
// is rejected. This matches the CEL admission rule, which converts via the same
// strconv.ParseFloat; this is the runtime backstop for that bound.
func ParseElevation(s string) (float64, error) {
	if s == "" {
		return 10.0, nil // default
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid elevation %q: %w", s, err)
	}
	// strconv.ParseFloat accepts "NaN"/"Inf" without error, and NaN fails EVERY ordered
	// comparison so `v < 0 || v > 90` would let it through — reject non-finite explicitly
	// so this backstop truly guarantees a finite value in [0,90].
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 90 {
		return 0, fmt.Errorf("elevation %q must be a finite value in [0,90]", s)
	}
	return v, nil
}

// ParseGeoCoord parses a string coordinate (e.g., "25.0330") to float64.
func ParseGeoCoord(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
