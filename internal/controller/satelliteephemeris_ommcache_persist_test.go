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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

// ommCacheScheme is makeScheme + corev1 (the cache lives in a ConfigMap).
func ommCacheScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := makeScheme(t)
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	return sch
}

func newOMMCacheEph(name string, uid types.UID) *ntnv1alpha1.SatelliteEphemeris {
	return &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: uid, Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 2 * time.Hour},
			},
		},
	}
}

func ommResultAt(fetchedAt time.Time) ephemeris.GPFetchResult {
	omm := issOMMForTest()
	omm.EpochStr = fetchedAt.Format("2006-01-02T15:04:05.000000")
	return ephemeris.GPFetchResult{OMMs: []sgp4.OMM{omm}, SatelliteCount: 1, FetchedAt: fetchedAt}
}

// TestOMMCache_PersistRestoreRoundTrip proves a persisted ConfigMap hydrates the in-memory cache
// with byte-identical OMMs and the original fetch metadata — the whole point of the durable cache.
func TestOMMCache_PersistRestoreRoundTrip(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-rt", "uid-rt")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}

	fetchedAt := time.Date(2026, 7, 30, 12, 0, 0, 123456000, time.UTC)
	result := ommResultAt(fetchedAt)
	fetchKey := fetchInputKey(eph.Spec)
	r.persistOMMCache(ctx, eph, result, fetchKey)

	// The ConfigMap must exist and carry the identity/integrity metadata.
	cm := &corev1.ConfigMap{}
	if err := cli.Get(ctx, types.NamespacedName{Namespace: eph.Namespace, Name: ommCacheNameFor(eph)}, cm); err != nil {
		t.Fatalf("persist did not create the cache ConfigMap: %v", err)
	}
	if cm.Labels[ommCacheLabelKey] != ommCacheLabelValue {
		t.Fatalf("cache ConfigMap missing marker label")
	}
	if cm.Annotations[ommCacheAnnFetchKey] != fetchKey {
		t.Fatalf("fetchKey annotation = %q, want %q", cm.Annotations[ommCacheAnnFetchKey], fetchKey)
	}
	if cm.Annotations[ommCacheAnnUID] != string(eph.UID) {
		t.Fatalf("uid annotation = %q, want %q", cm.Annotations[ommCacheAnnUID], eph.UID)
	}
	if cm.Annotations[ommCacheAnnDigest] != ommDigest([]byte(cm.Data[ommCacheDataKey])) {
		t.Fatalf("digest annotation does not match stored payload")
	}

	// Restore into a COLD cache must hydrate it, returning true.
	if !r.restoreOMMCache(ctx, ctrl.Request{NamespacedName: key}, eph, fetchKey) {
		t.Fatalf("restore returned false on a valid cold cache")
	}
	got, ok := r.cachedOMMResult(key)
	if !ok {
		t.Fatalf("restore did not populate the in-memory cache")
	}
	if got.fetchKey != fetchKey || got.uid != eph.UID {
		t.Fatalf("restored identity = (%q,%q), want (%q,%q)", got.fetchKey, got.uid, fetchKey, eph.UID)
	}
	if got.result.SatelliteCount != 1 || len(got.result.OMMs) != 1 {
		t.Fatalf("restored OMM count = %d, want 1", len(got.result.OMMs))
	}
	if got.result.OMMs[0].NoradCatID != result.OMMs[0].NoradCatID || got.result.OMMs[0].EpochStr != result.OMMs[0].EpochStr {
		t.Fatalf("restored OMM does not round-trip: got norad=%d epoch=%q",
			got.result.OMMs[0].NoradCatID, got.result.OMMs[0].EpochStr)
	}
	// FetchedAt must survive so the normal window/freshness gates still apply post-restore.
	if !got.result.FetchedAt.Equal(fetchedAt) {
		t.Fatalf("restored FetchedAt = %s, want %s (restore must not reset the fetch time)", got.result.FetchedAt.UTC(), fetchedAt)
	}
	// nextFetchAttempt/lastFetchErr must be zero so the next reconcile is free to fetch.
	if !got.nextFetchAttempt.IsZero() || got.lastFetchErr != nil || got.fetchFailures != 0 {
		t.Fatalf("restore must not carry backoff state: nextFetch=%s err=%v failures=%d",
			got.nextFetchAttempt, got.lastFetchErr, got.fetchFailures)
	}
}

// TestOMMCache_MetricsRecorded proves persist and restore increment the observability counters, so
// a silently-degraded failover cache is alertable (ADR-0007).
func TestOMMCache_MetricsRecorded(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-metric", "uid-metric")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}
	fetchKey := fetchInputKey(eph.Spec)

	// Reset any series left by another test for this key so the assertions are absolute.
	ntnmetrics.OMMCachePersistTotal.DeletePartialMatch(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name})
	ntnmetrics.OMMCacheRestoreTotal.DeletePartialMatch(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name})

	r.persistOMMCache(ctx, eph, ommResultAt(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)), fetchKey)
	if got := testutil.ToFloat64(ntnmetrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "success")); got != 1 {
		t.Fatalf("persist success counter = %v, want 1", got)
	}
	if !r.restoreOMMCache(ctx, ctrl.Request{NamespacedName: key}, eph, fetchKey) {
		t.Fatalf("restore returned false on a valid cold cache")
	}
	if got := testutil.ToFloat64(ntnmetrics.OMMCacheRestoreTotal.WithLabelValues(eph.Namespace, eph.Name, "hydrated")); got != 1 {
		t.Fatalf("restore hydrated counter = %v, want 1", got)
	}
}

// TestOMMCache_RestoreMigratesLegacyName proves a cache written under the pre-hash (truncated) name
// is adopted across the upgrade, so a restart during migration does not cold-start (ADR-0007).
func TestOMMCache_RestoreMigratesLegacyName(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-legacy", "uid-legacy")
	fetchedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	result := ommResultAt(fetchedAt)
	fetchKey := fetchInputKey(eph.Spec)

	// A valid cache ConfigMap under the OLD name, stamped exactly as the pre-hash code did.
	data, err := json.Marshal(ommCachePayload(result, eph))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: eph.Namespace, Name: ommCacheLegacyName(eph.Name)}}
	stampOMMCache(legacy, eph, fetchKey, ommDigest(data), 1, string(data), fetchedAt, "", "")
	if legacy.Name == ommCacheNameFor(eph) {
		t.Fatalf("test invalid: legacy and hashed names coincide for %q", eph.Name)
	}

	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, legacy).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}

	// The hashed name does not exist; restore must fall back to the legacy name and hydrate.
	if !r.restoreOMMCache(ctx, ctrl.Request{NamespacedName: key}, eph, fetchKey) {
		t.Fatalf("restore did not adopt the legacy-named cache")
	}
	got, ok := r.cachedOMMResult(key)
	if !ok || got.result.SatelliteCount != 1 {
		t.Fatalf("legacy adoption did not populate the in-memory cache")
	}
	if !got.result.FetchedAt.Equal(fetchedAt) {
		t.Fatalf("legacy adoption reset FetchedAt: got %s, want %s", got.result.FetchedAt.UTC(), fetchedAt)
	}
}

// TestOMMCache_RestoreRejectsInvalid proves restore refuses corrupt or wrong-owner/source data,
// so a tampered or orphaned ConfigMap never poisons the cache.
func TestOMMCache_RestoreRejectsInvalid(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	base := newOMMCacheEph("eph-rej", "uid-rej")
	fetchKey := fetchInputKey(base.Spec)

	// mutate applies a tampering to the persisted ConfigMap; restoreArgs overrides the eph/fetchKey.
	cases := []struct {
		name     string
		mutate   func(cm *corev1.ConfigMap)
		eph      *ntnv1alpha1.SatelliteEphemeris
		fetchKey string
	}{
		{name: "digest-corrupt", mutate: func(cm *corev1.ConfigMap) {
			cm.Data[ommCacheDataKey] += " " // payload no longer matches the digest annotation
		}, eph: base, fetchKey: fetchKey},
		{name: "missing-label", mutate: func(cm *corev1.ConfigMap) {
			delete(cm.Labels, ommCacheLabelKey)
		}, eph: base, fetchKey: fetchKey},
		{name: "empty-data", mutate: func(cm *corev1.ConfigMap) {
			cm.Data[ommCacheDataKey] = ""
		}, eph: base, fetchKey: fetchKey},
		{name: "uid-mismatch", mutate: func(cm *corev1.ConfigMap) {}, eph: newOMMCacheEph("eph-rej", "uid-OTHER"), fetchKey: fetchKey},
		{name: "fetchkey-mismatch", mutate: func(cm *corev1.ConfigMap) {}, eph: base, fetchKey: "some/other/source"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := ommCacheScheme(t)
			cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(base).Build()
			r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
			ctx := context.Background()
			key := types.NamespacedName{Namespace: base.Namespace, Name: base.Name}

			r.persistOMMCache(ctx, base, ommResultAt(fetchedAt), fetchKey)
			cm := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Namespace: base.Namespace, Name: ommCacheNameFor(base)}
			if err := cli.Get(ctx, cmKey, cm); err != nil {
				t.Fatalf("get cache cm: %v", err)
			}
			tc.mutate(cm)
			if err := cli.Update(ctx, cm); err != nil {
				t.Fatalf("update cache cm: %v", err)
			}

			if r.restoreOMMCache(ctx, ctrl.Request{NamespacedName: key}, tc.eph, tc.fetchKey) {
				t.Fatalf("restore accepted an invalid cache (%s)", tc.name)
			}
			if _, ok := r.cachedOMMResult(key); ok {
				t.Fatalf("restore populated the cache from invalid data (%s)", tc.name)
			}
		})
	}
}

// TestOMMCache_RestoreNoopWhenWarm proves a warm in-memory cache is never overwritten by a stale
// persisted copy (restore is a cold-start bootstrap only).
func TestOMMCache_RestoreNoopWhenWarm(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-warm", "uid-warm")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}
	fetchKey := fetchInputKey(eph.Spec)

	// Persist an OLD copy, then warm the cache with a NEWER in-memory entry.
	r.persistOMMCache(ctx, eph, ommResultAt(time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)), fetchKey)
	warm := cachedFetch{result: ommResultAt(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)), fetchKey: fetchKey, uid: eph.UID}
	r.ommCache.Store(key, warm)

	if r.restoreOMMCache(ctx, ctrl.Request{NamespacedName: key}, eph, fetchKey) {
		t.Fatalf("restore ran against a warm cache")
	}
	got, _ := r.cachedOMMResult(key)
	if !got.result.FetchedAt.Equal(warm.result.FetchedAt) {
		t.Fatalf("restore clobbered the warm cache: FetchedAt=%s want %s", got.result.FetchedAt.UTC(), warm.result.FetchedAt.UTC())
	}
}

// TestOMMCache_PersistNoopOnIdenticalDigest proves an unchanged payload does not rewrite the
// ConfigMap — the ~2h fetch cadence must not churn resourceVersion (#204-G3).
func TestOMMCache_PersistNoopOnIdenticalDigest(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-noop", "uid-noop")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()
	cmKey := types.NamespacedName{Namespace: eph.Namespace, Name: ommCacheNameFor(eph)}
	fetchKey := fetchInputKey(eph.Spec)

	fetchedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	r.persistOMMCache(ctx, eph, ommResultAt(fetchedAt), fetchKey)
	rv1 := getResourceVersion(t, cli, cmKey)

	r.persistOMMCache(ctx, eph, ommResultAt(fetchedAt), fetchKey) // identical payload
	if rv2 := getResourceVersion(t, cli, cmKey); rv2 != rv1 {
		t.Fatalf("identical persist rewrote the ConfigMap: rv %s -> %s", rv1, rv2)
	}

	// A changed payload MUST rewrite it.
	changed := ommResultAt(fetchedAt)
	changed.OMMs[0].NoradCatID = 99999
	r.persistOMMCache(ctx, eph, changed, fetchKey)
	if rv3 := getResourceVersion(t, cli, cmKey); rv3 == rv1 {
		t.Fatalf("changed persist did not rewrite the ConfigMap (rv still %s)", rv1)
	}
}

// TestOMMCache_PersistSetsOwnerRefForGC proves the ConfigMap is owner-ref'd to its CR so k8s
// garbage-collects it on CR deletion (the controller has no delete verb).
func TestOMMCache_PersistSetsOwnerRefForGC(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-gc", "uid-gc")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()

	r.persistOMMCache(ctx, eph, ommResultAt(time.Now().UTC()), fetchInputKey(eph.Spec))
	cm := &corev1.ConfigMap{}
	if err := cli.Get(ctx, types.NamespacedName{Namespace: eph.Namespace, Name: ommCacheNameFor(eph)}, cm); err != nil {
		t.Fatalf("get cm: %v", err)
	}
	if len(cm.OwnerReferences) != 1 {
		t.Fatalf("want exactly 1 owner ref, got %d", len(cm.OwnerReferences))
	}
	ref := cm.OwnerReferences[0]
	if ref.UID != eph.UID || ref.Name != eph.Name || ref.Kind != "SatelliteEphemeris" {
		t.Fatalf("owner ref = %+v, want controller SatelliteEphemeris/%s/%s", ref, eph.Name, eph.UID)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Fatalf("owner ref must be a controller ref for GC")
	}
}

// TestOMMCache_PersistSkipsOversizePayload proves an over-bound payload is skipped (no ConfigMap,
// no panic) so a pathological set never breaches the 1 MiB ConfigMap limit.
func TestOMMCache_PersistSkipsOversizePayload(t *testing.T) {
	sch := ommCacheScheme(t)
	eph := newOMMCacheEph("eph-big", "uid-big")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	ctx := context.Background()

	result := ommResultAt(time.Now().UTC())
	result.OMMs[0].ObjectName = strings.Repeat("A", maxOMMCacheBytes+1) // force marshal past the bound
	r.persistOMMCache(ctx, eph, result, fetchInputKey(eph.Spec))

	cm := &corev1.ConfigMap{}
	err := cli.Get(ctx, types.NamespacedName{Namespace: eph.Namespace, Name: ommCacheNameFor(eph)}, cm)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("oversize payload must not create a ConfigMap; got err=%v", err)
	}
}

// TestOMMCache_ConfigMapNameCollisionResistant proves the derived name is unique per object, a
// valid ConfigMap name, and deterministic. The old truncation mapped two long names to one
// ConfigMap, silently breaking the second CR's persistence (ADR-0007).
func TestOMMCache_ConfigMapNameCollisionResistant(t *testing.T) {
	// Two >243-char names sharing their first 250 chars collided under the old truncation.
	a, b := strings.Repeat("x", 250)+"-alpha", strings.Repeat("x", 250)+"-beta"
	na := ommCacheConfigMapName("ns", a, "uid-a")
	nb := ommCacheConfigMapName("ns", b, "uid-b")
	if na == nb {
		t.Fatalf("collision: two distinct long names mapped to one ConfigMap %q", na)
	}
	for _, n := range []string{na, nb, ommCacheConfigMapName("ns", "eph-a", "uid")} {
		if len(n) > maxConfigMapNameLen {
			t.Fatalf("name len = %d exceeds %d: %q", len(n), maxConfigMapNameLen, n)
		}
		if errs := validation.IsDNS1123Subdomain(n); len(errs) != 0 {
			t.Fatalf("name %q is not a valid ConfigMap name: %v", n, errs)
		}
	}
	// Deterministic: persist and restore must derive the same name.
	if x, y := ommCacheConfigMapName("ns", a, "uid-a"), ommCacheConfigMapName("ns", a, "uid-a"); x != y {
		t.Fatalf("non-deterministic: %q != %q", x, y)
	}
	// Same namespace+name, different UID (delete-recreate) → different name, so a recreated CR gets
	// a fresh cache object instead of fighting over the old owner's ConfigMap.
	if ommCacheConfigMapName("ns", a, "uid-a") == ommCacheConfigMapName("ns", a, "uid-b") {
		t.Fatalf("same name+ns but different UID must differ")
	}
	// Short name keeps a readable prefix.
	if short := ommCacheConfigMapName("ns", "eph-a", "uid"); !strings.HasPrefix(short, "eph-a-") {
		t.Fatalf("short name lost its readable prefix: %q", short)
	}
}

// TestOMMCache_PayloadFilteredAndCapped proves the persisted set honors the NORAD filter and the
// maxPropagatedStates cap (never the full upstream response).
func TestOMMCache_PayloadFilteredAndCapped(t *testing.T) {
	eph := newOMMCacheEph("eph-pl", "uid-pl")
	eph.Spec.Satellites = &ntnv1alpha1.SatelliteSelector{NoradIDs: []int{25544}}

	omms := make([]sgp4.OMM, 0, 3)
	for i := range 3 {
		o := issOMMForTest()
		o.NoradCatID = 25544 + i // only 25544 is selected
		omms = append(omms, o)
	}
	got := ommCachePayload(ephemeris.GPFetchResult{OMMs: omms}, eph)
	if len(got) != 1 || got[0].NoradCatID != 25544 {
		t.Fatalf("payload not filtered to NORAD 25544: %+v", got)
	}

	// Cap: an unfiltered oversize set is bounded to maxPropagatedStates.
	ephAll := newOMMCacheEph("eph-cap", "uid-cap")
	over := make([]sgp4.OMM, maxPropagatedStates+5)
	for i := range over {
		over[i] = issOMMForTest()
		over[i].NoradCatID = 40000 + i
	}
	if capped := ommCachePayload(ephemeris.GPFetchResult{OMMs: over}, ephAll); len(capped) != maxPropagatedStates {
		t.Fatalf("payload cap = %d, want %d", len(capped), maxPropagatedStates)
	}
}

// TestReconcile_ColdStartRestoresAndPropagatesDuringOutage is the unit-level proof of the mandated
// acceptance scenario: a warm fetch persists the cache; a fresh process (empty in-memory cache)
// facing a total upstream outage restores from the ConfigMap and keeps advancing the epoch — both
// while the refresh window is fresh (direct serve) AND after it expires (fetch fails → fallback).
func TestReconcile_ColdStartRestoresAndPropagatesDuringOutage(t *testing.T) {
	sch := ommCacheScheme(t)
	t0 := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	clock := t0
	eph := newOMMCacheEph("eph-cold", "uid-cold")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}
	ctx := context.Background()

	// ---- Phase A: warm process, upstream UP → fetch, persist, propagate.
	warmFetcher := &mockGPFetcher{result: ommResultAt(t0)}
	rWarm := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(200), Fetcher: warmFetcher,
		Now: func() time.Time { return clock },
	}
	if _, err := rWarm.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("warm reconcile: %v", err)
	}
	if warmFetcher.callCount() == 0 {
		t.Fatalf("warm phase never contacted upstream")
	}
	cm := &corev1.ConfigMap{}
	if err := cli.Get(ctx, types.NamespacedName{Namespace: eph.Namespace, Name: ommCacheNameFor(eph)}, cm); err != nil {
		t.Fatalf("warm phase did not persist the cache ConfigMap: %v", err)
	}

	// ---- Phase B: SIMULATED RESTART — fresh reconciler (empty ommCache), SAME client, upstream DOWN.
	downFetcher := &mockGPFetcher{err: context.DeadlineExceeded}
	rCold := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(200), Fetcher: downFetcher,
		Now: func() time.Time { return clock },
	}

	// B1: within the refresh window (t0+10m) — restore hydrates, cacheServe serves directly.
	clock = t0.Add(10 * time.Minute)
	if _, err := rCold.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("cold reconcile (in-window): %v", err)
	}
	assertEpochAdvanced(t, ctx, cli, key, clock)
	if _, ok := rCold.cachedOMMResult(key); !ok {
		t.Fatalf("cold restore did not hydrate the in-memory cache")
	}

	// B2: PAST the 2h window (t0+3h) — cacheServe expires, the fetch is attempted and FAILS, and the
	// obtainOMMs fallback (fetchKey+uid match, set by restore) keeps re-propagating.
	clock = t0.Add(3 * time.Hour)
	if _, err := rCold.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("cold reconcile (window expired): %v", err)
	}
	if downFetcher.callCount() == 0 {
		t.Fatalf("window expired but no upstream fetch was attempted")
	}
	assertEpochAdvanced(t, ctx, cli, key, clock)
}

// assertEpochAdvanced checks propagatedStates exist and are stamped at the CURRENT reconcile clock
// + lead (proving a fresh re-propagation, not a frozen carry-over).
func assertEpochAdvanced(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, at time.Time) {
	t.Helper()
	obj := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, obj); err != nil {
		t.Fatalf("get eph: %v", err)
	}
	if len(obj.Status.PropagatedStates) == 0 {
		t.Fatalf("no propagated states — continuity lost at %s", at.UTC())
	}
	want := at.Add(propagationEpochLead).UnixMilli()
	if got := obj.Status.PropagatedStates[0].EpochUnixMs; got != want {
		t.Fatalf("epoch = %d, want current-clock+lead = %d (frozen cache would keep an older epoch)", got, want)
	}
}

func getResourceVersion(t *testing.T, cli client.Client, key types.NamespacedName) string {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), key, cm); err != nil {
		t.Fatalf("get cm %s: %v", key, err)
	}
	return cm.ResourceVersion
}
