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
	"errors"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestHandlePassPrediction_ContextCancelled_IsControlFlowNotFailure pins the round-2 review fix:
// a context cancellation (controller shutdown, leader loss, per-reconcile timeout) surfaced by
// pass prediction is CONTROL FLOW, not a PredictionFailed domain error. handlePassPrediction must
// return it so Reconcile requeues, and must NOT clear the existing pass windows or record a
// PredictionFailed condition. Mutation: treating the cancellation like any other error (the
// pre-fix behaviour) clears NextPassWindows and sets PredictionFailed, so this fails.
func TestHandlePassPrediction_ContextCancelled_IsControlFlowNotFailure(t *testing.T) {
	sch := makeScheme(t)
	gs := &ntnv1alpha1.GroundStationLifecycle{
		ObjectMeta: metav1.ObjectMeta{Name: "gs-taipei", Namespace: "default"},
		Spec: ntnv1alpha1.GroundStationLifecycleSpec{
			Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654"}},
		},
	}
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-cancel", Namespace: "default", Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/x"},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{
				GroundStations: []string{"gs-taipei"},
				MinElevation:   "10",
				Horizon:        metav1.Duration{Duration: 24 * time.Hour},
			},
		},
	}
	// Pre-existing pass windows the cancellation path must NOT clear.
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{Satellite: "ISS", GroundStation: "gs-taipei"}}

	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := r.handlePassPrediction(ctx, eph,
		ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMForTest()}}, time.Now())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context must be returned as a control-flow error, got %v", err)
	}
	if len(eph.Status.NextPassWindows) != 1 {
		t.Fatalf("cancellation must NOT clear pass windows; got %d, want the 1 pre-existing window",
			len(eph.Status.NextPassWindows))
	}
	if c := meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted); c != nil &&
		c.Reason == "PredictionFailed" {
		t.Fatalf("cancellation must NOT record a PredictionFailed condition; got %+v", c)
	}
}
