/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package metrics produces network path quality measurements for an NTNSlice
// from a configurable source (annotations, Prometheus, ...).
//
// This package is the single choke point between failover decision logic
// (pkg/slice) and whatever happens to be providing live values in a given
// deployment. Callers receive a fully-formed slice.Metrics and a freshness
// flag; readers are responsible for translating their source into that
// contract and for signalling unavailability via ErrNoMetrics.
package metrics

import (
	"context"
	"errors"
	"time"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
)

// ErrNoMetrics is returned when a reader cannot produce any value — neither
// fresh nor stale. The controller is expected to translate this into a
// FailoverReady=Unknown condition rather than treat the slice as healthy.
var ErrNoMetrics = errors.New("metrics: no value available")

// Result carries measured metrics together with freshness information.
//
// Stale=true means the underlying source was unavailable but a previously
// observed value was returned. Failover logic may still act on a stale
// value; observability must surface that the source is degraded.
//
// LastFreshAt records when the returned values were last observed as fresh
// by the reader chain. For a fresh return LastFreshAt equals the time of
// the current read; for a stale return it is the timestamp of the previous
// successful read. A zero value means the reader does not track freshness
// (e.g., annotationReader, which has no notion of "out of date").
//
// ObservedAt is the SOURCE timestamp of the underlying observation (for Prometheus,
// model.Sample.Timestamp), NOT the read time — so a stale-cache replay carries the
// original observation's timestamp, and a reachable source that keeps returning the
// same (stalled-scrape) sample carries an unchanged timestamp. The anti-flap engine
// counts a confirmation only when ObservedAt advances, so a replayed or duplicate
// observation cannot falsely satisfy confirmationSamples (#203-M3). A zero value means
// the reader cannot supply a source timestamp; the engine then falls back to counting
// each call (the prior behavior).
type Result struct {
	Metrics     slice.Metrics
	Stale       bool
	LastFreshAt time.Time
	ObservedAt  time.Time
}

// Reader produces path quality measurements for a single NTNSlice.
// Implementations must be safe for concurrent use by multiple goroutines.
type Reader interface {
	Read(ctx context.Context, ns *ntnv1alpha1.NTNSlice) (Result, error)
}
