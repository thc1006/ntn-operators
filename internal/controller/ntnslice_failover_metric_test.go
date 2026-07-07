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
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

// TestReconcile_FailoverCounterNotIncrementedOnStatusConflict proves the
// FailoverTotal counter is emitted ONLY after the status write persists.
//
// A stale reconcile (its cached copy behind a fresher one's status write) can
// still re-decide DecisionFailover, but its own Status().Update then loses the
// optimistic-concurrency race and is discarded. If the counter were bumped
// inline in the switch, that discarded reconcile would count a failover it
// never persisted, drifting FailoverTotal +1 above status.FailoverCount.
//
// We reproduce that exact interleaving by forcing Status().Update to return a
// Conflict, and assert the counter does not move.
func TestReconcile_FailoverCounterNotIncrementedOnStatusConflict(t *testing.T) {
	const (
		nsName    = "default"
		sliceName = "failover-conflict"
		ephName   = "oneweb-constellation"
	)
	// Fixed to sit inside the pass window below (AOS 11:00 <= now < LOS 13:00),
	// so checkSatelliteAvailability reports the satellite path as available.
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	slice := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sliceName,
			Namespace: nsName,
			Annotations: map[string]string{
				// Degraded terrestrial signal → the "rsrp < -120" trigger fires.
				"ntn.operators.dev/simulated-rsrp":        "-130",
				"ntn.operators.dev/simulated-latency":     "20",
				"ntn.operators.dev/simulated-packet-loss": "0.1",
			},
		},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant: "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{
				Provider: "chunghwa-telecom", APN: "internet", Priority: "primary",
			},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: ephName,
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers: []string{"rsrp < -120", "latency > 200", "packetLoss > 5"},
			},
		},
	}
	// Read-only for the reconciler, so it need not be a status subresource;
	// its WithObjects status (the pass window) is preserved for Get.
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: ephName, Namespace: nsName},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			SatelliteCount: 651,
			NextPassWindows: []ntnv1alpha1.PassWindow{
				{
					Satellite:     "ONEWEB-0012",
					GroundStation: "gs-taipei-01",
					AOS:           metav1.Time{Time: time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC)},
					LOS:           metav1.Time{Time: time.Date(2026, 4, 17, 13, 0, 0, 0, time.UTC)},
					MaxElevation:  "45.0",
				},
			},
		},
	}

	sch := makeScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(slice, eph).
		WithStatusSubresource(slice).
		WithInterceptorFuncs(interceptor.Funcs{
			// Simulate the fresher reconcile having already advanced the
			// resourceVersion: this stale reconcile's status write conflicts.
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return apierrors.NewConflict(
					schema.GroupResource{Group: "ntn.operators.dev", Resource: "ntnslices"},
					sliceName, errors.New("stale revision"))
			},
		}).
		Build()

	labels := prometheus.Labels{
		"namespace": nsName, "slice": sliceName,
		"from_path": "terrestrial", "to_path": "satellite",
	}
	before := testutil.ToFloat64(ntnmetrics.FailoverTotal.With(labels))

	recorder := events.NewFakeRecorder(10)
	r := &NTNSliceReconciler{
		Client:   cli,
		Scheme:   sch,
		Recorder: recorder,
		Now:      func() time.Time { return now },
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: sliceName, Namespace: nsName},
	})
	// The conflict on the status write must surface so controller-runtime
	// re-reconciles from a fresh cache; it must not be swallowed.
	if err == nil {
		t.Fatal("expected the status-update conflict to surface as a reconcile error")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected a Conflict error, got: %v", err)
	}
	// Positive control: FailoverTriggered is emitted in the DecisionFailover
	// branch, before the (discarded) status write. Its presence proves the
	// reconcile actually decided to fail over — otherwise "counter unchanged"
	// would pass vacuously even if the setup never reached a failover.
	select {
	case ev := <-recorder.Events:
		if !strings.Contains(ev, "FailoverTriggered") {
			t.Fatalf("expected a FailoverTriggered event, got: %q", ev)
		}
	default:
		t.Fatal("no event emitted: the reconcile never reached the failover decision, so the counter assertion would be vacuous")
	}
	// The failover was never persisted, so the counter must not have moved.
	if got := testutil.ToFloat64(ntnmetrics.FailoverTotal.With(labels)); got != before {
		t.Errorf("FailoverTotal moved on a discarded failover: before=%v after=%v (want unchanged)", before, got)
	}
}
