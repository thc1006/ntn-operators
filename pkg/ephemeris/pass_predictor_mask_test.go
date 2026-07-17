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
	"errors"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

// TestRefineMaskCrossing pins the bisection root-finder in isolation on a
// synthetic, strictly-monotonic elevation ramp so the crossing time is known
// exactly — no orbital dependency. It checks both sub-second precision and that
// the returned boundary is on the usable (elevation >= mask) side.
func TestRefineMaskCrossing(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	span := 300 * time.Second
	const mask = 30.0

	// Rising ramp 0°→90° over 300s → crosses 30° at t=100s.
	rising := func(at time.Time) (float64, error) {
		return at.Sub(base).Seconds() / span.Seconds() * 90.0, nil
	}
	got, err := refineMaskCrossing(rising, base, base.Add(span), mask, true)
	if err != nil {
		t.Fatalf("rising: unexpected error: %v", err)
	}
	if d := got.Sub(base) - 100*time.Second; d < -maskCrossingTol || d > maskCrossingTol {
		t.Errorf("rising crossing = %v, want 100s (±%v)", got.Sub(base), maskCrossingTol)
	}
	if el, _ := rising(got); el < mask {
		t.Errorf("rising boundary elevation %.4f must be on the usable side (>= %.0f)", el, mask)
	}

	// Falling ramp 90°→0° over 300s → crosses 30° at t=200s.
	falling := func(at time.Time) (float64, error) {
		return 90.0 - at.Sub(base).Seconds()/span.Seconds()*90.0, nil
	}
	got, err = refineMaskCrossing(falling, base, base.Add(span), mask, false)
	if err != nil {
		t.Fatalf("falling: unexpected error: %v", err)
	}
	if d := got.Sub(base) - 200*time.Second; d < -maskCrossingTol || d > maskCrossingTol {
		t.Errorf("falling crossing = %v, want 200s (±%v)", got.Sub(base), maskCrossingTol)
	}
	if el, _ := falling(got); el < mask {
		t.Errorf("falling boundary elevation %.4f must be on the usable side (>= %.0f)", el, mask)
	}
}

// TestTrimPassToMask_Guards pins the non-orbital edge cases of the trimmer:
// mask <= 0 is a no-op, and a propagation error leaves that side untrimmed
// (degrade to the 0°-horizon window rather than dropping a real pass).
func TestTrimPassToMask_Guards(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	p := sgp4.PassDetails{
		AOS:              base,
		LOS:              base.Add(600 * time.Second),
		MaxElevationTime: base.Add(300 * time.Second),
		MaxElevation:     70,
	}

	// mask <= 0: the 0° horizon already satisfies it → return the window verbatim,
	// without ever calling the elevation function.
	called := false
	noop := func(time.Time) (float64, error) { called = true; return 0, nil }
	aos, los := trimPassToMask(noop, p, 0)
	if !aos.Equal(p.AOS) || !los.Equal(p.LOS) {
		t.Errorf("mask=0 must be a no-op, got [%v,%v]", aos, los)
	}
	if called {
		t.Error("mask=0 must not evaluate elevation")
	}

	// Propagation error on both endpoints → both sides left untrimmed.
	boom := func(time.Time) (float64, error) { return 0, errors.New("propagation failed") }
	aos, los = trimPassToMask(boom, p, 30)
	if !aos.Equal(p.AOS) || !los.Equal(p.LOS) {
		t.Errorf("elevation error must leave the window untrimmed, got [%v,%v]", aos, los)
	}
}

// TestTrimPassToMask_Synthetic drives the full trimmer with a synthetic unimodal
// elevation profile (a triangle peaking at the pass midpoint) so AOS/LOS mask
// crossings are analytically known, independent of any orbital propagation.
func TestTrimPassToMask_Synthetic(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	// Triangle: 0° at AOS, rises to 80° at the midpoint (300s), back to 0° at LOS
	// (600s). Slope = 80°/300s. mask=40° ⇒ up-crossing at 150s, down-crossing at 450s.
	peak := 80.0
	half := 300 * time.Second
	tri := func(at time.Time) (float64, error) {
		d := at.Sub(base)
		if d <= half {
			return d.Seconds() / half.Seconds() * peak, nil
		}
		return peak - (d-half).Seconds()/half.Seconds()*peak, nil
	}
	p := sgp4.PassDetails{
		AOS:              base,
		LOS:              base.Add(2 * half),
		MaxElevationTime: base.Add(half),
		MaxElevation:     peak,
	}
	const mask = 40.0
	aos, los := trimPassToMask(tri, p, mask)

	if d := aos.Sub(base) - 150*time.Second; d < -maskCrossingTol || d > maskCrossingTol {
		t.Errorf("AOS crossing = %v, want 150s (±%v)", aos.Sub(base), maskCrossingTol)
	}
	if d := los.Sub(base) - 450*time.Second; d < -maskCrossingTol || d > maskCrossingTol {
		t.Errorf("LOS crossing = %v, want 450s (±%v)", los.Sub(base), maskCrossingTol)
	}
	// Endpoints must sit on the usable (>= mask) side, and the window must be a
	// strict subset of the 0°-horizon span.
	if el, _ := tri(aos); el < mask {
		t.Errorf("trimmed AOS elevation %.4f below mask %.0f", el, mask)
	}
	if el, _ := tri(los); el < mask {
		t.Errorf("trimmed LOS elevation %.4f below mask %.0f", el, mask)
	}
	if !aos.After(p.AOS) || !los.Before(p.LOS) {
		t.Errorf("trimmed window [%v,%v] must be strictly inside [%v,%v]", aos, los, p.AOS, p.LOS)
	}
}

// TestPredictPasses_MaskConformance is the I-22 acceptance invariant on real
// orbital geometry: every reported pass window lies ENTIRELY at or above the
// mask. Before the fix, AOS/LOS were the 0° horizon crossings, so the window
// included unusable below-mask time near each end — this test fails on that code.
func TestPredictPasses_MaskConformance(t *testing.T) {
	const mask = 30.0
	omm := issOMM()
	gs := montrealStation
	start := time.Now().UTC()
	horizon := 72 * time.Hour

	masked, err := PredictPasses(context.Background(), []sgp4.OMM{omm}, []GroundStation{gs}, mask, horizon, nil, start)
	if err != nil {
		t.Fatalf("PredictPasses(mask=%.0f): %v", mask, err)
	}
	if len(masked) == 0 {
		t.Skipf("no ISS pass >= %.0f° over %s in %v — geometry-dependent", mask, gs.Name, horizon)
	}

	tle, err := omm.ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}
	observer := &sgp4.Location{Latitude: gs.Latitude, Longitude: gs.Longitude, Altitude: gs.Altitude}

	// tol absorbs the sub-second bisection residual (elevation can move ~1°/s at
	// low elevation for a LEO), not a below-mask window.
	const tol = 1.0
	for _, p := range masked {
		window := p.LOS.Sub(p.AOS)
		for _, frac := range []float64{0.0, 0.2, 0.5, 0.8, 1.0} {
			at := p.AOS.Add(time.Duration(frac * float64(window)))
			el, err := elevationAt(tle, observer, at)
			if err != nil {
				t.Fatalf("elevationAt: %v", err)
			}
			if el < mask-tol {
				t.Errorf("pass [%v,%v]: elevation %.3f° at frac %.1f is below the %.0f° mask — window over-claims unusable time",
					p.AOS.Format(time.RFC3339), p.LOS.Format(time.RFC3339), el, frac, mask)
			}
		}
	}
}

// TestPredictPasses_MaskWindowSubset proves the trim is not a no-op: a masked
// window is a subset of the 0°-horizon window for the same physical pass, and a
// pass whose peak comfortably clears the mask is strictly shorter. The two runs
// share one OMM + one start time, so geometry is identical and passes pair by
// their (preserved, unmodified) MaxElevation.
func TestPredictPasses_MaskWindowSubset(t *testing.T) {
	const mask = 25.0
	omm := issOMM()
	gs := montrealStation
	start := time.Now().UTC()
	horizon := 72 * time.Hour

	full, err := PredictPasses(context.Background(), []sgp4.OMM{omm}, []GroundStation{gs}, 0, horizon, nil, start)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	masked, err := PredictPasses(context.Background(), []sgp4.OMM{omm}, []GroundStation{gs}, mask, horizon, nil, start)
	if err != nil {
		t.Fatalf("masked: %v", err)
	}
	if len(masked) == 0 {
		t.Skipf("no ISS pass >= %.0f° over %s in %v — geometry-dependent", mask, gs.Name, horizon)
	}
	if len(masked) > len(full) {
		t.Fatalf("masked passes (%d) cannot exceed 0°-horizon passes (%d)", len(masked), len(full))
	}

	byPeak := make(map[float64]PassResult, len(full))
	for _, f := range full {
		byPeak[f.MaxElevation] = f
	}

	trimmed := 0
	for _, m := range masked {
		f, ok := byPeak[m.MaxElevation]
		if !ok {
			t.Fatalf("masked pass (peak %.6f°) has no matching 0°-horizon pass", m.MaxElevation)
		}
		if m.AOS.Before(f.AOS) || m.LOS.After(f.LOS) {
			t.Errorf("masked window [%v,%v] is not inside the 0° window [%v,%v]",
				m.AOS, m.LOS, f.AOS, f.LOS)
		}
		if m.LOS.Sub(m.AOS) < f.LOS.Sub(f.AOS) {
			trimmed++
		}
		// A pass peaking well above the mask must be shortened, unless its 0° window
		// was clipped by the horizon boundary (started at the window start or ended
		// at the window stop) so there was no sub-mask tail to trim on that side.
		clippedAtStart := f.AOS.Equal(start)
		clippedAtStop := f.LOS.Equal(start.Add(horizon))
		if m.MaxElevation > mask+15 && !clippedAtStart && !clippedAtStop &&
			m.LOS.Sub(m.AOS) >= f.LOS.Sub(f.AOS) {
			t.Errorf("high pass (peak %.1f°) not shortened by the %.0f° mask: masked %v vs 0° %v",
				m.MaxElevation, mask, m.LOS.Sub(m.AOS), f.LOS.Sub(f.AOS))
		}
	}
	if trimmed == 0 {
		t.Skip("no pass was trimmed (all clipped at the horizon boundary?) — geometry-dependent")
	}
}
