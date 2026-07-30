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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestReconcile_NoOpStatusWrite_G3 pins the terminal status-write guard: a steady-state reconcile
// (DecisionStay, healthy metrics, no switchback) must NOT re-issue Status().Update. NTNSlice
// requeues on a fixed interval, so an unconditional write would rewrite byte-identical status every
// cycle and churn every watcher — the #204-G3 churn the NTNCellConfig #271 guard removed, here
// propagated to NTNSlice. The in-memory anti-flap streak (storeFlapState) still commits every
// reconcile; only the durable status write is skipped. resourceVersion cannot catch a byte-identical
// write (the store short-circuits it so it never bumps), so the SubResourceUpdate interceptor counts
// write REQUESTS.
//
// Mutation: drop the DeepEqual guard on the terminal write → the steady-state reconcile writes again
// and the delta assertion fails.
func TestReconcile_NoOpStatusWrite_G3(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)

	eph := &ntnv1alpha1.SatelliteEphemeris{ObjectMeta: metav1.ObjectMeta{Name: "eph", Namespace: "default"}}
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "cht", APN: "internet", Priority: "primary"},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "eph",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:        []string{"rsrp < -100"},
				SwitchbackDelay: metav1.Duration{Duration: 60 * time.Second},
			},
		},
	}

	var statusUpdates int
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(nsObj, eph).
		WithStatusSubresource(nsObj, eph).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sr string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*ntnv1alpha1.NTNSlice); ok { // count NTNSlice status writes only
					statusUpdates++
				}
				return c.SubResource(sr).Update(ctx, obj, opts...)
			},
		}).Build()

	// Healthy, fresh metrics: RSRP -80 does not fire "rsrp < -100", so the slice stays on the
	// primary terrestrial path every reconcile (DecisionStay).
	fr := fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0},
		LastFreshAt: fixedNow,
	}}
	r := &NTNSliceReconciler{
		Client: cli, Scheme: sch,
		Now:            func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: fr},
	}

	key := client.ObjectKeyFromObject(nsObj)
	req := reconcile.Request{NamespacedName: key}
	// Drive to steady state (the first reconcile establishes ActivePathType + conditions).
	for range 3 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile to steady state: %v", err)
		}
	}
	before := statusUpdates

	// A steady-state DecisionStay reconcile re-derives identical status and queues no event → no write.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if statusUpdates != before {
		t.Fatalf("a steady-state (DecisionStay) reconcile must not re-write status (#204-G3): "+
			"Status().Update issued %d extra request(s)", statusUpdates-before)
	}
}
