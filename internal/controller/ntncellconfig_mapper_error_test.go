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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

// TestEphemerisToNTNCellConfig_IndexedListError_NoFallback pins the fallback removal that follows
// #204-G3: when the spec.ephemerisRef indexed List fails, the mapper must NOT fall back to a full
// namespace scan. The index is registered in SetupWithManager (startup-fatal otherwise), so a
// runtime error is a cache/index anomaly, not a missing index; a second O(namespace) List would
// re-introduce the exact cost the index removed and would mis-count the anomaly as an index
// fallback. Instead the mapper drops the single fan-out (returns nil after exactly one List call);
// the referencing cells re-resolve the ephemeris on their own RequeueAfter. It also classifies the
// error for the counter: a genuine anomaly increments EphemerisMapperIndexErrorTotal, while a
// canceled/expired context (shutdown or a deadline) does not.
func TestEphemerisToNTNCellConfig_IndexedListError_NoFallback(t *testing.T) {
	tests := []struct {
		name       string
		ns         string
		listErr    error
		wantMetric float64
	}{
		{"unexpected error increments the counter", "mapper-err-other", errors.New("simulated cache/index failure"), 1},
		{"canceled context is not counted", "mapper-err-canceled", context.Canceled, 0},
		{"deadline exceeded is not counted", "mapper-err-deadline", context.DeadlineExceeded, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ccRef := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cc-ref", Namespace: tc.ns},
				Spec:       ntnv1alpha1.NTNCellConfigSpec{EphemerisRef: "eph-x"},
			}
			var listCalls int
			c := fake.NewClientBuilder().
				WithScheme(makeScheme(t)).
				WithObjects(ccRef).
				WithIndex(&ntnv1alpha1.NTNCellConfig{}, ephemerisRefIndexKey, indexNTNCellConfigByEphemerisRef).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
						listCalls++
						return tc.listErr
					},
				}).
				Build()
			r := &NTNCellConfigReconciler{Client: c}

			label := prometheus.Labels{"namespace": tc.ns}
			before := testutil.ToFloat64(ntnmetrics.EphemerisMapperIndexErrorTotal.With(label))

			eph := &ntnv1alpha1.SatelliteEphemeris{ObjectMeta: metav1.ObjectMeta{Name: "eph-x", Namespace: tc.ns}}
			reqs := r.ephemerisToNTNCellConfig(context.Background(), eph)

			if reqs != nil {
				t.Errorf("on a List error the mapper must drop the fan-out (return nil), got %v", reqs)
			}
			if listCalls != 1 {
				t.Errorf("the mapper must not fall back to a second namespace-scan List: want 1 List call, got %d", listCalls)
			}
			if got := testutil.ToFloat64(ntnmetrics.EphemerisMapperIndexErrorTotal.With(label)) - before; got != tc.wantMetric {
				t.Errorf("EphemerisMapperIndexErrorTotal delta = %v, want %v", got, tc.wantMetric)
			}
		})
	}
}
