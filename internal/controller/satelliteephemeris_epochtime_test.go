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

	"github.com/akhenakh/sgp4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestReconcile_EpochSampledAtPropagationTime pins #235: the propagated epoch is stamped from the
// clock sampled at the ACTUAL propagation instant, not at reconcile-start. A fully fixed, injected
// two-value clock returns the reconcile-start instant on its first read and the (later) propagation
// instant on every subsequent read, so the stored epoch must equal EXACTLY propagation-time + lead.
// Mutations that fail here: stamping from the reconcile-start `now` (== base + lead, not
// propagationNow + lead); using a bare time.Now() instead of r.now() (would not equal the injected
// propagation sample); adding/omitting the lead. The OMM epoch is fixed to base so the propagation
// is a small, valid forward step and the test does not depend on the real wall clock.
func TestReconcile_EpochSampledAtPropagationTime(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	propagationNow := base.Add(time.Minute) // every clock read after the first
	sch := makeScheme(t)

	omm := issOMMForTest()
	omm.EpochStr = base.Format("2006-01-02T15:04:05.000000") // element epoch = base → forward propagation

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-epochtime", Namespace: "default", Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: "eph-epochtime", Namespace: "default"}

	var calls int
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: base,
		}},
		// First read is the reconcile-start instant; every later read (including the one just before
		// propagateStates) is the propagation instant. So the epoch is exact iff it re-sampled.
		Now: func() time.Time {
			calls++
			if calls == 1 {
				return base
			}
			return propagationNow
		},
	}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.PropagatedStates) == 0 {
		t.Fatal("expected a propagated state")
	}
	epochMs := got.Status.PropagatedStates[0].EpochUnixMs
	wantEpochMs := propagationNow.Add(propagationEpochLead).UnixMilli()
	if epochMs != wantEpochMs {
		t.Fatalf("epoch = %d, want EXACTLY the propagation-time sample + lead = %d "+
			"(reconcile-start + lead would be %d)", epochMs, wantEpochMs, base.Add(propagationEpochLead).UnixMilli())
	}
	if calls < 2 {
		t.Fatalf("clock sampled %d time(s); the epoch must come from a re-sample, so want at least 2", calls)
	}
}
