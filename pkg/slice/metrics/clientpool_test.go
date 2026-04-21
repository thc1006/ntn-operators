/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics_test

import (
	"sync"
	"testing"

	"github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

func TestClientPool_SameEndpointReturnsSameClient(t *testing.T) {
	p := metrics.NewClientPool()
	a, err := p.Get("http://prom.example:9090")
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	b, err := p.Get("http://prom.example:9090")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if a != b {
		t.Error("pool must return the same QueryClient for identical endpoint")
	}
}

func TestClientPool_DistinctEndpointsReturnDistinctClients(t *testing.T) {
	p := metrics.NewClientPool()
	a, _ := p.Get("http://prom-a:9090")
	b, _ := p.Get("http://prom-b:9090")
	if a == b {
		t.Error("pool must not share a client across different endpoints")
	}
}

func TestClientPool_EmptyEndpointIsRejected(t *testing.T) {
	p := metrics.NewClientPool()
	_, err := p.Get("")
	if err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

func TestClientPool_InvalidEndpointIsRejected(t *testing.T) {
	p := metrics.NewClientPool()
	// prometheus/client_golang api.NewClient rejects URLs it cannot parse.
	_, err := p.Get("::not a url::")
	if err == nil {
		t.Fatal("expected error for malformed endpoint")
	}
}

func TestClientPool_ConcurrentGetIsSafe(t *testing.T) {
	p := metrics.NewClientPool()
	const endpoint = "http://prom.example:9090"
	var wg sync.WaitGroup
	results := make(chan metrics.QueryClient, 16)
	for range 16 {
		wg.Go(func() {
			c, err := p.Get(endpoint)
			if err == nil {
				results <- c
			}
		})
	}
	wg.Wait()
	close(results)
	var first metrics.QueryClient
	for c := range results {
		if first == nil {
			first = c
			continue
		}
		if c != first {
			t.Error("concurrent Get returned different clients for same endpoint")
		}
	}
}
