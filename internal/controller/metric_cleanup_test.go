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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
