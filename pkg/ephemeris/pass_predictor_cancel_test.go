/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ephemeris

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

// TestPredictPasses_ContextCancelled pins #233: a cancelled/timed-out context aborts the sweep
// and PredictPasses returns ctx.Err() rather than running the full O(horizon × sats × stations)
// work and returning partial windows. Mutation: removing the ctx.Err() checks makes it return
// (passes, nil), so this fails.
func TestPredictPasses_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before any work runs

	passes, err := PredictPasses(ctx,
		[]sgp4.OMM{issOMM()}, []GroundStation{montrealStation},
		10, 24*time.Hour, nil, time.Now())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context must return context.Canceled; got passes=%d err=%v", len(passes), err)
	}
	if passes != nil {
		t.Errorf("no partial pass windows should be returned on cancellation, got %d", len(passes))
	}
}

// TestPredictPasses_CancelStopsDispatchingQueuedWork proves the per-worker ctx.Err() check does
// real work — it stops the pool from dispatching QUEUED items once the context is cancelled. The
// pre-cancel test above passes even with that check removed (the final return catches ctx.Err),
// so it cannot pin the between-item guarantee. Here 32 items are queued but only 8 (one per worker)
// are in flight when the context is cancelled; the remaining 24 must never reach predictSingleFn.
// Mutation: remove the worker's `if ctx.Err() != nil { return }` and all 32 run, so calls != 8.
func TestPredictPasses_CancelStopsDispatchingQueuedWork(t *testing.T) {
	const workers = 8
	omms := make([]sgp4.OMM, 32)
	for i := range omms {
		o := issOMM()
		o.NoradCatID = 40000 + i
		omms[i] = o
	}
	stations := []GroundStation{montrealStation}

	var calls atomic.Int64
	release := make(chan struct{})
	var firstBatch sync.WaitGroup
	firstBatch.Add(workers)

	orig := predictSingleFn
	defer func() { predictSingleFn = orig }()
	predictSingleFn = func(_ sgp4.OMM, _ GroundStation, _, _ time.Time, _ float64) ([]PassResult, error) {
		if n := calls.Add(1); n <= workers {
			firstBatch.Done() // this in-flight worker has started its single item
			<-release         // hold it here until the test has cancelled the context
		}
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var result error
	go func() {
		_, result = PredictPasses(ctx, omms, stations, 10, 24*time.Hour, nil, time.Now())
		close(done)
	}()

	firstBatch.Wait() // all 8 workers are now blocked inside predictSingleFn (8 items in flight)
	cancel()          // cancel while 24 items are still queued
	close(release)    // let the 8 in-flight items complete; workers then see ctx.Err and stop
	<-done

	if !errors.Is(result, context.Canceled) {
		t.Fatalf("cancelled PredictPasses must return context.Canceled, got %v", result)
	}
	if got := calls.Load(); got != workers {
		t.Fatalf("after cancel, only the %d in-flight items may run; the %d queued items must not be "+
			"dispatched, but predictSingleFn was called %d times", workers, len(omms)-workers, got)
	}
}
