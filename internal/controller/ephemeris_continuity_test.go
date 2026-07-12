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
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// ephForContinuity builds a SatelliteEphemeris whose refresh window has already elapsed
// against the seeded cache, so the next reconcile genuinely attempts an upstream fetch.
func ephForContinuity(t *testing.T, name string) (*SatelliteEphemerisReconciler, client.Client, types.NamespacedName, *mockGPFetcher) {
	t.Helper()
	sch := makeScheme(t)
	const ns = "default"
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	fetcher := &mockGPFetcher{}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50), Fetcher: fetcher,
	}
	// Cache older than the 4h window → a fetch is due on the very first reconcile.
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{issOMMForTest()}, SatelliteCount: 1, FetchedAt: time.Now().Add(-5 * time.Hour),
		},
		fetchKey: fetchInputKey(got.Spec),
		uid:      got.UID,
	})
	return r, cli, key, fetcher
}

// assertSustainedOutageKeepsPropagating is the property the whole I-18 "serve cache preserves
// SIB19 continuity" claim rests on, and which NOTHING tested before: across a SUSTAINED
// upstream outage the controller must
//
//	(a) contact the source only ONCE (the retry backoff must hold), while
//	(b) STILL re-propagating on every reconcile so the pushed epoch never expires, and
//	(c) requeueing on the 3-minute propagation cadence, not the 2–24h fetch backoff.
//
// The old code used the fetch backoff AS the reconcile cadence, so it re-propagated once to
// now+5m and then slept for hours — the epoch expired ~5 minutes in and the consumer refused
// the state for the rest of the outage. Continuity was ~4% (5min / 2h), not 100%.
func assertSustainedOutageKeepsPropagating(t *testing.T, fetchErr error) {
	t.Helper()
	ctx := context.Background()
	r, cli, key, fetcher := ephForContinuity(t, "eph-outage")
	fetcher.err = fetchErr // upstream is DOWN for the whole test

	var lastEpoch int64
	for cycle := 1; cycle <= 3; cycle++ {
		// Let the wall clock advance a little between cycles, so a re-propagated epoch is
		// measurably NEWER than the previous one. Without this the three reconciles land in
		// the same millisecond and "did the epoch advance?" cannot distinguish a fresh
		// re-propagation from a frozen one.
		if cycle > 1 {
			time.Sleep(2 * time.Millisecond)
		}
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("cycle %d: a serve-cache reconcile must not return an error (it would "+
				"override RequeueAfter with workqueue backoff): %v", cycle, err)
		}
		// (c) requeue on the PROPAGATION cadence, so the next cycle comes before the epoch dies.
		if res.RequeueAfter != propagationRefreshInterval {
			t.Fatalf("cycle %d: requeue must be the propagation cadence %s (the fetch is held back "+
				"separately); got %s — the epoch would expire before the next reconcile",
				cycle, propagationRefreshInterval, res.RequeueAfter)
		}
		// (a) the source must be contacted only on the FIRST cycle; the backoff holds after.
		if fetcher.calls != 1 {
			t.Fatalf("cycle %d: upstream must be contacted exactly once during the backoff "+
				"(politeness), got %d calls", cycle, fetcher.calls)
		}
		// (b) states must be re-propagated to a FRESH epoch every cycle.
		got := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, got); err != nil {
			t.Fatalf("cycle %d: get: %v", cycle, err)
		}
		if len(got.Status.PropagatedStates) == 0 {
			t.Fatalf("cycle %d: SIB19 continuity lost — no propagated states while serving cache", cycle)
		}
		epoch := got.Status.PropagatedStates[0].EpochUnixMs
		if epoch <= time.Now().UnixMilli() {
			t.Fatalf("cycle %d: propagated epoch %d is already in the past — the consumer will refuse it", cycle, epoch)
		}
		if cycle > 1 && epoch <= lastEpoch {
			t.Fatalf("cycle %d: epoch did not advance (%d <= %d) — the cache is not being re-propagated",
				cycle, epoch, lastEpoch)
		}
		lastEpoch = epoch
	}
}

// TestReconcile_RateLimitedServingCache_KeepsPropagating — RateLimited backs the fetch off to
// >= effectiveInterval (>= 2h). Propagation must continue regardless.
func TestReconcile_RateLimitedServingCache_KeepsPropagating(t *testing.T) {
	assertSustainedOutageKeepsPropagating(t, ephemeris.ErrRateLimited)
}

// TestReconcile_AuthFailedServingCache_KeepsPropagating — AuthFailed backs the fetch off to
// effectiveInterval (>= 2h). Propagation must continue regardless.
func TestReconcile_AuthFailedServingCache_KeepsPropagating(t *testing.T) {
	assertSustainedOutageKeepsPropagating(t, ephemeris.ErrAuthFailed)
}

// TestReconcile_GenericFailureServingCache_KeepsPropagating — a generic transient error used to
// be RETURNED so the workqueue applied exponential backoff (ramping to ~1000s, far past the
// 5-minute epoch lead). It must now also keep the propagation heartbeat.
func TestReconcile_GenericFailureServingCache_KeepsPropagating(t *testing.T) {
	assertSustainedOutageKeepsPropagating(t, errors.New("connection refused"))
}

// TestFetchRetryDelay_NeverStarvesPropagation pins the invariant that makes the above safe:
// the FETCH backoff may be long (that is the point — politeness), but it is applied to the
// FETCH, never to the reconcile. Here we just pin that each error class produces the intended
// fetch delay, so a future edit cannot quietly turn it back into the reconcile cadence.
func TestFetchRetryDelay_PerErrorClass(t *testing.T) {
	const interval = 4 * time.Hour
	if got := fetchRetryDelay(ephemeris.ErrRateLimited, interval, 1); got < interval {
		t.Errorf("rate-limited fetch delay must be at least the refresh interval, got %s", got)
	}
	if got := fetchRetryDelay(ephemeris.ErrAuthFailed, interval, 1); got != interval {
		t.Errorf("auth-failed fetch delay = %s, want %s", got, interval)
	}
	// Transient ramps 1m, 2m, 4m … and is capped at the interval.
	if got := fetchRetryDelay(errors.New("boom"), interval, 1); got != time.Minute {
		t.Errorf("first transient fetch delay = %s, want 1m", got)
	}
	if got := fetchRetryDelay(errors.New("boom"), interval, 3); got != 4*time.Minute {
		t.Errorf("third transient fetch delay = %s, want 4m", got)
	}
	if got := fetchRetryDelay(errors.New("boom"), interval, 50); got != interval {
		t.Errorf("transient fetch delay must cap at the refresh interval, got %s", got)
	}
}

// TestReconcile_ValidCache_MissingCredentialSecret_StillRepropagates: a reconcile the cache can
// answer must not need the source Secret at all. The fetcher is left unconfigured so
// fetcherForSource would ERROR if it ran — standing in for a Secret that is missing, briefly
// unreadable, or mid-rotation. Continuity must survive it.
func TestReconcile_ValidCache_MissingCredentialSecret_StillRepropagates(t *testing.T) {
	ctx := context.Background()
	r, cli, key, _ := ephForContinuity(t, "eph-nocred")
	r.Fetcher = nil // fetcherForSource would fail: "CelesTrak fetcher is not configured"
	// Make the cache answerable WITHOUT a fetch by putting it inside the refresh window.
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{issOMMForTest()}, SatelliteCount: 1, FetchedAt: time.Now(), // FRESH
		},
		fetchKey: fetchInputKey(got.Spec),
		uid:      got.UID,
	})

	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("a cache-answerable reconcile must not need credentials: %v", err)
	}
	after := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, after); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if cond := meta.FindStatusCondition(after.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched); cond != nil && cond.Reason == "FetcherSetupFailed" {
		t.Fatal("credential/fetcher setup must be SKIPPED when the cache can answer, but got FetcherSetupFailed")
	}
	if len(after.Status.PropagatedStates) == 0 {
		t.Error("a cache-answerable reconcile must still re-propagate (SIB19 continuity)")
	}
}

// TestPushRuntime_SourceEpoch1970_IsStaleNotBypassed pins the sentinel-collision fix: 0 is a
// LEGAL Unix ms (1970-01-01T00:00:00Z), not just "unparseable". The consumer used to fail OPEN
// on 0 ("unknown → allow"), so a 1970-dated element set bypassed the entire 7-day freshness
// gate. It must now simply be — correctly — stale.
// Mutation: restore `if state.SourceEpochUnixMs != 0 {` around the check and this pushes.
func TestPushRuntime_SourceEpoch1970_IsStaleNotBypassed(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	eph.Status.PropagatedStates[0].SourceEpochUnixMs = 0 // 1970-01-01T00:00:00Z — 56 years stale
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if pushed || mock.RuntimeCalls != 0 {
		t.Fatalf("a 1970 source epoch must be treated as STALE, not as 'unknown → allowed': pushed=%v calls=%d",
			pushed, mock.RuntimeCalls)
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonEphemerisStale {
		t.Fatalf("reason = %q, want %q", got, ephemerisReasonEphemerisStale)
	}
}

// TestPropagateStates_UnparseableEpoch_ProducesNoState: the producer must not emit a state with
// SourceEpochUnixMs = 0 for an epoch it could not parse — that is what made 0 ambiguous. (SGP4's
// own ToTLE parses the same epoch, so such an element set fails propagation anyway.)
func TestPropagateStates_UnparseableEpoch_ProducesNoState(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-badepoch", Namespace: "default"},
	}
	bad := issOMMForTest()
	bad.EpochStr = "not-a-timestamp"
	r.propagateStates(context.Background(), eph,
		ephemeris.GPFetchResult{OMMs: []sgp4.OMM{bad}}, time.Now().Add(propagationEpochLead))

	if n := len(eph.Status.PropagatedStates); n != 0 {
		t.Fatalf("an unparseable source epoch must produce NO state (never a 0 sentinel), got %d", n)
	}
	if cond := meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionSourceEpochRejected); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatal("a refused element set must be surfaced durably via SourceEpochRejected=True, not only a log line")
	}
}

// TestSourceEpochPlausible_ProducerAndConsumerAgree pins the shared-layer invariant: the
// producer used to compare the future bound against the propagation TARGET epoch (now + 5m)
// while the consumer compared against now, so a source epoch inside that 5-minute band was
// propagated and hash-stamped yet permanently refused at the push. Both now use the same rule
// against their own `now`, so there is exactly ONE boundary.
func TestSourceEpochPlausible_ProducerAndConsumerAgree(t *testing.T) {
	now := time.Now()
	justInside := now.Add(maxSourceEpochFutureSkew - time.Millisecond)
	exactly := now.Add(maxSourceEpochFutureSkew)
	justOutside := now.Add(maxSourceEpochFutureSkew + time.Millisecond)

	if err := sourceEpochPlausible(now, justInside); err != nil {
		t.Errorf("epoch just inside the skew bound must be allowed: %v", err)
	}
	if err := sourceEpochPlausible(now, exactly); err != nil {
		t.Errorf("epoch exactly at the skew bound must be allowed (strict After): %v", err)
	}
	if err := sourceEpochPlausible(now, justOutside); err == nil {
		t.Error("epoch just past the skew bound must be rejected")
	}
	// The old 5-minute disagreement band: an epoch in (now+skew, now+skew+lead] must now be
	// rejected by BOTH sides, not accepted by the producer and refused by the consumer.
	band := now.Add(maxSourceEpochFutureSkew + propagationEpochLead/2)
	if err := sourceEpochPlausible(now, band); err == nil {
		t.Error("an epoch in the old producer/consumer disagreement band must be rejected by the shared rule")
	}
}

// TestPropagationInputHash_DedupesNoradIDs: FilterOMMs applies the selector through a map, so
// [25544] and [25544, 25544] select the same satellites and produce byte-identical output —
// they must hash the same, or a duplicate ID triggers a needless EphemerisInputsStale +
// re-propagation + runtime re-push.
func TestPropagationInputHash_DedupesNoradIDs(t *testing.T) {
	base := ntnv1alpha1.SatelliteEphemerisSpec{
		Source:     ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/x"},
		Satellites: &ntnv1alpha1.SatelliteSelector{NoradIDs: []int{25544, 200}},
	}
	dup := *base.DeepCopy()
	dup.Satellites.NoradIDs = []int{200, 25544, 25544, 200}
	if propagationInputHash(base) != propagationInputHash(dup) {
		t.Fatal("duplicate NORAD IDs select the same satellites and must hash identically")
	}
}
