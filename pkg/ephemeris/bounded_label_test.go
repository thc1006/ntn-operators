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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akhenakh/sgp4"
)

// TestBoundedSatelliteLabel pins the status-amplification guard: an external OMM ObjectName is
// truncated to MaxSatelliteNameLen RUNES and stays valid UTF-8 (byte truncation would split a
// multi-byte rune into a U+FFFD on marshal).
func TestBoundedSatelliteLabel(t *testing.T) {
	multibyte := strings.Repeat("衛", 100) // 100 runes, 300 bytes
	cases := []struct {
		name     string
		in       string
		wantRune int  // expected rune count of the result
		wantSame bool // result must be byte-identical to input
	}{
		{"empty", "", 0, true},
		{"short ascii", "ISS (ZARYA)", 11, true},
		{"exactly max ascii", strings.Repeat("A", MaxSatelliteNameLen), MaxSatelliteNameLen, true},
		{"over max ascii truncated", strings.Repeat("A", MaxSatelliteNameLen+1), MaxSatelliteNameLen, false},
		{"1MB ascii truncated", strings.Repeat("A", 1<<20), MaxSatelliteNameLen, false},
		{"multibyte over max truncated", multibyte, MaxSatelliteNameLen, false},
		// byte length > max but rune count <= max: kept as-is (maxLength counts code points).
		{"multibyte under max kept", strings.Repeat("衛", 40), 40, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BoundedSatelliteLabel(tc.in)
			if n := utf8.RuneCountInString(got); n != tc.wantRune {
				t.Fatalf("rune count = %d, want %d", n, tc.wantRune)
			}
			if n := utf8.RuneCountInString(got); n > MaxSatelliteNameLen {
				t.Fatalf("result exceeds MaxSatelliteNameLen: %d runes", n)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8 (a rune was split): %q", got)
			}
			if tc.wantSame && got != tc.in {
				t.Fatalf("result should be unchanged, got %q", got)
			}
		})
	}
}

// TestPredictPasses_BoundsHugeObjectName is the mandated 1 MB OBJECT_NAME test at the pass-prediction
// layer: a valid OMM with a 1 MB name still produces passes, but each window's satellite label is
// bounded — so a single external name cannot be amplified across up to MaxPassWindows windows.
func TestPredictPasses_BoundsHugeObjectName(t *testing.T) {
	omm := issOMM()
	omm.ObjectName = strings.Repeat("A", 1<<20) // 1 MB
	passes, err := PredictPasses(context.Background(),
		[]sgp4.OMM{omm}, []GroundStation{montrealStation}, 0, 24*time.Hour, nil, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(passes) == 0 {
		t.Fatal("expected at least one pass to exercise the label bound")
	}
	for _, p := range passes {
		if n := utf8.RuneCountInString(p.Satellite); n > MaxSatelliteNameLen {
			t.Fatalf("pass window satellite label = %d runes, exceeds bound %d", n, MaxSatelliteNameLen)
		}
	}
}
