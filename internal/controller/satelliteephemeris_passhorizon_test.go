/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"
	"time"
)

// TestEffectivePassHorizon pins #233: the pass-prediction horizon defaults when unset (0), is
// REJECTED when negative (a config error, not silently defaulted — round-2 review), and is clamped
// to maxPassHorizon otherwise, so an unbounded horizon cannot stall the reconcile. Mutations:
// dropping the maxPassHorizon ceiling lets the 30-day case through; treating negative as 24h (the
// pre-fix behaviour) makes the negative case return no error — both fail here.
func TestEffectivePassHorizon(t *testing.T) {
	cases := []struct {
		name        string
		raw         time.Duration
		want        time.Duration
		wantClamped bool
		wantErr     bool
	}{
		{"unset defaults to 24h", 0, 24 * time.Hour, false, false},
		{"negative is rejected", -time.Hour, 0, false, true},
		{"one nanosecond negative is rejected", -time.Nanosecond, 0, false, true},
		{"within bound is unchanged", 12 * time.Hour, 12 * time.Hour, false, false},
		{"exactly at max is unchanged", maxPassHorizon, maxPassHorizon, false, false},
		{"just over max is clamped", maxPassHorizon + time.Nanosecond, maxPassHorizon, true, false},
		{"30 days is clamped", 30 * 24 * time.Hour, maxPassHorizon, true, false},
		{"very large duration is clamped", 1<<62 - 1, maxPassHorizon, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, clamped, err := effectivePassHorizon(c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("effectivePassHorizon(%v) err = %v, wantErr %v", c.raw, err, c.wantErr)
			}
			if c.wantErr {
				return // on error, horizon/clamped are not meaningful
			}
			if got != c.want || clamped != c.wantClamped {
				t.Fatalf("effectivePassHorizon(%v) = (%v, %v), want (%v, %v)",
					c.raw, got, clamped, c.want, c.wantClamped)
			}
		})
	}
}
