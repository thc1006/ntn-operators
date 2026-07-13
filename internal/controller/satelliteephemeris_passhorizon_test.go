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

// TestEffectivePassHorizon pins #233: the pass-prediction horizon defaults when unset and is
// clamped to maxPassHorizon otherwise, so an unbounded horizon cannot stall the reconcile.
// Mutation: dropping the maxPassHorizon ceiling makes the 30-day case pass through, so this fails.
func TestEffectivePassHorizon(t *testing.T) {
	cases := []struct {
		name        string
		raw         time.Duration
		want        time.Duration
		wantClamped bool
	}{
		{"unset defaults to 24h", 0, 24 * time.Hour, false},
		{"negative defaults to 24h", -time.Hour, 24 * time.Hour, false},
		{"within bound is unchanged", 12 * time.Hour, 12 * time.Hour, false},
		{"exactly at max is unchanged", maxPassHorizon, maxPassHorizon, false},
		{"just over max is clamped", maxPassHorizon + time.Hour, maxPassHorizon, true},
		{"30 days is clamped", 30 * 24 * time.Hour, maxPassHorizon, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, clamped := effectivePassHorizon(c.raw)
			if got != c.want || clamped != c.wantClamped {
				t.Fatalf("effectivePassHorizon(%v) = (%v, %v), want (%v, %v)",
					c.raw, got, clamped, c.want, c.wantClamped)
			}
		})
	}
}
