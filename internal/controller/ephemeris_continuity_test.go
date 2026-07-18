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
	"net/http"
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

// assertSustainedOutageKeepsPropagating covers ONE facet of the I-18 "serve cache preserves
// SIB19 continuity" claim: WITHIN a single fetch-retry backoff window, across a SUSTAINED
// upstream outage the controller must
//
//	(a) contact the source only ONCE (the retry backoff must hold WITHIN the window), while
//	(b) STILL re-propagating on every reconcile so the pushed epoch never expires, and
//	(c) requeueing on the 3-minute propagation cadence, not the 2–24h fetch backoff.
//
// The old code used the fetch backoff AS the reconcile cadence, so it re-propagated once to
// now+5m and then slept for hours — the epoch expired ~5 minutes in and the consumer refused
// the state for the rest of the outage. Continuity was ~4% (5min / 2h), not 100%.
//
// This uses the real wall clock (a 2ms sleep to separate epochs), so it can only observe the
// FIRST window — every cycle here sees calls==1. What happens as the outage OUTLASTS that
// window — the fetch re-attempting once per window and the 1m→2m→4m ramp growing while
// propagation stays continuous — is the OTHER facet, covered deterministically by
// TestReconcile_SustainedOutage_AdvancesClockAcrossMultipleRetryWindows (which needs an
// injected clock to jump past each nextFetchAttempt without sleeping for minutes).
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
	if got := fetchRetryDelay(ephemeris.ErrAuthFailed, interval, 1); got != maxAuthRetryBackoff {
		t.Errorf("auth-failed fetch delay must be capped for recovery = %s, want %s", got, maxAuthRetryBackoff)
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

// TestReconcile_ExpiredCache_MissingSecret_KeepsPropagating is the REAL credential-outage
// scenario, and the one my earlier test faked: it used a cache INSIDE the refresh window, so it
// only proved the (easy) "cache answers before credentials are read" path and never exercised
// the dangerous one.
//
// Here the SpaceTrack cache is EXPIRED (5h old vs a 4h window), so a fetch is genuinely due —
// cacheServe returns none, fetcherForSource runs, and the referenced Secret is MISSING. The old
// code set FetcherSetupFailed and returned RequeueAfter=1m WITHOUT propagating: the cache-on-
// error fallback lived inside obtainOMMs, which a setup failure never reaches. The pushed epoch
// then expired within ~2 minutes and SIB19 broke — on the very contract this PR claims to protect.
// Setup failure must now go through the SAME any-age cache fallback as a fetch failure.
func TestReconcile_ExpiredCache_MissingSecret_KeepsPropagating(t *testing.T) {
	sch := makeScheme(t)
	ctx := context.Background()
	const ns = "default"
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-st", Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "SpaceTrack", URL: "https://www.space-track.org/gp",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				// The Secret this points at is deliberately NEVER created.
				Credentials: &ntnv1alpha1.SecretReference{Name: "spacetrack-creds", Key: "password"},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: "eph-st", Namespace: ns}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50),
		// Non-nil so fetcherForSource gets PAST the "fetcher not configured" check and fails on
		// the Secret read itself — the real-world failure.
		SpaceTrackFetcher: ephemeris.NewSpaceTrackFetcher(&http.Client{}, "https://www.space-track.org"),
	}
	// EXPIRED cache (5h old vs the 4h window) → a fetch is genuinely due.
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{issOMMForTest()}, SatelliteCount: 1, FetchedAt: time.Now().Add(-5 * time.Hour),
		},
		fetchKey: fetchInputKey(got.Spec),
		uid:      got.UID,
	})

	var lastEpoch int64
	for cycle := 1; cycle <= 3; cycle++ {
		if cycle > 1 {
			time.Sleep(2 * time.Millisecond)
		}
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if res.RequeueAfter != propagationRefreshInterval {
			t.Fatalf("cycle %d: a setup failure with a usable cache must keep the PROPAGATION cadence "+
				"(%s), got %s — the epoch would expire before the next reconcile",
				cycle, propagationRefreshInterval, res.RequeueAfter)
		}
		after := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, after); err != nil {
			t.Fatalf("cycle %d: re-get: %v", cycle, err)
		}
		if len(after.Status.PropagatedStates) == 0 {
			t.Fatalf("cycle %d: SIB19 continuity lost — a missing Secret must not stop re-propagation "+
				"from a cache whose raw OMMs do not depend on credentials", cycle)
		}
		epoch := after.Status.PropagatedStates[0].EpochUnixMs
		if epoch <= time.Now().UnixMilli() {
			t.Fatalf("cycle %d: propagated epoch is already in the past — the consumer will refuse it", cycle)
		}
		if cycle > 1 && epoch <= lastEpoch {
			t.Fatalf("cycle %d: epoch did not advance — the cache is not being re-propagated", cycle)
		}
		lastEpoch = epoch
		// The degraded reason must name the SETUP failure, not be collapsed into the fetch one:
		// the operator action is different (fix the Secret vs check the source).
		cond := meta.FindStatusCondition(after.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
		if cond == nil || cond.Reason != reasonSetupFailedServingCache {
			t.Fatalf("cycle %d: expected GPDataFetched reason %q, got %+v", cycle, reasonSetupFailedServingCache, cond)
		}
	}
}

// TestCacheServe_CredentialChangeClearsBackoff: the payload cache key deliberately excludes
// credentials, but the BACKOFF was armed from them. Without separating the two, an operator who
// FIXES the credential reference would still be suppressed by the old 2–24h auth backoff —
// cacheServe would answer from cache and never even build the fetcher. A retry-input change must
// invalidate the backoff so the next fetch goes through immediately.
func TestCacheServe_CredentialChangeClearsBackoff(t *testing.T) {
	sch := makeScheme(t)
	now := time.Now()
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default", UID: "u1"},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "SpaceTrack", URL: "https://st/gp",
				Credentials: &ntnv1alpha1.SecretReference{Name: "old-creds", Key: "password"},
			},
		},
	}
	_ = sch
	r := &SatelliteEphemerisReconciler{}
	k := client.ObjectKeyFromObject(eph)
	const interval = 4 * time.Hour
	// Expired cache + an armed auth backoff built from the OLD credentials.
	r.ommCache.Store(k, cachedFetch{
		result:           ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMForTest()}, FetchedAt: now.Add(-5 * time.Hour)},
		fetchKey:         fetchInputKey(eph.Spec),
		uid:              eph.UID,
		nextFetchAttempt: now.Add(2 * time.Hour),
		retryKey:         retryInputKey(eph.Spec, interval),
		lastFetchErr:     ephemeris.ErrAuthFailed,
	})
	if _, why := r.cacheServe(k, eph, now, interval); why != cacheServeBackoff {
		t.Fatalf("with unchanged credentials the backoff must hold, got %v", why)
	}

	// Operator points at a FIXED Secret. Payload identity is unchanged (same type+url), so the
	// cached OMMs stay reusable — but the backoff must NOT.
	eph.Spec.Source.Credentials.Name = "new-creds"
	if _, why := r.cacheServe(k, eph, now, interval); why != cacheServeNone {
		t.Fatalf("a credential change must invalidate the stale backoff so a fetch is attempted, got %v", why)
	}
	// Same for lowering the refresh interval (a retry-policy input).
	eph.Spec.Source.Credentials.Name = "old-creds"
	if _, why := r.cacheServe(k, eph, now, 2*time.Hour); why != cacheServeNone {
		t.Fatalf("a refresh-interval change must invalidate the stale backoff, got %v", why)
	}
}

// TestCadenceInvariant_EpochOutlivesTheReconcileGap is the static guard the sustained-outage
// tests cannot give (they re-enter Reconcile immediately rather than waiting the real 3 minutes).
// The whole continuity design rests on this: a state propagated to now+propagationEpochLead must
// still clear the consumer's epochSkewMargin when the NEXT scheduled reconcile lands one
// propagationRefreshInterval later. Without it, someone could shorten the lead (or lengthen the
// cadence) and silently reintroduce the outage this PR fixes.
func TestCadenceInvariant_EpochOutlivesTheReconcileGap(t *testing.T) {
	const headroom = 60 * time.Second // deliberate slack for delivery + reconcile latency
	if propagationRefreshInterval+epochSkewMargin+headroom > propagationEpochLead {
		t.Fatalf("cadence invariant violated: propagationRefreshInterval(%s) + epochSkewMargin(%s) + "+
			"headroom(%s) must be <= propagationEpochLead(%s), or a pushed epoch expires before the "+
			"next scheduled re-propagation",
			propagationRefreshInterval, epochSkewMargin, headroom, propagationEpochLead)
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
