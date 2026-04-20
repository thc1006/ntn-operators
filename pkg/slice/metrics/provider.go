/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics

import (
	"errors"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// Provider maps an NTNSlice CR to a Reader selected from its spec.
//
// For slices using annotation-based simulation a fresh stateless reader
// is returned on every call. For slices using Prometheus the Provider
// keeps one staleCache-wrapped reader per (UID, spec-fingerprint), so:
//   - repeated reconciles of an unchanged CR see the same stale cache
//     (otherwise every reconcile would start from cold and cancel the
//     whole point of D1);
//   - a spec change — endpoint, queries, timeout — invalidates the
//     cached reader so stale values from the previous configuration do
//     not leak into the new one.
type Provider struct {
	pool *ClientPool

	mu      sync.Mutex
	readers map[readerKey]Reader

	// annotationSingleton is shared across all annotation-mode callers;
	// annotationReader is stateless so re-allocating per call wastes
	// nothing but a word of memory, but a singleton keeps pointer
	// identity predictable in tests and in the controller's logs.
	annotationSingleton Reader
}

// readerKey identifies a cached Prometheus-mode reader. The UID alone is
// not enough: a user who edits endpoint or queries must not continue to
// see stale values derived from the old configuration.
type readerKey struct {
	uid         types.UID
	fingerprint string
}

// NewProvider returns an empty Provider. Panics on nil pool — the caller
// is expected to wire a ClientPool at startup time.
func NewProvider(pool *ClientPool) *Provider {
	if pool == nil {
		panic("metrics.NewProvider: nil ClientPool")
	}
	return &Provider{
		pool:                pool,
		readers:             map[readerKey]Reader{},
		annotationSingleton: NewAnnotationReader(),
	}
}

// For returns the Reader appropriate for ns according to
// ns.Spec.MetricsSource. An explicit or implicit "annotations" type
// returns the shared annotation reader; "prometheus" returns a
// staleCache-wrapped prometheusReader constructed from the spec.
func (p *Provider) For(ns *ntnv1alpha1.NTNSlice) (Reader, error) {
	if ns == nil {
		return nil, errors.New("provider: nil NTNSlice")
	}
	src := ns.Spec.MetricsSource
	if src == nil || src.Type == "" || src.Type == ntnv1alpha1.MetricsSourceAnnotations {
		return p.annotationSingleton, nil
	}
	if src.Type != ntnv1alpha1.MetricsSourcePrometheus {
		return nil, fmt.Errorf("provider: unsupported metricsSource.type %q", src.Type)
	}
	if src.Prometheus == nil {
		return nil, errors.New("provider: metricsSource.type=prometheus requires prometheus block")
	}

	key := readerKey{uid: ns.UID, fingerprint: prometheusFingerprint(src.Prometheus)}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.readers[key]; ok {
		return r, nil
	}
	client, err := p.pool.Get(src.Prometheus.Endpoint)
	if err != nil {
		return nil, err
	}
	cfg := PrometheusConfig{
		Queries: Queries{
			RsrpDbm:           src.Prometheus.Queries.RsrpDbm,
			LatencyMs:         src.Prometheus.Queries.LatencyMs,
			PacketLossPercent: src.Prometheus.Queries.PacketLossPercent,
		},
	}
	if src.Prometheus.QueryTimeout != nil {
		cfg.Timeout = src.Prometheus.QueryTimeout.Duration
	}
	reader := NewStaleCache(NewPrometheusReader(client, cfg))
	// Evict any prior entry for this UID with a different fingerprint —
	// the user changed the spec; keeping the old reader would leak stale
	// values from the old endpoint.
	for k := range p.readers {
		if k.uid == ns.UID && k.fingerprint != key.fingerprint {
			delete(p.readers, k)
		}
	}
	p.readers[key] = reader
	return reader, nil
}

// prometheusFingerprint captures the fields whose change should trigger a
// rebuild of the cached reader. The encoding is intentionally simple: a
// typo in a PromQL string is a different fingerprint from a corrected one.
func prometheusFingerprint(p *ntnv1alpha1.PrometheusMetricsSource) string {
	timeout := "0"
	if p.QueryTimeout != nil {
		timeout = p.QueryTimeout.Duration.String()
	}
	return p.Endpoint + "|" +
		p.Queries.RsrpDbm + "|" +
		p.Queries.LatencyMs + "|" +
		p.Queries.PacketLossPercent + "|" +
		timeout
}
