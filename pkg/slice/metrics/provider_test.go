/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

func annotationsCR() *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default", UID: types.UID("u-a")},
	}
}

func prometheusCR(endpoint string) *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default", UID: types.UID("u-p")},
		Spec: ntnv1alpha1.NTNSliceSpec{
			MetricsSource: &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: endpoint,
					Queries:  ntnv1alpha1.PrometheusQueries{RsrpDbm: "avg(rsrp)"},
				},
			},
		},
	}
}

func TestProvider_NilSource_YieldsAnnotationReader(t *testing.T) {
	p := metrics.NewProvider(metrics.NewClientPool())
	r, err := p.For(annotationsCR())
	if err != nil {
		t.Fatalf("For(): %v", err)
	}
	// Round-trip through Read to prove it behaves like annotation reader.
	ns := annotationsCR()
	ns.Annotations = map[string]string{"ntn.operators.dev/simulated-rsrp": "-111"}
	got, _ := r.Read(context.Background(), ns)
	if got.Metrics.RSRP != -111 {
		t.Errorf("annotation reader did not observe annotation, got %+v", got.Metrics)
	}
}

func TestProvider_ExplicitAnnotations_YieldsAnnotationReader(t *testing.T) {
	ns := annotationsCR()
	ns.Spec.MetricsSource = &ntnv1alpha1.MetricsSource{Type: ntnv1alpha1.MetricsSourceAnnotations}
	p := metrics.NewProvider(metrics.NewClientPool())
	r, err := p.For(ns)
	if err != nil {
		t.Fatalf("For(): %v", err)
	}
	ns.Annotations = map[string]string{"ntn.operators.dev/simulated-latency": "77"}
	got, _ := r.Read(context.Background(), ns)
	if got.Metrics.LatencyMs != 77 {
		t.Errorf("annotation reader did not observe annotation: %+v", got.Metrics)
	}
}

func TestProvider_Prometheus_CachesPerUIDAcrossReconciles(t *testing.T) {
	// The staleCache keeps its cache across repeated For() calls for
	// the same NTNSlice UID; Provider must return the same wrapper.
	p := metrics.NewProvider(metrics.NewClientPool())
	ns := prometheusCR("http://prom.example:9090")
	r1, err := p.For(ns)
	if err != nil {
		t.Fatalf("first For(): %v", err)
	}
	r2, err := p.For(ns)
	if err != nil {
		t.Fatalf("second For(): %v", err)
	}
	if r1 != r2 {
		t.Error("Provider returned different readers for the same CR spec; stale cache would be lost between reconciles")
	}
}

func TestProvider_Prometheus_RebuildsWhenSpecChanges(t *testing.T) {
	// Changing the endpoint or queries must invalidate the cached
	// reader — otherwise stale results from the old endpoint leak.
	p := metrics.NewProvider(metrics.NewClientPool())
	ns1 := prometheusCR("http://prom-a:9090")
	ns2 := prometheusCR("http://prom-b:9090") // same UID, different endpoint
	r1, _ := p.For(ns1)
	r2, _ := p.For(ns2)
	if r1 == r2 {
		t.Error("Provider reused reader after endpoint changed")
	}
}

func TestProvider_Prometheus_InvalidEndpointPropagatesError(t *testing.T) {
	p := metrics.NewProvider(metrics.NewClientPool())
	ns := prometheusCR("::not a url::")
	_, err := p.For(ns)
	if err == nil {
		t.Fatal("expected error for malformed endpoint")
	}
}

func TestProvider_Prometheus_MissingPrometheusBlock_ReturnsError(t *testing.T) {
	// CEL catches this at admission, but the Provider must not crash
	// if a malformed CR reaches it (e.g., CRD schema downgrade).
	ns := annotationsCR()
	ns.Spec.MetricsSource = &ntnv1alpha1.MetricsSource{
		Type: ntnv1alpha1.MetricsSourcePrometheus,
		// Prometheus intentionally nil
	}
	p := metrics.NewProvider(metrics.NewClientPool())
	_, err := p.For(ns)
	if err == nil {
		t.Fatal("expected error when type=prometheus but prometheus block is nil")
	}
}

func TestProvider_NilNTNSlice_Rejected(t *testing.T) {
	p := metrics.NewProvider(metrics.NewClientPool())
	_, err := p.For(nil)
	if err == nil {
		t.Fatal("expected error for nil NTNSlice")
	}
}

func TestProvider_NilPool_PanicsAtConstruction(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil pool")
		}
	}()
	_ = metrics.NewProvider(nil)
}

func TestProvider_PrometheusMode_EmptyUID_IsRejected(t *testing.T) {
	// Two unsaved slices with UID="" must not share cache state. The
	// Provider defends by refusing empty-UID CRs up front in Prometheus
	// mode — staleCache itself also rejects them, but it is clearer to
	// fail at the entry point than to let a half-valid Reader be cached.
	p := metrics.NewProvider(metrics.NewClientPool())
	ns := prometheusCR("http://prom:9090")
	ns.UID = ""
	_, err := p.For(ns)
	if err == nil {
		t.Fatal("expected error for prometheus mode with empty UID")
	}
}

func TestProvider_Evict_RemovesCachedReader(t *testing.T) {
	// After Evict the Provider must not return the previously-cached
	// reader; the next For() call rebuilds, giving a fresh staleCache.
	p := metrics.NewProvider(metrics.NewClientPool())
	ns := prometheusCR("http://prom:9090")
	first, err := p.For(ns)
	if err != nil {
		t.Fatalf("first For(): %v", err)
	}
	p.Evict(client.ObjectKey{Namespace: ns.Namespace, Name: ns.Name})
	second, err := p.For(ns)
	if err != nil {
		t.Fatalf("second For(): %v", err)
	}
	if first == second {
		t.Error("Evict did not drop the cached reader: same reader returned after eviction")
	}
}

func TestProvider_Evict_OnMissingKey_IsNoOp(t *testing.T) {
	p := metrics.NewProvider(metrics.NewClientPool())
	// Should simply not panic.
	p.Evict(client.ObjectKey{Namespace: "does-not", Name: "exist"})
}

func TestProvider_Fingerprint_NilAndZeroTimeoutAreEqivalent(t *testing.T) {
	// A nil QueryTimeout and an explicit 0 QueryTimeout both mean
	// "fall back to default"; they must produce the same cached reader,
	// not two entries that rebuild each other on every reconcile.
	p := metrics.NewProvider(metrics.NewClientPool())
	nsA := prometheusCR("http://prom:9090")
	nsA.Spec.MetricsSource.Prometheus.QueryTimeout = nil
	nsB := prometheusCR("http://prom:9090")
	zero := metav1.Duration{Duration: 0}
	nsB.Spec.MetricsSource.Prometheus.QueryTimeout = &zero
	a, _ := p.For(nsA)
	b, _ := p.For(nsB)
	if a != b {
		t.Error("nil QueryTimeout and QueryTimeout=0s must cache as the same reader")
	}
}
