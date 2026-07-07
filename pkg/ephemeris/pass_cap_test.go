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
	"sort"
	"strconv"
	"testing"
	"time"
)

// A naive earliest-N-overall cap drops a satellite whose windows sit past the
// Nth pass, blinding availability checks. capPerSatellite must keep every
// satellite's earliest window.
func TestCapPerSatellite_KeepsEverySatellitesEarliestWindow(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	passes := make([]PassResult, 0, MaxPassWindows+101)
	// SAT-BUSY alone produces more than MaxPassWindows early passes.
	for i := range MaxPassWindows + 100 {
		passes = append(passes, PassResult{
			Satellite: "SAT-BUSY",
			AOS:       base.Add(time.Duration(i) * time.Minute),
			LOS:       base.Add(time.Duration(i)*time.Minute + 5*time.Minute),
		})
	}
	// SAT-LATE has a single window that, in global AOS order, lands past the 500th
	// SAT-BUSY pass — exactly the window the old truncation silently dropped.
	passes = append(passes, PassResult{
		Satellite: "SAT-LATE",
		AOS:       base.Add(time.Duration(MaxPassWindows+50) * time.Minute),
		LOS:       base.Add(time.Duration(MaxPassWindows+50)*time.Minute + 5*time.Minute),
	})
	sort.Slice(passes, func(i, j int) bool { return passes[i].AOS.Before(passes[j].AOS) })

	capped := capPerSatellite(passes, MaxPassWindows)

	if len(capped) > MaxPassWindows {
		t.Fatalf("capped exceeds limit: %d > %d", len(capped), MaxPassWindows)
	}
	found := false
	for _, p := range capped {
		if p.Satellite == "SAT-LATE" {
			found = true
			break
		}
	}
	if !found {
		t.Error("capPerSatellite dropped SAT-LATE's only window — availability would be blind")
	}
	// Result must stay AOS-sorted.
	for i := 1; i < len(capped); i++ {
		if capped[i].AOS.Before(capped[i-1].AOS) {
			t.Fatalf("capped not AOS-sorted at %d", i)
		}
	}
}

func TestCapPerSatellite_UnderLimitUnchanged(t *testing.T) {
	passes := []PassResult{
		{Satellite: "A", AOS: time.Unix(1, 0)},
		{Satellite: "B", AOS: time.Unix(2, 0)},
	}
	if got := capPerSatellite(passes, MaxPassWindows); len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

// With more satellites than the limit, keep the earliest `limit` passes.
func TestCapPerSatellite_MoreSatellitesThanLimit(t *testing.T) {
	base := time.Unix(0, 0)
	passes := make([]PassResult, 0, MaxPassWindows+50)
	for i := range MaxPassWindows + 50 {
		passes = append(passes, PassResult{
			Satellite: "SAT-" + strconv.Itoa(i),
			AOS:       base.Add(time.Duration(i) * time.Second),
		})
	}
	got := capPerSatellite(passes, MaxPassWindows)
	if len(got) != MaxPassWindows {
		t.Fatalf("expected exactly %d, got %d", MaxPassWindows, len(got))
	}
	if !got[0].AOS.Equal(base) {
		t.Error("earliest pass not kept")
	}
}

// A small explicit limit exercises the per-satellite quota path (and gives the
// limit parameter a value other than MaxPassWindows).
func TestCapPerSatellite_SmallLimitQuota(t *testing.T) {
	base := time.Unix(0, 0)
	passes := make([]PassResult, 0, 12)
	// 3 satellites × 4 passes; cap at 6 → quota = 6/3 = 2 per satellite.
	for _, sat := range []string{"A", "B", "C"} {
		for i := range 4 {
			passes = append(passes, PassResult{Satellite: sat, AOS: base.Add(time.Duration(i) * time.Second)})
		}
	}
	sort.Slice(passes, func(i, j int) bool { return passes[i].AOS.Before(passes[j].AOS) })

	got := capPerSatellite(passes, 6)
	if len(got) != 6 {
		t.Fatalf("expected 6 (cap), got %d", len(got))
	}
	perSat := map[string]int{}
	for _, p := range got {
		perSat[p.Satellite]++
	}
	if len(perSat) != 3 {
		t.Errorf("expected all 3 satellites represented, got %d", len(perSat))
	}
	for sat, n := range perSat {
		if n > 2 {
			t.Errorf("satellite %s exceeded per-satellite quota: %d > 2", sat, n)
		}
	}
}
