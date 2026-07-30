/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akhenakh/sgp4"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestPropagateStates_BoundsHugeObjectName is the mandated 1 MB OBJECT_NAME test at the
// propagated-state layer: a huge external name is truncated (rune-safe, shared with pass windows)
// before it is written to status, so one bounded GP input cannot amplify the status object past the
// etcd limit.
func TestPropagateStates_BoundsHugeObjectName(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}
	omm := issOMMForTest()
	omm.ObjectName = strings.Repeat("A", 1<<20) // 1 MB
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}}, time.Now().Add(2*time.Hour))

	if len(eph.Status.PropagatedStates) != 1 {
		t.Fatalf("expected 1 propagated state, got %d", len(eph.Status.PropagatedStates))
	}
	name := eph.Status.PropagatedStates[0].Satellite
	if n := utf8.RuneCountInString(name); n > ephemeris.MaxSatelliteNameLen {
		t.Fatalf("propagated state satellite = %d runes, exceeds bound %d", n, ephemeris.MaxSatelliteNameLen)
	}
	if !utf8.ValidString(name) {
		t.Fatalf("propagated state satellite is not valid UTF-8: %q", name)
	}
}
