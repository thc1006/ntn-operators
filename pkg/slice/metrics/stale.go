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

	"k8s.io/apimachinery/pkg/types"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
)

// staleCache wraps a Reader so a transient failure of the underlying source
// is surfaced as a stale-but-usable value rather than "everything is fine"
// (which would suppress failover) or "no data" (which would keep a slice
// indefinitely parked in Unknown). The cache is keyed by NTNSlice UID,
// which is stable across renames and persists for the lifetime of the CR.
type staleCache struct {
	inner Reader
	mu    sync.Mutex
	cache map[types.UID]slice.Metrics
}

// NewStaleCache returns a Reader that remembers the last fresh observation
// per NTNSlice. Panics if inner is nil — the caller must supply a concrete
// reader at wiring time.
func NewStaleCache(inner Reader) Reader {
	if inner == nil {
		panic("metrics.NewStaleCache: nil inner reader")
	}
	return &staleCache{inner: inner, cache: map[types.UID]slice.Metrics{}}
}

func (c *staleCache) Read(ctx context.Context, ns *ntnv1alpha1.NTNSlice) (Result, error) {
	if ns == nil {
		return Result{}, errors.New("staleCache: nil NTNSlice")
	}
	if ns.UID == "" {
		return Result{}, errors.New("staleCache: NTNSlice missing UID")
	}
	res, err := c.inner.Read(ctx, ns)
	if err == nil {
		c.mu.Lock()
		c.cache[ns.UID] = res.Metrics
		c.mu.Unlock()
		return res, nil
	}
	c.mu.Lock()
	cached, ok := c.cache[ns.UID]
	c.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("staleCache: no fresh or cached value: %w", err)
	}
	return Result{Metrics: cached, Stale: true}, nil
}
