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
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

// TestSixDigitNORAD is the regression gate for CelesTrak's 100000+ catalog numbers (the 5-digit
// space was exhausted in 2026-07). It proves a 6-digit NORAD flows through the whole ephemeris
// path as an int, with no hidden 5-char TLE-string truncation: OMM->TLE keeps the full catalog,
// SGP4 propagation succeeds, the NORAD filter matches exactly, and pass windows carry the full ID.
func TestSixDigitNORAD(t *testing.T) {
	const bigNORAD = 100000
	omm := issOMM()
	omm.NoradCatID = bigNORAD
	omm.ObjectName = "SIX-DIGIT-SAT"

	epoch, err := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)
	if err != nil {
		t.Fatalf("parse epoch: %v", err)
	}

	// 1. ToTLE keeps the 6-digit catalog as an int; a 69-char TLE round-trip would truncate cols 3-7.
	tle, err := omm.ToTLE()
	if err != nil {
		t.Fatalf("ToTLE for 6-digit NORAD: %v", err)
	}
	if tle.SatelliteNumber != bigNORAD {
		t.Fatalf("ToTLE truncated the catalog number: got %d, want %d", tle.SatelliteNumber, bigNORAD)
	}

	// 2. Propagation succeeds (SGP4 uses the orbital elements, not the catalog number).
	if _, err := PropagateToECEF(omm, epoch.Add(10*time.Minute)); err != nil {
		t.Fatalf("PropagateToECEF for 6-digit NORAD: %v", err)
	}

	// 3. The NORAD filter matches the full 6-digit ID and rejects a truncated 5-digit near-miss.
	if got := FilterOMMs([]sgp4.OMM{omm}, []int{bigNORAD}); len(got) != 1 || got[0].NoradCatID != bigNORAD {
		t.Fatalf("FilterOMMs did not match the 6-digit NORAD: %+v", got)
	}
	if got := FilterOMMs([]sgp4.OMM{omm}, []int{10000}); len(got) != 0 {
		t.Fatalf("FilterOMMs matched a truncated 5-digit id: got %d results", len(got))
	}

	// 4. Pass prediction reports the full 6-digit NORAD on every produced window.
	passes, err := PredictPasses(context.Background(), []sgp4.OMM{omm}, []GroundStation{montrealStation},
		0, 24*time.Hour, []int{bigNORAD}, epoch)
	if err != nil {
		t.Fatalf("PredictPasses for 6-digit NORAD: %v", err)
	}
	if len(passes) == 0 {
		t.Fatalf("no pass windows for the 6-digit satellite over 24h")
	}
	for _, p := range passes {
		if p.NoradID != bigNORAD {
			t.Fatalf("pass result reported NORAD %d, want %d", p.NoradID, bigNORAD)
		}
	}
}
