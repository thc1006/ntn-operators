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
type ClientPool struct {
	mu      sync.Mutex
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[endpoint]; ok {
		return c, nil
	}
	c, err := newPromClient(endpoint)
	if err != nil {
		return nil, fmt.Errorf("clientpool: build client for %q: %w", endpoint, err)
	}
	p.clients[endpoint] = c
	return c, nil
}

// newPromClient constructs a QueryClient wrapping the upstream
// prometheus/client_golang v1 API. Extracted for testability: tests can
// swap this package var to inject a fake builder if future cycles require
// exercising pool behaviour without touching the network stack.
var newPromClient = func(endpoint string) (QueryClient, error) {
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
