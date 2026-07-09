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
	"errors"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// Reconciling a deleted (NotFound) CR must release its per-CR metric series so
// /metrics does not accumulate dead series across create/delete churn. This
// exercises the shared NotFound-cleanup pattern used by the SatelliteEphemeris
// and GroundStationLifecycle reconcilers.
func TestSatelliteEphemerisReconcile_DeletedReleasesMetricSeries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	// Seed a per-CR series, as a live reconcile would (keyed by namespace+name).
	ntnmetrics.GPSatelliteCount.With(prometheus.Labels{"namespace": "ns", "ephemeris": "deleted-eph"}).Set(7)
	before := testutil.CollectAndCount(ntnmetrics.GPSatelliteCount)

	// Empty client → Get returns NotFound (the CR is "deleted").
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &SatelliteEphemerisReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "deleted-eph", Namespace: "ns"},
	}); err != nil {
		t.Fatalf("reconcile of deleted CR should not error: %v", err)
	}

	after := testutil.CollectAndCount(ntnmetrics.GPSatelliteCount)
	if after != before-1 {
		t.Errorf("deleted CR's metric series was not released: series count before=%d after=%d (want after=before-1)",
			before, after)
	}
}

// TestSatelliteEphemerisReconcile_DeletedDoesNotWipeOtherNamespace is the #180
// regression guard: SatelliteEphemeris is namespaced, so two CRs with the SAME
// name in DIFFERENT namespaces own distinct GPSatelliteCount series. Deleting
// one must release only its own {namespace,ephemeris} series and leave the
// other intact. Before the namespace label, the {ephemeris}-only
// DeletePartialMatch wiped both (self-healing only at the next ≥2h refresh).
func TestSatelliteEphemerisReconcile_DeletedDoesNotWipeOtherNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	const name = "oneweb-constellation"
	deleted := prometheus.Labels{"namespace": "team-a", "ephemeris": name}
	survivor := prometheus.Labels{"namespace": "team-b", "ephemeris": name}
	ntnmetrics.GPSatelliteCount.With(deleted).Set(651)
	ntnmetrics.GPSatelliteCount.With(survivor).Set(648)

	// Reconcile a NotFound for team-a's CR (it was deleted).
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &SatelliteEphemerisReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "team-a"},
	}); err != nil {
		t.Fatalf("reconcile of deleted CR should not error: %v", err)
	}

	// team-a's series must be released (a fresh read re-creates it at 0)...
	if got := testutil.ToFloat64(ntnmetrics.GPSatelliteCount.With(deleted)); got != 0 {
		t.Errorf("team-a series not released: got %v, want 0", got)
	}
	// ...but team-b's same-named series in another namespace must survive.
	if got := testutil.ToFloat64(ntnmetrics.GPSatelliteCount.With(survivor)); got != 648 {
		t.Errorf("deleting team-a wiped team-b's series (cross-namespace conflation): got %v, want 648", got)
	}
}

// TestGroundStationReconcile_DeletedReleasesAndIsolatesNamespace is the #183
// regression guard for the GroundStationHealth DELETE key: two same-named
// GroundStationLifecycle CRs in different namespaces own distinct series, and
// deleting one must release only its own {namespace,station,condition} series and
// leave the other namespace intact (a bare {station} delete would wipe both).
// (Write/delete key CONSISTENCY — the actual leak root cause — is guarded
// separately by TestGroundStationReconcile_WriteThenDeleteReleasesLiveSeries.)
func TestGroundStationReconcile_DeletedReleasesAndIsolatesNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	const name = "gs-taipei-01"
	deleted := prometheus.Labels{"namespace": "team-a", "station": name, "condition": ntnv1alpha1.ConditionRFLinkHealthy}
	survivor := prometheus.Labels{"namespace": "team-b", "station": name, "condition": ntnv1alpha1.ConditionRFLinkHealthy}
	ntnmetrics.GroundStationHealth.With(deleted).Set(1)
	ntnmetrics.GroundStationHealth.With(survivor).Set(1)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &GroundStationLifecycleReconciler{Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(10)}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "team-a"},
	}); err != nil {
		t.Fatalf("reconcile of deleted CR should not error: %v", err)
	}

	// team-a released (pre-#183 the composite/bare-name mismatch leaked it forever)...
	if got := testutil.ToFloat64(ntnmetrics.GroundStationHealth.With(deleted)); got != 0 {
		t.Errorf("team-a series not released (composite/bare-name leak): got %v, want 0", got)
	}
	// ...team-b's same-named series in another namespace survives.
	if got := testutil.ToFloat64(ntnmetrics.GroundStationHealth.With(survivor)); got != 1 {
		t.Errorf("deleting team-a wiped team-b's series: got %v, want 1", got)
	}
}

// TestGroundStationReconcile_WriteThenDeleteReleasesLiveSeries guards the #183
// write/delete key CONSISTENCY end-to-end (the actual leak root cause): a series
// written by a real reconcile MUST be released by a real NotFound reconcile.
// The seed-and-delete test above cannot catch a write-side regression because it
// never executes the Step-6 write path — reintroducing the old composite
// station="<ns>.<name>" write (which the bare-name delete never matched) would
// slip past it. This test drives BOTH keys through real code, so it fails if they
// ever drift apart again.
func TestGroundStationReconcile_WriteThenDeleteReleasesLiveSeries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	const ns, name = "team-live", "gs-live-release-01"
	gs := &ntnv1alpha1.GroundStationLifecycle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gs).WithStatusSubresource(gs).Build()
	r := &GroundStationLifecycleReconciler{Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(10)}

	base := testutil.CollectAndCount(ntnmetrics.GroundStationHealth)

	// Live reconcile writes one GroundStationHealth series per condition (3).
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("live reconcile: %v", err)
	}
	if afterWrite := testutil.CollectAndCount(ntnmetrics.GroundStationHealth); afterWrite != base+3 {
		t.Fatalf("live write did not emit 3 condition series: base=%d afterWrite=%d", base, afterWrite)
	}

	// Delete the CR, then a NotFound reconcile must release exactly those series.
	if err := c.Delete(context.Background(), gs); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("notfound reconcile: %v", err)
	}
	if afterDelete := testutil.CollectAndCount(ntnmetrics.GroundStationHealth); afterDelete != base {
		t.Errorf("live series leaked (write/delete key drift): base=%d afterDelete=%d", base, afterDelete)
	}
}

// TestNTNCellConfigFinalizer_DeletedDoesNotWipeOtherNamespace is the #183
// regression guard for ConfigApplyErrorsTotal (NTNCellConfig is namespaced). The
// finalizer's DeletePartialMatch must release only the deleted CR's
// {namespace,config} series and leave a same-named CR in another namespace intact.
func TestNTNCellConfigFinalizer_DeletedDoesNotWipeOtherNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	const name = "cell-config-1"
	deleted := prometheus.Labels{"namespace": "team-a", "config": name, "provider": "ocudu"}
	survivor := prometheus.Labels{"namespace": "team-b", "config": name, "provider": "ocudu"}
	// Counters accumulate; the survivor is never released by the reconcile under
	// test, so start from a clean slate to keep the absolute asserts -count-safe.
	ntnmetrics.ConfigApplyErrorsTotal.DeletePartialMatch(deleted)
	ntnmetrics.ConfigApplyErrorsTotal.DeletePartialMatch(survivor)
	ntnmetrics.ConfigApplyErrorsTotal.With(deleted).Add(3)
	ntnmetrics.ConfigApplyErrorsTotal.With(survivor).Add(2)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NTNCellConfigReconciler{Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(10)}
	// A deletion-timestamped CR with no finalizer: handleFinalizer fires the metric
	// DeletePartialMatch first, then returns without touching the ConfigMap path.
	cc := &ntnv1alpha1.NTNCellConfig{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "team-a",
		DeletionTimestamp: &metav1.Time{Time: time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)},
	}}
	if _, _, err := r.handleFinalizer(context.Background(), cc, nil); err != nil {
		t.Fatalf("finalizer cleanup should not error: %v", err)
	}

	if got := testutil.ToFloat64(ntnmetrics.ConfigApplyErrorsTotal.With(deleted)); got != 0 {
		t.Errorf("team-a series not released: got %v, want 0", got)
	}
	if got := testutil.ToFloat64(ntnmetrics.ConfigApplyErrorsTotal.With(survivor)); got != 2 {
		t.Errorf("deleting team-a wiped team-b's series: got %v, want 2", got)
	}
}

// TestNTNSliceReconcile_DeletedReleasesStaleSeriesWithNilProvider is the #183
// regression guard for the ReaderStaleUsedTotal evict-guard asymmetry: reads go
// through readerProvider() (a lazily-built default when ReaderProvider is nil),
// but the NotFound evict was previously guarded on the raw r.ReaderProvider field,
// so a stale series written via the lazy default was never released. The fix
// evicts via the same accessor, releasing the series even with a nil provider.
func TestNTNSliceReconcile_DeletedReleasesStaleSeriesWithNilProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	labels := prometheus.Labels{"namespace": "team-a", "name": "slice-1"}
	ntnmetrics.ReaderStaleUsedTotal.With(labels).Inc()
	if before := testutil.ToFloat64(ntnmetrics.ReaderStaleUsedTotal.With(labels)); before < 1 {
		t.Fatalf("seed failed: got %v", before)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	// ReaderProvider intentionally nil — the exact config where the pre-#183 evict
	// guard skipped release and leaked the stale series.
	r := &NTNSliceReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "slice-1", Namespace: "team-a"},
	}); err != nil {
		t.Fatalf("reconcile of deleted CR should not error: %v", err)
	}

	if got := testutil.ToFloat64(ntnmetrics.ReaderStaleUsedTotal.With(labels)); got != 0 {
		t.Errorf("stale series not released on delete with nil ReaderProvider: got %v, want 0", got)
	}
}

// TestNTNCellConfigReconcile_ApplyErrorWritesThenDeleteReleasesLiveSeries guards
// the ConfigApplyErrorsTotal write/delete key CONSISTENCY end-to-end. A real
// apply-error reconcile writes the {namespace,config,provider} series; a real
// finalizer-deletion reconcile must release it. The finalizer test above only
// SEEDS the series (delete-side), so a write-key regression (e.g. reintroducing
// a composite `config`) would slip past it — this drives BOTH keys through real
// code, mirroring the GroundStation live guard.
func TestNTNCellConfigReconcile_ApplyErrorWritesThenDeleteReleasesLiveSeries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	const ns, name = "team-live-cae", "cell-live-cae"
	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns},
			NTN: ntnv1alpha1.NTNParams{
				CellSpecificKoffset: 150,
				EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 20922195, PosY: 1967783, PosZ: 19770302},
				PayloadType:         "transparent",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc).WithStatusSubresource(cc).Build()
	r := &NTNCellConfigReconciler{
		Client:    c,
		Scheme:    scheme,
		Recorder:  events.NewFakeRecorder(10),
		Providers: map[string]provider.NTNProvider{"ocudu": &provider.MockProvider{ApplyErr: errors.New("connection refused")}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
	labels := prometheus.Labels{"namespace": ns, "config": name, "provider": "ocudu"}
	base := testutil.CollectAndCount(ntnmetrics.ConfigApplyErrorsTotal)

	// Reconcile 1 adds the finalizer + requeues; reconcile 2 applies → error → Inc.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 1 (finalizer): %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 2 (apply): %v", err)
	}
	if got := testutil.ToFloat64(ntnmetrics.ConfigApplyErrorsTotal.With(labels)); got < 1 {
		t.Fatalf("apply-error write did not increment the {namespace,config,provider} series: got %v", got)
	}
	if afterWrite := testutil.CollectAndCount(ntnmetrics.ConfigApplyErrorsTotal); afterWrite != base+1 {
		t.Fatalf("apply-error write did not emit exactly one series: base=%d afterWrite=%d", base, afterWrite)
	}

	// Delete → the finalizer-deletion reconcile must release the SAME series.
	if err := c.Delete(context.Background(), cc); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 3 (finalizer cleanup): %v", err)
	}
	if afterDelete := testutil.CollectAndCount(ntnmetrics.ConfigApplyErrorsTotal); afterDelete != base {
		t.Errorf("live series leaked (write/delete key drift): base=%d afterDelete=%d", base, afterDelete)
	}
}

// TestSatelliteEphemerisReconcile_FetchWritesThenDeleteReleasesLiveSeries guards
// the GPSatelliteCount write/delete key consistency end-to-end (from #180/#184):
// a real GP-fetch reconcile writes the {namespace,ephemeris} series and a real
// NotFound reconcile must release it. The cleanup tests above only SEED the
// series, so this closes the write-key regression surface for the last per-CR
// metric that lacked a live round-trip guard.
func TestSatelliteEphemerisReconcile_FetchWritesThenDeleteReleasesLiveSeries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	const ns, name = "team-live-gpsc", "eph-live-gpsc"
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type:            "CelesTrak",
				URL:             "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).WithStatusSubresource(eph).Build()
	r := &SatelliteEphemerisReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{
			SatelliteCount: 620,
			FetchedAt:      time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC),
		}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
	labels := prometheus.Labels{"namespace": ns, "ephemeris": name}
	base := testutil.CollectAndCount(ntnmetrics.GPSatelliteCount)

	// Live reconcile fetches GP data → Sets GPSatelliteCount{namespace,ephemeris}.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("fetch reconcile: %v", err)
	}
	if got := testutil.ToFloat64(ntnmetrics.GPSatelliteCount.With(labels)); got != 620 {
		t.Fatalf("fetch write did not set the {namespace,ephemeris} series to 620: got %v", got)
	}
	if afterWrite := testutil.CollectAndCount(ntnmetrics.GPSatelliteCount); afterWrite != base+1 {
		t.Fatalf("fetch write did not emit exactly one series: base=%d afterWrite=%d", base, afterWrite)
	}

	// Delete → the NotFound reconcile must release the SAME series.
	if err := c.Delete(context.Background(), eph); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("notfound reconcile: %v", err)
	}
	if afterDelete := testutil.CollectAndCount(ntnmetrics.GPSatelliteCount); afterDelete != base {
		t.Errorf("live series leaked (write/delete key drift): base=%d afterDelete=%d", base, afterDelete)
	}
}

// TestGPDeepSpaceRejectedCount_WriteDeleteRoundTrip guards the deep-space-rejected
// gauge's write/delete key consistency (deep-review D-observability): a reconcile
// over a feed with a tracked deep-space bird writes the {namespace,ephemeris}
// series to the rejected count, and the NotFound reconcile releases the SAME series.
func TestGPDeepSpaceRejectedCount_WriteDeleteRoundTrip(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	const ns, name = "team-deepspace", "eph-deepspace"
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type:            "CelesTrak",
				URL:             "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).WithStatusSubresource(eph).Build()
	r := &SatelliteEphemerisReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{
			// One LEO + one GEO (deep space); no noradIDs → the GEO is tracked & rejected.
			OMMs:           []sgp4.OMM{testISSOMM(), testGEOOMM()},
			SatelliteCount: 2,
			FetchedAt:      time.Now(),
		}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
	labels := prometheus.Labels{"namespace": ns, "ephemeris": name}
	base := testutil.CollectAndCount(ntnmetrics.GPDeepSpaceRejectedCount)

	// Live reconcile → Sets GPDeepSpaceRejectedCount{namespace,ephemeris} to 1 (one GEO).
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("fetch reconcile: %v", err)
	}
	if got := testutil.ToFloat64(ntnmetrics.GPDeepSpaceRejectedCount.With(labels)); got != 1 {
		t.Fatalf("reconcile did not set the rejected-count series to 1: got %v", got)
	}
	if afterWrite := testutil.CollectAndCount(ntnmetrics.GPDeepSpaceRejectedCount); afterWrite != base+1 {
		t.Fatalf("reconcile did not emit exactly one series: base=%d afterWrite=%d", base, afterWrite)
	}

	// Delete → the NotFound reconcile must release the SAME series.
	if err := c.Delete(context.Background(), eph); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("notfound reconcile: %v", err)
	}
	if afterDelete := testutil.CollectAndCount(ntnmetrics.GPDeepSpaceRejectedCount); afterDelete != base {
		t.Errorf("deep-space series leaked (write/delete key drift): base=%d afterDelete=%d", base, afterDelete)
	}
}

// TestEphemerisEpochStaleCount_WriteDeleteRoundTrip guards the staleness gauge's
// write/delete key consistency (I-19): a reconcile over a stale-epoch element set
// writes the {namespace,ephemeris} series, and the NotFound reconcile releases it.
func TestEphemerisEpochStaleCount_WriteDeleteRoundTrip(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	const ns, name = "team-stale", "eph-stale"
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).WithStatusSubresource(eph).Build()
	r := &SatelliteEphemerisReconciler{
		Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(10),
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{issOMMWithEpoch(30 * 24 * time.Hour)}, SatelliteCount: 1, FetchedAt: time.Now(),
		}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
	labels := prometheus.Labels{"namespace": ns, "ephemeris": name}
	base := testutil.CollectAndCount(ntnmetrics.EphemerisEpochStaleCount)

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := testutil.ToFloat64(ntnmetrics.EphemerisEpochStaleCount.With(labels)); got != 1 {
		t.Fatalf("reconcile did not set the stale-count series to 1: got %v", got)
	}
	if afterWrite := testutil.CollectAndCount(ntnmetrics.EphemerisEpochStaleCount); afterWrite != base+1 {
		t.Fatalf("reconcile did not emit exactly one series: base=%d afterWrite=%d", base, afterWrite)
	}
	if err := c.Delete(context.Background(), eph); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("notfound reconcile: %v", err)
	}
	if afterDelete := testutil.CollectAndCount(ntnmetrics.EphemerisEpochStaleCount); afterDelete != base {
		t.Errorf("stale series leaked (write/delete key drift): base=%d afterDelete=%d", base, afterDelete)
	}
}
