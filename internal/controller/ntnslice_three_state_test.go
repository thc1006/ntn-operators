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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestReconcile_MetricsUnreliable_ResetsStreakAndTriggersReadyUnknown pins two
// three-state controller fixes at once, on the metrics-unreliable path:
//
//	H2 — the in-memory recovery/confirmation streak must be RESET (a telemetry gap is
//	     not evidence of recovery; a stale RecoveryObservedAt + one later healthy
//	     sample would otherwise satisfy the switchback delay). The min-dwell clock is
//	     wall-clock based and must be PRESERVED.
//	C5 — TriggersReady must report Unknown/MetricsUnavailable for the current
//	     generation rather than keep a stale True/False from the last reliable read.
func TestReconcile_MetricsUnreliable_ResetsStreakAndTriggersReadyUnknown(t *testing.T) {
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
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: string(slice.PathSatellite)},
	}

	cli := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).Build()

	// Active pass window: satellite is available (so fail-static holds on satellite).
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
		LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	}}
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris pass window: %v", err)
	}

	// Metrics STALE beyond the freshness bound → unreliable path.
	fr := fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -90, LatencyMs: 10, PacketLossPercent: 0},
		Stale:       true,
		LastFreshAt: fixedNow.Add(-100 * time.Second),
	}}
	r := &NTNSliceReconciler{
		Client: cli, Scheme: sch,
		Now:            func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: fr},
	}

	key := client.ObjectKeyFromObject(nsObj)
	// Seed a recovery/confirmation streak AND a dwell clock from an earlier (reliable)
	// window, to prove the unreliable reconcile clears the former but keeps the latter.
	dwell := fixedNow.Add(-30 * time.Second)
	r.storeFlapState(key, slice.AntiFlapState{
		RecoveryObservedAt:  fixedNow.Add(-200 * time.Second),
		ConsecutiveDegraded: 2,
		LastSwitchback:      dwell,
	})

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// C5: TriggersReady must be Unknown for the current generation.
	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	c := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionTriggersReady)
	if c == nil || c.Status != metav1.ConditionUnknown || c.Reason != "MetricsUnavailable" {
		t.Fatalf("TriggersReady must be Unknown/MetricsUnavailable while metrics are unreliable, got %+v", c)
	}

	// H2: recovery + confirmation cleared, dwell preserved.
	st := r.loadFlapState(key)
	if !st.RecoveryObservedAt.IsZero() {
		t.Errorf("recovery clock must reset on unreliable metrics (H2), got %v", st.RecoveryObservedAt)
	}
	if st.ConsecutiveDegraded != 0 {
		t.Errorf("confirmation streak must reset on unreliable metrics (H2), got %d", st.ConsecutiveDegraded)
	}
	if !st.LastSwitchback.Equal(dwell) {
		t.Errorf("the min-dwell clock is wall-clock based and must be PRESERVED, got %v want %v", st.LastSwitchback, dwell)
	}
}
