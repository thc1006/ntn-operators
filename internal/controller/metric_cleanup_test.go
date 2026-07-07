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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
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
