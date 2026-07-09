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
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// makeScheme returns a runtime.Scheme populated with just what the fake client
// needs to round-trip NTN CRs.
func makeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	utilruntime.Must(ntnv1alpha1.AddToScheme(sch))
	return sch
}

// fakeReader returns a fixed metrics Result — used to drive the reconcile down a
// controlled path (e.g. a value stale beyond the freshness bound) without a live
// Prometheus or the staleCache's own clock.
type fakeReader struct {
	res slicemetrics.Result
	err error
}

func (f fakeReader) Read(_ context.Context, _ *ntnv1alpha1.NTNSlice) (slicemetrics.Result, error) {
	return f.res, f.err
}

// fakeReaderProvider hands the same fakeReader to every NTNSlice, satisfying the
// reconciler's metricsReaderProvider seam.
type fakeReaderProvider struct{ reader slicemetrics.Reader }

func (f fakeReaderProvider) For(_ *ntnv1alpha1.NTNSlice) (slicemetrics.Reader, error) {
	return f.reader, nil
}
func (f fakeReaderProvider) Evict(_ client.ObjectKey) {}

// TestReconcile_StaleBeyondBound_FailsStatic pins the B-4 guarantee end to end:
// when the reader returns a value STALE beyond metricsMaxStaleness, the reconcile
// must NOT act on it. The (stale) metrics here would fire the "rsrp < -100"
// failover trigger, but because they are too old the slice holds on terrestrial
// (fail static) — only the orbital signal may move it, and the pass is active so
// it stays. Exercises the controller's stale-beyond routing that the I-8 test
// (metrics *unavailable*) does not reach.
func TestReconcile_StaleBeyondBound_FailsStatic(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)

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
				Triggers:        []string{"rsrp < -100"},
				SwitchbackDelay: metav1.Duration{Duration: 60 * time.Second},
			},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(nsObj, eph).
		WithStatusSubresource(nsObj, eph).
		Build()

	// Active pass window around fixedNow: satellite is available now.
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
		LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	}}
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris pass window: %v", err)
	}

	// Value that WOULD fire "rsrp < -100" (RSRP -120), but stale: age 100s > 90s.
	fr := fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -120, LatencyMs: 10, PacketLossPercent: 0},
		Stale:       true,
		LastFreshAt: fixedNow.Add(-100 * time.Second),
	}}

	r := &NTNSliceReconciler{
		Client:         cli,
		Scheme:         sch,
		Now:            func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: fr},
	}

	key := client.ObjectKeyFromObject(nsObj)
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// B-4: a stale (degraded) value must not drive a failover — the slice holds.
	if got := updated.Status.ActivePathType; got != "terrestrial" {
		t.Fatalf("stale metrics must not drive a switch: ActivePathType=%q, want terrestrial", got)
	}
	frCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
	if frCond == nil || frCond.Status != metav1.ConditionUnknown || frCond.Reason != "MetricsStale" {
		t.Fatalf("FailoverReady must be Unknown/MetricsStale, got %+v", frCond)
	}
	msCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionMetricsStale)
	if msCond == nil || msCond.Status != metav1.ConditionTrue {
		t.Fatalf("MetricsStale must be True, got %+v", msCond)
	}
}
