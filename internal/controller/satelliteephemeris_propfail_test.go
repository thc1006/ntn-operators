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

	"github.com/akhenakh/sgp4"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

// TestPropagateStates_SurfacesPropagationFailure pins the fix: a tracked element set that passes the
// epoch checks but fails SGP4 propagation to ECEF is no longer silently dropped — it is counted and
// surfaced via the PropagationFailed condition + metric, with a BOUNDED message (reason + NORAD, never
// the raw external ObjectName), so it is not mistaken for a generic downstream PayloadMissing.
func TestPropagateStates_SurfacesPropagationFailure(t *testing.T) {
	ntnmetrics.EphemerisPropagationFailedCount.Reset()
	r := &SatelliteEphemerisReconciler{}

	bad := badOMMForTest() // eccentricity 1.5 → SGP4 propagation fails, but epoch is parseable+plausible
	bad.NoradCatID = 54321
	bad.ObjectName = "EVIL-SAT-SHOULD-NOT-APPEAR-IN-CONDITION"
	good := issOMMForTest()
	good.NoradCatID = 25544

	eph := &ntnv1alpha1.SatelliteEphemeris{}
	r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: []sgp4.OMM{bad, good}}, time.Now().Add(2*time.Hour))

	// The good satellite propagates; the bad one is dropped from status but SURFACED.
	if len(eph.Status.PropagatedStates) != 1 || eph.Status.PropagatedStates[0].NoradID != 25544 {
		t.Fatalf("expected only the good satellite (25544) propagated, got %+v", eph.Status.PropagatedStates)
	}
	cond := meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionPropagationFailed)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "PropagationFailed" {
		t.Fatalf("PropagationFailed must be True/PropagationFailed, got %+v", cond)
	}
	if !strings.Contains(cond.Message, "1 of 2") || !strings.Contains(cond.Message, "54321") {
		t.Fatalf("message must be bounded and name the failed NORAD, got %q", cond.Message)
	}
	if strings.Contains(cond.Message, bad.ObjectName) {
		t.Fatalf("condition must NOT embed the raw external ObjectName; got %q", cond.Message)
	}
	if got := testutil.ToFloat64(ntnmetrics.EphemerisPropagationFailedCount.WithLabelValues("", "")); got != 1 {
		t.Fatalf("propagation-failed metric = %v, want 1", got)
	}

	// All good: the condition clears to False when nothing fails.
	eph2 := &ntnv1alpha1.SatelliteEphemeris{}
	r.propagateStates(context.Background(), eph2, ephemeris.GPFetchResult{OMMs: []sgp4.OMM{good}}, time.Now().Add(2*time.Hour))
	if cond := meta.FindStatusCondition(eph2.Status.Conditions, ntnv1alpha1.ConditionPropagationFailed); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("PropagationFailed must be False when all propagate, got %+v", cond)
	}
}
