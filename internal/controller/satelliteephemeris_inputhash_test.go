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
)

// TestFetchInputKey_ExcludesNonFetchFields pins #221 review finding 2: the OMM cache key
// must identify the RAW UPSTREAM PAYLOAD (source type + URL) only. Keying it on
// .metadata.generation over-invalidated — ANY spec edit dropped the cache, so a fetch that
// failed right after an unrelated edit had NO fallback and the producer could not
// re-propagate. Note the NORAD selector is EXCLUDED on purpose: FilterOMMs applies it
// client-side to the raw payload, so a selector edit must not discard the raw OMMs.
func TestFetchInputKey_ExcludesNonFetchFields(t *testing.T) {
	base := ntnv1alpha1.SatelliteEphemerisSpec{
		Source: ntnv1alpha1.EphemerisSource{
			Type: "CelesTrak", URL: "https://celestrak.org/x",
			RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
		},
		Satellites: &ntnv1alpha1.SatelliteSelector{NoradIDs: []int{25544, 200}},
		PassPrediction: &ntnv1alpha1.PassPredictionSpec{
			GroundStations: []string{"gs"}, MinElevation: "10",
			Horizon: metav1.Duration{Duration: 24 * time.Hour},
		},
	}
	k0 := fetchInputKey(base)

	// Edits that do NOT change the upstream payload must keep the SAME key.
	pp := *base.DeepCopy()
	pp.PassPrediction.Horizon = metav1.Duration{Duration: 48 * time.Hour}
	if fetchInputKey(pp) != k0 {
		t.Error("a passPrediction edit must NOT invalidate the raw OMM cache")
	}
	ri := *base.DeepCopy()
	ri.Source.RefreshInterval = metav1.Duration{Duration: 12 * time.Hour}
	if fetchInputKey(ri) != k0 {
		t.Error("a refreshInterval edit must NOT invalidate the raw OMM cache")
	}
	ns := *base.DeepCopy()
	ns.Satellites.NoradIDs = []int{25544}
	if fetchInputKey(ns) != k0 {
		t.Error("a NORAD-selector edit must NOT invalidate the raw OMM cache (FilterOMMs is client-side)")
	}

	// Edits that DO change the upstream payload must change the key.
	su := *base.DeepCopy()
	su.Source.URL = "https://celestrak.org/y"
	if fetchInputKey(su) == k0 {
		t.Error("a source URL edit MUST invalidate the raw OMM cache")
	}
	st := *base.DeepCopy()
	st.Source.Type = "SpaceTrack"
	if fetchInputKey(st) == k0 {
		t.Error("a source type edit MUST invalidate the raw OMM cache")
	}
}

// TestReconcile_PassPredictionEdit_FetchFails_ReusesCache is the behavioral half of
// finding 2: a passPrediction-only edit (which bumps .metadata.generation but changes
// NOTHING about the upstream payload) followed by a FAILING fetch must still fall back to
// the last-good OMMs and keep re-propagating — the "non-propagation edits preserve
// continuity" property this PR claims. With the old generation-keyed cache the fallback was
// stranded and SIB19 continuity broke.
// Mutation: make fetchInputKey include passPrediction → the fallback misses → no states.
func TestReconcile_PassPredictionEdit_FetchFails_ReusesCache(t *testing.T) {
	sch := makeScheme(t)
	ctx := context.Background()
	const ns = "default"
	gs := &ntnv1alpha1.GroundStationLifecycle{
		ObjectMeta: metav1.ObjectMeta{Name: "gs-taipei", Namespace: ns},
		Spec: ntnv1alpha1.GroundStationLifecycleSpec{
			Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "ennoconn", Model: "edge-5000"},
			Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654", Alt: "15"}},
		},
	}
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-pp", Namespace: ns, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{
				GroundStations: []string{"gs-taipei"}, MinElevation: "10",
				Horizon: metav1.Duration{Duration: 24 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs, eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: "eph-pp", Namespace: ns}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10),
		Fetcher: &mockGPFetcher{err: errors.New("connection refused")}, // upstream is DOWN
	}
	// Cache seeded under the PRE-EDIT spec, older than the 4h window so a fetch is forced.
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{issOMMForTest()}, SatelliteCount: 1, FetchedAt: time.Now().Add(-5 * time.Hour),
		},
		fetchKey: fetchInputKey(got.Spec),
		uid:      got.UID,
	})

	// A passPrediction-ONLY edit. Bump generation to simulate the apiserver.
	got.Spec.PassPrediction.Horizon = metav1.Duration{Duration: 48 * time.Hour}
	got.Generation = 2
	if err := cli.Update(ctx, got); err != nil {
		t.Fatalf("passPrediction edit: %v", err)
	}

	// Fetch fails; the cache must still serve because the upstream payload identity is
	// unchanged, so propagation (and therefore SIB19 continuity) survives the outage.
	_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})

	after := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, after); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if len(after.Status.PropagatedStates) == 0 {
		t.Fatal("a passPrediction-only edit + failing fetch must STILL re-propagate from the cached OMMs " +
			"(#221 review finding 2): the cache key must not include non-fetch spec fields")
	}
	if after.Status.PropagatedStatesInputHash != propagationInputHash(after.Spec) {
		t.Errorf("the re-propagated states must be stamped with the current propagation-input hash")
	}
	cond := meta.FindStatusCondition(after.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "FetchFailedServingCache" {
		t.Errorf("expected GPDataFetched=False/FetchFailedServingCache (degraded but serving), got %+v", cond)
	}
}

// TestPropagateStates_SkipsFutureDatedSourceEpoch pins #221 review finding 3: an
// implausibly FUTURE-dated element set must be rejected by the PRODUCER, before SGP4 runs.
// The consumer's future-skew check only blocks delivery — it cannot prevent SGP4 being
// driven backward from a bogus epoch and writing a wildly wrong ECEF into status.
// Mutation: remove the producer-side guard → a state is produced from the future epoch.
func TestPropagateStates_SkipsFutureDatedSourceEpoch(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-future", Namespace: "default"},
	}
	epoch := time.Now().Add(propagationEpochLead)

	// Element epoch 30 days in the FUTURE (negative age) → must not be propagated at all.
	r.propagateStates(context.Background(), eph,
		ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMWithEpoch(-30 * 24 * time.Hour)}}, epoch)
	if n := len(eph.Status.PropagatedStates); n != 0 {
		t.Fatalf("an implausibly future-dated element set must NOT be propagated, got %d state(s)", n)
	}

	// Control: a recent element set still propagates normally.
	r.propagateStates(context.Background(), eph,
		ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMWithEpoch(2 * time.Hour)}}, epoch)
	if n := len(eph.Status.PropagatedStates); n != 1 {
		t.Fatalf("a fresh element set must still propagate, got %d state(s)", n)
	}
}

// TestPropagationInputHash_ScopedToPropagationInputs pins #221 review finding 2: the hash
// changes ONLY when a propagation-relevant input changes (source type/url, NORAD
// selector) and is STABLE across edits that do not change the propagated ECEF
// (pass-prediction params, refreshInterval) — so a pass-prediction-only edit does not
// falsely invalidate valid propagated states.
func TestPropagationInputHash_ScopedToPropagationInputs(t *testing.T) {
	base := ntnv1alpha1.SatelliteEphemerisSpec{
		Source: ntnv1alpha1.EphemerisSource{
			Type: "CelesTrak", URL: "https://celestrak.org/x",
			RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
		},
		Satellites: &ntnv1alpha1.SatelliteSelector{NoradIDs: []int{25544, 200}},
	}
	h0 := propagationInputHash(base)

	// NORAD order must not matter (sorted).
	reordered := *base.DeepCopy()
	reordered.Satellites.NoradIDs = []int{200, 25544}
	if propagationInputHash(reordered) != h0 {
		t.Fatal("NORAD selector order must not change the hash")
	}

	// refreshInterval change → SAME hash (not a propagation input).
	ri := *base.DeepCopy()
	ri.Source.RefreshInterval = metav1.Duration{Duration: 12 * time.Hour}
	if propagationInputHash(ri) != h0 {
		t.Fatal("a refreshInterval-only edit must NOT change the propagation-input hash")
	}

	// source URL change → DIFFERENT hash.
	su := *base.DeepCopy()
	su.Source.URL = "https://celestrak.org/y"
	if propagationInputHash(su) == h0 {
		t.Fatal("a source URL edit MUST change the propagation-input hash")
	}

	// NORAD selector change → DIFFERENT hash.
	ns := *base.DeepCopy()
	ns.Satellites.NoradIDs = []int{25544}
	if propagationInputHash(ns) == h0 {
		t.Fatal("a NORAD selector edit MUST change the propagation-input hash")
	}
}

// TestReconcile_InputHash_StampedOnSuccess_NotRestampedOnStaleInputsFetchFail pins the
// producer half of #204-G1 under the input-hash model: a successful reconcile stamps
// propagatedStatesInputHash = hash(spec); after a source edit whose re-fetch then FAILS,
// the hash is NOT restamped, so it keeps pointing at the OLD inputs (!= hash of the new
// spec) — which is exactly what lets the consumer detect the stale states.
func TestReconcile_InputHash_StampedOnSuccess_NotRestampedOnStaleInputsFetchFail(t *testing.T) {
	sch := makeScheme(t)
	ctx := context.Background()
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-hash", Namespace: "default", Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/original",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	oldHash := propagationInputHash(eph.Spec)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	fetcher := &mockGPFetcher{result: ephemeris.GPFetchResult{
		OMMs: []sgp4.OMM{issOMMForTest()}, SatelliteCount: 1, FetchedAt: time.Now(),
	}}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10), Fetcher: fetcher,
	}
	key := types.NamespacedName{Name: "eph-hash", Namespace: "default"}

	// 1) Success stamps hash(spec) and populates a per-state source epoch.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile 1 (success): %v", err)
	}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, got); err != nil {
		t.Fatalf("get after reconcile 1: %v", err)
	}
	if got.Status.PropagatedStatesInputHash != oldHash {
		t.Fatalf("successful reconcile must stamp the input hash, got %q want %q", got.Status.PropagatedStatesInputHash, oldHash)
	}
	if len(got.Status.PropagatedStates) == 0 || got.Status.PropagatedStates[0].SourceEpochUnixMs == 0 {
		t.Fatal("sanity: a successful propagate must write states with a source epoch")
	}

	// 2) Edit the source URL (new propagation inputs) and fail the re-fetch → the hash
	//    must NOT be restamped, so it still reflects the OLD inputs. Bump .Generation to
	//    simulate the apiserver (a spec edit bumps generation, which invalidates the
	//    generation-keyed OMM cache — otherwise the fetch-fail path would serve old-source
	//    cache and wrongly restamp; #221 review finding 6, test fidelity).
	got.Spec.Source.URL = "https://celestrak.org/changed"
	got.Generation = 2
	newHash := propagationInputHash(got.Spec)
	if newHash == oldHash {
		t.Fatal("test setup: the edited spec must hash differently")
	}
	if err := cli.Update(ctx, got); err != nil {
		t.Fatalf("edit source url: %v", err)
	}
	fetcher.err = errors.New("connection refused")
	fetcher.result = ephemeris.GPFetchResult{}
	_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	after := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, after); err != nil {
		t.Fatalf("get after reconcile 2: %v", err)
	}
	if after.Status.PropagatedStatesInputHash != oldHash {
		t.Fatalf("a failed re-fetch under new inputs must NOT restamp the hash: got %q want (old) %q",
			after.Status.PropagatedStatesInputHash, oldHash)
	}
	if after.Status.PropagatedStatesInputHash == newHash {
		t.Fatal("the stored hash must NOT match the new spec inputs (states are stale for them)")
	}
}
