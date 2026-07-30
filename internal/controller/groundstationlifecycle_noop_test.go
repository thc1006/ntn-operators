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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestGroundStationReconcile_NoOpStatusWrite_G3 pins the terminal status-write guard: a
// steady-state reconcile (nothing changed, no event queued) must NOT re-issue Status().Update.
// GroundStation requeues on a fixed interval, so an unconditional write would rewrite
// byte-identical status every cycle and churn every watcher — the #204-G3 churn the NTNCellConfig
// #271 guard removed, here propagated to the sibling controller. resourceVersion cannot catch
// this: the API store short-circuits a byte-identical write so it never bumps. The
// SubResourceUpdate interceptor therefore counts write REQUESTS instead.
//
// Mutation: drop the DeepEqual guard on the terminal write → the steady-state reconcile writes
// again and the delta assertion fails.
func TestGroundStationReconcile_NoOpStatusWrite_G3(t *testing.T) {
	sch := runtime.NewScheme()
	utilruntime.Must(ntnv1alpha1.AddToScheme(sch))
	utilruntime.Must(corev1.AddToScheme(sch))

	gs := &ntnv1alpha1.GroundStationLifecycle{
		ObjectMeta: metav1.ObjectMeta{Name: "gs-noop", Namespace: "default", Generation: 1},
	}

	var statusUpdates int
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs).WithStatusSubresource(gs).
		WithInterceptorFuncs(interceptor.Funcs{
			// c is the underlying client; calling it does not re-enter this interceptor.
			SubResourceUpdate: func(ctx context.Context, c client.Client, sr string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusUpdates++
				return c.SubResource(sr).Update(ctx, obj, opts...)
			},
		}).Build()

	r := &GroundStationLifecycleReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10)}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "gs-noop", Namespace: "default"}}

	// Drive to steady state. With no matching Node the health eval is deterministic
	// (Phase=Provisioning + NodeNotFound conditions), so status stabilizes after the first write.
	for range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile to steady state: %v", err)
		}
	}
	before := statusUpdates

	// A steady-state reconcile re-derives identical status and queues no event → no write.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if statusUpdates != before {
		t.Fatalf("a steady-state reconcile must not re-write status (#204-G3): "+
			"Status().Update issued %d extra request(s)", statusUpdates-before)
	}
}
