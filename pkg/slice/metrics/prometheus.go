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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

const sourceLabelPrometheus = "prometheus"

// Error-reason label values for ReaderErrorsTotal.
const (
	reasonQueryError      = "query_error"
	reasonEmptyVector     = "empty_vector"
	reasonAmbiguousVector = "ambiguous_vector"
	reasonNonFinite       = "non_finite"
	reasonUnsupportedType = "unsupported_type"
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

// PrometheusConfig configures a prometheusReader. Timeout is the hard
// limit applied to each single PromQL fetch, not the entire Read call;
// a Read that issues three queries with Timeout=2s therefore has an
// upper-bound wall-clock cost of ~6s before it returns.
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

	// Start with every field marked missing (I-10): a field is only "present"
	// once a configured query returns a real value. An empty query leaves the
	// placeholder AND the Missing flag, so a trigger over it is surfaced as inert
	// rather than silently evaluated against the healthy default.
	m := defaultMetrics
	m.RSRPMissing, m.LatencyMissing, m.PacketLossMissing = true, true, true
	fields := []struct {
		query   string
		set     func(float64)
		present func()
	}{
		{r.queries.RsrpDbm, func(v float64) { m.RSRP = v }, func() { m.RSRPMissing = false }},
		{r.queries.LatencyMs, func(v float64) { m.LatencyMs = v }, func() { m.LatencyMissing = false }},
		{r.queries.PacketLossPercent, func(v float64) { m.PacketLossPercent = v }, func() { m.PacketLossMissing = false }},
	}
	for _, f := range fields {
		if f.query == "" {
			continue
		}
		// Per-query timeout: r.timeout is the budget for a single PromQL
		// fetch, not the whole Read. A slow query burning the whole budget
		// must not starve the other metrics.
		qctx, cancel := context.WithTimeout(ctx, r.timeout)
		v, err := r.fetch(qctx, f.query)
		cancel()
		if err != nil {
			return Result{}, err
		}
		f.set(v)
		f.present() // real value obtained → the field is present (I-10)
	}
	return Result{Metrics: m}, nil
}

// fetch issues a single instant query, extracts a finite float64, and
// emits observability signals: the duration is always recorded (split by
// success / error outcome) and a reason-labelled error counter is bumped
// on every non-success path. Empty vectors and non-finite values surface
// as ErrNoMetrics so the caller sees a uniform "this metric is
// unobservable" signal.
func (r *prometheusReader) fetch(ctx context.Context, q string) (result float64, err error) {
	start := time.Now()
	var reason string
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "error"
		}
		ntnmetrics.ReaderQueryDuration.With(prometheus.Labels{
			"source": sourceLabelPrometheus, "outcome": outcome,
		}).Observe(time.Since(start).Seconds())
		if reason != "" {
			ntnmetrics.ReaderErrorsTotal.With(prometheus.Labels{
				"source": sourceLabelPrometheus, "reason": reason,
			}).Inc()
		}
	}()
	v, qerr := r.client.Query(ctx, q, r.now())
	if qerr != nil {
		reason = reasonQueryError
		return 0, fmt.Errorf("prometheusReader: query %q: %w", q, qerr)
	}
	switch value := v.(type) {
	case *model.Scalar:
		f := float64(value.Value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			reason = reasonNonFinite
			return 0, fmt.Errorf("prometheusReader: query %q non-finite (%v): %w", q, f, ErrNoMetrics)
		}
		return f, nil
	case model.Vector:
		switch len(value) {
		case 0:
			reason = reasonEmptyVector
			return 0, fmt.Errorf("prometheusReader: query %q empty vector: %w", q, ErrNoMetrics)
		case 1:
			// fall through to finite check below
		default:
			// Multiple samples means the PromQL matched more than one
			// time series and the "right" one is not obvious. Forcing the
			// user to reduce it with avg() / max() / a more specific
			// selector is safer than picking value[0] nondeterministically.
			reason = reasonAmbiguousVector
			return 0, fmt.Errorf(
				"prometheusReader: query %q returned %d samples, expected exactly one: %w",
				q, len(value), ErrNoMetrics)
		}
		f := float64(value[0].Value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			reason = reasonNonFinite
			return 0, fmt.Errorf("prometheusReader: query %q non-finite (%v): %w", q, f, ErrNoMetrics)
		}
		return f, nil
	default:
		reason = reasonUnsupportedType
		return 0, fmt.Errorf("prometheusReader: query %q unsupported result type %T: %w", q, v, ErrNoMetrics)
	}
}
