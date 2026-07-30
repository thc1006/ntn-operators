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

// taipeiGSForPass returns a GroundStationLifecycle over Taipei — the ISS (i≈51.6°) overflies 25°N, so a
// 24h/min-elev-10 sweep from issOMMForTest() yields real pass windows.
func taipeiGSForPass(ns, name string) *ntnv1alpha1.GroundStationLifecycle {
	return &ntnv1alpha1.GroundStationLifecycle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: ntnv1alpha1.GroundStationLifecycleSpec{
			Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "ennoconn", Model: "edge-5000"},
			Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654", Alt: "15"}},
		},
	}
}

// ephWithPass builds a pass-prediction SatelliteEphemeris over the given ground station(s).
func ephWithPass(ns, name string, gsNames ...string) *ntnv1alpha1.SatelliteEphemeris {
	return &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source:         ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "https://celestrak.org/test", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: gsNames, MinElevation: "10", Horizon: metav1.Duration{Duration: 24 * time.Hour}},
		},
	}
}

// TestCheckSatelliteAvailability_PassesPredictedGate is the round-5 Blocker-3 CONSUMER contract test: the
// NTNSlice reader must treat pass availability as KNOWN only when the producer's PassesPredicted condition
// is True. Absent (never predicted), Unknown (recomputing after an input change or a no-OMM failure), and
// False (PredictionFailed) all mean UNKNOWN → hold the current path — never read the (possibly stale or
// empty) NextPassWindows as a real end-of-pass. Only True + no active window is a genuine end-of-pass.
//
// Mutation: dropping the PassesPredicted gate (the pre-round-5 behavior) makes the "Unknown"/"False"/
// "absent-with-empty-windows" rows report known=true, so a recomputing/failed producer would steer
// failover — this test fails on every such row.
func TestCheckSatelliteAvailability_PassesPredictedGate(t *testing.T) {
	const ns = "default"
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	activeWindow := []ntnv1alpha1.PassWindow{{
		Satellite: "ISS", GroundStation: "gs",
		AOS: metav1.Time{Time: now.Add(-10 * time.Minute)}, LOS: metav1.Time{Time: now.Add(10 * time.Minute)},
	}}
	predicted := func(s metav1.ConditionStatus) []metav1.Condition {
		return []metav1.Condition{{Type: ntnv1alpha1.ConditionPassesPredicted, Status: s, Reason: "x"}}
	}

	cases := []struct {
		name          string
		conds         []metav1.Condition
		windows       []ntnv1alpha1.PassWindow
		wantAvailable bool
		wantKnown     bool
	}{
		{"true+active-window", predicted(metav1.ConditionTrue), activeWindow, true, true},
		{"true+no-window (genuine end-of-pass)", predicted(metav1.ConditionTrue), nil, false, true},
		{"unknown/recomputing holds despite an active window", predicted(metav1.ConditionUnknown), activeWindow, false, false},
		{"false/predictionfailed holds despite an active window", predicted(metav1.ConditionFalse), activeWindow, false, false},
		{"absent condition holds despite an active window", nil, activeWindow, false, false},
		{"absent condition + empty windows still holds (not a real end-of-pass)", nil, nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := makeScheme(t)
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "eph", Namespace: ns},
				Status:     ntnv1alpha1.SatelliteEphemerisStatus{NextPassWindows: tc.windows, Conditions: tc.conds},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: ns},
				Spec:       ntnv1alpha1.NTNSliceSpec{SatellitePath: ntnv1alpha1.SatellitePathSpec{EphemerisRef: "eph"}},
			}
			cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, slice).Build()
			r := &NTNSliceReconciler{Client: cli, Scheme: sch}
			available, known, _ := r.checkSatelliteAvailability(context.Background(), slice, now)
			if available != tc.wantAvailable || known != tc.wantKnown {
				t.Fatalf("checkSatelliteAvailability = (available=%v, known=%v), want (available=%v, known=%v)",
					available, known, tc.wantAvailable, tc.wantKnown)
			}
		})
	}
}

// TestReconcile_PassInputChange_WriteASequenceHoldsConsumer is the round-5 Blocker-1 acceptance test: an
// INPUT change must mark PassesPredicted != True (Unknown/Recomputing) with the windows CLEARED in WRITE A
// — the same write that carries the fresh epoch — so a consumer reading anywhere in the A->B gap HOLDS
// instead of steering on windows computed from the OLD inputs. It records every status write in order and
// asserts the sequence is [Unknown + 0 windows] then [True + N>0 windows].
//
// Mutation: the pre-round-5 flow (compute the sweep decision AFTER write A, no input-change invalidation)
// makes write A carry the stale True + old windows → the first snapshot is (True, >0) → this fails.
func TestReconcile_PassInputChange_WriteASequenceHoldsConsumer(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-inputchange-seq"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	gs := taipeiGSForPass(ns, "gs-taipei")
	eph := ephWithPass(ns, name, "gs-taipei")

	type snap struct {
		status  metav1.ConditionStatus
		hasCond bool
		windows int
	}
	var writes []snap
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs, eph).WithStatusSubresource(eph).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sr string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if e, ok := obj.(*ntnv1alpha1.SatelliteEphemeris); ok {
					s := snap{windows: len(e.Status.NextPassWindows)}
					if cond := meta.FindStatusCondition(e.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted); cond != nil {
						s.hasCond, s.status = true, cond.Status
					}
					writes = append(writes, s)
				}
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
	get := func() *ntnv1alpha1.SatelliteEphemeris {
		o := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return o
	}

	// Reconcile #1 establishes real windows + PassesPredicted=True.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #1: %v", err)
	}
	pre := get()
	if !meta.IsStatusConditionTrue(pre.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted) || len(pre.Status.NextPassWindows) == 0 {
		t.Fatalf("precondition: reconcile #1 must yield PassesPredicted=True with >0 windows; got cond=%+v windows=%d",
			meta.FindStatusCondition(pre.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted), len(pre.Status.NextPassWindows))
	}

	// Edit a pass input (minElevation) well inside the 15m cadence, then record the write sequence.
	pre.Spec.PassPrediction.MinElevation = "25"
	pre.Generation = 2
	if err := cli.Update(ctx, pre); err != nil {
		t.Fatalf("spec edit: %v", err)
	}
	writes = nil
	clock = t0.Add(2 * time.Minute)
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #2 (post-edit): %v", err)
	}

	if len(writes) != 2 {
		t.Fatalf("an input-change reconcile must do exactly WRITE A then WRITE B (2 status writes); got %d: %+v", len(writes), writes)
	}
	// WRITE A: windows cleared and PassesPredicted != True so the consumer holds during A->B.
	if writes[0].windows != 0 {
		t.Errorf("WRITE A must clear the stale windows on an input change; got %d windows", writes[0].windows)
	}
	if writes[0].hasCond && writes[0].status == metav1.ConditionTrue {
		t.Errorf("WRITE A must NOT leave PassesPredicted=True over stale/cleared windows; got %v", writes[0].status)
	}
	// WRITE B: fresh windows + True.
	if writes[1].windows == 0 || writes[1].status != metav1.ConditionTrue {
		t.Errorf("WRITE B must republish fresh windows + PassesPredicted=True; got status=%v windows=%d", writes[1].status, writes[1].windows)
	}
	// Final persisted state matches WRITE B.
	post := get()
	if !meta.IsStatusConditionTrue(post.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted) || len(post.Status.NextPassWindows) == 0 {
		t.Fatalf("final state must be PassesPredicted=True with windows; got cond=%+v windows=%d",
			meta.FindStatusCondition(post.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted), len(post.Status.NextPassWindows))
	}
}

// invalidatesPassStatus reconciles an eph pre-seeded with a healthy pass status down a NO-OMM failure path
// and asserts every field of the pass status is cleared. Shared by the setup-failure and insecure-URL
// round-5 Blocker-2 tests: any path that publishes no OMM leaves the previously-computed windows unbacked.
func assertNoOMMPathInvalidates(t *testing.T, r *SatelliteEphemerisReconciler, cli client.Client, key types.NamespacedName) {
	t.Helper()
	ctx := context.Background()
	// A requeue error is acceptable on these no-OMM paths; we assert on the persisted status.
	_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.NextPassWindows) != 0 {
		t.Errorf("a no-OMM failure path must clear NextPassWindows; got %d", len(got.Status.NextPassWindows))
	}
	if got.Status.LastPassPredictionTime != nil {
		t.Errorf("a no-OMM failure path must clear lastPassPredictionTime; got %v", got.Status.LastPassPredictionTime)
	}
	if got.Status.LastPassPredictionInputHash != "" {
		t.Errorf("a no-OMM failure path must clear lastPassPredictionInputHash; got %q", got.Status.LastPassPredictionInputHash)
	}
	if meta.IsStatusConditionTrue(got.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted) {
		t.Errorf("a no-OMM failure path must not leave PassesPredicted=True")
	}
}

// seedHealthyPassStatus pre-populates an eph with a healthy prior prediction so the no-OMM paths have
// something to invalidate.
func seedHealthyPassStatus(eph *ntnv1alpha1.SatelliteEphemeris, at time.Time) {
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ISS", GroundStation: "gs",
		AOS: metav1.Time{Time: at.Add(1 * time.Hour)}, LOS: metav1.Time{Time: at.Add(2 * time.Hour)},
	}}
	eph.Status.LastPassPredictionTime = &metav1.Time{Time: at}
	eph.Status.LastPassPredictionInputHash = "seeded-stale-hash"
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "Predicted",
	})
}

// TestReconcile_SetupFailureInvalidatesPassStatus — round-5 Blocker-2: a cold-cache fetcher/credential
// SETUP failure (SpaceTrack source, no fetcher configured, no cache) publishes no OMM, so it must clear the
// previously-published pass status. Mutation: dropping invalidatePassPredictionStatus from
// handleSetupFailure leaves PassesPredicted=True over windows nothing will refresh → the consumer keeps
// steering on them → this fails.
func TestReconcile_SetupFailureInvalidatesPassStatus(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-setupfail-invalidate"
	)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			// SpaceTrack with no SpaceTrackFetcher wired → fetcherForSource errors; no cache → cold.
			Source:         ntnv1alpha1.EphemerisSource{Type: "SpaceTrack", URL: "https://space-track.org/x", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs"}, MinElevation: "10"},
		},
	}
	seedHealthyPassStatus(eph, t0)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50),
		Now: func() time.Time { return t0 },
	}
	assertNoOMMPathInvalidates(t, r, cli, types.NamespacedName{Name: name, Namespace: ns})
}

// TestReconcile_InsecureURLInvalidatesPassStatus — round-5 Blocker-2: a rejected cleartext-http public
// source fetches no OMM, so it must clear the previously-published pass status. Mutation: dropping
// invalidatePassPredictionStatus from handleInsecureURL leaves stale True windows the consumer trusts.
func TestReconcile_InsecureURLInvalidatesPassStatus(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-insecure-invalidate"
	)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source:         ntnv1alpha1.EphemerisSource{Type: "CelesTrak", URL: "http://8.8.8.8/gp.json", RefreshInterval: metav1.Duration{Duration: 4 * time.Hour}},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{GroundStations: []string{"gs"}, MinElevation: "10"},
		},
	}
	seedHealthyPassStatus(eph, t0)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50),
		// A non-nil fetcher so fetcherForSource(CelesTrak) succeeds and the insecure-URL guard (not a
		// setup failure) is what rejects the fetch.
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{}},
		Now:     func() time.Time { return t0 },
	}
	assertNoOMMPathInvalidates(t, r, cli, types.NamespacedName{Name: name, Namespace: ns})
}

// TestPassSweepDue_FutureTimestampIsDue — round-5 P2: a lastPassPredictionTime in the FUTURE (clock skew,
// restored backup, hand-edited status) must be treated as DUE so it self-heals, not suppress every sweep
// until wall-clock catches up. Mutation: dropping the `last.After(now)` guard returns false here.
func TestPassSweepDue_FutureTimestampIsDue(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			LastPassPredictionInputHash: "same",
			LastPassPredictionTime:      &metav1.Time{Time: now.Add(1 * time.Hour)}, // in the future
		},
	}
	if !passSweepDue(eph, now, "same") {
		t.Fatal("a future lastPassPredictionTime with an unchanged input hash must still be DUE (self-heal)")
	}
	// Sanity: an in-past, within-interval timestamp with an unchanged hash is NOT due.
	eph.Status.LastPassPredictionTime = &metav1.Time{Time: now.Add(-1 * time.Minute)}
	if passSweepDue(eph, now, "same") {
		t.Fatal("a recent (within-interval) timestamp with an unchanged hash must NOT be due")
	}
}

// TestReconcile_CadenceStampedAtSweepCompletion — round-5 P2: the cadence timestamp is stamped at sweep
// COMPLETION (a fresh r.now()), not the reconcile-start time. A sweep whose own duration outlasts the
// interval would otherwise be "due" the instant it returns and re-sweep back-to-back. We advance the clock
// by +20m (> the 15m interval) exactly when the sweep reads its ground station, then assert the stamped
// time is the COMPLETION time, not the start. Mutation: stamping the reconcile-start `now` stamps t0.
func TestReconcile_CadenceStampedAtSweepCompletion(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-completion-stamp"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	gs := taipeiGSForPass(ns, "gs-taipei")
	eph := ephWithPass(ns, name, "gs-taipei")

	var gsGets atomic.Int32
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs, eph).WithStatusSubresource(eph).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				// GS is read twice: 1st by the input signature, 2nd by the sweep. Advance the clock on the
				// 2nd (the sweep) so sweep-completion time differs from reconcile-start time.
				if _, ok := obj.(*ntnv1alpha1.GroundStationLifecycle); ok && gsGets.Add(1) == 2 {
					clock = t0.Add(20 * time.Minute)
				}
				return c.Get(ctx, key, obj, opts...)
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
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	wantCompletion := t0.Add(20 * time.Minute)
	if got.Status.LastPassPredictionTime == nil || !got.Status.LastPassPredictionTime.Time.Equal(wantCompletion) {
		t.Fatalf("cadence must be stamped at sweep COMPLETION (%v), not reconcile-start (%v); got %v",
			wantCompletion, t0, got.Status.LastPassPredictionTime)
	}
}

// TestReconcile_TerminatingGroundStationExcludedAndResweeps — round-5 P3: a ground station under deletion
// (a deletionTimestamp does NOT bump SatelliteEphemeris generation) must (a) flip the input signature so a
// sweep re-runs, and (b) be excluded from the sweep. With the eph's ONLY ground station terminating, the
// re-sweep yields zero windows while PassesPredicted stays a definite condition. Mutation: omitting the
// terminating flag from the signature leaves the sweep un-triggered (windows keep the terminating GS);
// omitting the predictPasses skip keeps computing its passes.
func TestReconcile_TerminatingGroundStationExcludedAndResweeps(t *testing.T) {
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	sch := makeScheme(t)
	const (
		ns   = "default"
		name = "eph-terminating-gs"
	)
	omm := issOMMForTest()
	omm.EpochStr = t0.Format("2006-01-02T15:04:05.000000")
	// A finalizer keeps the object Gettable after Delete sets its deletionTimestamp.
	gs := taipeiGSForPass(ns, "gs-taipei")
	gs.Finalizers = []string{"ntn.operators.dev/test-hold"}
	eph := ephWithPass(ns, name, "gs-taipei")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs, eph).WithStatusSubresource(eph).Build()
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

	// Live GS → real windows.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #1: %v", err)
	}
	if len(get().Status.NextPassWindows) == 0 {
		t.Fatal("precondition: a live ground station must yield >0 windows")
	}
	sigLive := get().Status.LastPassPredictionInputHash

	// Put the GS into terminating (deletionTimestamp set, finalizer holds it). Do NOT advance the clock
	// past the interval — only the signature flip may re-trigger the sweep.
	live := &ntnv1alpha1.GroundStationLifecycle{}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: ns, Name: "gs-taipei"}, live); err != nil {
		t.Fatalf("get gs: %v", err)
	}
	if err := cli.Delete(ctx, live); err != nil {
		t.Fatalf("delete (terminate) gs: %v", err)
	}
	if get2 := (&ntnv1alpha1.GroundStationLifecycle{}); cli.Get(ctx, client.ObjectKey{Namespace: ns, Name: "gs-taipei"}, get2) != nil || get2.DeletionTimestamp == nil {
		t.Fatal("precondition: the ground station must be terminating (deletionTimestamp set, finalizer holds it)")
	}
	clock = t0.Add(1 * time.Minute) // well inside the 15m cadence
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #2 (terminating gs): %v", err)
	}
	after := get()
	if after.Status.LastPassPredictionInputHash == sigLive {
		t.Fatal("a ground station entering deletion must flip the input signature (it does not bump generation)")
	}
	if len(after.Status.NextPassWindows) != 0 {
		t.Fatalf("a terminating ground station must be excluded from the sweep; got %d windows for a sole-terminating-GS eph", len(after.Status.NextPassWindows))
	}
}
