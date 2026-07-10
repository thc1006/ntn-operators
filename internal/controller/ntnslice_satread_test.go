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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestReconcile_TransientEphemerisReadError_HoldsSatellite pins I-13: when the
// slice is on satellite with FRESH healthy metrics but the SatelliteEphemeris read
// fails transiently (a non-NotFound error, e.g. an informer-cache blip), the
// reconcile must HOLD the current (satellite) path rather than force a switchback.
// Before I-13, checkSatelliteAvailability returned false on any read error, which
// drove the engine to switch traffic off satellite on an unread pass window.
func TestReconcile_TransientEphemerisReadError_HoldsSatellite(t *testing.T) {
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
		// Currently ON satellite — the state that a wrong switchback would disrupt.
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: "satellite"},
	}

	// Intercept Get for the SatelliteEphemeris ONLY and return a transient
	// (non-NotFound) error; the NTNSlice Get and everything else pass through.
	cli := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(nsObj, eph).
		WithStatusSubresource(nsObj, eph).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*ntnv1alpha1.SatelliteEphemeris); ok {
					return apierrors.NewInternalError(errors.New("simulated transient apiserver read error"))
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	// FRESH, healthy metrics → qualityReliable is true, so a switch (if any) would
	// come purely from the satellite-availability path, isolating the I-13 fix.
	fr := fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0},
		Stale:       false,
		LastFreshAt: fixedNow,
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

	// I-13: the transient read must HOLD satellite, not switch back.
	if got := updated.Status.ActivePathType; got != "satellite" {
		t.Fatalf("transient ephemeris read error must hold current path: ActivePathType=%q, want satellite", got)
	}
	// No failover should have been counted.
	if updated.Status.FailoverCount != 0 {
		t.Errorf("a hold must not count a failover, got FailoverCount=%d", updated.Status.FailoverCount)
	}
	// FailoverReady must be Unknown with the read-failed reason.
	frCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
	if frCond == nil || frCond.Status != metav1.ConditionUnknown || frCond.Reason != "SatelliteReadFailed" {
		t.Fatalf("FailoverReady must be Unknown/SatelliteReadFailed, got %+v", frCond)
	}
}

// TestReconcile_InertTrigger_SurfacesTriggersReadyFalse pins I-10 end to end:
// a trigger over a metric with no configured source (LatencyMissing) is inert;
// the reconcile must surface TriggersReady=False/InertTriggers rather than let the
// operator trust an armed-but-dead policy. Metrics are FRESH so the engine runs.
func TestReconcile_InertTrigger_SurfacesTriggersReadyFalse(t *testing.T) {
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
				// latency has no source below (LatencyMissing) → this trigger is inert.
				Triggers:        []string{"latency > 200"},
				SwitchbackDelay: metav1.Duration{Duration: 60 * time.Second},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(nsObj, eph).
		WithStatusSubresource(nsObj, eph).Build()

	// FRESH metrics, but latency is MISSING (no configured source).
	fr := fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -80, LatencyMs: 20, PacketLossPercent: 0.1, LatencyMissing: true},
		Stale:       false,
		LastFreshAt: fixedNow,
	}}
	r := &NTNSliceReconciler{Client: cli, Scheme: sch, Now: func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: fr}}

	key := client.ObjectKeyFromObject(nsObj)
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionTriggersReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InertTriggers" {
		t.Fatalf("TriggersReady must be False/InertTriggers, got %+v", cond)
	}
}
