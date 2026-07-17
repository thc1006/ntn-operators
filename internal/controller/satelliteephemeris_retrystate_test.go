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
	"strings"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestApplyFetchBackoff_AuthThenTransientRestartsAtOneMinute pins #236: the consecutive-failure
// counter is shared, but only fetchRetryDelay's TRANSIENT branch consumes it. A run of auth
// failures (whose delay ignores the count) must not leave the count high and slam the FIRST
// following transient failure past the 1-minute ramp base.
func TestApplyFetchBackoff_AuthThenTransientRestartsAtOneMinute(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	const interval = 4 * time.Hour
	const key = "same-credential-key"
	c := &cachedFetch{}

	// A run of auth failures: the counter climbs, but the auth delay is a bounded fixed probe.
	for range 4 {
		applyFetchBackoff(c, ephemeris.ErrAuthFailed, key, interval, base)
	}
	if got, want := c.nextFetchAttempt.Sub(base), min(interval, maxAuthRetryBackoff); got != want {
		t.Fatalf("auth backoff = %v, want %v (auth delay must ignore the counter)", got, want)
	}
	if c.fetchFailures != 4 {
		t.Fatalf("an auth run should still accumulate the counter: got %d, want 4", c.fetchFailures)
	}

	// Operator fixes credentials IN PLACE (same Secret → same retryKey), then a genuine transient
	// blip. The transient ramp MUST restart at its 1-minute base, not inherit the 4 auth failures.
	transient := errors.New("dial tcp: i/o timeout")
	applyFetchBackoff(c, transient, key, interval, base)
	if got := c.nextFetchAttempt.Sub(base); got != time.Minute {
		t.Fatalf("transient backoff after an auth run = %v, want 1m — the ramp was not restarted "+
			"on the class change (without the reset fetchFailures=5 → 1m<<4 = 16m)", got)
	}
	if c.fetchFailures != 1 {
		t.Fatalf("a class change must restart the ramp: fetchFailures = %d, want 1", c.fetchFailures)
	}
}

// TestApplyFetchBackoff_RetryKeyChangeStartsNewEpisode pins #236: a spec/interval edit changes the
// retryKey — cacheServe already bypasses the old backoff, and the counter must reset with it so the
// new episode's first transient failure also starts at the ramp base (the class does NOT change
// here, so this isolates the retryKey reset).
func TestApplyFetchBackoff_RetryKeyChangeStartsNewEpisode(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	const interval = 4 * time.Hour
	transient := errors.New("connection refused")
	c := &cachedFetch{}

	// Three transient failures on episode A: the ramp climbs 1m, 2m, 4m.
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		applyFetchBackoff(c, transient, "keyA", interval, base)
		if got := c.nextFetchAttempt.Sub(base); got != want {
			t.Fatalf("episode-A step %d backoff = %v, want %v", i+1, got, want)
		}
	}

	// A spec/interval edit changes the retryKey → a fresh episode. The ramp restarts at 1m.
	applyFetchBackoff(c, transient, "keyB", interval, base)
	if got := c.nextFetchAttempt.Sub(base); got != time.Minute {
		t.Fatalf("a retryKey change must start a new episode at the 1m ramp base, got %v "+
			"(without the reset fetchFailures=4 → 1m<<3 = 8m)", got)
	}
	if c.fetchFailures != 1 {
		t.Fatalf("a retryKey change must restart the ramp: fetchFailures = %d, want 1", c.fetchFailures)
	}
}

// TestApplyFetchBackoff_SameEpisodeRampAccumulates is the positive control: within ONE episode
// (same error class AND same retryKey) the ramp must keep climbing. It guards against an
// over-correction that resets on every call, which would peg the backoff at 1m forever and defeat
// the polite-to-source exponential ramp.
func TestApplyFetchBackoff_SameEpisodeRampAccumulates(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	const interval = 4 * time.Hour
	transient := errors.New("i/o timeout")
	c := &cachedFetch{}

	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		applyFetchBackoff(c, transient, "k", interval, base)
		if got := c.nextFetchAttempt.Sub(base); got != want {
			t.Fatalf("same-episode transient step %d = %v, want %v — the reset must be CONDITIONAL, "+
				"not fire every call", i+1, got, want)
		}
	}
	if c.fetchFailures != 4 {
		t.Fatalf("same-episode ramp must accumulate: fetchFailures = %d, want 4", c.fetchFailures)
	}
}

// TestPropagateStates_RejectedEpochBeyondCapIsReported pins #236: element-set epoch health is
// reported against the FULL len(omms) denominator, but the pre-#236 counters were incremented only
// inside the maxPropagatedStates-bounded propagation loop. A >cap constellation therefore
// under-reported unparseable/future epochs sitting beyond the cap. The counting now runs in a
// full-set pre-pass, so a beyond-cap rejection is still surfaced.
func TestPropagateStates_RejectedEpochBeyondCapIsReported(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}

	// maxPropagatedStates valid, propagatable satellites fill the cap ...
	omms := make([]sgp4.OMM, 0, maxPropagatedStates+2)
	for i := range maxPropagatedStates {
		o := issOMMForTest()
		o.NoradCatID = 25544 + i
		omms = append(omms, o)
	}
	// ... then two element sets whose OMM EPOCH is UNPARSEABLE sit BEYOND the cap. The cap-bounded
	// propagation loop breaks before reaching them, so before #236 they were invisible to
	// reportSourceEpochRejected even though its denominator (len(omms)) counts them.
	for i := range 2 {
		bad := issOMMForTest()
		bad.NoradCatID = 40000 + i
		bad.EpochStr = "not-a-timestamp"
		omms = append(omms, bad)
	}

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-beyondcap", Namespace: "default"},
	}
	r.propagateStates(context.Background(), eph,
		ephemeris.GPFetchResult{OMMs: omms}, time.Now().Add(propagationEpochLead))

	// The cap is still enforced — only maxPropagatedStates states are emitted ...
	if n := len(eph.Status.PropagatedStates); n != maxPropagatedStates {
		t.Fatalf("expected the state list capped at %d, got %d", maxPropagatedStates, n)
	}
	// ... yet the two beyond-cap unparseable epochs are surfaced durably.
	cond := meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionSourceEpochRejected)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatal("beyond-cap unparseable epochs must be surfaced via SourceEpochRejected=True — " +
			"before #236 the cap-bounded loop skipped them, so the counter stayed 0 and this was False")
	}
	if !strings.Contains(cond.Message, "2 of 130") {
		t.Fatalf("the condition must count BOTH beyond-cap rejections against the FULL 130-element "+
			"denominator (128 valid + 2 unparseable), got message: %q", cond.Message)
	}
	if cond.Reason != "UnparseableSourceEpoch" {
		t.Fatalf("unparseable-only rejections must use Reason=UnparseableSourceEpoch, got %q", cond.Reason)
	}
}

// TestObtainOMMs_TransientAfterAuth_RestartsRampThroughWiring is the end-to-end companion to the
// applyFetchBackoff helper tests: it drives the ACTUAL production path (obtainOMMs), so a future
// change that reverts obtainOMMs to an inline `c.fetchFailures++` while keeping the helper would be
// caught here. The cache is seeded mid-AUTH-episode with a high count; a transient fetch error must
// restart the ramp to 1 (class change auth->transient) and arm nextFetchAttempt ~1 minute out.
// Mutation: an inline `++` that ignores the episode reset leaves fetchFailures at 6, failing this.
func TestObtainOMMs_TransientAfterAuth_RestartsRampThroughWiring(t *testing.T) {
	sch := makeScheme(t)
	const interval = 4 * time.Hour
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-wire", Namespace: "default", UID: "uid-wire", Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/x",
				RefreshInterval: metav1.Duration{Duration: interval},
			},
		},
	}
	key := client.ObjectKeyFromObject(eph)
	r := &SatelliteEphemerisReconciler{
		Client: fake.NewClientBuilder().WithScheme(sch).Build(), Scheme: sch, Recorder: events.NewFakeRecorder(10),
	}

	// Seed the cache mid-AUTH-episode with a high consecutive-failure count and a valid served result.
	r.ommCache.Store(key, cachedFetch{
		result:        ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMForTest()}, FetchedAt: time.Now().Add(-time.Hour)},
		fetchKey:      fetchInputKey(eph.Spec),
		uid:           eph.UID,
		fetchFailures: 5,
		lastFetchErr:  ephemeris.ErrAuthFailed,
		retryKey:      retryInputKey(eph.Spec, interval),
	})

	fetcher := &mockGPFetcher{err: errors.New("dial tcp: connection refused")} // a genuine transient error
	before := time.Now()
	outcome, res, err := r.obtainOMMs(context.Background(),
		reconcile.Request{NamespacedName: key}, eph, fetcher, interval, time.Now())
	if err != nil || res != nil {
		t.Fatalf("a transient error over a valid cache must serve cache, not error/short-circuit; err=%v res=%v", err, res)
	}
	if !outcome.servedCacheOnError {
		t.Fatalf("expected servedCacheOnError, got %+v", outcome)
	}

	got, ok := r.cachedOMMResult(key)
	if !ok {
		t.Fatal("cache entry missing after obtainOMMs")
	}
	if got.fetchFailures != 1 {
		t.Fatalf("auth->transient through obtainOMMs must restart the ramp to 1, got %d "+
			"(an inline ++ that ignores the episode reset would give 6)", got.fetchFailures)
	}
	if delay := got.nextFetchAttempt.Sub(before); delay < time.Minute || delay > time.Minute+5*time.Second {
		t.Fatalf("the first transient backoff must be ~1 minute from fetch completion, got %s", delay)
	}
}
