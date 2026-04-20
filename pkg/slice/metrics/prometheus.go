/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/prometheus/common/model"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// defaultPrometheusTimeout is the per-query hard limit used when
// PrometheusConfig.Timeout is zero or negative. Kept short to bound the
// reconcile latency contribution of a misbehaving Prometheus.
const defaultPrometheusTimeout = 2 * time.Second

// QueryClient is the subset of prometheus/client_golang/api/prometheus/v1.API
// that prometheusReader depends on. Narrowing the surface keeps unit tests
// free of HTTP plumbing and makes the dependency easier to swap in the
// future (e.g., for push-based backends).
type QueryClient interface {
	Query(ctx context.Context, query string, ts time.Time) (model.Value, error)
}

// Queries holds the PromQL expressions for each of the three metrics the
// reader produces. An empty string means "not configured for this slice":
// the reader leaves that field at its default value and does not issue a
// network call for it.
//
// Failure semantics: any non-empty PromQL that returns an unobservable
// value — a query error, an empty vector, or a NaN/±Inf — causes the whole
// Read to fail with ErrNoMetrics. This is intentional: failover logic
// consumes all three metrics together, and mixing a stale latency value
// with a fresh RSRP value can produce nonsensical decisions. Configure
// every query you care about; leave the rest empty.
type Queries struct {
	RsrpDbm           string
	LatencyMs         string
	PacketLossPercent string
}

// PrometheusConfig configures a prometheusReader.
type PrometheusConfig struct {
	Queries Queries
	Timeout time.Duration
}

type prometheusReader struct {
	client  QueryClient
	queries Queries
	timeout time.Duration
	now     func() time.Time
}

// NewPrometheusReader returns a Reader backed by a Prometheus instant-query
// API. Panics if client is nil — this is a programmer error at wiring time,
// not a runtime condition we want to quietly paper over.
func NewPrometheusReader(client QueryClient, cfg PrometheusConfig) Reader {
	if client == nil {
		panic("metrics.NewPrometheusReader: nil client")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultPrometheusTimeout
	}
	return &prometheusReader{
		client:  client,
		queries: cfg.Queries,
		timeout: timeout,
		now:     time.Now,
	}
}

func (r *prometheusReader) Read(ctx context.Context, ns *ntnv1alpha1.NTNSlice) (Result, error) {
	if ns == nil {
		return Result{}, errors.New("prometheusReader: nil NTNSlice")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("prometheusReader: %w", err)
	}

	qctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	m := defaultMetrics
	fields := []struct {
		query string
		set   func(float64)
	}{
		{r.queries.RsrpDbm, func(v float64) { m.RSRP = v }},
		{r.queries.LatencyMs, func(v float64) { m.LatencyMs = v }},
		{r.queries.PacketLossPercent, func(v float64) { m.PacketLossPercent = v }},
	}
	for _, f := range fields {
		if f.query == "" {
			continue
		}
		v, err := r.fetch(qctx, f.query)
		if err != nil {
			return Result{}, err
		}
		f.set(v)
	}
	return Result{Metrics: m}, nil
}

// fetch issues a single instant query and extracts a finite float64.
// Empty vectors and non-finite values both surface as ErrNoMetrics so the
// caller sees a uniform "this metric is unobservable" signal.
//
// TODO(cycle 7): wrap this call in a duration histogram
// (ntn_metrics_reader_query_duration_seconds) and an errors counter.
func (r *prometheusReader) fetch(ctx context.Context, q string) (float64, error) {
	v, err := r.client.Query(ctx, q, r.now())
	if err != nil {
		return 0, fmt.Errorf("prometheusReader: query %q: %w", q, err)
	}
	switch value := v.(type) {
	case *model.Scalar:
		return finiteOrErr(q, float64(value.Value))
	case model.Vector:
		if len(value) == 0 {
			return 0, fmt.Errorf("prometheusReader: query %q empty vector: %w", q, ErrNoMetrics)
		}
		return finiteOrErr(q, float64(value[0].Value))
	default:
		return 0, fmt.Errorf("prometheusReader: query %q unsupported result type %T: %w", q, v, ErrNoMetrics)
	}
}

func finiteOrErr(q string, f float64) (float64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("prometheusReader: query %q non-finite (%v): %w", q, f, ErrNoMetrics)
	}
	return f, nil
}
