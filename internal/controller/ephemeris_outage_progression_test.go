/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestReconcile_SustainedOutage_AdvancesClockAcrossMultipleRetryWindows is the SECOND facet of
// the I-18 "serve cache preserves SIB19 continuity" claim (assertSustainedOutageKeepsPropagating
// in ephemeris_continuity_test.go covers the first — the WITHIN-a-single-window behaviour, which
// on a real clock can only ever observe calls==1). Here the outage OUTLASTS the initial 1-minute
// backoff, so the property under test is the retry-window PROGRESSION: the controller must
//
//	(a) re-attempt the upstream fetch exactly ONCE per elapsed backoff window (not every
//	    reconcile, not never), while the transient ramp GROWS 1m → 2m → 4m, and
//	(b) keep re-propagating the cached OMMs to a fresh epoch on EVERY reconcile throughout — so the
//	    fetch-retry state machine never BLOCKS the propagation heartbeat. (That the epoch is stamped
//	    far enough ahead to stay valid BETWEEN propagations is a separate, constant-level property
//	    locked by TestPropagationEpochLeadVsSkewMargin; this test cannot prove it, because its expected
//	    epoch is derived from the same propagationEpochLead constant.)
//
// A single injected clock — shared by cacheServe's suppression gate AND obtainOMMs' backoff arm
// (this is why #237 moved that arm from a bare time.Now() to r.now()) — is stepped to and across each
// nextFetchAttempt, so the whole 7-minute progression runs in microseconds without sleeping.
//
// Mutations this pins:
//   - obtainOMMs arming the backoff from a bare time.Now() instead of r.now(): the injected
//     clock (2026-07-13) would never reach a real-wall-clock nextFetchAttempt, so the fetch
//     would stay suppressed forever — the per-window calls==N assertion fails on window 2.
//   - the transient ramp NOT consuming fetchFailures (a reset every cycle): the delay would
//     stay 1m — the exact nextFetchAttempt and fetchFailures==N assertions fail.
//   - dropping the suppression gate (re-fetch every reconcile): the held sub-cycle's
//     calls-unchanged assertion fails.
//   - propagation stalling under the outage (frozen/not-re-propagated cache): the fresh
//     THIS-cycle-clock+lead epoch assertion fails.
func TestReconcile_SustainedOutage_AdvancesClockAcrossMultipleRetryWindows(t *testing.T) {
	t0 := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	clock := t0 // mutable: every r.now() in a reconcile returns this; stepped BETWEEN reconciles
	sch := makeScheme(t)
	const (
		ns                = "default"
		name              = "eph-outage-progression"
		effectiveInterval = 4 * time.Hour
	)

	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000") // element epoch = t0 → small forward propagation

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: effectiveInterval},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}

	// Upstream is DOWN for the whole test with a TRANSIENT error → the 1m, 2m, 4m ramp.
	fetcher := &mockGPFetcher{err: errors.New("connection refused")}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(200), Fetcher: fetcher,
		Now: func() time.Time { return clock },
	}
	// Seed a last-good cache already older than the 4h window, so the FIRST reconcile at t0
	// genuinely attempts a fetch (no need to jump the clock forward 4h first). UID is empty on
	// both sides (the fake client assigns none), matching obtainOMMs' c.uid == eph.UID gate.
	r.ommCache.Store(key, cachedFetch{
		result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0.Add(-5 * time.Hour),
		},
		fetchKey: fetchInputKey(eph.Spec),
	})

	ctx := context.Background()

	// reconcileAt steps the shared clock to `at`, runs one reconcile, and returns the resulting
	// cache entry (for backoff assertions) and the reconcile result. A serve-cache reconcile must
	// NEVER return an error — an error would make the workqueue override RequeueAfter with its own
	// exponential backoff and starve the propagation heartbeat.
	reconcileAt := func(at time.Time) (cachedFetch, reconcile.Result) {
		clock = at
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("reconcile @ %s: serve-cache reconcile must not error: %v", at.UTC(), err)
		}
		v, ok := r.ommCache.Load(key)
		if !ok {
			t.Fatalf("reconcile @ %s: cache entry vanished — the continuity fallback was lost", at.UTC())
		}
		return v.(cachedFetch), res
	}

	// assertContinuity checks the SIB19 heartbeat held on a cycle whose clock is `at`: requeue on
	// the propagation cadence (NOT the fetch backoff), and a fresh epoch stamped at at+lead.
	assertContinuity := func(label string, at time.Time, res reconcile.Result) {
		t.Helper()
		if res.RequeueAfter <= 0 {
			t.Fatalf("%s: propagation heartbeat disabled — RequeueAfter=%s; controller-runtime only reschedules "+
				"for a POSITIVE duration, so a non-positive cadence stops periodic re-propagation and the epoch "+
				"expires", label, res.RequeueAfter)
		}
		if res.RequeueAfter != propagationRefreshInterval {
			t.Fatalf("%s: requeue must be the propagation cadence %s (the fetch is held separately); "+
				"got %s — the epoch would die before the next reconcile",
				label, propagationRefreshInterval, res.RequeueAfter)
		}
		obj := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, obj); err != nil {
			t.Fatalf("%s: get: %v", label, err)
		}
		if len(obj.Status.PropagatedStates) == 0 {
			t.Fatalf("%s: SIB19 continuity lost — no propagated states while serving cache", label)
		}
		wantEpoch := at.Add(propagationEpochLead).UnixMilli()
		if got := obj.Status.PropagatedStates[0].EpochUnixMs; got != wantEpoch {
			t.Fatalf("%s: epoch = %d, want THIS-cycle clock + lead = %d (a frozen/not-re-propagated "+
				"cache would keep an older epoch)", label, got, wantEpoch)
		}
	}

	// Walk three retry windows. In each we first prove the fetch RE-ATTEMPTS once (and the ramp
	// grew), then prove the backoff SUPPRESSES a fetch just before the next attempt is due — both
	// while propagation stays continuous.
	fetchInstant := t0
	for window := 1; window <= 3; window++ {
		wantDelay := min(time.Minute<<(window-1), effectiveInterval) // 1m, 2m, 4m

		// (i) FETCH DUE: step to this window's fetch instant (>= the previous nextFetchAttempt).
		c, res := reconcileAt(fetchInstant)
		if n := fetcher.callCount(); n != window {
			t.Fatalf("window %d: upstream must be contacted exactly once per elapsed window; "+
				"got %d calls, want %d", window, n, window)
		}
		if c.fetchFailures != window {
			t.Fatalf("window %d: consecutive-failure ramp counter = %d, want %d (a reset every "+
				"cycle would freeze the ramp at 1m)", window, c.fetchFailures, window)
		}
		wantNext := fetchInstant.Add(wantDelay)
		if !c.nextFetchAttempt.Equal(wantNext) {
			t.Fatalf("window %d: nextFetchAttempt = %s, want fetch-instant + %s = %s (the ramp must "+
				"grow 1m→2m→4m off the INJECTED clock, not the wall clock)",
				window, c.nextFetchAttempt.UTC(), wantDelay, wantNext.UTC())
		}
		assertContinuity(fmt.Sprintf("window %d fetch-due", window), fetchInstant, res)

		// (ii) BACKOFF HELD: step to just BEFORE nextFetchAttempt → no new fetch, cache still served.
		held := wantNext.Add(-time.Second)
		_, res = reconcileAt(held)
		if n := fetcher.callCount(); n != window {
			t.Fatalf("window %d: fetch must be SUPPRESSED inside the backoff window; calls jumped "+
				"to %d (want still %d)", window, n, window)
		}
		assertContinuity(fmt.Sprintf("window %d held", window), held, res)

		// Next window's fetch lands EXACTLY on this nextFetchAttempt. The gate uses Before, so
		// now == deadline must re-fetch — this pins the boundary and kills a !now.After() mutation
		// that would keep suppressing at equality (the held sub-cycle above pins now < deadline).
		fetchInstant = wantNext
	}
}
