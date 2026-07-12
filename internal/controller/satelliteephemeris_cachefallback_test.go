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

// TestReconcile_FetchError_ServesCache pins I-18: when a GP fetch fails but a
// still-valid cache exists, the reconcile keeps serving the cached OMMs — pass
// windows are re-predicted (not wiped) and the runtime-push states re-propagated —
// so SIB19 continuity is maintained instead of blackholed. A generic transient
// error is still returned so the workqueue backs off the fetch retry.
func TestReconcile_FetchError_ServesCache(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{Name: "eph-cache", Namespace: ns},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
			PassPrediction: &ntnv1alpha1.PassPredictionSpec{
				GroundStations: []string{"gs-taipei"},
				MinElevation:   "10",
				Horizon:        metav1.Duration{Duration: 24 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(gs, eph).WithStatusSubresource(eph).Build()
	key := types.NamespacedName{Name: "eph-cache", Namespace: ns}

	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get: %v", err)
	}

	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10),
		Fetcher: &mockGPFetcher{err: errors.New("connection refused")}, // fetch will FAIL
	}
	// Seed a valid cache whose FetchedAt is OLDER than the 4h refresh window, so the
	// reconcile actually fetches (and fails) rather than reusing the cache silently.
	r.ommCache.Store(client.ObjectKeyFromObject(got), cachedFetch{
		result: ephemeris.GPFetchResult{
			OMMs: []sgp4.OMM{testISSOMM()}, SatelliteCount: 1, FetchedAt: time.Now().Add(-5 * time.Hour),
		},
		fetchKey: fetchInputKey(got.Spec),
		uid:      got.UID,
	})

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	// Generic transient error → returned so the workqueue applies exponential backoff.
	if err == nil {
		t.Fatal("a generic fetch error must be returned for workqueue backoff, got nil")
	}

	updated := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	// The fetch failed but the cache was served (degraded, not blackholed).
	cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "FetchFailedServingCache" {
		t.Fatalf("GPDataFetched must be False/FetchFailedServingCache, got %+v", cond)
	}
	// The core of I-18: SIB19 continuity is preserved.
	if len(updated.Status.PropagatedStates) == 0 {
		t.Error("PropagatedStates must be re-propagated from the served cache (SIB19 push preserved)")
	}
	if len(updated.Status.NextPassWindows) == 0 {
		t.Error("NextPassWindows must be re-predicted from the served cache, not wiped")
	}
	if updated.Status.SatelliteCount != 1 {
		t.Errorf("SatelliteCount should reflect the served cache (1), got %d", updated.Status.SatelliteCount)
	}
}
