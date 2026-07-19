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
	"sync/atomic"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestPassPredictionIntervalExceedsHeartbeat locks the invariant that lets the requeue stay the bare
// propagation cadence (ADR 0006 / #234): because passPredictionInterval > propagationRefreshInterval,
// the heartbeat re-evaluates passSweepDue every cycle and runs the sweep within one heartbeat of it
// becoming due, so no separate pass term is needed in RequeueAfter. If someone lowered
// passPredictionInterval below the heartbeat, the sweep could slip past its due time — this fails first.
func TestPassPredictionIntervalExceedsHeartbeat(t *testing.T) {
	if passPredictionInterval <= propagationRefreshInterval {
		t.Fatalf("passPredictionInterval (%s) must exceed propagationRefreshInterval (%s): the heartbeat "+
			"re-checks passSweepDue every cycle, so a shorter pass interval would let the sweep slip past "+
			"its due time (the requeue carries no separate pass term)",
			passPredictionInterval, propagationRefreshInterval)
	}
}

// TestPassSweepDue pins the gate on BOTH arms: the time cadence (never-run is due; boundary inclusive;
// strictly inside the interval is not due when the inputs match) AND the input-change arm (a signature
// mismatch is due immediately, overriding the time gate — this is the #234 fix for stale windows after
// a ground-station or selector edit).
func TestPassSweepDue(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	const savedHash = "sig-A"
	mk := func(last *time.Time) *ntnv1alpha1.SatelliteEphemeris {
		eph := &ntnv1alpha1.SatelliteEphemeris{}
		eph.Status.LastPassPredictionInputHash = savedHash
		if last != nil {
			eph.Status.LastPassPredictionTime = &metav1.Time{Time: *last}
		}
		return eph
	}
	justUnder := now.Add(-passPredictionInterval + time.Second)
	exactly := now.Add(-passPredictionInterval)
	over := now.Add(-passPredictionInterval - time.Second)
	cases := []struct {
		name    string
		last    *time.Time
		curHash string
		want    bool
	}{
		{"never run (nil), same inputs is due", nil, savedHash, true},
		{"just under interval, same inputs: not due", &justUnder, savedHash, false},
		{"exactly at interval, same inputs: due (inclusive)", &exactly, savedHash, true},
		{"over interval, same inputs: due", &over, savedHash, true},
		{"inputs changed within interval: due immediately (overrides time)", &justUnder, "sig-B", true},
		{"inputs changed at zero elapsed: due", &now, "sig-B", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := passSweepDue(mk(tc.last), now, tc.curHash); got != tc.want {
				t.Fatalf("passSweepDue = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReconcile_PassPredictionRunsOnLowerCadence is the core Option B guarantee (ADR 0006 / #234): the
// propagation heartbeat re-propagates a FRESH epoch on EVERY reconcile, while the expensive pass-window
// sweep runs only once per passPredictionInterval — never on the intervening heartbeats. It also proves
// the epoch still refreshes every cycle even though the sweep produces no windows (the ground station
// is absent here, so the sweep fails fast).
//
// Mutations this pins:
//   - dropping the passSweepDue gate (sweep every heartbeat): LastPassPredictionTime would advance on
//     every cycle, so the "unchanged at t+3/6/9/12m" assertions fail.
//   - not stamping LastPassPredictionTime after a sweep: passSweepDue would stay true (nil) forever, so
//     the t0 "stamps t0" assertion fails.
//
// The ctx-CANCELLED-sweep guarantee (epoch survives + cadence not advanced) is covered separately by
// TestReconcile_SlowSweepDoesNotDelayEpoch_AndCancelPersists, which blocks the sweep at its
// ground-station Get; this cadence test uses a fast-failing sweep and does not exercise cancellation.
func TestReconcile_PassPredictionRunsOnLowerCadence(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-pass-cadence"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
			// Pass prediction CONFIGURED, but the ground station does not exist → the sweep runs and
			// fails fast (records PredictionFailed). We observe only WHEN the sweep runs (the stamp) and
			// that the epoch survives its failure.
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}

	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}},
		Now:     func() time.Time { return clock },
	}
	// Seed a FRESH cache (fetched at t0) so obtainOMMs serves from cache within the 4h window — no fetch
	// is due across this 15-minute test, so every reconcile is a pure propagation heartbeat.
	r.ommCache.Store(key, cachedFetch{
		result:   ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0},
		fetchKey: fetchInputKey(eph.Spec),
	})

	ctx := context.Background()
	get := func() *ntnv1alpha1.SatelliteEphemeris {
		obj := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, obj); err != nil {
			t.Fatalf("get: %v", err)
		}
		return obj
	}
	reconcileAt := func(at time.Time) {
		clock = at
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile @ %s: %v", at.UTC(), err)
		}
	}
	epochAt := func(at time.Time) int64 { return at.Add(propagationEpochLead).UnixMilli() }

	// t0: first reconcile. Sweep is due (LastPassPredictionTime nil) → runs → stamps t0.
	reconcileAt(t0)
	obj := get()
	if len(obj.Status.PropagatedStates) == 0 || obj.Status.PropagatedStates[0].EpochUnixMs != epochAt(t0) {
		t.Fatalf("t0: epoch heartbeat missing/stale; want %d, got %+v", epochAt(t0), obj.Status.PropagatedStates)
	}
	if obj.Status.LastPassPredictionTime == nil || !obj.Status.LastPassPredictionTime.Time.Equal(t0) {
		t.Fatalf("t0: sweep must run on the first reconcile and stamp t0; got %v", obj.Status.LastPassPredictionTime)
	}

	// t0+3/6/9/12m: heartbeats. Epoch refreshes EVERY cycle; sweep does NOT run (not due), so
	// LastPassPredictionTime stays at t0.
	for _, d := range []time.Duration{3 * time.Minute, 6 * time.Minute, 9 * time.Minute, 12 * time.Minute} {
		at := t0.Add(d)
		reconcileAt(at)
		obj = get()
		if got := obj.Status.PropagatedStates[0].EpochUnixMs; got != epochAt(at) {
			t.Fatalf("t0+%s: epoch = %d, want fresh %d (heartbeat must refresh every cycle between sweeps)",
				d, got, epochAt(at))
		}
		if obj.Status.LastPassPredictionTime == nil || !obj.Status.LastPassPredictionTime.Time.Equal(t0) {
			t.Fatalf("t0+%s: sweep must NOT re-run before passPredictionInterval; LastPassPredictionTime = %v, want %s",
				d, obj.Status.LastPassPredictionTime, t0.UTC())
		}
	}

	// t0+15m: sweep is due again → runs → stamps t0+15m. Epoch still fresh.
	at := t0.Add(passPredictionInterval)
	reconcileAt(at)
	obj = get()
	if got := obj.Status.PropagatedStates[0].EpochUnixMs; got != epochAt(at) {
		t.Fatalf("t0+15m: epoch = %d, want fresh %d", got, epochAt(at))
	}
	if obj.Status.LastPassPredictionTime == nil || !obj.Status.LastPassPredictionTime.Time.Equal(at) {
		t.Fatalf("t0+15m: sweep must re-run at passPredictionInterval and stamp %s; got %v",
			at.UTC(), obj.Status.LastPassPredictionTime)
	}
}

// TestReconcile_EpochWrittenFirstInTwoWrites locks the epoch-first two-write structure of Option B
// (ADR 0006 / #234): on a sweep-due cycle the reconcile does TWO status writes — WRITE A carries the
// fresh propagation epoch and lands BEFORE the pass sweep, WRITE B carries the pass result — so a slow
// or cancelled sweep can never delay or drop the epoch (it is already persisted by write A).
//
// Mutations this pins:
//   - collapsing back to a SINGLE write (epoch + pass together, pre-#234): updates would be 1, not 2.
//   - moving the propagate/epoch stamp AFTER write A: the first write would not yet carry the epoch.
func TestReconcile_EpochWrittenFirstInTwoWrites(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-two-write"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}},
		},
	}
	wantEpoch := t0.Add(propagationEpochLead).UnixMilli()

	var statusUpdates int
	var firstWriteHadEpoch bool
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).
		WithInterceptorFuncs(interceptor.Funcs{
			// c is the underlying client; calling it does NOT re-enter this interceptor.
			SubResourceUpdate: func(ctx context.Context, c client.Client, sr string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusUpdates++
				if statusUpdates == 1 {
					if e, ok := obj.(*ntnv1alpha1.SatelliteEphemeris); ok {
						firstWriteHadEpoch = len(e.Status.PropagatedStates) > 0 &&
							e.Status.PropagatedStates[0].EpochUnixMs == wantEpoch
					}
				}
				return c.SubResource(sr).Update(ctx, obj, opts...)
			},
		}).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}

	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}},
		Now:     func() time.Time { return t0 },
	}
	r.ommCache.Store(key, cachedFetch{
		result:   ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0},
		fetchKey: fetchInputKey(eph.Spec),
	})

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !firstWriteHadEpoch {
		t.Fatalf("WRITE A (first status update) must carry the fresh epoch %d BEFORE the pass sweep — "+
			"the epoch heartbeat must not be gated behind the sweep (ADR 0006 / #234)", wantEpoch)
	}
	if statusUpdates != 2 {
		t.Fatalf("a sweep-due cycle must do exactly two status writes (epoch heartbeat A, then pass B); "+
			"got %d — a single combined write reintroduces the pre-#234 coupling", statusUpdates)
	}
}

// TestReconcile_PassPredictionDisabledClearsStaleStatus covers the disabled path (ADR 0006 / #234):
// turning pass prediction off must clear the stale PassesPredicted condition, NextPassWindows, and
// lastPassPredictionTime (so a later re-enable sweeps immediately), riding with WRITE A (no sweep),
// while the epoch heartbeat keeps refreshing.
//
// Mutation this pins: removing the disabled-clear block leaves the stale pass status behind, so the
// post-disable assertions fail. (Without this test, that mutation SURVIVES — verified.)
func TestReconcile_PassPredictionDisabledClearsStaleStatus(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-pass-disable"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}},
		Now:     func() time.Time { return clock },
	}
	r.ommCache.Store(key, cachedFetch{
		result:   ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0},
		fetchKey: fetchInputKey(eph.Spec),
	})
	ctx := context.Background()
	get := func() *ntnv1alpha1.SatelliteEphemeris {
		o := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return o
	}

	// t0: pass configured (absent GS) → sweep runs, fails fast → PredictionFailed condition + stamp.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile t0: %v", err)
	}
	obj := get()
	if obj.Status.LastPassPredictionTime == nil {
		t.Fatal("precondition: the absent-GS sweep should have stamped lastPassPredictionTime")
	}
	if meta.FindStatusCondition(obj.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted) == nil {
		t.Fatal("precondition: the absent-GS sweep should have set the PassesPredicted (PredictionFailed) condition")
	}

	// Disable pass prediction (spec update; status subresource keeps the stale pass status), reconcile.
	obj.Spec.PassPrediction = nil
	if err := cli.Update(ctx, obj); err != nil {
		t.Fatalf("disable update: %v", err)
	}
	clock = t0.Add(3 * time.Minute)
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile after disable: %v", err)
	}

	obj = get()
	if c := meta.FindStatusCondition(obj.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted); c != nil {
		t.Fatalf("disabling pass prediction must REMOVE the PassesPredicted condition; still present: %+v", c)
	}
	if obj.Status.LastPassPredictionTime != nil {
		t.Fatalf("disabling pass prediction must CLEAR lastPassPredictionTime; got %v", obj.Status.LastPassPredictionTime)
	}
	if obj.Status.NextPassWindows != nil {
		t.Fatalf("disabling pass prediction must CLEAR NextPassWindows; got %d entries", len(obj.Status.NextPassWindows))
	}
	if len(obj.Status.PropagatedStates) == 0 ||
		obj.Status.PropagatedStates[0].EpochUnixMs != clock.Add(propagationEpochLead).UnixMilli() {
		t.Fatalf("epoch heartbeat must still refresh while pass prediction is disabled")
	}
}

// TestReconcile_PassInputChangeTriggersImmediateSweep is the Blocker-1 fix (ADR 0006 / #234): a change
// to a pass-prediction input must re-sweep AT ONCE, not wait out passPredictionInterval. Without the
// input-hash arm of passSweepDue, a spec edit 3m after a sweep (well inside the 15m window) would leave
// stale windows. Mutation: dropping the hash arm leaves LastPassPredictionTime at t0 → this fails.
func TestReconcile_PassInputChangeTriggersImmediateSweep(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-pass-inputchange"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source:         ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/test", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}, MinElevation: "10"},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}},
		Now:     func() time.Time { return clock },
	}
	r.ommCache.Store(key, cachedFetch{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0}, fetchKey: fetchInputKey(eph.Spec)})
	ctx := context.Background()
	get := func() *ntnv1alpha1.SatelliteEphemeris {
		o := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return o
	}
	rec := func() {
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile @ %s: %v", clock.UTC(), err)
		}
	}
	rec()
	obj := get()
	sig1 := obj.Status.LastPassPredictionInputHash
	if sig1 == "" || obj.Status.LastPassPredictionTime == nil || !obj.Status.LastPassPredictionTime.Time.Equal(t0) {
		t.Fatalf("t0: expected first sweep to stamp time=t0 and a non-empty input hash; got time=%v hash=%q",
			obj.Status.LastPassPredictionTime, sig1)
	}
	// Change a pass-prediction input (minElevation) well inside the 15m window.
	obj.Spec.PassPrediction.MinElevation = "25"
	if err := cli.Update(ctx, obj); err != nil {
		t.Fatalf("spec update: %v", err)
	}
	clock = t0.Add(3 * time.Minute)
	rec()
	obj = get()
	if obj.Status.LastPassPredictionTime == nil || obj.Status.LastPassPredictionTime.Time.Equal(t0) {
		t.Fatalf("input change must re-sweep IMMEDIATELY (< 15m), advancing lastPassPredictionTime off t0; got %v",
			obj.Status.LastPassPredictionTime)
	}
	if !obj.Status.LastPassPredictionTime.Time.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("re-sweep should stamp t0+3m; got %v", obj.Status.LastPassPredictionTime)
	}
	if obj.Status.LastPassPredictionInputHash == sig1 {
		t.Fatalf("input hash must change after a minElevation edit; still %q", sig1)
	}
}

// TestReconcile_FetchFailureInvalidatesPassCadence is the Blocker-2 fix (ADR 0006 / #234): a cold-cache
// fetch failure that clears the pass windows must ALSO clear the cadence timestamp + input hash, so a
// rapid recovery re-sweeps at once. Mutation: leaving lastPassPredictionTime set (the old handleFetchError
// behavior) blocks the recovery sweep for up to 15m → the recovery assertion fails.
func TestReconcile_FetchFailureInvalidatesPassCadence(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-fetchfail-cadence"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source:         ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/test", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}, MinElevation: "10"},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}
	fetcher := &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400), Fetcher: fetcher,
		Now: func() time.Time { return clock },
	}
	r.ommCache.Store(key, cachedFetch{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0}, fetchKey: fetchInputKey(eph.Spec)})
	ctx := context.Background()
	get := func() *ntnv1alpha1.SatelliteEphemeris {
		o := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return o
	}

	// t0: normal reconcile → sweep stamps timestamp + hash.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile t0: %v", err)
	}
	if get().Status.LastPassPredictionTime == nil {
		t.Fatal("precondition: first reconcile should have stamped lastPassPredictionTime")
	}

	// Cold-cache fetch failure: drop the cache and make the fetch error → handleFetchError path.
	r.ommCache.Delete(key)
	fetcher.mu.Lock()
	fetcher.result = ephemeris.GPFetchResult{}
	fetcher.err = errFetchBoom
	fetcher.mu.Unlock()
	clock = t0.Add(3 * time.Minute)
	_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // may return an error; we assert on status
	obj := get()
	if obj.Status.LastPassPredictionTime != nil {
		t.Fatalf("cold-cache fetch failure must CLEAR lastPassPredictionTime; got %v", obj.Status.LastPassPredictionTime)
	}
	if obj.Status.LastPassPredictionInputHash != "" {
		t.Fatalf("cold-cache fetch failure must CLEAR the input hash; got %q", obj.Status.LastPassPredictionInputHash)
	}

	// Recovery: fetch succeeds again → the sweep must re-run immediately (timestamp was cleared), not
	// wait out 15m.
	fetcher.mu.Lock()
	fetcher.result = ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}
	fetcher.err = nil
	fetcher.mu.Unlock()
	clock = t0.Add(4 * time.Minute)
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile recovery: %v", err)
	}
	obj = get()
	if obj.Status.LastPassPredictionTime == nil || !obj.Status.LastPassPredictionTime.Time.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("recovery must re-sweep immediately (stamp t0+4m), not wait 15m; got %v", obj.Status.LastPassPredictionTime)
	}
}

var errFetchBoom = fmtErrorf("simulated GP fetch failure")

func fmtErrorf(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// TestReconcile_SlowSweepDoesNotDelayEpoch_AndCancelPersists is the Blocker-3 acceptance test the ADR
// requires (ADR 0006 / #234). It blocks the pass sweep mid-flight (at its GroundStationLifecycle Get)
// and proves, at the full Reconcile level, that (a) the fresh epoch is ALREADY persisted while the sweep
// is still blocked — a slow sweep does not delay the epoch — and (b) cancelling the context then makes
// Reconcile return context.Canceled with the epoch still persisted and the cadence NOT advanced.
//
// This is mutation-proof against moving the sweep before WRITE A (the epoch would not be readable while
// blocked) and against stamping the cadence on a cancelled sweep.
func TestReconcile_SlowSweepDoesNotDelayEpoch_AndCancelPersists(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-slow-sweep"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source:         ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/test", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}, MinElevation: "10"},
		},
	}
	var gsGets atomic.Int32
	sweepStarted := make(chan struct{})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				// The GroundStationLifecycle is read TWICE per reconcile: 1st by the input signature,
				// 2nd by the sweep (predictPasses). Block only the 2nd — the sweep — so WRITE A (before
				// the sweep) has already happened.
				if _, ok := obj.(*ntnv1alpha1.GroundStationLifecycle); ok && gsGets.Add(1) == 2 {
					close(sweepStarted)
					<-ctx.Done() // hold the sweep until the test cancels
					return ctx.Err()
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}},
		Now:     func() time.Time { return t0 },
	}
	r.ommCache.Store(key, cachedFetch{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0}, fetchKey: fetchInputKey(eph.Spec)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		done <- err
	}()

	select {
	case <-sweepStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep did not start")
	}
	// (a) the fresh epoch must already be persisted while the sweep is blocked.
	wantEpoch := t0.Add(propagationEpochLead).UnixMilli()
	obj := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, obj); err != nil {
		t.Fatalf("read while blocked: %v", err)
	}
	if len(obj.Status.PropagatedStates) == 0 || obj.Status.PropagatedStates[0].EpochUnixMs != wantEpoch {
		t.Fatalf("a blocked (slow) sweep must NOT delay the epoch: want %d persisted by WRITE A, got %+v",
			wantEpoch, obj.Status.PropagatedStates)
	}
	if obj.Status.LastPassPredictionTime != nil {
		t.Fatal("cadence must not be stamped before the sweep completes")
	}

	// (b) cancel → Reconcile returns context.Canceled; epoch survives; cadence not advanced.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Reconcile should return context.Canceled from the cancelled sweep; got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Reconcile did not return after cancel")
	}
	obj = &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, obj); err != nil {
		t.Fatalf("read after cancel: %v", err)
	}
	if len(obj.Status.PropagatedStates) == 0 || obj.Status.PropagatedStates[0].EpochUnixMs != wantEpoch {
		t.Fatal("epoch must survive a cancelled sweep (already persisted by WRITE A)")
	}
	if obj.Status.LastPassPredictionTime != nil {
		t.Fatalf("a cancelled sweep must NOT advance the cadence timestamp; got %v", obj.Status.LastPassPredictionTime)
	}
}

// TestReconcile_PassPredictionWriteCountBounded locks the ACCEPTED churn bound (ADR 0006 Option A / #234
// churn decision, @thc1006 2026-07-19): WRITE A (epoch) fires every heartbeat; WRITE B (pass) fires ONLY
// on a due sweep — never a second write on every heartbeat. Over 6 heartbeats with 2 due sweeps the total
// is 6 + 2 = 8 status writes. Mutation "second write every heartbeat" makes it 12 → caught.
func TestReconcile_PassPredictionWriteCountBounded(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-writecount"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source:         ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/test", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs-absent"}, MinElevation: "10"},
		},
	}
	var statusWrites atomic.Int32
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sr string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusWrites.Add(1)
				return c.SubResource(sr).Update(ctx, obj, opts...)
			},
		}).Build()
	key := types.NamespacedName{Name: name, Namespace: ns}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(400),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1}},
		Now:     func() time.Time { return clock },
	}
	r.ommCache.Store(key, cachedFetch{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: t0}, fetchKey: fetchInputKey(eph.Spec)})
	ctx := context.Background()
	// 6 heartbeats: t0 (sweep-due), +3/+6/+9/+12m (heartbeat only), +15m (sweep-due again).
	for _, d := range []time.Duration{0, 3 * time.Minute, 6 * time.Minute, 9 * time.Minute, 12 * time.Minute, 15 * time.Minute} {
		clock = t0.Add(d)
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile @ %s: %v", clock.UTC(), err)
		}
	}
	// 6 WRITE A (one per heartbeat) + 2 WRITE B (due sweeps at t0 and t0+15m) = 8.
	if got := statusWrites.Load(); got != 8 {
		t.Fatalf("status writes = %d, want 8 (6 epoch heartbeats + 2 due-sweep pass writes). A second write "+
			"on EVERY heartbeat would be 12 — the churn regression Option A forbids", got)
	}
}
