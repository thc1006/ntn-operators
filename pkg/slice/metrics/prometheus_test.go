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
	"errors"
	"math"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// fakeQueryClient is a table-driven QueryClient for unit tests. Each PromQL
// string is looked up in responses/errors; unmapped queries fail loudly so
// a mis-matched test reveals the mismatch rather than masking it.
type fakeQueryClient struct {
	responses map[string]model.Value
	errors    map[string]error
	calls     []string
}

func (f *fakeQueryClient) Query(_ context.Context, query string, _ time.Time) (model.Value, error) {
	f.calls = append(f.calls, query)
	if err, ok := f.errors[query]; ok {
		return nil, err
	}
	if v, ok := f.responses[query]; ok {
		return v, nil
	}
	return nil, errors.New("fakeQueryClient: no response configured for query: " + query)
}

func scalar(v float64) model.Value {
	return &model.Scalar{Value: model.SampleValue(v), Timestamp: 0}
}

func vectorOne(v float64) model.Value {
	return model.Vector{&model.Sample{Value: model.SampleValue(v), Timestamp: 0}}
}

func sliceCR() *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
}

func defaultConfig() metrics.PrometheusConfig {
	return metrics.PrometheusConfig{
		Queries: metrics.Queries{
			RsrpDbm:           "rsrp_query",
			LatencyMs:         "latency_query",
			PacketLossPercent: "loss_query",
		},
		Timeout: 2 * time.Second,
	}
}

func TestPrometheusReader_AllScalars_ReturnsParsedMetrics(t *testing.T) {
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query":    scalar(-95),
			"latency_query": scalar(45),
			"loss_query":    scalar(1.2),
		},
	}
	r := metrics.NewPrometheusReader(client, defaultConfig())
	got, err := r.Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Stale {
		t.Error("fresh read must not be marked stale")
	}
	if got.Metrics.RSRP != -95 || got.Metrics.LatencyMs != 45 || got.Metrics.PacketLossPercent != 1.2 {
		t.Errorf("parsed metrics wrong: %+v", got.Metrics)
	}
}

// TestPrometheusReader_ObservedAtFromSampleTimestamp pins #203-M3's plumbing: the reader must carry
// the SOURCE sample timestamp (model.Sample.Timestamp) through Result.ObservedAt — it was previously
// discarded — so the anti-flap engine can tell a new observation from a stalled-scrape duplicate.
func TestPrometheusReader_ObservedAtFromSampleTimestamp(t *testing.T) {
	obs := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ts := model.TimeFromUnixNano(obs.UnixNano())
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query":    model.Vector{&model.Sample{Value: -100, Timestamp: ts}},
			"latency_query": model.Vector{&model.Sample{Value: 40, Timestamp: ts}},
			"loss_query":    model.Vector{&model.Sample{Value: 0.5, Timestamp: ts}},
		},
	}
	r := metrics.NewPrometheusReader(client, defaultConfig())
	got, err := r.Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !got.ObservedAt.Equal(obs) {
		t.Fatalf("ObservedAt must be the source sample timestamp %v, got %v (the reader must not discard "+
			"model.Sample.Timestamp, #203-M3)", obs, got.ObservedAt)
	}
}

func TestPrometheusReader_VectorWithOneSample_Parsed(t *testing.T) {
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query":    vectorOne(-120),
			"latency_query": vectorOne(100),
			"loss_query":    vectorOne(5),
		},
	}
	got, err := metrics.NewPrometheusReader(client, defaultConfig()).Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Metrics.RSRP != -120 || got.Metrics.LatencyMs != 100 || got.Metrics.PacketLossPercent != 5 {
		t.Errorf("vector parse wrong: %+v", got.Metrics)
	}
}

func TestPrometheusReader_EmptyVector_ReturnsErrNoMetrics(t *testing.T) {
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query":    model.Vector{}, // no samples
			"latency_query": scalar(20),
			"loss_query":    scalar(0),
		},
	}
	_, err := metrics.NewPrometheusReader(client, defaultConfig()).Read(context.Background(), sliceCR())
	if !errors.Is(err, metrics.ErrNoMetrics) {
		t.Fatalf("want ErrNoMetrics, got %v", err)
	}
}

func TestPrometheusReader_NaNInResponse_ReturnsErrNoMetrics(t *testing.T) {
	cases := []struct {
		name string
		val  float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeQueryClient{
				responses: map[string]model.Value{
					"rsrp_query":    scalar(tc.val),
					"latency_query": scalar(20),
					"loss_query":    scalar(0),
				},
			}
			_, err := metrics.NewPrometheusReader(client, defaultConfig()).Read(context.Background(), sliceCR())
			if !errors.Is(err, metrics.ErrNoMetrics) {
				t.Fatalf("want ErrNoMetrics for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestPrometheusReader_UnderlyingQueryError_IsPropagated(t *testing.T) {
	sentinel := errors.New("prometheus unreachable")
	client := &fakeQueryClient{
		errors: map[string]error{"rsrp_query": sentinel},
		responses: map[string]model.Value{
			"latency_query": scalar(20),
			"loss_query":    scalar(0),
		},
	}
	_, err := metrics.NewPrometheusReader(client, defaultConfig()).Read(context.Background(), sliceCR())
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel, got %v", err)
	}
}

func TestPrometheusReader_ContextCancellation_AbortsBeforeFurtherCalls(t *testing.T) {
	// Ensure we honour the caller's context: a cancelled ctx must
	// short-circuit before issuing any queries.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query":    scalar(-80),
			"latency_query": scalar(20),
			"loss_query":    scalar(0),
		},
	}
	_, err := metrics.NewPrometheusReader(client, defaultConfig()).Read(ctx, sliceCR())
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if len(client.calls) > 0 {
		t.Errorf("reader issued %d queries after ctx cancel", len(client.calls))
	}
}

func TestPrometheusReader_EmptyQueryString_SkipsThatMetric(t *testing.T) {
	// An empty PromQL string for a specific metric means "not configured";
	// the reader must not call the client for it, and the default is used.
	cfg := metrics.PrometheusConfig{
		Queries: metrics.Queries{
			RsrpDbm:           "rsrp_query",
			LatencyMs:         "", // not configured
			PacketLossPercent: "loss_query",
		},
		Timeout: time.Second,
	}
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query": scalar(-95),
			"loss_query": scalar(1),
		},
	}
	got, err := metrics.NewPrometheusReader(client, cfg).Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LatencyMs should have come from defaultMetrics.
	if got.Metrics.LatencyMs != 20 {
		t.Errorf("unset query must fall back to default 20, got %v", got.Metrics.LatencyMs)
	}
	if got.Metrics.RSRP != -95 || got.Metrics.PacketLossPercent != 1 {
		t.Errorf("configured queries mis-parsed: %+v", got.Metrics)
	}
	for _, q := range client.calls {
		if q == "" {
			t.Error("reader called client with empty query string")
		}
	}
}

func TestPrometheusReader_AllEmptyQueries_ReturnsDefaultsNoCalls(t *testing.T) {
	cfg := metrics.PrometheusConfig{
		Queries: metrics.Queries{},
		Timeout: time.Second,
	}
	client := &fakeQueryClient{}
	got, err := metrics.NewPrometheusReader(client, cfg).Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Metrics.RSRP != -80 || got.Metrics.LatencyMs != 20 || got.Metrics.PacketLossPercent != 0.1 {
		t.Errorf("all-empty queries must return defaults, got %+v", got.Metrics)
	}
	if len(client.calls) != 0 {
		t.Errorf("expected no client calls, got %d", len(client.calls))
	}
}

func TestPrometheusReader_NilNTNSlice_IsRejected(t *testing.T) {
	client := &fakeQueryClient{}
	_, err := metrics.NewPrometheusReader(client, defaultConfig()).Read(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil NTNSlice")
	}
}

func TestPrometheusReader_NilClient_IsRejectedAtConstruction(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil client")
		}
	}()
	_ = metrics.NewPrometheusReader(nil, defaultConfig())
}

func TestPrometheusReader_ZeroTimeout_DefaultsToTwoSeconds(t *testing.T) {
	// The default timeout is 2s when not configured. We can't easily assert
	// timeout value externally without exposing it; instead verify that a
	// zero-configured reader still functions (accepts calls and returns).
	client := &fakeQueryClient{
		responses: map[string]model.Value{
			"rsrp_query":    scalar(-80),
			"latency_query": scalar(20),
			"loss_query":    scalar(0),
		},
	}
	cfg := defaultConfig()
	cfg.Timeout = 0
	got, err := metrics.NewPrometheusReader(client, cfg).Read(context.Background(), sliceCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Stale {
		t.Error("no reason to be stale")
	}
}
