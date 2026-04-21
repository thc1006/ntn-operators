/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Observability-level tests for the metrics readers: assert that the
// Prometheus counters in pkg/metrics are incremented at the right call
// sites. Kept in a dedicated _test file so the rest of the suite stays
// free of cross-package coupling.

package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/slice"
	"github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// histogramSampleCount pulls the cumulative observation count off a
// single Histogram child. testutil.CollectAndCount counts series — not
// observations — and thus cannot distinguish "this call did emit an
// observation" from "a previous test already made the series exist".
func histogramSampleCount(t *testing.T, h prometheus.Observer) uint64 {
	t.Helper()
	c, ok := h.(prometheus.Collector)
	if !ok {
		t.Fatalf("observer is not a collector: %T", h)
	}
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	close(ch)
	m := <-ch
	if m == nil {
		return 0
	}
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		t.Fatalf("metric write: %v", err)
	}
	return pb.GetHistogram().GetSampleCount()
}

func TestPrometheusReader_Observability_QueryDurationObservedOnSuccess(t *testing.T) {
	// Compare the observation count on the specific {prometheus, success}
	// series before and after a single Read. The global testutil package
	// accumulates across the whole package run, so asserting "count > 0"
	// could pass on residue from earlier tests even if the Read under
	// test did not observe anything.
	successLabels := prometheus.Labels{"source": "prometheus", "outcome": "success"}
	before := histogramSampleCount(t, ntnmetrics.ReaderQueryDuration.With(successLabels))

	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query": &model.Scalar{Value: -95},
		},
	}
	cfg := metrics.PrometheusConfig{Queries: metrics.Queries{RsrpDbm: "rsrp_query"}}
	_, err := metrics.NewPrometheusReader(client, cfg).Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := histogramSampleCount(t, ntnmetrics.ReaderQueryDuration.With(successLabels))
	if after != before+1 {
		t.Errorf("expected exactly one new observation on {prometheus,success}: before=%d after=%d", before, after)
	}
}

func TestPrometheusReader_Observability_ErrorCounterPerReason(t *testing.T) {
	cases := []struct {
		name     string
		response model.Value
		err      error
		reason   string
	}{
		{"query_error", nil, errors.New("boom"), "query_error"},
		{"empty_vector", model.Vector{}, nil, "empty_vector"},
		{"ambiguous_vector", model.Vector{&model.Sample{Value: 1}, &model.Sample{Value: 2}}, nil, "ambiguous_vector"},
		{"non_finite_scalar", &model.Scalar{Value: model.SampleValue(neverFinite())}, nil, "non_finite"},
		{"unsupported_type", model.Matrix{}, nil, "unsupported_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := testutil.ToFloat64(ntnmetrics.ReaderErrorsTotal.With(prometheus.Labels{
				"source": "prometheus", "reason": tc.reason,
			}))
			client := &fakeQueryClient{
				responses: map[string]model.Value{"q": tc.response},
			}
			if tc.err != nil {
				client.errors = map[string]error{"q": tc.err}
			}
			_, _ = metrics.NewPrometheusReader(client, metrics.PrometheusConfig{
				Queries: metrics.Queries{RsrpDbm: "q"},
			}).Read(context.Background(), sliceCR())
			after := testutil.ToFloat64(ntnmetrics.ReaderErrorsTotal.With(prometheus.Labels{
				"source": "prometheus", "reason": tc.reason,
			}))
			if after <= before {
				t.Errorf("counter for reason=%s did not increment: before=%v after=%v", tc.reason, before, after)
			}
		})
	}
}

// neverFinite returns a NaN so tests can exercise the "non_finite" reason
// without pulling math into this file's direct dependency graph.
func neverFinite() float64 {
	var zero float64
	return zero / zero
}

func TestStaleCache_Observability_StaleCounterIncrements(t *testing.T) {
	labels := prometheus.Labels{"namespace": "default", "name": "obs-slice"}
	before := testutil.ToFloat64(ntnmetrics.ReaderStaleUsedTotal.With(labels))
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -90}}},
		{err: metrics.ErrNoMetrics},
	}}
	c := metrics.NewStaleCache(inner)
	ns := &ntnv1alpha1.NTNSlice{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "obs-slice", UID: types.UID("u-obs"),
	}}
	// First read succeeds; should NOT increment stale counter.
	_, _ = c.Read(context.Background(), ns)
	mid := testutil.ToFloat64(ntnmetrics.ReaderStaleUsedTotal.With(labels))
	if mid != before {
		t.Errorf("fresh read must not increment stale counter: before=%v mid=%v", before, mid)
	}
	// Second read is stale; must increment.
	_, _ = c.Read(context.Background(), ns)
	after := testutil.ToFloat64(ntnmetrics.ReaderStaleUsedTotal.With(labels))
	if after != before+1 {
		t.Errorf("stale counter expected before+1=%v got %v", before+1, after)
	}
}
