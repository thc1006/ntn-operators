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
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestReconcile_FailedSwitchbackThenPassEnded_NoGhostDwell is the transaction-boundary
// regression (review Finding 1). A quality-switchback whose Status().Update() FAILS must not
// leave a speculative LastSwitchback in shared memory: otherwise a later pass-ended FORCED
// switchback (which must NOT start a dwell) launders that ghost timestamp into durable status,
// wrongly blocking the next pass's failover. Because storeFlapState now commits only AFTER a
// durable write, R2 sees an empty clock and persists nothing. Mutation: committing memory before
// the write (the pre-fix order) makes R2 persist the ghost T1, so this fails.
func TestReconcile_FailedSwitchbackThenPassEnded_NoGhostDwell(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: "default"},
	}
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "chunghwa-telecom", APN: "internet", Priority: "primary"},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "oneweb-constellation",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:            []string{"rsrp < -100"},
				SwitchbackDelay:     metav1.Duration{Duration: 60 * time.Second},
				MinTerrestrialDwell: metav1.Duration{Duration: 60 * time.Second},
			},
		},
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: string(slice.PathSatellite)},
	}

	var sliceStatusUpdates int
	cli := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*ntnv1alpha1.NTNSlice); ok {
					sliceStatusUpdates++
					if sliceStatusUpdates == 1 { // R1's switchback write fails
						return apierrors.NewConflict(
							schema.GroupResource{Group: "ntn.operators.dev", Resource: "ntnslices"},
							obj.GetName(), fmt.Errorf("forced conflict"))
					}
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()

	setPass := func(aos, los time.Time) {
		eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
			Satellite: "ONEWEB-0012", GroundStation: "gs",
			AOS: metav1.Time{Time: aos}, LOS: metav1.Time{Time: los},
		}}
		// PassesPredicted=True: the prediction is current, so an ENDED window is a genuine end-of-pass
		// under the consumer's 3-state contract (ADR 0006 / #234), not "unknown".
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "Predicted",
		})
		if err := cli.Status().Update(context.Background(), eph); err != nil {
			t.Fatalf("seed pass window: %v", err)
		}
	}
	r := &NTNSliceReconciler{Client: cli, Scheme: sch, Now: func() time.Time { return fixedNow }}
	key := client.ObjectKeyFromObject(nsObj)
	// Terrestrial healthy long enough to switch back; satellite pass currently ACTIVE.
	r.storeFlapState(key, "", slice.AntiFlapState{RecoveryObservedAt: fixedNow.Add(-90 * time.Second)})
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0}, Stale: false, LastFreshAt: fixedNow,
	}}}
	setPass(fixedNow.Add(-1*time.Hour), fixedNow.Add(1*time.Hour))

	// R1: quality-switchback computed, but its status write is forced to CONFLICT.
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err == nil {
		t.Fatal("R1 status update was expected to conflict")
	}

	// Pass ends → satellite unavailable → R2 does a FORCED (pass-ended) switchback.
	setPass(fixedNow.Add(-2*time.Hour), fixedNow.Add(-1*time.Hour))
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("R2 reconcile: %v", err)
	}

	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.ActivePathType != string(slice.PathTerrestrial) {
		t.Fatalf("R2 pass-ended switchback should land on terrestrial, got %q", updated.Status.ActivePathType)
	}
	if updated.Status.LastSwitchbackTime != nil {
		t.Fatalf("a pass-ended forced switchback must NOT persist a dwell clock; the failed R1 "+
			"quality-switchback left a GHOST LastSwitchback=%v", updated.Status.LastSwitchbackTime)
	}
}

// baseSliceForDwell builds an NTNSlice with a min-dwell failover policy and a live
// satellite pass, used by the LastSwitchback-persistence tests below.
func baseSliceForDwell(t *testing.T, fixedNow time.Time, status ntnv1alpha1.NTNSliceStatus) (
	*NTNSliceReconciler, client.ObjectKey,
) {
	t.Helper()
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: "default"},
	}
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "chunghwa-telecom", APN: "internet", Priority: "primary"},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "oneweb-constellation",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:            []string{"rsrp < -100"},
				SwitchbackDelay:     metav1.Duration{Duration: 60 * time.Second},
				MinTerrestrialDwell: metav1.Duration{Duration: 60 * time.Second},
			},
		},
		Status: status,
	}
	cli := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).Build()
	// Live pass window: satellite available now. PassesPredicted=True marks the prediction current so
	// the consumer's 3-state contract (ADR 0006 / #234) trusts the windows.
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
		LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	}}
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "Predicted",
	})
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris pass window: %v", err)
	}
	r := &NTNSliceReconciler{Client: cli, Scheme: sch, Now: func() time.Time { return fixedNow }}
	return r, client.ObjectKeyFromObject(nsObj)
}

// TestReconcile_MinDwell_SeededFromStatus_BlocksEarlyRefailover is the H1 core: after a
// controller restart the in-memory flap state is empty, but status.lastSwitchbackTime lets
// the min-dwell clock be reloaded, so a re-degradation within the dwell window does NOT
// fail over. Mutation: drop the status→af seed and this fails over to satellite instead.
func TestReconcile_MinDwell_SeededFromStatus_BlocksEarlyRefailover(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	// On terrestrial, switched back 15s ago (well within the 60s min-dwell). No in-memory
	// flap entry is seeded — this is the post-restart cold cache.
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType:     string(slice.PathTerrestrial),
		LastSwitchbackTime: &metav1.Time{Time: fixedNow.Add(-15 * time.Second)},
	})
	// Terrestrial DEGRADED (rsrp -110 < -100) and reliable; satellite available.
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -110, LatencyMs: 10, PacketLossPercent: 0},
		Stale:   false, LastFreshAt: fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.ActivePathType != string(slice.PathTerrestrial) {
		t.Fatalf("min-dwell (reloaded from status.lastSwitchbackTime) must BLOCK the re-failover; "+
			"want stay terrestrial, got %q — the LastSwitchback status seed was not applied",
			updated.Status.ActivePathType)
	}
}

// TestReconcile_Switchback_PersistsLastSwitchbackTime proves a quality-driven switchback
// writes status.lastSwitchbackTime. Mutation: drop the persist and it stays nil.
func TestReconcile_Switchback_PersistsLastSwitchbackTime(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	// On satellite; terrestrial has been healthy long enough for the switchback delay.
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType: string(slice.PathSatellite),
	})
	r.storeFlapState(key, "", slice.AntiFlapState{RecoveryObservedAt: fixedNow.Add(-90 * time.Second)})
	// Terrestrial HEALTHY (rsrp -80 does not fire), reliable; satellite available.
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0},
		Stale:   false, LastFreshAt: fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.ActivePathType != string(slice.PathTerrestrial) {
		t.Fatalf("expected a switchback to terrestrial, got %q", updated.Status.ActivePathType)
	}
	if updated.Status.LastSwitchbackTime == nil || !updated.Status.LastSwitchbackTime.Time.Equal(fixedNow) {
		t.Fatalf("a switchback must persist status.lastSwitchbackTime=%v, got %v",
			fixedNow, updated.Status.LastSwitchbackTime)
	}
}

// TestReconcile_SelfHealsDurableLastSwitchback pins the monotonic-merge robustness (review
// Finding 1): when the in-memory min-dwell clock is AHEAD of the durable status (e.g. a prior
// switchback's Status().Update() failed and left status behind memory), any later reconcile
// must REPAIR status from memory — it must not treat "memory has a value, status is nil" as an
// acceptable steady state. Mutation: the prior "write only when memory changed this cycle"
// gate leaves status nil here (no switchback this cycle), so this fails.
func TestReconcile_SelfHealsDurableLastSwitchback(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	// On terrestrial; in-memory clock set 30s ago (within the 60s dwell), status nil.
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType: string(slice.PathTerrestrial),
	})
	memClock := fixedNow.Add(-30 * time.Second)
	r.storeFlapState(key, "", slice.AntiFlapState{LastSwitchback: memClock})
	// Terrestrial DEGRADED (would fail over if dwell were satisfied), satellite available.
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -110, LatencyMs: 10, PacketLossPercent: 0},
		Stale:   false, LastFreshAt: fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.LastSwitchbackTime == nil || !updated.Status.LastSwitchbackTime.Time.Equal(memClock) {
		t.Fatalf("durable status must be REPAIRED from the ahead-of-status in-memory clock (%v), got %v",
			memClock, updated.Status.LastSwitchbackTime)
	}
	if updated.Status.ActivePathType != string(slice.PathTerrestrial) {
		t.Fatalf("in-memory min-dwell must still block the re-failover, got %q", updated.Status.ActivePathType)
	}
}

// TestReconcile_MonotonicSeed_IgnoresFutureAndOlder pins two monotonicity properties (Findings
// 1 & 3): the seed adopts status only when it is NEWER than memory (never downgrades), and it
// ignores a FUTURE-dated status (clock skew / rollback) so a bad timestamp cannot lock
// terrestrial. Here status is OLDER than memory AND memory is within dwell → the slice must
// stay terrestrial and status must advance to the memory value.
func TestReconcile_MonotonicSeed_KeepsNewerMemory(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	memClock := fixedNow.Add(-10 * time.Second)  // within 60s dwell
	oldStatus := fixedNow.Add(-90 * time.Second) // outside dwell (would fail over if adopted)
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType:     string(slice.PathTerrestrial),
		LastSwitchbackTime: &metav1.Time{Time: oldStatus},
	})
	r.storeFlapState(key, "", slice.AntiFlapState{LastSwitchback: memClock})
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -110, LatencyMs: 10, PacketLossPercent: 0},
		Stale:   false, LastFreshAt: fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.ActivePathType != string(slice.PathTerrestrial) {
		t.Fatalf("seed must NOT downgrade to the older status clock (would satisfy dwell and fail over); got %q",
			updated.Status.ActivePathType)
	}
	if updated.Status.LastSwitchbackTime == nil || !updated.Status.LastSwitchbackTime.Time.Equal(memClock) {
		t.Fatalf("status must advance to the newer in-memory clock (%v), got %v",
			memClock, updated.Status.LastSwitchbackTime)
	}
}

// TestReconcile_FutureDurableTimestampHealedBySwitchback pins that a poisoned FUTURE durable
// value (clock skew / rollback) is not stuck: a real quality-switchback must overwrite it rather
// than be blocked by the monotonic "never go backward" rule (review P2). Mutation: dropping the
// `p.After(now)` heal in persist leaves the future value in place, so the switchback's real time
// cannot be recovered after a restart.
func TestReconcile_FutureDurableTimestampHealedBySwitchback(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	// On satellite, terrestrial recovered; durable clock poisoned 1h into the future.
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType:     string(slice.PathSatellite),
		LastSwitchbackTime: &metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	})
	r.storeFlapState(key, "", slice.AntiFlapState{RecoveryObservedAt: fixedNow.Add(-90 * time.Second)})
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0}, Stale: false, LastFreshAt: fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.LastSwitchbackTime == nil || !updated.Status.LastSwitchbackTime.Time.Equal(fixedNow) {
		t.Fatalf("a real switchback must HEAL a future durable timestamp to now (%v), got %v",
			fixedNow, updated.Status.LastSwitchbackTime)
	}
}

// TestReconcile_FutureStatusTimestampIgnored pins the future-timestamp guard (Finding 3): a
// status.lastSwitchbackTime dated in the future (clock skew / rollback) must be IGNORED by the
// seed, so it cannot indefinitely lock a degraded terrestrial. Here memory is empty and status
// is 1h in the future → min-dwell must NOT apply → the slice fails over. Mutation: seeding the
// future value makes now.Sub(future) negative < dwell, wrongly holding terrestrial.
func TestReconcile_FutureStatusTimestampIgnored(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType:     string(slice.PathTerrestrial),
		LastSwitchbackTime: &metav1.Time{Time: fixedNow.Add(1 * time.Hour)}, // implausible future
	})
	// no in-memory entry (cold cache); terrestrial DEGRADED; satellite available.
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -110, LatencyMs: 10, PacketLossPercent: 0},
		Stale:   false, LastFreshAt: fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.ActivePathType != string(slice.PathSatellite) {
		t.Fatalf("a future-dated lastSwitchbackTime must NOT lock terrestrial; expected failover to satellite, got %q",
			updated.Status.ActivePathType)
	}
}

// TestReconcile_FutureDurableTimestampClearedNotRevived pins Finding 1 (round-4): a future-dated
// lastSwitchbackTime must be CLEARED durably, not merely ignored, so it cannot silently become
// valid history once the wall clock passes it and block a legitimate failover with a ghost dwell.
// The seed observes the future value while terrestrial is healthy (no switchback) and clears it;
// after the clock advances past it, a real degradation must still fail over. Mutation: leaving the
// future value in status (the old ignore-only behaviour) revives it, and now.Sub(revived) < dwell
// wrongly holds terrestrial.
func TestReconcile_FutureDurableTimestampClearedNotRevived(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType:     string(slice.PathTerrestrial),
		LastSwitchbackTime: &metav1.Time{Time: fixedNow.Add(30 * time.Second)}, // future (skew / rollback)
	})
	now := fixedNow
	r.Now = func() time.Time { return now }

	// Reconcile 1: terrestrial HEALTHY (no switchback) while the timestamp is still in the future.
	// The seed must clear it durably.
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0}, Stale: false, LastFreshAt: fixedNow,
	}}}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	mid := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, mid); err != nil {
		t.Fatalf("get after reconcile 1: %v", err)
	}
	if mid.Status.LastSwitchbackTime != nil {
		t.Fatalf("a future-dated lastSwitchbackTime must be cleared, not left to revive; got %v",
			mid.Status.LastSwitchbackTime)
	}

	// Advance past the former future timestamp but stay within the 60s min-dwell, then degrade
	// terrestrial: the failover must NOT be blocked by a revived ghost dwell.
	now = fixedNow.Add(35 * time.Second)
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -110, LatencyMs: 10, PacketLossPercent: 0}, Stale: false, LastFreshAt: now,
	}}}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	got := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get after reconcile 2: %v", err)
	}
	if got.Status.ActivePathType != string(slice.PathSatellite) {
		t.Fatalf("a cleared future timestamp must not revive after the clock passes it; "+
			"expected failover to satellite, got %q", got.Status.ActivePathType)
	}
}

// TestReconcile_StaleFlapStateForDifferentUID_IsNotInherited pins Finding 2 (round-4): the
// anti-flap cache is keyed by (name, UID), so a same-name slice recreated with a new UID — a
// delete+create the workqueue coalesced into one reconcile, so the intervening NotFound was never
// observed — does NOT inherit the deleted object's min-dwell clock. The reconciled object's UID
// differs from the UID the stale entry was stored under, so the entry is evicted and the slice
// starts from zero state. Mutation: keying on name alone loads the stale recent-switchback clock
// and wrongly holds terrestrial (15s < 60s dwell).
func TestReconcile_StaleFlapStateForDifferentUID_IsNotInherited(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	r, key := baseSliceForDwell(t, fixedNow, ntnv1alpha1.NTNSliceStatus{
		ActivePathType: string(slice.PathTerrestrial),
	})
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -110, LatencyMs: 10, PacketLossPercent: 0}, Stale: false, LastFreshAt: fixedNow,
	}}}
	// Leftover state stored under a DIFFERENT (deleted predecessor's) UID, with a recent switchback
	// that would block failover (15s < 60s dwell) if it were wrongly inherited. The reconciled
	// object built by baseSliceForDwell has a different UID (empty), so this must be evicted.
	r.storeFlapState(key, "uid-of-deleted-predecessor", slice.AntiFlapState{
		LastSwitchback: fixedNow.Add(-15 * time.Second),
	})

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(context.Background(), key, got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.Status.ActivePathType != string(slice.PathSatellite) {
		t.Fatalf("a recreated same-name slice must not inherit a deleted object's min-dwell clock; "+
			"expected failover to satellite, got %q", got.Status.ActivePathType)
	}
}
