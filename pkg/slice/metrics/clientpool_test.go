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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thc1006/ntn-operators/pkg/netutil"
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

// TestClientPool_SafeClientBlocksPrivateIP proves the dial-level SSRF guard
// (findings.md I-24): with WithSafeClient and an empty allowlist, a query to the
// cloud-metadata / link-local address is blocked at DIAL (building the client
// still succeeds — the block is not at construction).
func TestClientPool_SafeClientBlocksPrivateIP(t *testing.T) {
	p := metrics.NewClientPool(metrics.WithSafeClient(netutil.EndpointAllowlist{}))
	c, err := p.Get("http://169.254.169.254:9090") // AWS/GCP/Azure IMDS
	if err != nil {
		t.Fatalf("building the client must succeed (SSRF block is at dial, not build): %v", err)
	}
	_, qerr := c.Query(context.Background(), "up", time.Unix(0, 0))
	if qerr == nil {
		t.Fatal("a query to the link-local metadata IP must be blocked at dial")
	}
	if !errors.Is(qerr, netutil.ErrPrivateIP) && !strings.Contains(qerr.Error(), "private") {
		t.Errorf("expected a private-IP block, got: %v", qerr)
	}
}

// TestClientPool_SafeClientAllowsListedHost proves an allow-listed host bypasses
// the private-IP block, so an in-cluster Prometheus keeps working once its host
// is listed. localhost resolves to loopback (a private IP); with it allow-listed
// the dial proceeds and fails for a NON-SSRF reason (connection refused), never
// ErrPrivateIP.
func TestClientPool_SafeClientAllowsListedHost(t *testing.T) {
	allow := netutil.ParseEndpointAllowlist("localhost")
	p := metrics.NewClientPool(metrics.WithSafeClient(allow))
	c, err := p.Get("http://localhost:9090")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, qerr := c.Query(context.Background(), "up", time.Unix(0, 0))
	if qerr != nil && errors.Is(qerr, netutil.ErrPrivateIP) {
		t.Errorf("an allow-listed host must bypass the private-IP block, got: %v", qerr)
	}
}

// TestClientPool_NoSafeClientDoesNotBlock documents that WITHOUT WithSafeClient
// (the test/lazy-default construction) there is no dial guard — this is why
// production MUST pass WithSafeClient.
func TestClientPool_NoSafeClientDoesNotBlock(t *testing.T) {
	p := metrics.NewClientPool() // no safe client
	c, err := p.Get("http://169.254.169.254:9090")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// The dial is NOT blocked by an SSRF guard; it fails only by connect
	// error/timeout. Assert it is not the ErrPrivateIP sentinel.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, qerr := c.Query(ctx, "up", time.Unix(0, 0))
	if qerr != nil && errors.Is(qerr, netutil.ErrPrivateIP) {
		t.Errorf("plain pool must not apply the SSRF guard, got ErrPrivateIP: %v", qerr)
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
