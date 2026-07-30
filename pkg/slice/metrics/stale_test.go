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
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	"github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// scriptedReader returns each scripted Result/error in order, one per Read.
// Further calls panic — tests that mis-match their script should fail fast.
type scriptedReader struct {
	mu      sync.Mutex
	script  []scriptStep
	calls   int
	lastCtx context.Context //nolint:containedctx // assertion only
}

type scriptStep struct {
	res metrics.Result
	err error
}

func (s *scriptedReader) Read(ctx context.Context, _ *ntnv1alpha1.NTNSlice) (metrics.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCtx = ctx
	if s.calls >= len(s.script) {
		panic("scriptedReader: call beyond script")
	}
	step := s.script[s.calls]
	s.calls++
	return step.res, step.err
}

func nsWithUID(uid string) *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{ObjectMeta: metav1.ObjectMeta{
		Name: "s", Namespace: "default", UID: types.UID(uid),
	}}
}

func TestStaleCache_FirstSuccess_Cached(t *testing.T) {
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -95, LatencyMs: 30, PacketLossPercent: 0.3}}},
	}}
	c := metrics.NewStaleCache(inner)
	got, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Stale {
		t.Error("fresh first read must not be stale")
	}
	if got.Metrics.RSRP != -95 {
		t.Errorf("got %v want -95", got.Metrics.RSRP)
	}
}

// TestStaleCache_ReplayPreservesObservedAt pins #203-M3's plumbing: a stale-cache replay must carry
// the ORIGINAL observation's source timestamp, not a new one, so the anti-flap engine sees the same
// ObservedAt and does not count the replay as a fresh confirmation.
func TestStaleCache_ReplayPreservesObservedAt(t *testing.T) {
	obs := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -95}, ObservedAt: obs}},
		{err: metrics.ErrNoMetrics},
	}}
	c := metrics.NewStaleCache(inner)
	fresh, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil || !fresh.ObservedAt.Equal(obs) {
		t.Fatalf("fresh read must carry the source ObservedAt %v, got %v (err=%v)", obs, fresh.ObservedAt, err)
	}
	stale, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil {
		t.Fatalf("stale replay error: %v", err)
	}
	if !stale.Stale {
		t.Fatal("replay must be marked stale")
	}
	if !stale.ObservedAt.Equal(obs) {
		t.Fatalf("a stale replay must preserve the original ObservedAt %v, got %v — else the anti-flap "+
			"would treat the replay as a new observation (#203-M3)", obs, stale.ObservedAt)
	}
}

func TestStaleCache_ErrorAfterSuccess_ReturnsStale(t *testing.T) {
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -95, LatencyMs: 30, PacketLossPercent: 0.3}}},
		{err: metrics.ErrNoMetrics},
	}}
	c := metrics.NewStaleCache(inner)
	_, _ = c.Read(context.Background(), nsWithUID("u1"))
	got, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil {
		t.Fatalf("error on second call was not absorbed: %v", err)
	}
	if !got.Stale {
		t.Error("subsequent error must yield stale=true")
	}
	if got.Metrics.RSRP != -95 {
		t.Errorf("wrong cached value returned: %+v", got.Metrics)
	}
}

func TestStaleCache_ErrorWithEmptyCache_PropagatesError(t *testing.T) {
	sentinel := errors.New("prom down")
	inner := &scriptedReader{script: []scriptStep{{err: sentinel}}}
	c := metrics.NewStaleCache(inner)
	_, err := c.Read(context.Background(), nsWithUID("u1"))
	if err == nil {
		t.Fatal("expected error when underlying fails and cache is empty")
	}
	// The underlying error must remain retrievable for debugging...
	if !errors.Is(err, sentinel) {
		t.Fatalf("want wrapped sentinel, got %v", err)
	}
	// ...and the Reader contract says "no usable value, fresh or stale"
	// must also be expressible as ErrNoMetrics so callers can take the
	// MetricsUnavailable branch uniformly regardless of which inner
	// failure class got us here.
	if !errors.Is(err, metrics.ErrNoMetrics) {
		t.Fatalf("empty-cache path must also wrap ErrNoMetrics, got %v", err)
	}
}

func TestStaleCache_SuccessAfterStale_RefreshesAndMarksFresh(t *testing.T) {
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -95, LatencyMs: 30, PacketLossPercent: 0.3}}},
		{err: metrics.ErrNoMetrics},
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -80, LatencyMs: 20, PacketLossPercent: 0.1}}},
	}}
	c := metrics.NewStaleCache(inner)
	_, _ = c.Read(context.Background(), nsWithUID("u1"))
	_, _ = c.Read(context.Background(), nsWithUID("u1"))
	got, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Stale {
		t.Error("fresh read after recovery must have stale=false")
	}
	if got.Metrics.RSRP != -80 {
		t.Errorf("refreshed value not seen: %+v", got.Metrics)
	}
}

func TestStaleCache_PerUIDIsolation(t *testing.T) {
	// Two NTNSlice UIDs should have independent cache slots.
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -90}}},  // u1 success
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -120}}}, // u2 success
		{err: metrics.ErrNoMetrics},                               // u1 fails -> stale u1 (-90)
		{err: metrics.ErrNoMetrics},                               // u2 fails -> stale u2 (-120)
	}}
	c := metrics.NewStaleCache(inner)
	_, _ = c.Read(context.Background(), nsWithUID("u1"))
	_, _ = c.Read(context.Background(), nsWithUID("u2"))
	got1, _ := c.Read(context.Background(), nsWithUID("u1"))
	got2, _ := c.Read(context.Background(), nsWithUID("u2"))
	if got1.Metrics.RSRP != -90 {
		t.Errorf("u1 stale leaked: %+v", got1.Metrics)
	}
	if got2.Metrics.RSRP != -120 {
		t.Errorf("u2 stale leaked: %+v", got2.Metrics)
	}
	if !got1.Stale || !got2.Stale {
		t.Error("both should be stale")
	}
}

func TestStaleCache_MissingUID_Rejected(t *testing.T) {
	inner := &scriptedReader{script: []scriptStep{}}
	c := metrics.NewStaleCache(inner)
	ns := &ntnv1alpha1.NTNSlice{ObjectMeta: metav1.ObjectMeta{Name: "x"}} // no UID
	_, err := c.Read(context.Background(), ns)
	if err == nil {
		t.Fatal("expected error for NTNSlice without UID")
	}
}

func TestStaleCache_NilNTNSlice_Rejected(t *testing.T) {
	inner := &scriptedReader{script: []scriptStep{}}
	_, err := metrics.NewStaleCache(inner).Read(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil NTNSlice")
	}
}

func TestStaleCache_NilInner_RejectedAtConstruction(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil inner reader")
		}
	}()
	_ = metrics.NewStaleCache(nil)
}

func TestStaleCache_ParentContextCanceled_PropagatesInsteadOfServingStale(t *testing.T) {
	// If the parent context is canceled (controller shutdown, manager
	// deadline, explicit caller cancel), the caller is abandoning the
	// reconcile. Returning a stale value with err=nil would let them
	// keep running against a dead context. The staleCache must propagate
	// ctx.Err() instead, even when it has a cached value it *could* have
	// served.
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -90}}}, // first call: success (populates cache)
		{err: metrics.ErrNoMetrics},                              // second call: inner reports failure
	}}
	c := metrics.NewStaleCache(inner)
	ns := nsWithUID("u-ctx")

	if _, err := c.Read(context.Background(), ns); err != nil {
		t.Fatalf("first fresh read failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // kill the parent before the second Read returns
	_, err := c.Read(ctx, ns)
	if err == nil {
		t.Fatal("staleCache must not swallow a dead parent ctx by returning a cached value")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
}

func TestStaleCache_LastFreshAt_TracksFreshnessAcrossStaleReturns(t *testing.T) {
	// Scenario: success -> fail -> fail.
	// LastFreshAt returned on stale reads must equal the timestamp of the
	// first success, not the zero value, so callers can age-out stale data.
	inner := &scriptedReader{script: []scriptStep{
		{res: metrics.Result{Metrics: slice.Metrics{RSRP: -90}}},
		{err: metrics.ErrNoMetrics},
		{err: metrics.ErrNoMetrics},
	}}
	c := metrics.NewStaleCache(inner)
	first, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.LastFreshAt.IsZero() {
		t.Fatal("fresh read must set LastFreshAt")
	}
	freshAt := first.LastFreshAt

	stale1, err := c.Read(context.Background(), nsWithUID("u1"))
	if err != nil {
		t.Fatalf("stale read 1: %v", err)
	}
	if !stale1.LastFreshAt.Equal(freshAt) {
		t.Errorf("stale LastFreshAt must equal prior fresh time: got %v want %v", stale1.LastFreshAt, freshAt)
	}

	stale2, _ := c.Read(context.Background(), nsWithUID("u1"))
	if !stale2.LastFreshAt.Equal(freshAt) {
		t.Errorf("LastFreshAt must not drift across successive stale returns, got %v", stale2.LastFreshAt)
	}
}

func TestStaleCache_ConcurrentReads_AreSafe(t *testing.T) {
	// Race detector will flag concurrent map access if the cache lacks
	// a proper lock. Two goroutines read the same UID repeatedly.
	script := make([]scriptStep, 100)
	for i := range script {
		script[i] = scriptStep{res: metrics.Result{Metrics: slice.Metrics{RSRP: -90}}}
	}
	inner := &scriptedReader{script: script}
	c := metrics.NewStaleCache(inner)

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range 50 {
				_, _ = c.Read(context.Background(), nsWithUID("u1"))
			}
		})
	}
	wg.Wait()
}
