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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestCheckSatelliteAvailability_SourceEpochFreshnessGate pins the fix for the control-plane split:
// NTNSlice must NOT report a satellite available off a pass window whose backing element set is too
// stale to DELIVER — otherwise it fails over to a satellite whose ephemeris NTNCellConfig then refuses
// to push (both use maxEpochAge, but only the consumer used to enforce it). The gate correlates the
// active window to its propagatedState by NORAD.
func TestCheckSatelliteAvailability_SourceEpochFreshnessGate(t *testing.T) {
	const ns = "default"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	freshMs := now.Add(-time.Hour).UnixMilli()               // well within maxEpochAge
	staleMs := now.Add(-maxEpochAge - time.Hour).UnixMilli() // just past maxEpochAge

	window := func(norad int) ntnv1alpha1.PassWindow {
		return ntnv1alpha1.PassWindow{
			Satellite: "SAT", NoradID: norad, GroundStation: "gs",
			AOS: metav1.Time{Time: now.Add(-5 * time.Minute)}, LOS: metav1.Time{Time: now.Add(5 * time.Minute)},
		}
	}
	state := func(norad int, srcMs int64) ntnv1alpha1.PropagatedState {
		return ntnv1alpha1.PropagatedState{NoradID: norad, EpochUnixMs: now.Add(5 * time.Minute).UnixMilli(), SourceEpochUnixMs: srcMs}
	}
	predictedTrue := []metav1.Condition{{Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "x"}}

	cases := []struct {
		name          string
		windows       []ntnv1alpha1.PassWindow
		states        []ntnv1alpha1.PropagatedState
		wantAvailable bool
		wantKnown     bool
		detailHas     string // substring required in the returned detail (empty = no check)
	}{
		{"active window backed by a FRESH element set → available",
			[]ntnv1alpha1.PassWindow{window(25544)}, []ntnv1alpha1.PropagatedState{state(25544, freshMs)}, true, true, ""},
		{"active window backed by a STALE element set → unavailable (genuine, not hold)",
			[]ntnv1alpha1.PassWindow{window(25544)}, []ntnv1alpha1.PropagatedState{state(25544, staleMs)}, false, true, "stale"},
		{"active window whose NORAD has no propagatedState → available (no-state out of scope; only present-but-stale demotes)",
			[]ntnv1alpha1.PassWindow{window(25544)}, []ntnv1alpha1.PropagatedState{state(40000, freshMs)}, true, true, ""},
		{"legacy window (noradID=0) is never demoted, even against a stale state → available",
			[]ntnv1alpha1.PassWindow{window(0)}, []ntnv1alpha1.PropagatedState{state(25544, staleMs)}, true, true, ""},
		{"two active windows, one fresh one stale → fresh wins (a deliverable satellite is overhead)",
			[]ntnv1alpha1.PassWindow{window(40000), window(25544)},
			[]ntnv1alpha1.PropagatedState{state(40000, staleMs), state(25544, freshMs)}, true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := makeScheme(t)
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "eph", Namespace: ns},
				Status:     ntnv1alpha1.SatelliteEphemerisStatus{NextPassWindows: tc.windows, PropagatedStates: tc.states, Conditions: predictedTrue},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: ns},
				Spec:       ntnv1alpha1.NTNSliceSpec{SatellitePath: ntnv1alpha1.SatellitePathSpec{EphemerisRef: "eph"}},
			}
			cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, slice).Build()
			r := &NTNSliceReconciler{Client: cli, Scheme: sch}

			available, known, detail := r.checkSatelliteAvailability(context.Background(), slice, now)
			if available != tc.wantAvailable || known != tc.wantKnown {
				t.Fatalf("got (available=%v, known=%v), want (available=%v, known=%v); detail=%q",
					available, known, tc.wantAvailable, tc.wantKnown, detail)
			}
			if tc.detailHas != "" && !strings.Contains(detail, tc.detailHas) {
				t.Fatalf("detail %q does not contain %q", detail, tc.detailHas)
			}
		})
	}
}

// TestReconcile_NoFailoverToStaleSatellite is the MANDATED acceptance test for the control-plane
// split: warm cache → sustained upstream outage → the element-set age crosses maxEpochAge → an active
// pass window is still present (pass prediction keeps succeeding off the stale, drifting OMM, so
// PassesPredicted stays True) → a terrestrial failover trigger fires → the slice must NOT fail over to
// the satellite, because its stale ephemeris is not deliverable (NTNCellConfig would refuse the push).
// Before the fix, NTNSlice reported the satellite available off the active window and steered traffic
// onto a path the gNB cannot be configured for.
func TestReconcile_NoFailoverToStaleSatellite(t *testing.T) {
	fixedNow := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
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
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: "terrestrial"},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).Build()

	// Active pass window around now, but its backing element set is STALE beyond maxEpochAge — pass
	// prediction still "succeeds" off the drifting OMM (PassesPredicted=True) yet the state is not
	// deliverable (same sourceEpochFresh bound NTNCellConfig enforces at the push).
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", NoradID: 44057, GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-time.Hour)}, LOS: metav1.Time{Time: fixedNow.Add(time.Hour)},
	}}
	eph.Status.PropagatedStates = []ntnv1alpha1.PropagatedState{{
		NoradID: 44057, EpochUnixMs: fixedNow.Add(5 * time.Minute).UnixMilli(),
		SourceEpochUnixMs: fixedNow.Add(-maxEpochAge - time.Hour).UnixMilli(),
	}}
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "Predicted"})
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris: %v", err)
	}

	// FRESH metrics that DO fire "rsrp < -100" — failover to satellite is genuinely wanted, so the ONLY
	// thing that can hold the slice on terrestrial is the satellite being (correctly) unavailable.
	fr := fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{RSRP: -120, LatencyMs: 10, PacketLossPercent: 0},
		Stale:   false, LastFreshAt: fixedNow,
	}}
	r := &NTNSliceReconciler{
		Client: cli, Scheme: sch,
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

	if got := updated.Status.ActivePathType; got != "terrestrial" {
		t.Fatalf("must NOT fail over to a stale satellite: ActivePathType=%q, want terrestrial", got)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
	if cond == nil || cond.Reason != "SatelliteUnavailable" || !strings.Contains(cond.Message, "stale") {
		t.Fatalf("FailoverReady must be SatelliteUnavailable citing staleness, got %+v", cond)
	}
}

// TestReconcile_SwitchesBackFromStaleSatellite is the companion to NoFailoverToStaleSatellite: a slice
// ALREADY on satellite whose element set ages past maxEpochAge must be moved BACK to terrestrial —
// leaving it on a satellite whose ephemeris NTNCellConfig can no longer push would strand it. Metrics
// are fresh and GOOD (no quality trigger fires), so the ONLY driver of the switch is the staleness gate.
func TestReconcile_SwitchesBackFromStaleSatellite(t *testing.T) {
	fixedNow := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: "default"}}
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "chunghwa-telecom", APN: "internet", Priority: "primary"},
			SatellitePath:   ntnv1alpha1.SatellitePathSpec{PathSpec: ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"}, EphemerisRef: "oneweb-constellation"},
			FailoverPolicy:  ntnv1alpha1.FailoverPolicy{Triggers: []string{"rsrp < -100"}, SwitchbackDelay: metav1.Duration{Duration: 60 * time.Second}},
		},
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: "satellite"}, // currently ON satellite
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).Build()
	eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{{
		Satellite: "ONEWEB-0012", NoradID: 44057, GroundStation: "gs",
		AOS: metav1.Time{Time: fixedNow.Add(-time.Hour)}, LOS: metav1.Time{Time: fixedNow.Add(time.Hour)},
	}}
	eph.Status.PropagatedStates = []ntnv1alpha1.PropagatedState{{
		NoradID: 44057, EpochUnixMs: fixedNow.Add(5 * time.Minute).UnixMilli(),
		SourceEpochUnixMs: fixedNow.Add(-maxEpochAge - time.Hour).UnixMilli(),
	}}
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "Predicted"})
	if err := cli.Status().Update(context.Background(), eph); err != nil {
		t.Fatalf("seed ephemeris: %v", err)
	}
	fr := fakeReader{res: slicemetrics.Result{Metrics: slice.Metrics{RSRP: -80, LatencyMs: 10, PacketLossPercent: 0}, Stale: false, LastFreshAt: fixedNow}}
	r := &NTNSliceReconciler{Client: cli, Scheme: sch, Now: func() time.Time { return fixedNow }, ReaderProvider: fakeReaderProvider{reader: fr}}
	key := client.ObjectKeyFromObject(nsObj)
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &ntnv1alpha1.NTNSlice{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got := updated.Status.ActivePathType; got != "terrestrial" {
		t.Fatalf("a slice on a now-stale satellite must switch back to terrestrial: ActivePathType=%q", got)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
	if cond == nil || cond.Reason != "SatelliteUnavailable" || !strings.Contains(cond.Message, "stale") {
		t.Fatalf("FailoverReady must be SatelliteUnavailable citing staleness, got %+v", cond)
	}
}
