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
	"sync"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
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
}

// NewClientPool returns an empty pool.
func NewClientPool() *ClientPool {
	return &ClientPool{clients: map[string]QueryClient{}}
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
	built, err := buildPromClient(endpoint)
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
// prometheus/client_golang v1 API.
func buildPromClient(endpoint string) (QueryClient, error) {
	api, err := promapi.NewClient(promapi.Config{Address: endpoint})
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
