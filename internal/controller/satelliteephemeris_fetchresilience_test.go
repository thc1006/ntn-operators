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

// TestReconcile_FreshCache_SkipsFetcherSetup_C2 pins #200-C2: a within-window cached fetch
// must be served BEFORE fetcher/credential setup, so a brief credential unavailability
// cannot break a reconcile the cache already answers. The CelesTrak fetcher is left nil so
// fetcherForSource would ERROR ("fetcher is not configured") if it ran — standing in for
// the real-world Space-Track Secret-read failure the finding is about. With the fresh
// cache served first, the reconcile succeeds and never consults the fetcher.
// Mutation: move fetcherForSource back before the cache check and this gets
// FetcherSetupFailed instead.
func TestReconcile_FreshCache_SkipsFetcherSetup_C2(t *testing.T) {
	sch := makeScheme(t)
	const ns = "default"
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-c2", Namespace: ns},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: "eph-c2", Namespace: ns}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10), Fetcher: nil, // deliberately unconfigured
	}
	// FRESH cache (FetchedAt = now, inside the 4h window).
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result:   ephemeris.GPFetchResult{OMMs: []sgp4.OMM{testISSOMM()}, SatelliteCount: 1, FetchedAt: time.Now()},
		fetchKey: fetchInputKey(got.Spec), uid: got.UID,
	})

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("a fresh cache must serve without fetcher/credential setup, got err: %v", err)
	}
	updated := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched); cond != nil && cond.Reason == "FetcherSetupFailed" {
		t.Fatal("fetcher setup must be SKIPPED when the cache is fresh (#200-C2), but got FetcherSetupFailed")
	}
	if len(updated.Status.PropagatedStates) == 0 {
		t.Error("the fresh cache must be propagated (SIB19 push preserved) without a fetch")
	}
}

// TestReconcile_PassWindows_StartFromNow_C3 pins #200-C3: pass windows must be computed
// from the CURRENT time, not fetchResult.FetchedAt. On a served cache whose FetchedAt is
// 23h in the past (still inside a 24h window), the buggy code would start the window 23h
// ago and emit passes already in the past; the fix starts it at now, so every window is
// in the future. Mutation: revert to fetchResult.FetchedAt and a past-AOS window appears.
func TestReconcile_PassWindows_StartFromNow_C3(t *testing.T) {
	sch := makeScheme(t)
	const ns = "default"
	gs := &ntnv1alpha1.GroundStationLifecycle{
		ObjectMeta: metav1.ObjectMeta{Name: "gs-taipei", Namespace: ns},
		Spec: ntnv1alpha1.GroundStationLifecycleSpec{
			Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "ennoconn", Model: "edge-5000"},
			Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654", Alt: "15"}},
		},
	}
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-c3", Namespace: ns},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 24 * time.Hour},
			},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{
				GroundStations: []string{"gs-taipei"},
				MinElevation:   "10",
				Horizon:        metav1.Duration{Duration: 24 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs, eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: "eph-c3", Namespace: ns}
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{OMMs: []sgp4.OMM{testISSOMM()}, SatelliteCount: 1, FetchedAt: time.Now()}},
	}
	// Served cache 23h old (inside the 24h window). The bug would start windows 23h ago.
	staleFetchedAt := time.Now().Add(-23 * time.Hour)
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result:   ephemeris.GPFetchResult{OMMs: []sgp4.OMM{testISSOMM()}, SatelliteCount: 1, FetchedAt: staleFetchedAt},
		fetchKey: fetchInputKey(got.Spec), uid: got.UID,
	})

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	now := time.Now()
	updated := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if len(updated.Status.NextPassWindows) == 0 {
		t.Fatal("expected pass windows re-predicted from the served cache")
	}
	for _, w := range updated.Status.NextPassWindows {
		if w.AOS.Time.Before(now.Add(-2 * time.Minute)) {
			t.Errorf("pass window AOS %s precedes now %s — windows were computed from the stale FetchedAt (%s), not the current time (#200-C3)",
				w.AOS.UTC(), now.UTC(), staleFetchedAt.UTC())
		}
	}
}
