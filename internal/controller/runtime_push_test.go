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
	"fmt"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

func TestSelectPropagatedState(t *testing.T) {
	states := []ntnv1alpha1.PropagatedState{
		{NoradID: 100, ECEF: ntnv1alpha1.EphemerisECEF{PosX: 1}},
		{NoradID: 200, ECEF: ntnv1alpha1.EphemerisECEF{PosX: 2}},
	}
	if got := selectPropagatedState(states, new(200)); got == nil || got.NoradID != 200 {
		t.Fatalf("by-noradID selection failed: %+v", got)
	}
	if got := selectPropagatedState(states, nil); got == nil || got.NoradID != 100 {
		t.Fatalf("nil selector should pick first: %+v", got)
	}
	if got := selectPropagatedState(states, new(999)); got != nil {
		t.Fatalf("unknown noradID should return nil: %+v", got)
	}
	if got := selectPropagatedState(nil, nil); got != nil {
		t.Fatal("empty states should return nil")
	}
}

func ephWithPropagatedState(futureMs int64) *ntnv1alpha1.SatelliteEphemeris {
	return &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph1", Namespace: "ns", Generation: 1},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			LastUpdated: &metav1.Time{Time: time.Now()},
			PropagatedStates: []ntnv1alpha1.PropagatedState{{
				Satellite: "SAT-A", NoradID: 25544, EpochUnixMs: futureMs,
				ECEF: ntnv1alpha1.EphemerisECEF{PosX: 5000000, PosY: 4000000, PosZ: 3000000},
			}},
		},
	}
}

// ccWithRemoteControl builds an NTNCellConfig on the runtime-push path: remote
// control + cellID + a noradID selector, plus a static spec.ntn ephemeris that
// DIFFERS from the propagated state (to prove the runtime path ignores it).
func ccWithRemoteControl() *ntnv1alpha1.NTNCellConfig {
	return &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc1", Namespace: "ns", Generation: 1},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{
				Type:          "ocudu",
				RemoteControl: &ntnv1alpha1.RemoteControlRef{Endpoint: "127.0.0.1:8001"},
			},
			CellID:           &ntnv1alpha1.CellID{PLMN: "00101", NCI: 6733824},
			EphemerisRef:     "eph1",
			EphemerisNoradID: new(25544),
			NTN: ntnv1alpha1.NTNParams{
				EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 111, PosY: 222, PosZ: 333},
			},
		},
	}
}

// Runtime path: with remoteControl + cellID, the reconciler must push via
// PushRuntimeUpdate, targeting the configured endpoint + cell, and (closing #176)
// sourcing the ephemeris from status.propagatedStates — NOT the CR's static
// spec.ntn.ephemerisECEF.
func TestPushEphemerisUpdateIfNeeded_RuntimePath_Closes176(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	futureMs := time.Now().Add(time.Hour).UnixMilli()
	eph := ephWithPropagatedState(futureMs)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}

	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !pushed {
		t.Fatal("expected pushed=true")
	}
	if mock.RuntimeCalls != 1 || mock.EphemerisCalls != 0 {
		t.Fatalf("expected runtime path (RuntimeCalls=1, EphemerisCalls=0), got %d/%d",
			mock.RuntimeCalls, mock.EphemerisCalls)
	}
	if mock.LastTarget == nil || mock.LastTarget.Endpoint != "127.0.0.1:8001" {
		t.Fatalf("wrong runtime target: %+v", mock.LastTarget)
	}
	if mock.LastRuntime == nil || mock.LastRuntime.Cell.PLMN != "00101" || mock.LastRuntime.Cell.NCI != 6733824 {
		t.Fatalf("wrong cell identity: %+v", mock.LastRuntime)
	}
	// #176: ephemeris must come from the propagated state (PosX 5000000), not the
	// CR's static spec.ntn.ephemerisECEF (PosX 111).
	if mock.LastRuntime.Ephemeris.ECEF == nil || mock.LastRuntime.Ephemeris.ECEF.PosX != 5000000 {
		t.Fatalf("#176: ephemeris not sourced from propagated state: %+v", mock.LastRuntime.Ephemeris.ECEF)
	}
	if mock.LastRuntime.EpochUnixMs != futureMs {
		t.Fatalf("epoch not sourced from propagated state: got %d want %d", mock.LastRuntime.EpochUnixMs, futureMs)
	}
}

// A pending satSwitchWithResync must ride along in the runtime push (issue #52 —
// the only surface that carries k_mac), delivered with the serving ephemeris.
func TestPushEphemerisUpdateIfNeeded_SatSwitchAttached(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}

	kmac := 200
	cc := ccWithRemoteControl()
	cc.Spec.NTN.SatSwitchWithResync = &ntnv1alpha1.SatSwitchWithResync{
		NTNConfig: ntnv1alpha1.SatSwitchNTNConfig{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 6000000, PosY: 1, PosZ: 1},
			KMac:          &kmac,
		},
	}

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed {
		t.Fatalf("push: pushed=%v err=%v", pushed, err)
	}
	if mock.LastRuntime == nil || mock.LastRuntime.SatSwitch == nil {
		t.Fatal("sat switch was not attached to the runtime push")
	}
	if k := mock.LastRuntime.SatSwitch.NTNConfig.KMac; k == nil || *k != 200 {
		t.Errorf("k_mac not propagated to the push: %+v", mock.LastRuntime.SatSwitch.NTNConfig)
	}
}

// Without a pending switch, the runtime push must not carry a sat-switch block.
func TestPushEphemerisUpdateIfNeeded_NoSatSwitchByDefault(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}

	cc := ccWithRemoteControl()
	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed {
		t.Fatalf("push: pushed=%v err=%v", pushed, err)
	}
	// Must actually have pushed (so the SatSwitch==nil check below isn't vacuous).
	if mock.RuntimeCalls != 1 || mock.LastRuntime == nil {
		t.Fatalf("expected exactly one runtime push, got RuntimeCalls=%d", mock.RuntimeCalls)
	}
	if mock.LastRuntime.SatSwitch != nil {
		t.Error("runtime push carried a sat-switch when none was configured")
	}
}

// Backward compatibility: without remoteControl, the reconciler must use the
// ConfigMap PushEphemerisUpdate path with the CR's static ephemeris.
func TestPushEphemerisUpdateIfNeeded_BackwardCompatConfigMapPath(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}

	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc2", Namespace: "ns", Generation: 1},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider:     ntnv1alpha1.ProviderRef{Type: "ocudu"}, // no remoteControl
			EphemerisRef: "eph1",
			NTN: ntnv1alpha1.NTNParams{
				EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 111, PosY: 222, PosZ: 333},
			},
		},
	}

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !pushed {
		t.Fatal("expected pushed=true")
	}
	if mock.EphemerisCalls != 1 || mock.RuntimeCalls != 0 {
		t.Fatalf("expected ConfigMap path (EphemerisCalls=1, RuntimeCalls=0), got %d/%d",
			mock.EphemerisCalls, mock.RuntimeCalls)
	}
	if mock.LastEphemeris == nil || mock.LastEphemeris.ECEF == nil || mock.LastEphemeris.ECEF.PosX != 111 {
		t.Fatalf("ConfigMap path must push the CR's static ephemeris: %+v", mock.LastEphemeris)
	}
}

func issOMMForTest() sgp4.OMM {
	return sgp4.OMM{
		ObjectName: "ISS (ZARYA)", ObjectID: "1998-067A",
		EpochStr:   time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		MeanMotion: 15.49554387, Eccentricity: 0.000588, Inclination: 51.6381,
		RAOfAscNode: 276.7884, ArgOfPericenter: 282.5765, MeanAnomaly: 192.7824,
		ClassificationType: "U", NoradCatID: 25544, ElementSetNo: 999,
		RevAtEpoch: 47189, BStar: 0.00025892, MeanMotionDot: 0.00019394,
	}
}

// PR C: truncation is transition-gated — the Warning note is returned only on the
// FIRST entry into the truncated state (so the ~3-min re-propagation cadence does
// not re-fire it), while status.truncatedSatelliteCount + the StatesTruncated
// condition always reflect the current state.
func TestPropagateStates_TruncationTransitionGated(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}
	const over = 5
	big := make([]sgp4.OMM, 0, maxPropagatedStates+over)
	for i := range maxPropagatedStates + over {
		o := issOMMForTest()
		o.NoradCatID = 25544 + i
		big = append(big, o)
	}
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	epoch := time.Now().Add(2 * time.Hour)
	isTruncated := func() bool {
		return meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionStatesTruncated)
	}

	// First reconcile over the cap: status + condition set, event note returned.
	note1 := r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: big}, epoch)
	if eph.Status.TruncatedSatelliteCount != over {
		t.Errorf("TruncatedSatelliteCount = %d, want %d", eph.Status.TruncatedSatelliteCount, over)
	}
	if !isTruncated() {
		t.Error("StatesTruncated condition should be True after exceeding the cap")
	}
	if note1 == "" {
		t.Error("first transition into truncated should return a non-empty event note")
	}

	// Second reconcile, still over the cap: NO new note (transition-gated → no spam).
	if note2 := r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: big}, epoch); note2 != "" {
		t.Errorf("steady-state truncation must not re-emit an event, got %q", note2)
	}

	// Recover within the cap: count 0, condition False, no note.
	note3 := r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMForTest()}}, epoch)
	if eph.Status.TruncatedSatelliteCount != 0 || isTruncated() {
		t.Error("recovery should clear the truncation count and condition")
	}
	if note3 != "" {
		t.Error("recovery should not emit a truncation Warning")
	}
}

// badOMMForTest returns an OMM that PropagateToECEF rejects (eccentricity >= 1 is
// invalid for SGP4), used to prove propagation failures do not inflate the count.
func badOMMForTest() sgp4.OMM {
	o := issOMMForTest()
	o.Eccentricity = 1.5
	return o
}

// PR C review follow-up: truncatedSatelliteCount must be the count actually dropped
// by the cap, NOT (selected - cap). Satellites that fail SGP4 propagation reduce the
// successful set and must never be reported as cap-truncated.
func TestPropagateStates_TruncationCountsCapDropsNotPropagationFailures(t *testing.T) {
	epoch := time.Now().Add(2 * time.Hour)
	isTruncated := func(eph *ntnv1alpha1.SatelliteEphemeris) bool {
		return meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionStatesTruncated)
	}
	build := func(nBad, nGood int) []sgp4.OMM {
		omms := make([]sgp4.OMM, 0, nBad+nGood)
		for i := range nBad {
			b := badOMMForTest()
			b.NoradCatID = 40000 + i
			omms = append(omms, b)
		}
		for i := range nGood {
			g := issOMMForTest()
			g.NoradCatID = 25544 + i
			omms = append(omms, g)
		}
		return omms
	}

	// 133 selected, 10 fail → 123 succeed (< 128) → the cap dropped nothing.
	t.Run("failures below the cap → 0 truncated", func(t *testing.T) {
		r := &SatelliteEphemerisReconciler{}
		eph := &ntnv1alpha1.SatelliteEphemeris{}
		note := r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: build(10, 123)}, epoch)
		if len(eph.Status.PropagatedStates) != 123 {
			t.Fatalf("expected 123 propagated states, got %d", len(eph.Status.PropagatedStates))
		}
		if eph.Status.TruncatedSatelliteCount != 0 {
			t.Errorf("cap dropped nothing (123 < 128) but TruncatedSatelliteCount = %d", eph.Status.TruncatedSatelliteCount)
		}
		if isTruncated(eph) || note != "" {
			t.Error("StatesTruncated must be False + no event when the cap dropped nothing")
		}
	})

	// 140 selected, 2 fail before the cap → 128 successes reached after 130 OMMs →
	// 10 remaining un-attempted → truncatedSatelliteCount = 10 (NOT 140-128=12).
	t.Run("cap reached after some failures → counts only the un-attempted", func(t *testing.T) {
		r := &SatelliteEphemerisReconciler{}
		eph := &ntnv1alpha1.SatelliteEphemeris{}
		r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: build(2, 138)}, epoch)
		if len(eph.Status.PropagatedStates) != maxPropagatedStates {
			t.Fatalf("expected %d propagated states, got %d", maxPropagatedStates, len(eph.Status.PropagatedStates))
		}
		if eph.Status.TruncatedSatelliteCount != 10 {
			t.Errorf("expected 10 un-attempted due to cap, got %d", eph.Status.TruncatedSatelliteCount)
		}
		if !isTruncated(eph) {
			t.Error("StatesTruncated must be True when the cap was reached")
		}
	})
}

// #176 producer: propagateStates fills status.propagatedStates from the tracked
// satellites' SGP4-propagated ECEF at the given future epoch.
func TestPropagateStates_FillsStatus(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}
	eph := &ntnv1alpha1.SatelliteEphemeris{
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Satellites: &ntnv1alpha1.SatelliteSelector{NoradIDs: []int{25544}},
		},
	}
	epoch := time.Now().Add(2 * time.Hour)
	r.propagateStates(context.Background(), eph, ephemeris.GPFetchResult{OMMs: []sgp4.OMM{issOMMForTest()}}, epoch)

	if len(eph.Status.PropagatedStates) != 1 {
		t.Fatalf("expected 1 propagated state, got %d", len(eph.Status.PropagatedStates))
	}
	s := eph.Status.PropagatedStates[0]
	if s.NoradID != 25544 || s.EpochUnixMs != epoch.UnixMilli() {
		t.Fatalf("unexpected state metadata: %+v", s)
	}
	if s.ECEF.PosX == 0 && s.ECEF.PosY == 0 && s.ECEF.PosZ == 0 {
		t.Fatalf("ECEF not populated by SGP4: %+v", s.ECEF)
	}
}

// A past/stale epoch must be SKIPPED (not pushed), because OCUDU rejects past
// epochs and re-labeling the state would corrupt it — and the skip must not
// tight-requeue (it clears when the producer refreshes).
func TestPushEphemerisUpdateIfNeeded_StaleEpochSkipped(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(-time.Hour).UnixMilli()) // PAST
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if pushed {
		t.Fatal("a stale (past) epoch must not be pushed")
	}
	if mock.RuntimeCalls != 0 {
		t.Fatalf("stale epoch must be skipped before the WS push, got RuntimeCalls=%d", mock.RuntimeCalls)
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonEphemerisStale {
		t.Fatalf("expected EphemerisStale reason, got %q", got)
	}
	if ephemerisPushShouldRequeue(ephemerisReasonEphemerisStale) {
		t.Fatal("a stale epoch must not tight-requeue")
	}
}

// A permanent gNB rejection must be classified as ProviderPushRejected and must
// NOT tight-requeue (retrying without a config change cannot help).
func TestPushEphemerisUpdateIfNeeded_PermanentRejectionNoRequeue(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{
		RuntimeErr: fmt.Errorf("%w: bad cell config", provider.ErrRuntimePushRejected),
	}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if pushed {
		t.Fatal("expected pushed=false on rejection")
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonProviderPushRejected {
		t.Fatalf("expected ProviderPushRejected reason, got %q", got)
	}
	if ephemerisPushShouldRequeue(ephemerisReasonProviderPushRejected) {
		t.Fatal("a permanent rejection must not tight-requeue")
	}
}

// The producer stamps propagated epochs at now+propagationEpochLead; the consumer
// skips epochs at/before now+epochSkewMargin. The lead must exceed the margin (with
// headroom) so a freshly propagated state is pushed, not skipped — and it must stay
// small so OCUDU's internal propagation from the epoch is short (guards against a
// regression back to now+refreshInterval, which back-propagated hours).
func TestPropagationEpochLeadVsSkewMargin(t *testing.T) {
	if propagationEpochLead <= epochSkewMargin {
		t.Fatalf("propagationEpochLead (%v) must exceed epochSkewMargin (%v) so a fresh state is not stale-skipped",
			propagationEpochLead, epochSkewMargin)
	}
	if propagationEpochLead > 30*time.Minute {
		t.Fatalf("propagationEpochLead (%v) is too large; a near-now epoch keeps OCUDU's internal propagation short",
			propagationEpochLead)
	}
}

func TestEphemerisPushShouldRequeue(t *testing.T) {
	for _, reason := range []string{
		ephemerisReasonRefNotFound, ephemerisReasonPayloadMissing,
		ephemerisReasonEphemerisStale, ephemerisReasonProviderPushRejected,
	} {
		if ephemerisPushShouldRequeue(reason) {
			t.Errorf("%s should NOT tight-requeue", reason)
		}
	}
	for _, reason := range []string{
		ephemerisReasonGetFailed, ephemerisReasonProviderPushFailed, ephemerisReasonPushFailed,
	} {
		if !ephemerisPushShouldRequeue(reason) {
			t.Errorf("%s SHOULD requeue (transient)", reason)
		}
	}
}
