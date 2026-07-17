/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ephemeris

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

// TestPredictPasses_RejectsInvalidMask pins the exported-API [0,90]-finite invariant
// (#223 review finding 2): a direct caller passing NaN/Inf/out-of-range must get an error,
// not a silent degradation to 0°-horizon passes (NaN mask comparisons are all false).
func TestPredictPasses_RejectsInvalidMask(t *testing.T) {
	for _, mask := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 90.1, 1000} {
		_, err := PredictPasses(context.Background(),
			[]sgp4.OMM{issOMM()}, []GroundStation{montrealStation}, mask, 24*time.Hour, nil, time.Time{})
		if err == nil {
			t.Errorf("PredictPasses(minElevation=%v) must error (invalid mask), got nil", mask)
		}
	}
	// A valid mask still works.
	_, err := PredictPasses(context.Background(),
		[]sgp4.OMM{issOMM()}, []GroundStation{montrealStation}, 10, 24*time.Hour, nil, time.Time{})
	if err != nil {
		t.Fatalf("a valid mask must not error: %v", err)
	}
}

// TestCeilFloorToSecond pins the conservative whole-second rounding helpers (#201-P2-1):
// ceil never moves earlier than t, floor never later, both are no-ops on a whole second.
// Mutation: swap the ceil/floor direction and this fails.
func TestCeilFloorToSecond(t *testing.T) {
	base := time.Date(2026, 7, 13, 10, 30, 15, 0, time.UTC)

	// Already on a second boundary → both are no-ops.
	if got := ceilToSecond(base); !got.Equal(base) {
		t.Errorf("ceilToSecond(whole) = %v, want %v", got, base)
	}
	if got := floorToSecond(base); !got.Equal(base) {
		t.Errorf("floorToSecond(whole) = %v, want %v", got, base)
	}

	// Sub-second value → ceil rounds UP to next second, floor DOWN to this second.
	sub := base.Add(200 * time.Millisecond)
	if got := ceilToSecond(sub); !got.Equal(base.Add(time.Second)) {
		t.Errorf("ceilToSecond(+200ms) = %v, want %v", got, base.Add(time.Second))
	}
	if got := floorToSecond(sub); !got.Equal(base) {
		t.Errorf("floorToSecond(+200ms) = %v, want %v", got, base)
	}

	// Direction invariants: ceil >= t, floor <= t.
	if ceilToSecond(sub).Before(sub) {
		t.Error("ceilToSecond must never move earlier than t (would over-claim AOS)")
	}
	if floorToSecond(sub).After(sub) {
		t.Error("floorToSecond must never move later than t (would over-claim LOS)")
	}
}

// TestConservativePassWindow_Direction pins the ROUNDING DIRECTION at the production call
// site (#201-P2-1, review finding 5): predictSingle rounds via conservativePassWindow, so
// swapping/removing either side here fails this test — the earlier helper + whole-second
// tests alone would still pass under a floor-AOS/ceil-LOS swap. AOS must round UP, LOS DOWN.
func TestConservativePassWindow_Direction(t *testing.T) {
	base := time.Date(2026, 7, 13, 10, 30, 15, 0, time.UTC)
	rawAOS := base.Add(200 * time.Millisecond) // 10:30:15.2
	rawLOS := base.Add(300*time.Second + 800*time.Millisecond)

	aos, los, valid := conservativePassWindow(rawAOS, rawLOS)

	if !valid {
		t.Fatal("a multi-second window must be reported valid")
	}
	// AOS rounds UP (never earlier than the computed acquisition).
	if want := base.Add(time.Second); !aos.Equal(want) {
		t.Fatalf("AOS must ceil up to %v, got %v (direction reversed?)", want, aos)
	}
	if aos.Before(rawAOS) {
		t.Fatalf("rounded AOS %v is earlier than the computed AOS %v — over-claims acquisition", aos, rawAOS)
	}
	// LOS rounds DOWN (never later than the computed loss).
	if want := base.Add(300 * time.Second); !los.Equal(want) {
		t.Fatalf("LOS must floor down to %v, got %v (direction reversed?)", want, los)
	}
	if los.After(rawLOS) {
		t.Fatalf("rounded LOS %v is later than the computed LOS %v — over-claims loss", los, rawLOS)
	}
}

// TestConservativePassWindow_CollapsesSubSecondWindow deterministically pins the collapse
// decision (#223 review finding 1): a sub-second window straddling a second boundary rounds
// to zero length (ceil AOS == floor LOS) and conservativePassWindow reports it INVALID, which
// predictSingle drops via `if !valid`. The decision lives in the helper, so a mutation that
// weakens `After` to `!Before` (keeping a zero-length window) fails here — not left to a
// real-orbit test hitting a grazing pass by chance.
func TestConservativePassWindow_CollapsesSubSecondWindow(t *testing.T) {
	base := time.Date(2026, 7, 13, 10, 30, 15, 0, time.UTC)
	// [15.800, 16.200] — 400 ms long, but ceil(AOS)=:16 and floor(LOS)=:16.
	aos, los, valid := conservativePassWindow(base.Add(800*time.Millisecond), base.Add(1200*time.Millisecond))
	if valid {
		t.Fatalf("a sub-second window straddling a second boundary must be reported INVALID (so predictSingle "+
			"drops it), got AOS=%v LOS=%v valid=true", aos.UTC(), los.UTC())
	}
	if !aos.Equal(base.Add(time.Second)) || !los.Equal(base.Add(time.Second)) {
		t.Fatalf("both edges must round to :16, got AOS=%v LOS=%v", aos.UTC(), los.UTC())
	}
}

// TestPredictPasses_WholeSecondWindow_P2_1 pins the end-to-end conservative window: every
// AOS/LOS emitted by PredictPasses is on a whole-second boundary, so metav1.Time's
// second-granularity serialization can't shift the window. mask=10 exercises the
// sub-second bisection (refineMaskCrossing, ~200ms tol) that the rounding then cleans up.
// Mutation: remove the ceil/floor in predictSingle → AOS/LOS carry sub-second nanos.
func TestPredictPasses_WholeSecondWindow_P2_1(t *testing.T) {
	passes, err := PredictPasses(context.Background(),
		[]sgp4.OMM{issOMM()},
		[]GroundStation{montrealStation, taipeiStation},
		10, // above the horizon, so the mask trim + bisection actually run
		48*time.Hour,
		nil,
		time.Time{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(passes) == 0 {
		t.Fatal("expected ISS passes over Montreal/Taipei in 48h at a 10° mask")
	}
	for _, p := range passes {
		if p.AOS.Nanosecond() != 0 {
			t.Errorf("AOS %v must be ceil'd to a whole second (#201-P2-1)", p.AOS.UTC())
		}
		if p.LOS.Nanosecond() != 0 {
			t.Errorf("LOS %v must be floor'd to a whole second (#201-P2-1)", p.LOS.UTC())
		}
		if !p.LOS.After(p.AOS) {
			t.Errorf("window collapsed after rounding but was still emitted: AOS=%v LOS=%v", p.AOS, p.LOS)
		}
	}
}
