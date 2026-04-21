/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

// Provider maps an NTNSlice CR to a Reader selected from its spec.
//
// For slices using annotation-based simulation the same stateless
// annotationReader is returned on every call; annotations carry no
// cross-call state, so a singleton is sound. For slices using
// Prometheus the Provider keeps one staleCache-wrapped reader per
// (namespace, name, UID, spec-fingerprint), so:
//   - repeated reconciles of an unchanged CR see the same stale cache
//     (otherwise every reconcile would start from cold and cancel the
//     whole point of D1);
//   - a spec change — endpoint, queries, timeout — invalidates the
//     cached reader so stale values from the previous configuration do
//     not leak into the new one;
//   - on CR deletion the reconciler calls Evict so the entry does not
//     linger forever in a long-lived controller.
type Provider struct {
	pool *ClientPool

	mu      sync.RWMutex
	readers map[readerKey]Reader

	// annotationSingleton is shared across all annotation-mode callers;
	// annotationReader is stateless so re-allocating per call would waste
	// a word of memory for no benefit, and a singleton keeps pointer
	// identity predictable in tests and in the controller's logs.
	annotationSingleton Reader
}

// readerKey identifies a cached Prometheus-mode reader. The UID alone
// is not enough: a user who edits endpoint or queries must not continue
// to see stale values derived from the old configuration; namespace +
// name are carried along so a deleted NTNSlice can be evicted without
// the caller needing to remember its UID.
type readerKey struct {
	namespace   string
	name        string
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
//
// Prometheus mode requires a UID because the stale cache is keyed on
// it; refusing empty-UID callers catches programmer errors up front
// rather than letting two unsaved objects masquerade as the same
// cache tenant.
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
	if ns.UID == "" {
		return nil, errors.New("provider: prometheus mode requires a saved NTNSlice (UID)")
	}

	key := readerKey{
		namespace:   ns.Namespace,
		name:        ns.Name,
		uid:         ns.UID,
		fingerprint: prometheusFingerprint(src.Prometheus),
	}

	// Fast path: read-locked cache lookup.
	p.mu.RLock()
	if r, ok := p.readers[key]; ok {
		p.mu.RUnlock()
		return r, nil
	}
	p.mu.RUnlock()

	// Build outside the write lock so slow clients for unrelated
	// endpoints do not serialise all Provider.For callers.
	reader, err := p.buildReader(src.Prometheus)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.readers[key]; ok {
		return existing, nil
	}
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

// Evict removes every cached reader that matches key.Namespace and
// key.Name, and also drops the {namespace, name}-labelled series from
// ReaderStaleUsedTotal so a deleted NTNSlice leaves no state behind
// (neither in-memory cache nor in-process Prometheus series).
// Called by the reconciler on NotFound. Safe to call with a name
// that has no cached entry.
func (p *Provider) Evict(key client.ObjectKey) {
	p.mu.Lock()
	for k := range p.readers {
		if k.namespace == key.Namespace && k.name == key.Name {
			delete(p.readers, k)
		}
	}
	p.mu.Unlock()
	ntnmetrics.ReaderStaleUsedTotal.DeletePartialMatch(prometheus.Labels{
		"namespace": key.Namespace,
		"name":      key.Name,
	})
}

func (p *Provider) buildReader(src *ntnv1alpha1.PrometheusMetricsSource) (Reader, error) {
	qclient, err := p.pool.Get(src.Endpoint)
	if err != nil {
		return nil, err
	}
	cfg := PrometheusConfig{
		Queries: Queries{
			RsrpDbm:           src.Queries.RsrpDbm,
			LatencyMs:         src.Queries.LatencyMs,
			PacketLossPercent: src.Queries.PacketLossPercent,
		},
	}
	if src.QueryTimeout != nil {
		cfg.Timeout = src.QueryTimeout.Duration
	}
	return NewStaleCache(NewPrometheusReader(qclient, cfg)), nil
}

// prometheusFingerprint captures the fields whose change should trigger
// a rebuild of the cached reader. JSON-encoded rather than delimiter-
// joined so that PromQL regex matchers (which commonly contain "|")
// cannot collide across field boundaries — the earlier "|"-separated
// encoding let ("rsrp=b|c", "latency=d") and ("rsrp=b", "latency=c|d")
// fingerprint identically. Timeout is canonicalised through
// time.Duration so an unset (nil) timeout and an explicit zero-length
// one fingerprint identically; both mean "fall back to the default".
func prometheusFingerprint(p *ntnv1alpha1.PrometheusMetricsSource) string {
	var timeout time.Duration
	if p.QueryTimeout != nil {
		timeout = p.QueryTimeout.Duration
	}
	fp := struct {
		Endpoint          string        `json:"e"`
		RsrpDbm           string        `json:"r"`
		LatencyMs         string        `json:"l"`
		PacketLossPercent string        `json:"p"`
		Timeout           time.Duration `json:"t"`
	}{
		Endpoint:          p.Endpoint,
		RsrpDbm:           p.Queries.RsrpDbm,
		LatencyMs:         p.Queries.LatencyMs,
		PacketLossPercent: p.Queries.PacketLossPercent,
		Timeout:           timeout,
	}
	// json.Marshal of a plain struct is deterministic: field order is
	// the Go declaration order. Errors from Marshal on this shape are
	// impossible, so ignore.
	b, _ := json.Marshal(fp)
	return string(b)
}
