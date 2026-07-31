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
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// candidateEph builds an ephemeris whose windows/states the caller supplies, with predictions
// current (otherwise availability is Unknown before any candidate is considered).
func candidateEph(windows []ntnv1alpha1.PassWindow, states []ntnv1alpha1.PropagatedState) *ntnv1alpha1.SatelliteEphemeris {
	return &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph", Namespace: "default"},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			Conditions: []metav1.Condition{{
				Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "x",
			}},
			NextPassWindows:  windows,
			PropagatedStates: states,
		},
	}
}

func candidateSlice() *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: "default"},
		Spec:       ntnv1alpha1.NTNSliceSpec{SatellitePath: ntnv1alpha1.SatellitePathSpec{EphemerisRef: "eph"}},
	}
}

// TestContactCandidate_DeterministicAndAudited is the ADR-0008 candidate contract. FailoverReady
// alone says an opportunity exists but not which member it rests on, so an operator cannot tell a
// healthy handover from the same member re-evaluated, and cannot audit the decision afterwards.
func TestContactCandidate_DeterministicAndAudited(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).UnixMilli()
	sch := makeScheme(t)

	// Two members overhead at once — the handover overlap. B sets first.
	eph := candidateEph(
		[]ntnv1alpha1.PassWindow{
			{Satellite: "A", NoradID: 25544, GroundStation: "gs-a",
				AOS: metav1.Time{Time: now.Add(-5 * time.Minute)}, LOS: metav1.Time{Time: now.Add(20 * time.Minute)}},
			{Satellite: "B", NoradID: 40000, GroundStation: "gs-b",
				AOS: metav1.Time{Time: now.Add(-9 * time.Minute)}, LOS: metav1.Time{Time: now.Add(4 * time.Minute)}},
		},
		[]ntnv1alpha1.PropagatedState{
			{NoradID: 25544, SourceEpochUnixMs: fresh, EpochUnixMs: now.Add(time.Minute).UnixMilli()},
			{NoradID: 40000, SourceEpochUnixMs: fresh, EpochUnixMs: now.Add(2 * time.Minute).UnixMilli()},
		})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, candidateSlice()).Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: sch}

	ev := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
	if !ev.available || ev.reason != reasonConstellationMemberAvailable {
		t.Fatalf("available=%v reason=%q, want a constellation member available", ev.available, ev.reason)
	}
	c := ev.candidate
	if c == nil {
		t.Fatal("no candidate recorded: FailoverReady=True with nothing saying which member it rests on")
	}
	if c.NoradID != 40000 {
		t.Errorf("candidate NORAD = %d, want 40000 (earliest LOS); picking by slice order makes the "+
			"reported member depend on how the producer happened to sort its windows", c.NoradID)
	}
	if c.GroundStation != "gs-b" || c.Satellite != "B" {
		t.Errorf("candidate = %+v, want the observer and label of the member actually selected", c)
	}
	if c.SelectionReason != "EarliestLOS" {
		t.Errorf("selectionReason = %q; without it the choice cannot be reproduced from status alone", c.SelectionReason)
	}
	if c.SourceEpochUnixMs != fresh {
		t.Errorf("sourceEpoch = %d, want %d — the age that decides deliverability", c.SourceEpochUnixMs, fresh)
	}
	if c.AOS == nil || c.LOS == nil || !c.LOS.Time.Equal(now.Add(4*time.Minute)) {
		t.Errorf("candidate window = %+v / %+v, want the selected member's own window", c.AOS, c.LOS)
	}

	// Same inputs, second evaluation: identical candidate. A field that flips between two equally
	// valid members on every reconcile is status churn pretending to be information.
	// DeepEqual, not ==: ContactCandidate holds *metav1.Time, so struct equality would compare
	// pointers and pass for two candidates that merely happen to be distinct allocations.
	again := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
	if !reflect.DeepEqual(c, again.candidate) {
		t.Errorf("candidate is not deterministic: %+v then %+v", c, again.candidate)
	}
}

// TestContactCandidate_ValidUntilTakesTheEarlierBound: reporting LOS alone would promise a window
// the runtime push refuses to use before it closes — the element set ages out first.
func TestContactCandidate_ValidUntilTakesTheEarlierBound(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	// Element set expires (sourceEpoch+maxEpochAge) 10 minutes from now; the pass runs 3 hours.
	sourceEpoch := now.Add(10 * time.Minute).Add(-maxEpochAge)
	eph := candidateEph(
		[]ntnv1alpha1.PassWindow{{Satellite: "A", NoradID: 25544, GroundStation: "gs",
			AOS: metav1.Time{Time: now.Add(-time.Minute)}, LOS: metav1.Time{Time: now.Add(3 * time.Hour)}}},
		[]ntnv1alpha1.PropagatedState{{NoradID: 25544, SourceEpochUnixMs: sourceEpoch.UnixMilli()}})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, candidateSlice()).Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: sch}

	ev := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
	if ev.candidate == nil || ev.candidate.ValidUntil == nil {
		t.Fatalf("no validUntil recorded: %+v", ev.candidate)
	}
	if got := ev.candidate.ValidUntil.Time; !got.Equal(sourceEpoch.Add(maxEpochAge).UTC()) {
		t.Errorf("validUntil = %s, want the freshness expiry %s — LOS is %s away and would overstate it",
			got, sourceEpoch.Add(maxEpochAge).UTC(), now.Add(3*time.Hour))
	}
}

// TestContactCandidate_ClearedWithItsCondition: a candidate that outlives the condition it explains
// is worse than none, because it reads as current evidence for a path that is gone.
func TestContactCandidate_ClearedWithItsCondition(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	for _, tc := range []struct {
		name       string
		eph        *ntnv1alpha1.SatelliteEphemeris
		wantReason string
	}{
		{"no member overhead", candidateEph(nil, nil), reasonNoActiveContact},
		{"overhead but stale", candidateEph(
			[]ntnv1alpha1.PassWindow{{Satellite: "A", NoradID: 25544, GroundStation: "gs",
				AOS: metav1.Time{Time: now.Add(-time.Minute)}, LOS: metav1.Time{Time: now.Add(time.Hour)}}},
			[]ntnv1alpha1.PropagatedState{{NoradID: 25544,
				SourceEpochUnixMs: now.Add(-maxEpochAge - time.Hour).UnixMilli()}}), reasonAllCandidatesStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(tc.eph, candidateSlice()).Build()
			r := &NTNSliceReconciler{Client: cli, Scheme: sch}
			ev := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
			if ev.available {
				t.Fatalf("must not be available: %+v", ev)
			}
			if ev.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q — these need different operator actions and only "+
					"staleness self-heals on the next fetch", ev.reason, tc.wantReason)
			}
			if ev.candidate != nil {
				t.Errorf("candidate %+v reported for an unavailable path", ev.candidate)
			}
		})
	}
}

// TestContactCandidate_PredictionUnavailableIsNotAReadFailure: one is an apiserver problem, the
// other a producer that has not finished recomputing. Collapsing them sends the operator to the
// wrong subsystem during an outage.
func TestContactCandidate_PredictionUnavailableIsNotAReadFailure(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	eph := candidateEph(nil, nil)
	eph.Status.Conditions[0].Status = metav1.ConditionUnknown // recomputing after an input change
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, candidateSlice()).Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: sch}

	ev := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
	if ev.known || ev.available {
		t.Fatalf("stale predictions must leave availability UNKNOWN so the current path is held: %+v", ev)
	}
	if ev.reason != reasonPredictionUnavailable {
		t.Errorf("reason = %q, want %q", ev.reason, reasonPredictionUnavailable)
	}
	if ev.candidate != nil {
		t.Errorf("candidate %+v built from predictions the producer disowned", ev.candidate)
	}
}

// TestContactCandidate_ClearedWhenMetricsGoUnreliable is the invariant the field's own doc claims:
// written and cleared WITH the condition it explains. setMetricsDegraded owns FailoverReady for
// that reconcile (the satellite branch is skipped), so a candidate left behind would present a
// constellation member as the evidence for a condition that is actually about metrics — stale
// evidence that looks current, which is worse than no field at all.
func TestContactCandidate_ClearedWhenMetricsGoUnreliable(t *testing.T) {
	slice := candidateSlice()
	slice.Status.ContactCandidate = &ntnv1alpha1.ContactCandidate{
		NoradID: 40000, Satellite: "B", GroundStation: "gs-b", SelectionReason: "EarliestLOS",
	}
	r := &NTNSliceReconciler{}
	r.setMetricsDegraded(slice, "MetricsReaderError", "reader is down")

	cond := meta.FindStatusCondition(slice.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != "MetricsReaderError" {
		t.Fatalf("FailoverReady = %+v, want Unknown/MetricsReaderError", cond)
	}
	if slice.Status.ContactCandidate != nil {
		t.Errorf("candidate %+v survived a condition it had no part in", slice.Status.ContactCandidate)
	}
}

// TestContactCandidate_StableInputsDoNotChurnStatus ties the determinism requirement to the
// ACCEPTED write-churn bound (#234 / ADR-0006): persistStatusIfChanged skips the write when the
// status deep-equals its previous value, so a candidate that is merely *plausible* rather than
// deterministic would rewrite status on EVERY reconcile of an unchanged constellation. Determinism
// is what keeps that decision intact, not a readability preference.
func TestContactCandidate_StableInputsDoNotChurnStatus(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).UnixMilli()
	sch := makeScheme(t)
	// Two members overhead with IDENTICAL windows apart from identity: the case where an
	// order-dependent pick would flip.
	eph := candidateEph(
		[]ntnv1alpha1.PassWindow{
			{Satellite: "A", NoradID: 25544, GroundStation: "gs",
				AOS: metav1.Time{Time: now.Add(-time.Minute)}, LOS: metav1.Time{Time: now.Add(time.Hour)}},
			{Satellite: "B", NoradID: 40000, GroundStation: "gs",
				AOS: metav1.Time{Time: now.Add(-time.Minute)}, LOS: metav1.Time{Time: now.Add(time.Hour)}},
		},
		[]ntnv1alpha1.PropagatedState{
			{NoradID: 25544, SourceEpochUnixMs: fresh}, {NoradID: 40000, SourceEpochUnixMs: fresh},
		})
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph, candidateSlice()).Build()
	r := &NTNSliceReconciler{Client: cli, Scheme: sch}

	first := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
	for i := range 5 {
		got := r.checkSatelliteAvailability(context.Background(), candidateSlice(), now)
		if !reflect.DeepEqual(first.candidate, got.candidate) {
			t.Fatalf("evaluation %d produced a different candidate for unchanged inputs (%+v vs %+v); "+
				"persistStatusIfChanged would then write status every reconcile", i+2, first.candidate, got.candidate)
		}
	}
	if first.candidate.NoradID != 25544 {
		t.Errorf("tie on LOS must break on the lower NORAD, got %d", first.candidate.NoradID)
	}
}
