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
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// stalingReader returns a fresh Result on the first Read and a stale-but-usable
// Result on every later Read, driving a single slice through a fresh->stale
// transition across two reconciles.
type stalingReader struct {
	calls int
	fresh slicemetrics.Result
	stale slicemetrics.Result
}

func (r *stalingReader) Read(_ context.Context, _ *ntnv1alpha1.NTNSlice) (slicemetrics.Result, error) {
	r.calls++
	if r.calls == 1 {
		return r.fresh, nil
	}
	return r.stale, nil
}

// TestReconcile_MetricsStaleEvent_FiresOnFreshToStaleTransition guards the
// MetricsStale Warning against a pointer-aliasing bug: meta.FindStatusCondition
// returns a pointer INTO the conditions slice, which meta.SetStatusCondition then
// mutates in place, so a gate that reads that pointer AFTER the write always sees
// True and misses every False->True transition (it would fire only on the very
// first, absent->True, outage). The controller snapshots the prior state as a
// value instead. Here the first (fresh) read persists MetricsStale=False, so the
// second (stale-but-usable) read is a genuine False->True transition that MUST
// emit exactly one MetricsStale Warning.
func TestReconcile_MetricsStaleEvent_FiresOnFreshToStaleTransition(t *testing.T) {
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
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{Triggers: []string{"rsrp < -100"}},
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(nsObj, eph).
		WithStatusSubresource(nsObj, eph).
		Build()

	// Active pass window so satellite availability is known (keeps the reconcile on
	// the normal quality path rather than an ephemeris-unknown hold).
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
		LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
	}}
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris pass window: %v", err)
	}

	rec := events.NewFakeRecorder(20)
	rdr := &stalingReader{
		// Healthy RSRP (-80 does not trip "rsrp < -100"), so metrics staleness — not a
		// failover — is what this test exercises.
		fresh: slicemetrics.Result{Metrics: slice.Metrics{RSRP: -80}, Stale: false, LastFreshAt: fixedNow},
		// Stale but only 10s old — WITHIN metricsMaxStaleness — so it takes the
		// MetricsStale=True (usable) branch, not the fail-static branch.
		stale: slicemetrics.Result{Metrics: slice.Metrics{RSRP: -80}, Stale: true, LastFreshAt: fixedNow.Add(-10 * time.Second)},
	}
	r := &NTNSliceReconciler{
		Client:         cli,
		Scheme:         sch,
		Recorder:       rec,
		Now:            func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: rdr},
	}

	key := client.ObjectKeyFromObject(nsObj)
	// 1: fresh -> MetricsStale=False (no event). 2: fresh->stale transition -> one
	// event. 3: stale->stale steady state -> no NEW event (episode gate).
	for i, phase := range []string{"fresh", "fresh->stale", "steady-stale"} {
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile %d (%s): %v", i+1, phase, err)
		}
	}

	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if c := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionMetricsStale); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("MetricsStale must be True after the stale read, got %+v", c)
	}

	staleEvents := 0
	for drained := false; !drained; {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, "MetricsStale") {
				staleEvents++
			}
		default:
			drained = true
		}
	}
	if staleEvents != 1 {
		t.Fatalf("MetricsStale must fire EXACTLY once across fresh->stale->stale "+
			"(one transition, then the steady stale state emits nothing), got %d", staleEvents)
	}
}
