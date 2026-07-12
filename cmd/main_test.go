/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestCacheSyncReadyCheck pins that /readyz fails until caches sync and passes after.
// Mutation guard: inverting the `ready.Load()` branch fails one of the two assertions.
func TestCacheSyncReadyCheck(t *testing.T) {
	var ready atomic.Bool
	check := cacheSyncReadyCheck(&ready)

	if err := check(nil); err == nil {
		t.Fatal("readyz must FAIL before informer caches sync (ready=false)")
	}
	ready.Store(true)
	if err := check(nil); err != nil {
		t.Fatalf("readyz must PASS once caches sync (ready=true), got %v", err)
	}
}

// TestCacheReadyRunnable_NeedLeaderElectionFalse is the load-bearing contract: if this
// runnable needed leader election it would run only on the elected leader, so a standby's
// readyz would never pass and rollouts would deadlock. It MUST be a non-leader runnable.
func TestCacheReadyRunnable_NeedLeaderElectionFalse(t *testing.T) {
	if (cacheReadyRunnable{}).NeedLeaderElection() {
		t.Fatal("cacheReadyRunnable.NeedLeaderElection() must be false (non-leader runnable) " +
			"or the standby never becomes Ready and rollouts deadlock")
	}
}

// TestCacheReadyRunnable_StartSetsFlagThenBlocks proves Start flips the flag (so readyz
// can pass) and then blocks until context cancel (so it does not exit and get restarted).
func TestCacheReadyRunnable_StartSetsFlagThenBlocks(t *testing.T) {
	var ready atomic.Bool
	r := cacheReadyRunnable{ready: &ready}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	// Flag must be set essentially immediately (before Start blocks on ctx.Done).
	deadline := time.Now().Add(2 * time.Second)
	for !ready.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !ready.Load() {
		t.Fatal("Start must set ready=true before blocking on ctx.Done")
	}

	// It must still be running (blocked) — done should not have fired yet.
	select {
	case err := <-done:
		t.Fatalf("Start returned before ctx cancel (err=%v); it must block", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v after ctx cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}
