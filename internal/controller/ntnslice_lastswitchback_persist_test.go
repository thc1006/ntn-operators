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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

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
	// Live pass window: satellite available now.
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
		LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	}}
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
	r.storeFlapState(key, slice.AntiFlapState{RecoveryObservedAt: fixedNow.Add(-90 * time.Second)})
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
	r.storeFlapState(key, slice.AntiFlapState{LastSwitchback: memClock})
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
	r.storeFlapState(key, slice.AntiFlapState{LastSwitchback: memClock})
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
