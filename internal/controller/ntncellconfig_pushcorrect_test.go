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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestPushRuntime_SameMarkerBecomesStale_Blocked pins #221 review finding 1: the dedup
// early-return must NOT short-circuit past the currency gates. When the producer stalls
// (controller down / fetcher-setup failure / upstream outage) it stops re-propagating, so
// the marker STOPS CHANGING while the already-delivered epoch expires on the gNB. If dedup
// ran first, every 5-minute reconcile would return "up to date" and the caller would set
// EphemerisPushReady=1 — falsely healthy in exactly the producer-stall case the hard gate
// exists for. Here the state's epoch has EXPIRED but the marker is identical to the one
// already recorded as pushed; the push must fail CLOSED with EphemerisStale.
// Mutation: move isEphemerisPushUpToDate back above the epoch check → err becomes nil.
func TestPushRuntime_SameMarkerBecomesStale_Blocked(t *testing.T) {
	ctx := context.Background()
	// Previously-pushed state whose propagated epoch has since expired (producer stalled).
	eph := ephWithPropagatedState(time.Now().Add(-time.Hour).UnixMilli())
	marker := runtimeEphemerisPushMarker(eph, &eph.Status.PropagatedStates[0])

	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()
	// Record the EXACT same marker as already pushed, so dedup would match.
	meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionEphemerisPushed,
		Status:             metav1.ConditionTrue,
		Reason:             "Pushed",
		Message:            marker,
		ObservedGeneration: cc.Generation,
	})

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if pushed || mock.RuntimeCalls != 0 {
		t.Fatalf("an expired state must never be (re)pushed: pushed=%v RuntimeCalls=%d", pushed, mock.RuntimeCalls)
	}
	if err == nil {
		t.Fatal("a same-marker reconcile whose delivered epoch has EXPIRED must report EphemerisStale, " +
			"not silently return up-to-date (the caller would then set EphemerisPushReady=1 — falsely healthy)")
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonEphemerisStale {
		t.Fatalf("stale-after-dedup reason = %q, want %q", got, ephemerisReasonEphemerisStale)
	}
}

// TestPushRuntime_StaleInputs_Blocked_G1 pins #204-G1: when a source/selector edit has
// changed the propagation inputs but the re-propagate has not yet succeeded (the stored
// propagatedStatesInputHash no longer matches hash(spec)), the persisted states are stale
// for the current inputs and MUST NOT be pushed.
func TestPushRuntime_StaleInputs_Blocked_G1(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	// Simulate a source edit AFTER the fixture stamped the hash: the live spec now hashes
	// differently from the persisted states' recorded inputs.
	eph.Spec.Source.URL = "https://celestrak.org/CHANGED"
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if pushed || mock.RuntimeCalls != 0 {
		t.Fatalf("states computed under superseded inputs must NOT be pushed: pushed=%v RuntimeCalls=%d", pushed, mock.RuntimeCalls)
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonInputsStale {
		t.Fatalf("blocked-push reason = %q, want %q", got, ephemerisReasonInputsStale)
	}
	if ephemerisPushShouldRequeue(ephemerisReasonInputsStale) {
		t.Fatal("inputs-stale must be non-requeuing (it clears on the ephemeris watch)")
	}
}

// TestPushRuntime_CurrentInputs_Pushes_G1 is the paired positive: matching input hash
// (the fixture stamps it) pushes normally — proving the gate is input-specific.
func TestPushRuntime_CurrentInputs_Pushes_G1(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if err != nil || !pushed || mock.RuntimeCalls != 1 {
		t.Fatalf("current-inputs states must push: pushed=%v err=%v RuntimeCalls=%d", pushed, err, mock.RuntimeCalls)
	}
}

// TestPushRuntime_EmptyInputHash_Blocked_ColdUpgrade pins the fail-closed cold-upgrade
// behavior (#221 review finding 3): an object whose propagatedStates predate this field
// (empty hash) blocks until the first post-upgrade re-propagation stamps it.
func TestPushRuntime_EmptyInputHash_Blocked_ColdUpgrade(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	eph.Status.PropagatedStatesInputHash = "" // legacy object, never re-propagated since upgrade
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if pushed || mock.RuntimeCalls != 0 {
		t.Fatalf("an empty input hash must fail closed until re-propagation: pushed=%v RuntimeCalls=%d", pushed, mock.RuntimeCalls)
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonInputsStale {
		t.Fatalf("cold-upgrade block reason = %q, want %q", got, ephemerisReasonInputsStale)
	}
}

// TestPushRuntime_SelectedSatelliteStale_Blocked_C4 pins per-satellite #200-C4: the
// SELECTED satellite's own source element epoch is beyond the freshness bound → block,
// even with a valid future propagation epoch and matching input hash.
func TestPushRuntime_SelectedSatelliteStale_Blocked_C4(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	eph.Status.PropagatedStates[0].SourceEpochUnixMs = time.Now().Add(-8 * 24 * time.Hour).UnixMilli() // 8 days old
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if pushed || mock.RuntimeCalls != 0 {
		t.Fatalf("a stale SELECTED satellite must block the push: pushed=%v RuntimeCalls=%d", pushed, mock.RuntimeCalls)
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonEphemerisStale {
		t.Fatalf("selected-stale reason = %q, want %q", got, ephemerisReasonEphemerisStale)
	}
}

// TestPushRuntime_UnselectedSiblingStale_Pushes_C4 is THE regression fix for #221 review
// finding 1: a stale UNSELECTED sibling in the same SatelliteEphemeris must NOT block a
// cell whose selected satellite is fresh. The old per-resource EphemerisEpochStale gate
// wrongly blocked this.
func TestPushRuntime_UnselectedSiblingStale_Pushes_C4(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli()) // NORAD 25544, fresh (selected)
	// Add a stale sibling the cell does NOT select.
	eph.Status.PropagatedStates = append(eph.Status.PropagatedStates, ntnv1alpha1.PropagatedState{
		Satellite: "SAT-OLD", NoradID: 200, EpochUnixMs: time.Now().Add(time.Hour).UnixMilli(),
		SourceEpochUnixMs: time.Now().Add(-30 * 24 * time.Hour).UnixMilli(), // 30 days old
		ECEF:              ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
	})
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl() // selects NORAD 25544

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if err != nil || !pushed || mock.RuntimeCalls != 1 {
		t.Fatalf("a stale UNSELECTED sibling must not block a fresh selected satellite: pushed=%v err=%v RuntimeCalls=%d",
			pushed, err, mock.RuntimeCalls)
	}
}

// TestPushRuntime_FutureDatedSource_Blocked pins #221 review finding 5: an implausibly
// future-dated source epoch is rejected rather than driving a long backward propagation.
func TestPushRuntime_FutureDatedSource_Blocked(t *testing.T) {
	ctx := context.Background()
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	eph.Status.PropagatedStates[0].SourceEpochUnixMs = time.Now().Add(30 * 24 * time.Hour).UnixMilli() // 30 days future
	c := fake.NewClientBuilder().WithScheme(makeScheme(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if pushed || mock.RuntimeCalls != 0 {
		t.Fatalf("an implausibly future-dated source epoch must block: pushed=%v RuntimeCalls=%d", pushed, mock.RuntimeCalls)
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonEphemerisStale {
		t.Fatalf("future-dated reason = %q, want %q", got, ephemerisReasonEphemerisStale)
	}
}
