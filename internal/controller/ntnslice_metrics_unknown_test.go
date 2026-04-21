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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// makeScheme returns a runtime.Scheme populated with just what the
// fake client needs to round-trip an NTNSlice. Kept local so other
// controller tests' scheme building is not coupled.
func makeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	utilruntime.Must(ntnv1alpha1.AddToScheme(sch))
	return sch
}

func TestSetMetricsUnknown_ConflictIsSwallowed(t *testing.T) {
	// A conflict on Status().Update() is expected when the resource
	// version moved; setMetricsUnknown swallows it and returns a
	// non-zero Result.RequeueAfter to request an explicit requeue,
	// so the caller never surfaces the conflict as a reconcile error.
	ns := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
	}
	cli := fake.NewClientBuilder().
		WithScheme(makeScheme(t)).
		WithObjects(ns).
		WithStatusSubresource(ns).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return apierrors.NewConflict(schema.GroupResource{Group: "ntn.operators.dev", Resource: "ntnslices"}, "s", errors.New("stale revision"))
			},
		}).
		Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: makeScheme(t)}
	res, err := r.setMetricsUnknown(context.Background(), ns, "MetricsReaderError", "mock")
	if err != nil {
		t.Fatalf("conflict must not surface as a reconcile error, got: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("expected a non-zero requeue, got %v", res.RequeueAfter)
	}
}

func TestSetMetricsUnknown_UnexpectedServerErrorPropagates(t *testing.T) {
	// A non-conflict update error (network, 500, etc.) is not safe to
	// ignore: we may have failed to persist the Unknown condition, so
	// surface the error and let controller-runtime do exponential backoff.
	ns := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
	}
	sentinel := errors.New("internal server error")
	cli := fake.NewClientBuilder().
		WithScheme(makeScheme(t)).
		WithObjects(ns).
		WithStatusSubresource(ns).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return sentinel
			},
		}).
		Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: makeScheme(t)}
	_, err := r.setMetricsUnknown(context.Background(), ns, "MetricsReaderError", "mock")
	if err == nil {
		t.Fatal("non-conflict Status().Update() error must propagate")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("want wrapped sentinel, got %v", err)
	}
}

func TestSetMetricsUnknown_SetsConditionOnSuccess(t *testing.T) {
	ns := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
	}
	sch := makeScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(ns).
		WithStatusSubresource(ns).
		Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: sch}
	_, err := r.setMetricsUnknown(context.Background(), ns, "MetricsReaderError", "prometheus unreachable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got ntnv1alpha1.NTNSlice
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatalf("re-get failed: %v", err)
	}
	var found bool
	for _, c := range got.Status.Conditions {
		if c.Type == ntnv1alpha1.ConditionFailoverReady {
			found = true
			if c.Status != metav1.ConditionUnknown {
				t.Errorf("status=%v want Unknown", c.Status)
			}
			if c.Reason != "MetricsReaderError" {
				t.Errorf("reason=%q want MetricsReaderError", c.Reason)
			}
		}
	}
	if !found {
		t.Error("FailoverReady condition was not persisted")
	}
}
