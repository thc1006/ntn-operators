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
	"math"
	"strings"
	"testing"

	"github.com/akhenakh/sgp4"
)

// ommWithMeanMotion builds a minimal OMM carrying only the fields the
// orbit-regime guard reads (mean motion, name, NORAD id).
func ommWithMeanMotion(name string, norad int, meanMotion float64) sgp4.OMM {
	return sgp4.OMM{ObjectName: name, NoradCatID: norad, MeanMotion: meanMotion}
}

func TestOrbitalPeriodMinutes(t *testing.T) {
	cases := []struct {
		name       string
		meanMotion float64
		want       float64 // 0 means "expect exactly 0"
	}{
		{"ISS LEO", 15.49554387, 92.928},
		{"OneWeb LEO", 12.85, 112.062},
		{"O3b MEO", 5.0, 288.0},
		{"GEO", 1.00272, 1436.100},
		{"boundary 6.4rev/day", 6.4, 225.0},
		{"zero mean motion (malformed)", 0, 0},
		{"negative mean motion (malformed)", -3, 0},
		{"NaN mean motion (malformed)", math.NaN(), 0},
		{"positive infinity (malformed)", math.Inf(1), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OrbitalPeriodMinutes(ommWithMeanMotion(tc.name, 1, tc.meanMotion))
			if math.Abs(got-tc.want) > 0.01 {
				t.Errorf("OrbitalPeriodMinutes(mm=%g) = %.3f, want %.3f", tc.meanMotion, got, tc.want)
			}
		})
	}
}

func TestIsDeepSpace(t *testing.T) {
	cases := []struct {
		name       string
		meanMotion float64
		want       bool
	}{
		{"ISS LEO", 15.49554387, false},
		{"OneWeb LEO", 12.85, false},
		{"just inside near-earth (221.5 min)", 6.5, false},
		{"exactly at 225 min boundary", 6.4, true}, // period >= 225 is deep space
		{"just past boundary (228.6 min)", 6.3, true},
		{"O3b MEO (288 min)", 5.0, true},
		{"GEO (1436 min)", 1.00272, true},
		{"malformed zero mean motion", 0, false}, // treated near-earth; propagation surfaces the error
		{"NaN mean motion", math.NaN(), false},   // never classified deep-space (no "NaN min" summary)
		{"positive infinity", math.Inf(1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDeepSpace(ommWithMeanMotion(tc.name, 1, tc.meanMotion)); got != tc.want {
				t.Errorf("IsDeepSpace(mm=%g, period=%.1f min) = %v, want %v",
					tc.meanMotion, OrbitalPeriodMinutes(ommWithMeanMotion(tc.name, 1, tc.meanMotion)), got, tc.want)
			}
		})
	}
}

// geoOMM (from propagator_test.go, MeanMotion 1.00272) is the canonical
// deep-space element set; oneWebOMM (12.85) is the canonical near-earth one.
func TestGEOOMMHelperIsDeepSpace(t *testing.T) {
	if !IsDeepSpace(geoOMM()) {
		t.Errorf("geoOMM() (GEO, %.0f min) must be classified deep-space", OrbitalPeriodMinutes(geoOMM()))
	}
	if IsDeepSpace(oneWebOMM()) {
		t.Errorf("oneWebOMM() (LEO, %.0f min) must be classified near-earth", OrbitalPeriodMinutes(oneWebOMM()))
	}
}

func TestSplitByOrbitRegime(t *testing.T) {
	leo1 := ommWithMeanMotion("LEO-1", 101, 15.0)
	meo := ommWithMeanMotion("MEO-O3b", 202, 5.0)
	leo2 := ommWithMeanMotion("LEO-2", 103, 13.0)
	geo := ommWithMeanMotion("GEO-1", 204, 1.0)
	input := []sgp4.OMM{leo1, meo, leo2, geo}

	// Snapshot the input to prove the call does not mutate the backing array
	// (which may be shared with the fetch cache).
	snapshot := append([]sgp4.OMM(nil), input...)

	nearEarth, deepSpace := SplitByOrbitRegime(input)

	if len(nearEarth) != 2 || nearEarth[0].NoradCatID != 101 || nearEarth[1].NoradCatID != 103 {
		t.Errorf("nearEarth partition wrong: got %+v", noradIDs(nearEarth))
	}
	if len(deepSpace) != 2 || deepSpace[0].NoradCatID != 202 || deepSpace[1].NoradCatID != 204 {
		t.Errorf("deepSpace partition wrong: got %+v", noradIDs(deepSpace))
	}
	for i := range snapshot {
		if input[i] != snapshot[i] {
			t.Errorf("SplitByOrbitRegime mutated input[%d]: %+v != %+v", i, input[i], snapshot[i])
		}
	}
}

func TestSplitByOrbitRegime_Empty(t *testing.T) {
	nearEarth, deepSpace := SplitByOrbitRegime(nil)
	if len(nearEarth) != 0 || len(deepSpace) != 0 {
		t.Errorf("empty input must yield empty partitions, got near=%d deep=%d", len(nearEarth), len(deepSpace))
	}
}

func TestSplitByOrbitRegime_AllNearEarth(t *testing.T) {
	input := []sgp4.OMM{oneWebOMM(), ommWithMeanMotion("LEO", 1, 14.0)}
	nearEarth, deepSpace := SplitByOrbitRegime(input)
	if len(nearEarth) != 2 || len(deepSpace) != 0 {
		t.Errorf("all-near-earth input misclassified: near=%d deep=%d", len(nearEarth), len(deepSpace))
	}
}

func TestDeepSpaceSummary(t *testing.T) {
	t.Run("empty is blank", func(t *testing.T) {
		if s := DeepSpaceSummary(nil); s != "" {
			t.Errorf("empty summary should be blank, got %q", s)
		}
	})

	t.Run("names satellites with periods", func(t *testing.T) {
		s := DeepSpaceSummary([]sgp4.OMM{geoOMM()})
		for _, want := range []string{"1 element set(s) rejected", "INTELSAT-10", "1436 min", ">= 225 min", "SDP4"} {
			if !strings.Contains(s, want) {
				t.Errorf("summary %q missing %q", s, want)
			}
		}
	})

	t.Run("bounds examples and appends ellipsis", func(t *testing.T) {
		many := make([]sgp4.OMM, 5)
		for i := range many {
			many[i] = ommWithMeanMotion("SAT", 300+i, 1.0)
		}
		s := DeepSpaceSummary(many)
		if !strings.Contains(s, "5 element set(s) rejected") {
			t.Errorf("summary should report the full count of 5, got %q", s)
		}
		if !strings.Contains(s, "...") {
			t.Errorf("summary should append an ellipsis when >3 rejected, got %q", s)
		}
		if n := strings.Count(s, "(1440 min)"); n != 3 {
			t.Errorf("summary should name exactly 3 examples, got %d in %q", n, s)
		}
	})

	t.Run("falls back to NORAD id when name is empty", func(t *testing.T) {
		s := DeepSpaceSummary([]sgp4.OMM{ommWithMeanMotion("", 28358, 1.0)})
		if !strings.Contains(s, "NORAD 28358") {
			t.Errorf("summary should fall back to NORAD id for an unnamed satellite, got %q", s)
		}
	})
}

// noradIDs is a test helper for readable partition-mismatch messages.
func noradIDs(omms []sgp4.OMM) []int {
	ids := make([]int, len(omms))
	for i := range omms {
		ids[i] = omms[i].NoradCatID
	}
	return ids
}
