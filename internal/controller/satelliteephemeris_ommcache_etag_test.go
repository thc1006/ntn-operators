/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// seedRecordingFetcher is a GPFetcher that also implements conditionalCacheSeeder, recording the
// validators restoreOMMCache re-seeds and returning a canned fetch result.
type seedRecordingFetcher struct {
	result       ephemeris.GPFetchResult
	err          error
	seedCalls    int
	seededURL    string
	seededETag   string
	seededLastMd string
}

func (f *seedRecordingFetcher) Fetch(context.Context, string) (ephemeris.GPFetchResult, error) {
	return f.result, f.err
}

func (f *seedRecordingFetcher) SeedConditionalCache(url, etag, lastModified string) {
	f.seedCalls++
	f.seededURL, f.seededETag, f.seededLastMd = url, etag, lastModified
}

// TestOMMCache_PersistsAndClearsValidators: a fetch that carried ETag/Last-Modified stamps both as
// ConfigMap annotations; a later fetch of the SAME body without validators clears the stale ones,
// so a restore never re-seeds a validator that no longer matches the data.
func TestOMMCache_PersistsAndClearsValidators(t *testing.T) {
	sch := ommCacheScheme(t)
	ctx := context.Background()
	eph := newOMMCacheEph("eph-val", "uid-val")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}
	fetchKey := fetchInputKey(eph.Spec)
	cmKey := types.NamespacedName{Namespace: eph.Namespace, Name: ommCacheConfigMapName(eph.Name)}

	t0 := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	withVal := ommResultAt(t0)
	withVal.ETag = `"v1"`
	withVal.LastModified = "Fri, 31 Jul 2026 09:00:00 GMT"
	r.persistOMMCache(ctx, eph, withVal, fetchKey)

	cm := &corev1.ConfigMap{}
	if err := cli.Get(ctx, cmKey, cm); err != nil {
		t.Fatalf("get cache cm: %v", err)
	}
	if cm.Annotations[ommCacheAnnETag] != `"v1"` {
		t.Errorf("ETag annotation = %q, want %q", cm.Annotations[ommCacheAnnETag], `"v1"`)
	}
	if cm.Annotations[ommCacheAnnLastModified] != "Fri, 31 Jul 2026 09:00:00 GMT" {
		t.Errorf("Last-Modified annotation = %q", cm.Annotations[ommCacheAnnLastModified])
	}

	// Same body, but the origin stopped sending validators (or a different feed) → they must clear,
	// not linger. Change the epoch so the digest differs and the no-op-on-identical guard re-stamps.
	noVal := ommResultAt(t0.Add(time.Hour))
	r.persistOMMCache(ctx, eph, noVal, fetchKey)
	if err := cli.Get(ctx, cmKey, cm); err != nil {
		t.Fatalf("get cache cm (2): %v", err)
	}
	if _, ok := cm.Annotations[ommCacheAnnETag]; ok {
		t.Errorf("stale ETag annotation not cleared: %q", cm.Annotations[ommCacheAnnETag])
	}
	if _, ok := cm.Annotations[ommCacheAnnLastModified]; ok {
		t.Errorf("stale Last-Modified annotation not cleared: %q", cm.Annotations[ommCacheAnnLastModified])
	}
}

// TestOMMCache_RestoreSeedsFetcherValidators: on cold-start restore the persisted validators are
// re-seeded into the (CelesTrak) fetcher, so its first fetch this process is a conditional GET.
func TestOMMCache_RestoreSeedsFetcherValidators(t *testing.T) {
	sch := ommCacheScheme(t)
	ctx := context.Background()
	eph := newOMMCacheEph("eph-seed", "uid-seed")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).Build()
	fetchKey := fetchInputKey(eph.Spec)
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}

	// Persist a last-good entry carrying validators, using a throwaway reconciler.
	withVal := ommResultAt(time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC))
	withVal.ETag = `"seed-etag"`
	withVal.LastModified = "Fri, 31 Jul 2026 08:00:00 GMT"
	(&SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50)}).
		persistOMMCache(ctx, eph, withVal, fetchKey)

	// Cold reconciler with a seed-recording fetcher; restore must call SeedConditionalCache.
	seedFetcher := &seedRecordingFetcher{}
	rCold := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(50), Fetcher: seedFetcher}
	if !rCold.restoreOMMCache(ctx, ctrl.Request{NamespacedName: key}, eph, fetchKey) {
		t.Fatal("restore did not hydrate")
	}
	if seedFetcher.seedCalls != 1 {
		t.Fatalf("SeedConditionalCache called %d times, want 1", seedFetcher.seedCalls)
	}
	if seedFetcher.seededURL != eph.Spec.Source.URL {
		t.Errorf("seeded url = %q, want %q", seedFetcher.seededURL, eph.Spec.Source.URL)
	}
	if seedFetcher.seededETag != `"seed-etag"` {
		t.Errorf("seeded etag = %q, want %q", seedFetcher.seededETag, `"seed-etag"`)
	}
	if seedFetcher.seededLastMd != "Fri, 31 Jul 2026 08:00:00 GMT" {
		t.Errorf("seeded last-modified = %q", seedFetcher.seededLastMd)
	}
}

// TestReconcile_ColdStart304ReservesRestoredOMMs is the end-to-end payoff: warm fetch persists a
// validator; after a simulated restart the window-expired fetch returns 304 with an EMPTY body
// (fetcher body lost, only the validator re-seeded); the reconciler must re-serve THIS CR's
// restored OMMs — propagation continues (epoch advances) with the 304 (NotModified) status —
// instead of collapsing to zero states.
func TestReconcile_ColdStart304ReservesRestoredOMMs(t *testing.T) {
	sch := ommCacheScheme(t)
	ctx := context.Background()
	t0 := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	clock := t0
	eph := newOMMCacheEph("eph-304", "uid-304")
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Namespace: eph.Namespace, Name: eph.Name}

	// Warm process: upstream 200 carrying a validator → fetch, persist (writes the ETag annotation).
	warm := ommResultAt(t0)
	warm.ETag = `"warm-etag"`
	rWarm := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(200),
		Fetcher: &mockGPFetcher{result: warm}, Now: func() time.Time { return clock },
	}
	if _, err := rWarm.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("warm reconcile: %v", err)
	}

	// Simulated restart: fresh reconciler, empty in-memory cache. The cold fetcher returns a 304
	// with an EMPTY body (its url-keyed body cache was lost on restart).
	coldFetcher := &seedRecordingFetcher{result: ephemeris.GPFetchResult{NotModified: true}}
	rCold := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(200),
		Fetcher: coldFetcher, Now: func() time.Time { return clock },
	}

	// Advance past the 2h refresh window so a fetch is actually attempted (→ the cold-start 304).
	clock = t0.Add(3 * time.Hour)
	if _, err := rCold.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("cold reconcile: %v", err)
	}

	// Restore re-seeded the fetcher with the warm validator.
	if coldFetcher.seedCalls == 0 || coldFetcher.seededETag != `"warm-etag"` {
		t.Fatalf("cold start did not re-seed the validator: calls=%d etag=%q", coldFetcher.seedCalls, coldFetcher.seededETag)
	}

	// Continuity: propagation advanced to the current clock despite the empty 304 body.
	assertEpochAdvanced(t, ctx, cli, key, clock)

	obj := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(ctx, key, obj); err != nil {
		t.Fatalf("get eph: %v", err)
	}
	if obj.Status.SatelliteCount != 1 {
		t.Errorf("SatelliteCount = %d, want 1 (restored), not 0 (empty 304 body)", obj.Status.SatelliteCount)
	}
	cond := meta.FindStatusCondition(obj.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "NotModified" {
		t.Fatalf("expected GPDataFetched=True/NotModified, got %+v", cond)
	}
}
