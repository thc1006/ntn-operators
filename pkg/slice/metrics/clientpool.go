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
	"net/http"
	"sync"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"github.com/thc1006/ntn-operators/pkg/netutil"
)

// ClientPool deduplicates prometheus/client_golang clients by endpoint URL.
// Multiple NTNSlice CRs that point at the same Prometheus share a single
// HTTP transport and its underlying connection pool. The pool has no LRU
// eviction: cluster Prometheus endpoints are few and long-lived, so the
// cost of unbounded growth is bounded in practice.
//
// Lookup uses a double-checked read-then-upgrade pattern so a slow build
// path (e.g., future TLS certificate loading) does not serialise unrelated
// endpoints behind a single write lock.
type ClientPool struct {
	mu      sync.RWMutex
	clients map[string]QueryClient

	// httpClient, when non-nil, is the SSRF-safe HTTP client every pooled
	// Prometheus client dials through: it blocks connections to private/
	// reserved IPs (e.g. the 169.254.169.254 cloud-metadata service, internal
	// ClusterIP services) unless the target host is in the endpoint allowlist.
	// nil = the library default transport (no dial-level SSRF guard) — used
	// only by the test/lazy default; production wires WithSafeClient (I-24).
	httpClient *http.Client
}

// PoolOption configures a ClientPool.
type PoolOption func(*ClientPool)

// WithSafeClient makes every pooled Prometheus client dial through an
// SSRF-safe transport (netutil.NewSafeHTTPClient) that blocks dials to
// private/reserved IPs unless the target host is in allow. This closes the
// blind-SSRF surface where a namespaced NTNSlice.spec.metricsSource.endpoint
// could aim the operator's egress at cloud metadata or internal services
// (findings.md I-24). Public Prometheus endpoints keep working; an in-cluster
// Prometheus at a private ClusterIP must have its host listed in
// --prometheus-allowed-endpoint-hosts (which also gates the hostname), exactly
// as SatelliteEphemeris internal mirrors use --ephemeris-allowed-private-hosts.
// Timeout is 0: per-query deadlines are governed by the reader's
// context.WithTimeout; the dialer keeps its own fixed connect timeout.
func WithSafeClient(allow netutil.EndpointAllowlist) PoolOption {
	return func(p *ClientPool) {
		p.httpClient = netutil.NewSafeHTTPClient(0, netutil.WithPrivateHostAllowlist(allow))
	}
}

// NewClientPool returns an empty pool. Without WithSafeClient the pooled
// clients use the library default transport and have NO dial-level SSRF guard,
// so production callers (cmd/main.go) MUST pass WithSafeClient.
func NewClientPool(opts ...PoolOption) *ClientPool {
	p := &ClientPool{clients: map[string]QueryClient{}}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Get returns a QueryClient for endpoint, creating one on first use.
// Concurrent callers for the same endpoint see the same client.
func (p *ClientPool) Get(endpoint string) (QueryClient, error) {
	if endpoint == "" {
		return nil, errors.New("clientpool: empty endpoint")
	}
	p.mu.RLock()
	c, ok := p.clients[endpoint]
	p.mu.RUnlock()
	if ok {
		return c, nil
	}
	// Cache miss: build outside the lock so concurrent Get calls for other
	// endpoints do not block. The second check under the write lock absorbs
	// the race where two callers miss simultaneously.
	built, err := buildPromClient(endpoint, p.httpClient)
	if err != nil {
		return nil, fmt.Errorf("clientpool: build client for %q: %w", endpoint, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.clients[endpoint]; ok {
		return existing, nil
	}
	p.clients[endpoint] = built
	return built, nil
}

// buildPromClient constructs a QueryClient wrapping the upstream
// prometheus/client_golang v1 API. When httpClient is non-nil it is used as the
// transport, so the SSRF-safe client from WithSafeClient governs every dial;
// nil falls back to the library default transport.
func buildPromClient(endpoint string, httpClient *http.Client) (QueryClient, error) {
	cfg := promapi.Config{Address: endpoint}
	if httpClient != nil {
		cfg.Client = httpClient
	}
	api, err := promapi.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &promClient{api: promv1.NewAPI(api)}, nil
}

// promClient adapts promv1.API to QueryClient. The upstream API returns
// (model.Value, Warnings, error); we drop Warnings here — caller logs go
// through the controller's logger, not this layer.
type promClient struct {
	api promv1.API
}

func (c *promClient) Query(ctx context.Context, q string, ts time.Time) (model.Value, error) {
	v, _, err := c.api.Query(ctx, q, ts)
	return v, err
}
