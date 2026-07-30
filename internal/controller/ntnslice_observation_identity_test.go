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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestReconcile_StaleReplayObservation_DoesNotConfirmFailover is the controller-level proof of the
// observation-identity wiring: a metrics source STUCK re-serving ONE degraded observation (Stale,
// with a fixed LastFreshAt inside the 90s staleness bound — the source-outage-during-degradation
// case) must not let successive reconciles climb the N-consecutive counter. Before the fix each
// reconcile advanced it, so the third reconcile spuriously confirmed a failover on a SINGLE
// reading; now readPathQuality carries the observation stamp and the streak folds it in once.
func TestReconcile_StaleReplayObservation_DoesNotConfirmFailover(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	n3 := 3

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: "default"},
	}
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "chunghwa-telecom", APN: "internet", Priority: "primary"},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "oneweb-constellation",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:            []string{"rsrp < -100"},
				SwitchbackDelay:     metav1.Duration{Duration: 60 * time.Second},
				ConfirmationSamples: &n3,
			},
		},
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: string(slice.PathTerrestrial)},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).Build()

	// A failover TARGET exists: an active pass window, and PassesPredicted=True so the consumer
	// trusts it (checkSatelliteAvailability returns known+available only then).
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
		LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	}}
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue,
		Reason: "Predicted", Message: "windows current",
	})
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris pass window: %v", err)
	}

	// ONE degraded observation, re-served stale on every read (fixed LastFreshAt, age 30s < 90s).
	fr := fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -125, LatencyMs: 10, PacketLossPercent: 0}, // rsrp < -100 fires
		Stale:       true,
		LastFreshAt: fixedNow.Add(-30 * time.Second),
	}}
	r := &NTNSliceReconciler{
		Client: cli, Scheme: sch,
		Now:            func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: fr},
	}
	key := client.ObjectKeyFromObject(nsObj)

	for i := 1; i <= 3; i++ {
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	// The counter reflects the ONE observation, and the slice never confirmed a failover.
	st := r.loadFlapState(key, nsObj.UID)
	if st.ConsecutiveDegraded != 1 {
		t.Fatalf("one re-served observation across 3 reconciles must count once, got %d", st.ConsecutiveDegraded)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if updated.Status.ActivePathType == string(slice.PathSatellite) {
		t.Fatal("a single degraded observation re-served stale must NOT confirm N=3 and fail over to satellite")
	}
}
