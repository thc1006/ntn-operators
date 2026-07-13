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
