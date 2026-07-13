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
	"reflect"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

// TestPredictPasses_DeterministicOrdering pins I-15: two identical-orbit
// satellites produce passes with the SAME AOS, and the workers append
// concurrently, so an AOS-only unstable sort could order them differently
// run-to-run. The total-order tiebreak (satellite, then station, then LOS) must
// make the output byte-identical across runs and put the alphabetically-earlier
// satellite first — enabling idempotent status convergence and the #188
// DeepEqual reconcile skip.
func TestPredictPasses_DeterministicOrdering(t *testing.T) {
	base := issOMM()
	satA, satB := base, base
	satA.ObjectName, satA.NoradCatID = "AAA-SAT", 90001
	satB.ObjectName, satB.NoradCatID = "BBB-SAT", 90002 // identical orbit → identical AOS
	omms := []sgp4.OMM{satB, satA}                      // input order deliberately B-before-A

	stations := []GroundStation{{Latitude: 25.0330, Longitude: 121.5654, Altitude: 15}}
	start := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)

	first, err := PredictPasses(context.Background(), omms, stations, 10.0, 24*time.Hour, nil, start)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(first) < 2 {
		t.Fatalf("expected multiple passes for two satellites, got %d", len(first))
	}

	// 1. Byte-identical across repeated runs (determinism).
	for i := range 8 {
		again, err := PredictPasses(context.Background(), omms, stations, 10.0, 24*time.Hour, nil, start)
		if err != nil {
			t.Fatalf("predict run %d: %v", i, err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("pass ordering is non-deterministic across runs (run %d differs)", i)
		}
	}

	// 2. Non-decreasing AOS, and equal-AOS ties resolve by satellite name (A<B).
	for i := 1; i < len(first); i++ {
		prev, cur := first[i-1], first[i]
		if cur.AOS.Before(prev.AOS) {
			t.Fatalf("passes not sorted by AOS at %d: %v before %v", i, prev.AOS, cur.AOS)
		}
		if cur.AOS.Equal(prev.AOS) && prev.GroundStation == cur.GroundStation && prev.Satellite > cur.Satellite {
			t.Errorf("equal-AOS tie not resolved by satellite name: %q before %q", prev.Satellite, cur.Satellite)
		}
	}
}
