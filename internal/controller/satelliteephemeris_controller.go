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

package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlrt "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/prometheus/client_golang/prometheus"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/netutil"
)

const minRefreshInterval = 2 * time.Hour

// maxRefreshInterval bounds how stale a fetched element set may become: GP data
// is re-fetched at least this often, so SGP4 propagation never runs on an element
// set older than roughly this age (LEO in-track error grows ~1–3 km/day from the
// element epoch). CelesTrak/SpaceTrack publish updates well within a day, so a
// daily floor on freshness costs nothing (findings.md I-17).
const maxRefreshInterval = 24 * time.Hour

// SatelliteEphemerisReconciler reconciles a SatelliteEphemeris object
type SatelliteEphemerisReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                events.EventRecorder
	MaxConcurrentReconciles int
	Fetcher                 ephemeris.GPFetcher          // CelesTrak fetcher
	SpaceTrackFetcher       *ephemeris.SpaceTrackFetcher // SpaceTrack fetcher (nil = disabled)

	// ommCache holds the last real GP-fetch result per SatelliteEphemeris so the
	// orbit can be re-propagated on a short cadence WITHOUT re-contacting the GP
	// source (rate-limit etiquette; #179). Keyed by client.ObjectKey, evicted on
	// CR deletion. In-memory only — a cold cache (e.g. after a restart) just forces
	// one fetch on the next reconcile.
	ommCache sync.Map
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris/finalizers,verbs=update
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=groundstationlifecycles,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile fetches GP data, computes pass predictions, and updates status.
func (r *SatelliteEphemerisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("reconciling")
	reconcileStart := time.Now()
	defer func() {
		log.V(1).Info("reconcile complete", "duration", time.Since(reconcileStart))
	}()

	// Step 1: Get the SatelliteEphemeris resource.
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	if err := r.Get(ctx, req.NamespacedName, eph); err != nil {
		if apierrors.IsNotFound(err) {
			// CR deleted — release its per-CR metric series so /metrics does not
			// accumulate dead series across create/delete churn, and drop its
			// cached OMMs so the in-memory cache does not leak (#179).
			ntnmetrics.GPSatelliteCount.DeletePartialMatch(prometheus.Labels{"namespace": req.Namespace, "ephemeris": req.Name})
			ntnmetrics.GPDeepSpaceRejectedCount.DeletePartialMatch(prometheus.Labels{"namespace": req.Namespace, "ephemeris": req.Name})
			r.ommCache.Delete(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 2: Clamp the refresh interval into [minRefreshInterval, maxRefreshInterval].
	// The floor honours the GP source's rate-limit etiquette; the ceiling bounds how
	// stale the element set may get — an unbounded refreshInterval (e.g. 720h) would
	// re-propagate a 30-day-old element set as if fresh, and LEO SGP4 error grows
	// ~1–3 km/day from the element epoch (findings.md I-17). metav1.Duration
	// serialises as a string, so both bounds are enforced here rather than via CEL.
	effectiveInterval := eph.Spec.Source.RefreshInterval.Duration
	switch {
	case effectiveInterval < minRefreshInterval:
		log.Info("RefreshInterval below minimum, clamping", "configured", effectiveInterval, "effective", minRefreshInterval)
		effectiveInterval = minRefreshInterval
		if r.Recorder != nil {
			r.Recorder.Eventf(eph, nil, "Warning", "RefreshIntervalClamped", "RefreshIntervalClamped",
				"refreshInterval %s is below minimum 2h; using 2h", eph.Spec.Source.RefreshInterval.Duration)
		}
	case effectiveInterval > maxRefreshInterval:
		log.Info("RefreshInterval above maximum, clamping", "configured", effectiveInterval, "effective", maxRefreshInterval)
		effectiveInterval = maxRefreshInterval
		if r.Recorder != nil {
			r.Recorder.Eventf(eph, nil, "Warning", "RefreshIntervalClamped", "RefreshIntervalClamped",
				"refreshInterval %s exceeds maximum 24h; using 24h to keep the element set fresh", eph.Spec.Source.RefreshInterval.Duration)
		}
	}

	// Step 3: Select fetcher and load credentials if needed.
	fetcher, fetcherErr := r.fetcherForSource(ctx, eph)
	if fetcherErr != nil {
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionFalse,
			Reason:             "FetcherSetupFailed",
			Message:            fetcherErr.Error(),
			ObservedGeneration: eph.Generation,
		})
		if err := r.Status().Update(ctx, eph); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Step 4b: Obtain OMM data. Within the GP-refresh window, re-propagate from the
	// cached OMMs of the last real fetch WITHOUT contacting the GP source — this is
	// what keeps the runtime-push epoch fresh on a short cadence without re-hitting
	// CelesTrak/SpaceTrack (their rate-limit etiquette is why the fetch is floored
	// to effectiveInterval). Only fetch when the window has elapsed or the cache is
	// cold (e.g. the first reconcile after a restart). (#179)
	now := time.Now()
	var result ephemeris.GPFetchResult
	reusedCache := false
	if c, ok := r.cachedOMMResult(req.NamespacedName); ok &&
		c.generation == eph.Generation && c.uid == eph.UID &&
		now.Sub(c.result.FetchedAt) < effectiveInterval {
		result = c.result
		reusedCache = true
		log.V(1).Info("re-propagating from cached OMMs; GP source not contacted",
			"cacheAge", now.Sub(c.result.FetchedAt).String(), "refreshInterval", effectiveInterval.String())
	} else {
		// Refuse a cleartext http:// source that resolves to a PUBLIC IP: an
		// on-path attacker could inject forged OMM JSON that is propagated into
		// SIB19 (findings.md I-21). In-cluster http mirrors and E2E mocks (which
		// resolve to private IPs, behind NetworkPolicy) stay allowed, and https
		// is always fine.
		if ip, insecure := r.publicHTTPSource(ctx, eph.Spec.Source.URL); insecure {
			return r.handleInsecureURL(ctx, eph, ip)
		}
		fetchStart := time.Now()
		var fetchErr error
		result, fetchErr = fetcher.Fetch(ctx, eph.Spec.Source.URL)
		fetchStatus := "success"
		if fetchErr != nil {
			fetchStatus = "error"
		}
		ntnmetrics.GPFetchDuration.With(prometheus.Labels{"source_type": eph.Spec.Source.Type, "status": fetchStatus}).Observe(time.Since(fetchStart).Seconds())

		// Step 5: Handle fetch errors.
		if fetchErr != nil {
			return r.handleFetchError(ctx, eph, fetchErr, effectiveInterval)
		}
		r.ommCache.Store(req.NamespacedName, cachedFetch{result: result, generation: eph.Generation, uid: eph.UID})
	}

	// Step 6: Update status with new data.
	//
	// Note: both paths (200 fresh fetch, 304 NotModified) populate
	// SatelliteCount and LastUpdated. The 304 path previously only
	// updated conditions, which silently corrupted status on the
	// edge case where a second CR reuses an already-cached URL
	// while the first CR's status populated the fetcher's in-memory
	// OMM cache (the GPFetchResult on 304 carries the cached OMMs
	// via fetcher.ommCache, so len(result.OMMs) == count is correct).
	// Fixed along with #105's E2E mock which reliably reproduces the
	// case: two sequential CRs with the same URL hit 304 on the
	// second reconcile.
	eph.Status.SatelliteCount = result.SatelliteCount
	eph.Status.LastUpdated = &metav1.Time{Time: result.FetchedAt}
	ntnmetrics.GPSatelliteCount.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(float64(result.SatelliteCount))

	switch {
	case reusedCache:
		log.V(1).Info("GP data re-propagated from cache (no fetch)", "satelliteCount", result.SatelliteCount)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionTrue,
			Reason:             "CachedRepropagation",
			Message:            fmt.Sprintf("Re-propagated %d satellites from cached OMMs; GP source not contacted within the refresh window", result.SatelliteCount),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             "CachedOMMs",
			Message:            "Using cached OMM data from previous fetch",
			ObservedGeneration: eph.Generation,
		})
	case result.NotModified:
		log.Info("GP data unchanged (304 Not Modified)", "satelliteCount", result.SatelliteCount)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionTrue,
			Reason:             "NotModified",
			Message:            fmt.Sprintf("GP data unchanged from %s (304)", eph.Spec.Source.Type),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             "CachedOMMs",
			Message:            "Using cached OMM data from previous fetch",
			ObservedGeneration: eph.Generation,
		})
	default:
		log.Info("Fetched GP data successfully", "satelliteCount", result.SatelliteCount)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionTrue,
			Reason:             "FetchSucceeded",
			Message:            fmt.Sprintf("Fetched %d satellites from %s", result.SatelliteCount, eph.Spec.Source.Type),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             "ParseSucceeded",
			Message:            "OMM JSON parsed successfully",
			ObservedGeneration: eph.Generation,
		})
	}

	// Step 6b: Orbit-regime guard (findings.md B-5). The bundled SGP4 propagator
	// (github.com/akhenakh/sgp4) is near-earth only — it hardcodes
	// use_deep_space_ = false — so an element set whose orbital period is >= 225
	// min (roughly MEO and up) would be propagated into a silently-wrong ECEF.
	// Reject those explicitly here so only the near-earth set reaches pass
	// prediction and the runtime-push propagation below. v1.0 is LEO-only; MEO/GEO
	// support is a v1.1 roadmap item. SplitByOrbitRegime allocates fresh slices, so
	// the cached OMMs stored above are not mutated by reassigning result.OMMs.
	nearEarth, deepSpace := ephemeris.SplitByOrbitRegime(result.OMMs)
	result.OMMs = nearEarth
	// Report only on the tracked (spec.satellites.noradIDs) deep-space sets. A
	// broad/mixed upstream feed (e.g. a CelesTrak group with LEO + MEO + GEO) must
	// not raise UnsupportedOrbitRegime for MEO/GEO birds the operator explicitly
	// excluded via noradIDs — that would be a false alarm on satellites they never
	// intended to propagate. An unset selector tracks the whole feed, so every
	// deep-space set is reported. Propagation is already deep-space-free (result.OMMs
	// above); propagateStates / predictPasses then apply this same noradIDs selector,
	// so only tracked near-earth sats are ever propagated.
	var trackedNorad []int
	if eph.Spec.Satellites != nil {
		trackedNorad = eph.Spec.Satellites.NoradIDs
	}
	trackedDeepSpace := ephemeris.FilterOMMs(deepSpace, trackedNorad)
	orbitRegimeEvent := r.reportOrbitRegime(ctx, eph, len(trackedDeepSpace), ephemeris.DeepSpaceSummary(trackedDeepSpace))
	ntnmetrics.GPDeepSpaceRejectedCount.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(float64(len(trackedDeepSpace)))

	// Step 7: Compute pass predictions if configured; clear stale data if disabled.
	if eph.Spec.PassPrediction != nil && len(eph.Spec.PassPrediction.GroundStations) > 0 {
		if err := r.predictPasses(ctx, eph, result); err != nil {
			log.Error(err, "pass prediction failed")
			eph.Status.NextPassWindows = nil // clear stale pass data
			meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionPassesPredicted,
				Status:             metav1.ConditionFalse,
				Reason:             "PredictionFailed",
				Message:            err.Error(),
				ObservedGeneration: eph.Generation,
			})
			if r.Recorder != nil {
				r.Recorder.Eventf(eph, nil, "Warning", "PredictionFailed", "PredictionFailed", "%s", err.Error())
			}
		}
	} else {
		// Pass prediction not configured; clear stale pass windows and condition.
		eph.Status.NextPassWindows = nil
		meta.RemoveStatusCondition(&eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
	}

	// Step 7b: Propagate tracked satellites to a NEAR-now epoch for the runtime
	// ephemeris push (#176). The epoch is only a small lead ahead of now — enough
	// to satisfy OCUDU's "epoch must be in the future" check and the consumer's
	// stale-epoch guard plus reconcile/delivery latency. OCUDU internally
	// re-propagates this state vector from the epoch to each SIB19 Tx time
	// (ntn_assistance_info_generator: propagate(SIB19_Tx - epoch)), so the epoch
	// must be the true reference time of the ECEF. Using now+refreshInterval (as
	// an earlier version did) made OCUDU back-propagate hours from a position that
	// was itself SGP4-propagated hours forward, compounding LEO error; a near-now
	// epoch keeps OCUDU's internal propagation short and forward.
	r.propagateStates(ctx, eph, result, now.Add(propagationEpochLead))

	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}

	// Emit the orbit-regime Warning ONLY after the status persisted, so a failed
	// update does not fire an event for a transition that rolled back (it would
	// re-fire on the next reconcile, defeating the transition gate). This also
	// suppresses a spurious event on a terminating object, whose Status().Update
	// fails NotFound above. (findings.md B-5; deep-review C-#4)
	if orbitRegimeEvent != "" && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Warning", "UnsupportedOrbitRegime", "OrbitRegimeGuard", "%s", orbitRegimeEvent)
	}

	// Only emit the "fetched" event on a real fetch, not on the short-cadence
	// cache re-propagations — otherwise they would flood the event stream (#179).
	if r.Recorder != nil && !reusedCache {
		r.Recorder.Eventf(eph, nil, "Normal", "GPDataFetched", "GPDataFetched", "Fetched %d satellites", result.SatelliteCount)
	}

	// Requeue on the short propagation cadence (not the GP-refresh interval) so the
	// runtime-push epoch stays fresh for off-cycle consumers; the fetch itself
	// stays gated to effectiveInterval by the cache check above (#179).
	return ctrl.Result{RequeueAfter: min(effectiveInterval, propagationRefreshInterval)}, nil
}

// maxPropagatedStates caps how many per-satellite propagated states are stored
// in status, to respect etcd object-size limits (~1.5 MB).
const maxPropagatedStates = 128

// propagationEpochLead is how far ahead of now the runtime-push epoch is stamped.
// It must exceed the consumer's epochSkewMargin (10s) with room for reconcile +
// WebSocket delivery latency, yet stay small so OCUDU's internal propagation from
// this epoch is short (a few minutes of SGP4/RK4 divergence is negligible for
// LEO). The push happens on the watch-triggered reconcile right after this
// propagation, so the epoch is fresh at push time.
const propagationEpochLead = 5 * time.Minute

// propagationRefreshInterval is how often the orbit is re-propagated (from cached
// OMMs, WITHOUT a GP fetch) to keep the runtime-push epoch fresh between GP
// refreshes (#179). It must be shorter than propagationEpochLead minus the
// consumer's epochSkewMargin (10s) + delivery latency, so an off-cycle consumer
// reconcile always finds a future epoch. The producer requeues at this cadence;
// each re-propagation updates status.propagatedStates, which fans out (via the
// NTNCellConfig watch) a fresh push to every referencing cell.
const propagationRefreshInterval = 3 * time.Minute

// cachedFetch is a per-CR OMM cache entry. generation+uid pin it to the exact
// spec revision of the exact object: a spec/source edit (generation bump) or a
// delete-recreate reusing the same name (uid change) invalidates the entry and
// forces a fresh fetch, so an edited source.URL/type/credentials takes effect
// immediately rather than after the refresh window (#179 regression guard).
type cachedFetch struct {
	result     ephemeris.GPFetchResult
	generation int64
	uid        types.UID
}

// cachedOMMResult returns the cached fetch for key, if any.
func (r *SatelliteEphemerisReconciler) cachedOMMResult(key client.ObjectKey) (cachedFetch, bool) {
	v, ok := r.ommCache.Load(key)
	if !ok {
		return cachedFetch{}, false
	}
	c, ok := v.(cachedFetch)
	return c, ok
}

// maxSatelliteNameLen bounds the externally-sourced OMM ObjectName stored per
// propagated state, so a malformed GP feed cannot bloat the status object.
const maxSatelliteNameLen = 64

// maxEpochAge bounds how old a fetched element set's OWN epoch may be before its
// SGP4 propagation is flagged unreliable (findings.md I-17). Independent of the
// refresh cadence — a source can serve elements whose epoch is already stale. ~7
// days keeps LEO in-track error to at most a few km.
const maxEpochAge = 7 * 24 * time.Hour

// parseOMMEpoch parses an OMM element-set epoch (ISO-8601, with or without a
// fractional second or trailing Z), returning ok=false when unparseable.
func parseOMMEpoch(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05.000000", "2006-01-02T15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// reportEphemerisEpochStale sets the EphemerisEpochStale condition: True when one
// or more propagated satellites' element-set epoch is older than maxEpochAge, so
// the pushed ECEF is derived from stale elements and drifting (I-17). Condition-
// only (no event) — the durable signal for alerting; the 3-minute re-propagation
// cadence would make an event noisy.
func (r *SatelliteEphemerisReconciler) reportEphemerisEpochStale(eph *ntnv1alpha1.SatelliteEphemeris, stale, total int) {
	c := metav1.Condition{
		Type:               ntnv1alpha1.ConditionEphemerisEpochStale,
		Status:             metav1.ConditionFalse,
		Reason:             "EpochsFresh",
		Message:            "All propagated element sets are within the freshness bound",
		ObservedGeneration: eph.Generation,
	}
	if stale > 0 {
		c.Status = metav1.ConditionTrue
		c.Reason = "EpochStale"
		c.Message = fmt.Sprintf("%d of %d tracked element set(s) have an epoch older than %s; the pushed ECEF is derived from stale elements and is drifting",
			stale, total, maxEpochAge)
	}
	meta.SetStatusCondition(&eph.Status.Conditions, c)
}

// propagateStates propagates the tracked satellites' orbits (SGP4) to the given
// future epoch and records the ECEF state vectors in status.propagatedStates for
// downstream runtime ephemeris push (#176). The set is filtered by
// spec.satellites.noradIDs and capped; un-propagatable satellites are skipped.
func (r *SatelliteEphemerisReconciler) propagateStates(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	result ephemeris.GPFetchResult,
	epoch time.Time,
) {
	var norad []int
	if eph.Spec.Satellites != nil {
		norad = eph.Spec.Satellites.NoradIDs
	}
	omms := ephemeris.FilterOMMs(result.OMMs, norad)
	if len(omms) > maxPropagatedStates {
		// No silent truncation: a satellite beyond the cap won't be pushable at
		// runtime (its NoradID selector would miss). Tell the operator to narrow
		// the set with spec.satellites.noradIDs.
		logf.FromContext(ctx).Info("propagated-state list capped; some satellites omitted from runtime-push status",
			"tracked", len(omms), "cap", maxPropagatedStates,
			"hint", "set spec.satellites.noradIDs to the satellites pushed at runtime")
	}
	epochMs := epoch.UnixMilli()

	// I-17: count tracked element sets whose OWN epoch is stale — a data property
	// independent of whether SGP4 propagation then succeeds.
	now := epoch.Add(-propagationEpochLead) // epoch is now+lead; recover ~now
	staleEpochs := 0
	for i := range omms {
		if t, ok := parseOMMEpoch(omms[i].EpochStr); ok && now.Sub(t) > maxEpochAge {
			staleEpochs++
		}
	}

	states := make([]ntnv1alpha1.PropagatedState, 0, len(omms))
	for i := range omms {
		if len(states) >= maxPropagatedStates {
			break
		}
		ecef, err := ephemeris.PropagateToECEF(omms[i], epoch)
		if err != nil {
			continue // skip satellites whose SGP4 propagation fails / goes out of range
		}
		// ObjectName comes from external GP data; bound it so a malformed/huge
		// name can't bloat the status object past the etcd size limit.
		name := omms[i].ObjectName
		if len(name) > maxSatelliteNameLen {
			name = name[:maxSatelliteNameLen]
		}
		states = append(states, ntnv1alpha1.PropagatedState{
			Satellite:   name,
			NoradID:     omms[i].NoradCatID,
			EpochUnixMs: epochMs,
			ECEF:        *ecef,
		})
	}
	eph.Status.PropagatedStates = states
	r.reportEphemerisEpochStale(eph, staleEpochs, len(omms))
}

// reportOrbitRegime records the UnsupportedOrbitRegime condition and RETURNS the
// Warning-event note the caller must emit only AFTER a successful Status().Update
// ("" = emit nothing). Element sets whose orbital period is >=
// ephemeris.DeepSpacePeriodMinutes need the deep-space (SDP4) model this operator
// does not implement, so they are rejected upstream rather than propagated into a
// wrong ECEF (findings.md B-5). rejectedCount and summary come from the caller's
// ephemeris.SplitByOrbitRegime / DeepSpaceSummary, keeping this method free of the
// sgp4 element type.
//
// The event is DEFERRED to post-persist (returned, not emitted here) so a failed
// Status().Update cannot fire an event for a transition that never persisted —
// otherwise the next reconcile, re-reading the old status, would re-fire it and
// defeat the transition gate. This mirrors handleFetchError's update-then-event
// ordering. The note is non-empty only on the first transition into the rejected
// state, so the short re-propagation cadence (#179) does not flood the stream.
func (r *SatelliteEphemerisReconciler) reportOrbitRegime(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	rejectedCount int,
	summary string,
) string {
	if rejectedCount == 0 {
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionUnsupportedOrbitRegime,
			Status:             metav1.ConditionFalse,
			Reason:             "AllNearEarth",
			Message:            "No deep-space element sets among the tracked satellites",
			ObservedGeneration: eph.Generation,
		})
		return ""
	}

	// Read the persisted state before mutating it, so the returned note is
	// non-empty once per entry into the rejected state rather than every reconcile.
	firstTransition := !meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionUnsupportedOrbitRegime,
		Status:             metav1.ConditionTrue,
		Reason:             "DeepSpaceElementsRejected",
		Message:            summary,
		ObservedGeneration: eph.Generation,
	})
	logf.FromContext(ctx).Info("rejected deep-space element sets; near-earth SGP4 cannot propagate them",
		"rejected", rejectedCount, "detail", summary)
	if firstTransition {
		return summary
	}
	return ""
}

// predictPasses resolves ground stations and computes pass windows.
func (r *SatelliteEphemerisReconciler) predictPasses(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	fetchResult ephemeris.GPFetchResult,
) error {
	log := logf.FromContext(ctx)
	pp := eph.Spec.PassPrediction

	// Resolve ground station coordinates.
	var stations []ephemeris.GroundStation
	for _, gsName := range pp.GroundStations {
		gs := &ntnv1alpha1.GroundStationLifecycle{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: eph.Namespace, Name: gsName}, gs); err != nil {
			return fmt.Errorf("ground station %q not found: %w", gsName, err)
		}
		lat, err := ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Lat)
		if err != nil {
			return fmt.Errorf("invalid latitude for %q: %w", gsName, err)
		}
		lon, err := ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Lon)
		if err != nil {
			return fmt.Errorf("invalid longitude for %q: %w", gsName, err)
		}
		alt := 0.0
		if gs.Spec.Deployment.Location.Alt != "" {
			alt, err = ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Alt)
			if err != nil {
				return fmt.Errorf("invalid altitude for %q: %w", gsName, err)
			}
		}
		stations = append(stations, ephemeris.GroundStation{
			Name:      gsName,
			Latitude:  lat,
			Longitude: lon,
			Altitude:  alt,
		})
	}

	// Parse minElevation.
	minEl, err := ephemeris.ParseElevation(pp.MinElevation)
	if err != nil {
		return fmt.Errorf("invalid minElevation: %w", err)
	}

	// Parse horizon.
	horizon := pp.Horizon.Duration
	if horizon == 0 {
		horizon = 24 * time.Hour
	}

	// Build NORAD filter from SatelliteSelector.
	var noradFilter []int
	if eph.Spec.Satellites != nil {
		noradFilter = eph.Spec.Satellites.NoradIDs
	}

	// Run pass prediction.
	passes, err := ephemeris.PredictPasses(fetchResult.OMMs, stations, minEl, horizon, noradFilter, fetchResult.FetchedAt)
	if err != nil {
		return fmt.Errorf("computing passes: %w", err)
	}

	// Convert to CRD PassWindow format.
	windows := make([]ntnv1alpha1.PassWindow, 0, len(passes))
	for _, p := range passes {
		windows = append(windows, ntnv1alpha1.PassWindow{
			Satellite:     p.Satellite,
			GroundStation: p.GroundStation,
			AOS:           metav1.Time{Time: p.AOS},
			LOS:           metav1.Time{Time: p.LOS},
			MaxElevation:  fmt.Sprintf("%.1f", p.MaxElevation),
		})
	}
	eph.Status.NextPassWindows = windows

	log.Info("Pass prediction completed", "passCount", len(windows), "stationCount", len(stations))

	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionPassesPredicted,
		Status:             metav1.ConditionTrue,
		Reason:             "PredictionSucceeded",
		Message:            fmt.Sprintf("Computed %d passes over %d ground stations", len(windows), len(stations)),
		ObservedGeneration: eph.Generation,
	})

	if r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Normal", "PassesPredicted", "PassesPredicted", "Computed %d passes over %d ground stations", len(windows), len(stations))
	}

	return nil
}

// publicHTTPSource reports whether raw is a cleartext http:// URL whose host
// resolves to a public IP, and if so returns that IP. Cleartext http to a
// private/in-cluster host (a NetworkPolicy-protected mirror or an E2E mock) is
// permitted; https is always fine. It fails OPEN when the host cannot be
// resolved (returns false): the fetch's own SSRF-safe client still blocks
// private-IP dials, and a public host that momentarily fails DNS is re-checked
// on the next reconcile — so a transient resolver blip never wedges a valid
// https source, and never silently downgrades a public one for long. A literal
// public IP (e.g. http://203.0.113.5) resolves to itself and is rejected;
// a literal private IP is allowed (findings.md I-21).
func (r *SatelliteEphemerisReconciler) publicHTTPSource(ctx context.Context, raw string) (string, bool) {
	u, err := neturl.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "http" {
		return "", false
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return "", false // cannot classify — fail open
	}
	for _, ip := range ips {
		if !netutil.IsPrivateIP(ip.IP) {
			return ip.IP.String(), true
		}
	}
	return "", false
}

// handleInsecureURL records the InsecureURL condition + a Warning event for a
// rejected cleartext-http public source and requeues without fetching (I-21).
func (r *SatelliteEphemerisReconciler) handleInsecureURL(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	ip string,
) (ctrl.Result, error) {
	msg := fmt.Sprintf(
		"cleartext http source resolves to public IP %s; use https to prevent forged OMM injection into SIB19",
		ip)
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataFetched,
		Status:             metav1.ConditionFalse,
		Reason:             "InsecureURL",
		Message:            msg,
		ObservedGeneration: eph.Generation,
	})
	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Warning", "InsecureURL", "InsecureURL", "%s", msg)
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// handleFetchError records the error as a Condition and Event, then requeues.
func (r *SatelliteEphemerisReconciler) handleFetchError(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	fetchErr error,
	effectiveInterval time.Duration,
) (ctrl.Result, error) {
	reason := "FetchFailed"
	requeueAfter := time.Minute

	if errors.Is(fetchErr, ephemeris.ErrRateLimited) {
		reason = "RateLimited"
		requeueAfter = effectiveInterval
	}

	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataFetched,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            fetchErr.Error(),
		ObservedGeneration: eph.Generation,
	})
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataParsed,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            "No data to parse due to fetch failure",
		ObservedGeneration: eph.Generation,
	})

	// Clear stale pass windows to prevent NTNSlice from using outdated data
	// for failover readiness decisions.
	eph.Status.NextPassWindows = nil
	meta.RemoveStatusCondition(&eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)

	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Warning", reason, reason, "%s", fetchErr.Error())
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// fetcherForSource returns the appropriate GPFetcher for the source type.
// For SpaceTrack, it also loads credentials from the referenced Secret.
func (r *SatelliteEphemerisReconciler) fetcherForSource(
	ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris,
) (ephemeris.GPFetcher, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("selecting fetcher", "sourceType", eph.Spec.Source.Type)
	switch eph.Spec.Source.Type {
	case "CelesTrak":
		if r.Fetcher == nil {
			return nil, fmt.Errorf("CelesTrak fetcher is not configured")
		}
		return r.Fetcher, nil

	case "SpaceTrack":
		if r.SpaceTrackFetcher == nil {
			return nil, fmt.Errorf("SpaceTrack fetcher is not configured")
		}
		// Load credentials from Secret.
		creds := eph.Spec.Source.Credentials
		if creds == nil {
			return nil, fmt.Errorf("SpaceTrack requires credentials; set spec.source.credentials")
		}
		secret := &corev1.Secret{}
		secretKey := types.NamespacedName{Namespace: eph.Namespace, Name: creds.Name}
		if err := r.Get(ctx, secretKey, secret); err != nil {
			return nil, fmt.Errorf("reading credentials Secret %q: %w", creds.Name, err)
		}
		key := creds.Key
		if key == "" {
			key = "password"
		}
		password, ok := secret.Data[key]
		if !ok {
			return nil, fmt.Errorf("secret %q does not contain key %q", creds.Name, key)
		}
		username, ok := secret.Data["username"]
		if !ok {
			return nil, fmt.Errorf("secret %q does not contain key %q", creds.Name, "username")
		}
		// Return a credentials-bound fetcher to prevent interleaving
		// when multiple CRs share the SpaceTrackFetcher instance.
		return &boundSpaceTrackFetcher{
			fetcher:  r.SpaceTrackFetcher,
			username: string(username),
			password: string(password),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported source type %q", eph.Spec.Source.Type)
	}
}

// boundSpaceTrackFetcher wraps SpaceTrackFetcher with request-scoped credentials.
// FetchWithCredentials serializes login+fetch under a mutex, preventing
// cookie/session interleaving across concurrent reconciles.
type boundSpaceTrackFetcher struct {
	fetcher  *ephemeris.SpaceTrackFetcher
	username string
	password string
}

func (b *boundSpaceTrackFetcher) Fetch(ctx context.Context, url string) (ephemeris.GPFetchResult, error) {
	return b.fetcher.FetchWithCredentials(ctx, url, b.username, b.password)
}

// SetupWithManager sets up the controller with the Manager.
// Watches GroundStationLifecycle changes to trigger pass re-prediction
// when ground stations are added, modified, or deleted.
func (r *SatelliteEphemerisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// reconcileTriggerPredicate (predicates.go) filters this controller's own
		// status writes (a fresh push epoch every reconcile) so they don't
		// re-enqueue it, while still delivering spec changes and deletions. The
		// GroundStationLifecycle Watches below deliberately have NO predicate,
		// since fan-in must react to status changes.
		For(&ntnv1alpha1.SatelliteEphemeris{}, builder.WithPredicates(reconcileTriggerPredicate())).
		Watches(&ntnv1alpha1.GroundStationLifecycle{},
			handler.EnqueueRequestsFromMapFunc(r.groundStationToEphemeris),
		).
		Named("satelliteephemeris").
		WithOptions(ctrlrt.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
		Complete(r)
}

// groundStationToEphemeris maps a GroundStationLifecycle change to all
// SatelliteEphemeris resources that reference it in passPrediction.groundStations.
func (r *SatelliteEphemerisReconciler) groundStationToEphemeris(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	gs, ok := obj.(*ntnv1alpha1.GroundStationLifecycle)
	if !ok {
		return nil
	}

	log := logf.FromContext(ctx)

	var ephList ntnv1alpha1.SatelliteEphemerisList
	if err := r.List(ctx, &ephList, client.InNamespace(gs.Namespace)); err != nil {
		log.Error(err, "failed to list SatelliteEphemeris for ground station mapper")
		return nil
	}

	var requests []reconcile.Request
	for _, eph := range ephList.Items {
		if eph.Spec.PassPrediction == nil {
			continue
		}
		if slices.Contains(eph.Spec.PassPrediction.GroundStations, gs.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      eph.Name,
					Namespace: eph.Namespace,
				},
			})
		}
	}
	return requests
}
