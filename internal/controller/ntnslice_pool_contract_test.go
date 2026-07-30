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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestCheckSatelliteAvailability_PoolMemberOverheadKeepsPathAvailable pins the ADR-0008 constellation-
// pool contract: the satellite path is available whenever ANY tracked member is overhead and
// deliverable, regardless of which NORAD any cell selects. Member A (25544) has an active, fresh window;
// member B (40000) is tracked but NOT overhead (no window). The path is available (A serves the pool).
//
// This documents the reviewed "A active, B selected, B unavailable" case as INTENDED, not a bug:
// NTNSlice.satellitePath carries no NORAD (LEO members hand over), so it cannot and must not be pinned
// to one satellite; a cell pinned to a non-overhead member is a cell-configuration concern. The
// complementary "all overhead members stale -> unavailable" case is TestCheckSatelliteAvailability_SourceEpochFreshnessGate.
func TestCheckSatelliteAvailability_PoolMemberOverheadKeepsPathAvailable(t *testing.T) {
	const ns = "default"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).UnixMilli()
	sch := makeScheme(t)

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph", Namespace: ns},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			Conditions: []metav1.Condition{{Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "x"}},
			// Member A (25544) is overhead; member B (40000) is tracked but has NO active window.
			NextPassWindows: []ntnv1alpha1.PassWindow{{
				Satellite: "A", NoradID: 25544, GroundStation: "gs",
				AOS: metav1.Time{Time: now.Add(-5 * time.Minute)}, LOS: metav1.Time{Time: now.Add(5 * time.Minute)},
			}},
			PropagatedStates: []ntnv1alpha1.PropagatedState{
				{NoradID: 25544, SourceEpochUnixMs: fresh},
				{NoradID: 40000, SourceEpochUnixMs: fresh},
			},
		},
	}
	slice := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: ns},
		Spec:       ntnv1alpha1.NTNSliceSpec{SatellitePath: ntnv1alpha1.SatellitePathSpec{EphemerisRef: "eph"}},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, slice).Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: sch}

	available, known, _ := r.checkSatelliteAvailability(context.Background(), slice, now)
	if !available || !known {
		t.Fatalf("pool contract (ADR-0008): an overhead, deliverable member must make the path available; got (available=%v, known=%v)", available, known)
	}
}
