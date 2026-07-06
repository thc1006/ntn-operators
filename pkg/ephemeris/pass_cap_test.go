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
	var passes []PassResult
	// SAT-BUSY alone produces more than MaxPassWindows early passes.
	for i := 0; i < MaxPassWindows+100; i++ {
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
	var passes []PassResult
	for i := 0; i < MaxPassWindows+50; i++ {
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
