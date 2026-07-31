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
	"reflect"
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
		{NoradID: 100000, ECEF: ntnv1alpha1.EphemerisECEF{PosX: 3}}, // CelesTrak 100000+ six-digit catalog
	}
	if got := selectPropagatedState(states, new(200)); got == nil || got.NoradID != 200 {
		t.Fatalf("by-noradID selection failed: %+v", got)
	}
	// A six-digit NORAD (CelesTrak exhausted the 5-digit space in 2026-07) must select cleanly.
	if got := selectPropagatedState(states, new(100000)); got == nil || got.NoradID != 100000 {
		t.Fatalf("six-digit NORAD selection failed: %+v", got)
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
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph1", Namespace: "ns", Generation: 1},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			LastUpdated: &metav1.Time{Time: time.Now()},
			PropagatedStates: []ntnv1alpha1.PropagatedState{{
				Satellite: "SAT-A", NoradID: 25544, EpochUnixMs: futureMs,
				// Fresh source element epoch so the per-satellite C4 freshness gate passes.
				SourceEpochUnixMs: time.Now().Add(-time.Hour).UnixMilli(),
				ECEF:              ntnv1alpha1.EphemerisECEF{PosX: 5000000, PosY: 4000000, PosZ: 3000000},
			}},
		},
	}
	// A reconciled ephemeris stamps the propagation-input hash of its current spec; the
	// runtime-push gate requires status.propagatedStatesInputHash == hash(spec) to treat
	// the propagatedStates as current, so the fixture stamps it.
	eph.Status.PropagatedStatesInputHash = propagationInputHash(eph.Spec)
	return eph
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
	// ccWithRemoteControl leaves ntnUlSyncValidityDur unset, so the runtime push must send the operator's
	// 5 s default on the wire (effectiveUlSyncValidityDurSeconds) — the SAME value the SIB19-cadence advisory
	// evaluates against. This pins the "shared helper, cannot drift" contract end-to-end (#228 round-4).
	if mock.LastRuntime.UlSyncValidityDur != 5 {
		t.Fatalf("unset ntnUlSyncValidityDur must push the 5 s operator default on the wire; got %d", mock.LastRuntime.UlSyncValidityDur)
	}
}

// TestPushEphemerisUpdateIfNeeded_RuntimePath_ExplicitUlSyncValidity pins the non-nil branch of the shared
// effectiveUlSyncValidityDurSeconds on the runtime-push path: an explicit ntnUlSyncValidityDur must
// round-trip to the wire (not be replaced by the default). Complements the unset→5 assertion above, so a
// push that ignored the spec value and always sent 5 would be caught here.
func TestPushEphemerisUpdateIfNeeded_RuntimePath_ExplicitUlSyncValidity(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()
	cc.Spec.NTN.NTNUlSyncValidityDur = new(30)

	if _, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock); err != nil {
		t.Fatalf("push: %v", err)
	}
	if mock.LastRuntime == nil || mock.LastRuntime.UlSyncValidityDur != 30 {
		t.Fatalf("explicit ntnUlSyncValidityDur=30 must round-trip to the wire; got %+v", mock.LastRuntime)
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

// TestPushEphemerisUpdateIfNeeded_SatSwitchGatedOnIntent pins that a pending switch is delivered
// ONCE per intent, not re-sent on every ephemeris heartbeat (issue #207). The serving ephemeris is
// set-state and re-pushes on every ~3-min epoch (since #204), but sat_switch_with_resync may carry
// MAC/RRC-resync command semantics whose idempotency is unproven, so an unchanged switch must not
// ride every heartbeat; a CHANGED intent must re-send. Mutation: drop the
// `switchDigest != cc.Status.LastPushedSatSwitchDigest` gate → push 2 re-attaches the identical
// switch and the "must NOT re-send" assertion fails.
func TestPushEphemerisUpdateIfNeeded_SatSwitchGatedOnIntent(t *testing.T) {
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

	// Push 1: a new switch intent (empty stored digest) → attached + recorded in status.
	if _, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	if mock.LastRuntime.SatSwitch == nil {
		t.Fatal("push 1: a new switch intent must be attached")
	}
	if cc.Status.LastPushedSatSwitchDigest == "" {
		t.Fatal("push 1: the delivered switch intent must be recorded in status")
	}

	// Push 2: same intent → the serving ephemeris still pushes (set-state), but the switch must NOT
	// re-attach on the heartbeat.
	mock.LastRuntime = nil
	if _, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	if mock.LastRuntime == nil {
		t.Fatal("push 2: the serving ephemeris must still be pushed")
	}
	if mock.LastRuntime.SatSwitch != nil {
		t.Fatal("push 2: an unchanged switch intent must NOT be re-attached on the ephemeris heartbeat (#207)")
	}

	// Push 3: the switch intent changes (different k_mac) → re-attached.
	newK := 201
	cc.Spec.NTN.SatSwitchWithResync.NTNConfig.KMac = &newK
	mock.LastRuntime = nil
	if _, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock); err != nil {
		t.Fatalf("push 3: %v", err)
	}
	if mock.LastRuntime.SatSwitch == nil || mock.LastRuntime.SatSwitch.NTNConfig.KMac == nil ||
		*mock.LastRuntime.SatSwitch.NTNConfig.KMac != 201 {
		t.Fatal("push 3: a changed switch intent must be re-attached")
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
	// The SGP4-failing OMMs get LOWER NORADs than the good ones so propagateStates'
	// canonicalizeOMMs (dedup + sort by NORAD) keeps them ahead of the cap boundary — i.e.
	// they are ATTEMPTED and fail, which is the "failures before the cap" scenario this test
	// pins (they must not be counted as cap-truncated). Good NORADs start at 25544.
	build := func(nBad, nGood int) []sgp4.OMM {
		omms := make([]sgp4.OMM, 0, nBad+nGood)
		for i := range nBad {
			b := badOMMForTest()
			b.NoradCatID = 1000 + i
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
	// The heartbeat only reschedules on a POSITIVE cadence: the reconcile returns
	// RequeueAfter = min(effectiveInterval, propagationRefreshInterval), and controller-runtime requeues
	// after a duration only when it is > 0, so a zero/negative cadence disables periodic propagation.
	if propagationRefreshInterval <= 0 {
		t.Fatalf("propagationRefreshInterval must be positive; got %v — a non-positive RequeueAfter disables "+
			"the scheduled propagation heartbeat entirely", propagationRefreshInterval)
	}
	// The producer stamps each epoch at now+propagationEpochLead and re-propagates every
	// propagationRefreshInterval; the consumer rejects a state whose EpochUnixMs <= now+epochSkewMargin.
	// Both sides serialize through UnixMilli, which truncates sub-millisecond, and the producer's `now` can
	// sit at ANY sub-millisecond phase — so comparing UnixMilli at a single fixed phase (e.g. the Unix
	// epoch) is not enough: two instants less than 1ms apart floor into the same millisecond at some phase,
	// and the consumer's <= then rejects the epoch. The necessary-and-sufficient constant-level condition,
	// over every phase, is that the nominal headroom (lead - cadence - skew) is at least ONE full
	// millisecond; then floor(p+lead) is always at least one bucket above floor(p+cadence+skew) for every
	// phase p (see TestPropagationEpochSerialization_AllClockPhases). This is the constant-level LOWER BOUND
	// on continuity — runtime processing / queue / status-watch / delivery latency live outside it (tracked
	// by #234); it is NOT an unconditional "never expires" guarantee. (The outage progression test proves
	// only that the fetch-retry state machine does not BLOCK the heartbeat; it cannot prove the lead is
	// large enough, as its expected epoch is derived from this same constant.) This subsumes the old
	// lead > epochSkewMargin check.
	if headroom := propagationEpochLead - propagationRefreshInterval - epochSkewMargin; headroom < time.Millisecond {
		t.Fatalf("propagationEpochLead (%v) must leave at least 1ms of nominal headroom after "+
			"propagationRefreshInterval (%v) + epochSkewMargin (%v); got %v — at some sub-millisecond clock "+
			"phase the serialized (UnixMilli) epoch collapses onto the consumer's stale cutoff",
			propagationEpochLead, propagationRefreshInterval, epochSkewMargin, headroom)
	}
	if propagationEpochLead > 30*time.Minute {
		t.Fatalf("propagationEpochLead (%v) is too large; a near-now epoch keeps OCUDU's internal propagation short",
			propagationEpochLead)
	}
}

// TestPropagationEpochSerialization_AllClockPhases is the standalone precision proof behind the
// >= 1ms headroom requirement in TestPropagationEpochLeadVsSkewMargin: with only a sub-millisecond gap
// there IS a clock phase where the serialized (UnixMilli) epoch floors into the same millisecond as the
// consumer's stale cutoff, so the consumer's <= would reject it. It is not tied to the production
// constants; it demonstrates why a single fixed-phase UnixMilli comparison is insufficient.
func TestPropagationEpochSerialization_AllClockPhases(t *testing.T) {
	const gap = 100 * time.Microsecond // a sub-millisecond headroom
	lead := propagationEpochLead
	cutoff := lead - gap
	for phase := time.Duration(0); phase < time.Millisecond; phase += time.Microsecond {
		base := time.Unix(0, int64(phase))
		if base.Add(lead).UnixMilli() <= base.Add(cutoff).UnixMilli() {
			return // found the phase where a sub-1ms gap collapses under UnixMilli — exactly why >= 1ms is required
		}
	}
	t.Fatal("expected a sub-millisecond clock phase where a 100µs headroom collapses under UnixMilli, found none")
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
		ephemerisReasonRemoteControlCredential, ephemerisReasonRemoteControlEndpointNotAllowed,
		ephemerisReasonRemoteEndpointRejected,
	} {
		if !ephemerisPushShouldRequeue(reason) {
			t.Errorf("%s SHOULD requeue (its fix is either transient or unwatched)", reason)
		}
	}
}

// A refused handshake (401/403/429/redirect/4xx) must self-heal on the slow poll, not join the
// never-requeue set. The endpoint said no, so a per-minute retry is wrong — but the fix is a
// credential, a proxy rule or the gNB itself, none of which this controller watches, so "never
// retry" would leave the cell dependent on an unrelated SatelliteEphemeris heartbeat fan-out and
// permanently stranded if the producer stalls. Mirrors RemoteControlCredentialUnavailable (#282).
func TestPushEphemerisUpdateIfNeeded_RefusedHandshakeSelfHeals(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{
		RuntimeErr: fmt.Errorf("%w: handshake rejected: HTTP 401", provider.ErrRuntimePushRetryLater),
	}
	cc := ccWithRemoteControl()

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if pushed {
		t.Fatal("expected pushed=false on a refused handshake")
	}
	if got := ephemerisPushConditionReason(err); got != ephemerisReasonRemoteEndpointRejected {
		t.Fatalf("expected RemoteEndpointRejected reason, got %q", got)
	}
	if !ephemerisPushShouldRequeue(ephemerisReasonRemoteEndpointRejected) {
		t.Error("a refused handshake must self-requeue: its fix is unwatched, so no requeue strands the cell")
	}
	if got := ephemerisPushRequeueInterval(ephemerisReasonRemoteEndpointRejected); got != remoteControlConfigRequeue {
		t.Errorf("refused-handshake requeue = %v, want %v (a slow self-heal poll, not a tight retry)", got, remoteControlConfigRequeue)
	}
}

// TestPushEphemerisUpdateIfNeeded_CrashBeforeStatusPersists_RepushIsIdempotent (#230 item 6): if the leader
// pushes to the gNB (PushRuntimeUpdate succeeds) but crashes BEFORE its status write persists the push
// marker, the new leader — reading a status without the marker — re-pushes. isEphemerisPushUpToDate keys the
// dedup on the persisted EphemerisPushed condition Message (the epoch-derived marker), so a marker that never
// persisted means the re-push happens; the SAFETY is that it re-emits a byte-identical, SAME-epoch frame (a
// level re-assert, no delta) — de-duplicating a repeated same-epoch push is the gNB/OCUDU runtime's job, not
// something this unit test itself proves. We simulate the crash by never writing the marker back to cc.Status
// between two pushEphemerisUpdateIfNeeded calls, then show the dedup closes once the marker finally persists.
func TestPushEphemerisUpdateIfNeeded_CrashBeforeStatusPersists_RepushIsIdempotent(t *testing.T) {
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

	// Push #1: the leader pushes the runtime ephemeris.
	pushed1, marker1, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed1 {
		t.Fatalf("push #1: pushed=%v err=%v", pushed1, err)
	}
	if mock.RuntimeCalls != 1 {
		t.Fatalf("push #1 must call the provider once; got %d", mock.RuntimeCalls)
	}
	epoch1 := mock.LastRuntime.EpochUnixMs

	// CRASH before status persists: the marker is NOT written to cc.Status, so isEphemerisPushUpToDate stays
	// false. The new leader reconciles the same (unchanged) state.
	pushed2, marker2, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed2 {
		t.Fatalf("re-push #2: pushed=%v err=%v", pushed2, err)
	}
	if mock.RuntimeCalls != 2 {
		t.Fatalf("an unpersisted marker must trigger a re-push; got %d provider calls", mock.RuntimeCalls)
	}
	epoch2 := mock.LastRuntime.EpochUnixMs

	// Safety: the re-push is an idempotent level re-assert — SAME epoch-derived marker, SAME epoch, and (below)
	// a byte-identical whole frame. Whether the downstream gNB acts on or dedups the repeated same-epoch push is
	// the runtime's responsibility, not asserted here.
	if marker1 != marker2 {
		t.Fatalf("re-push marker must be identical (epoch-keyed idempotent); got %q then %q", marker1, marker2)
	}
	if epoch1 != futureMs || epoch2 != futureMs {
		t.Fatalf("both pushes must carry the same propagated epoch %d; got %d then %d", futureMs, epoch1, epoch2)
	}
	// Stronger than marker+epoch: the WHOLE runtime frame (cell, epoch, UL-sync validity, ephemeris payload,
	// satSwitch) AND the resolved target must be identical across the two pushes. RuntimeHistory/TargetHistory
	// hold a DEEP COPY of each call, so this compares the actual frames, not an aliased view of the latest one.
	if len(mock.RuntimeHistory) != 2 || len(mock.TargetHistory) != 2 {
		t.Fatalf("expected 2 recorded runtime frames + targets; got %d / %d", len(mock.RuntimeHistory), len(mock.TargetHistory))
	}
	if !reflect.DeepEqual(mock.RuntimeHistory[0], mock.RuntimeHistory[1]) {
		t.Fatalf("re-push runtime frame must be byte-identical; got %+v then %+v", mock.RuntimeHistory[0], mock.RuntimeHistory[1])
	}
	if !reflect.DeepEqual(mock.TargetHistory[0], mock.TargetHistory[1]) {
		t.Fatalf("re-push target must be identical; got %+v then %+v", mock.TargetHistory[0], mock.TargetHistory[1])
	}

	// Once the marker DOES persist (the new leader completes its status write), a further reconcile must NOT
	// re-push — the epoch-keyed dedup closes.
	meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
		Type: ntnv1alpha1.ConditionEphemerisPushed, Status: metav1.ConditionTrue,
		Reason: "Pushed", Message: marker2, ObservedGeneration: cc.Generation,
	})
	pushed3, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("push #3: %v", err)
	}
	if pushed3 || mock.RuntimeCalls != 2 {
		t.Fatalf("once the marker persists the dedup must close (no re-push); pushed=%v calls=%d", pushed3, mock.RuntimeCalls)
	}
}

// TestPushEphemerisUpdateIfNeeded_CrashBeforeStatus_PendingSatSwitch_IsIdempotent (#230 item 6, satSwitch
// path): a pending SatSwitchWithResync — the most state-changing runtime payload, and the only surface
// carrying k_mac (#52) — rides on every runtime push. If the leader crashes after PushRuntimeUpdate but
// before persisting the marker, the new leader re-pushes with the SAME pending switch at the SAME epoch. This
// must be an EXACT idempotent duplicate (identical switch incl. k_mac, same epoch-keyed marker) — a repeated
// same-epoch frame, NOT a new/mutated delta (whether OCUDU dedups it by epoch is the runtime's concern, not
// asserted here). This complements the ephemeris-only crash-dedup test above, which only excused the satSwitch
// case in a comment.
func TestPushEphemerisUpdateIfNeeded_CrashBeforeStatus_PendingSatSwitch_IsIdempotent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()
	kmac := 42
	cc.Spec.NTN.SatSwitchWithResync = &ntnv1alpha1.SatSwitchWithResync{
		NTNConfig: ntnv1alpha1.SatSwitchNTNConfig{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 7000000, PosY: 1000000, PosZ: 2000000},
			KMac:          &kmac,
		},
		EpochUnixMs: 1893456000000,
	}

	// Push #1: the leader pushes ephemeris + the pending satSwitch.
	pushed1, marker1, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed1 || mock.RuntimeCalls != 1 {
		t.Fatalf("push #1: pushed=%v err=%v calls=%d", pushed1, err, mock.RuntimeCalls)
	}
	if mock.LastRuntime.SatSwitch == nil {
		t.Fatal("push #1 must carry the pending satSwitch")
	}
	sw1 := *mock.LastRuntime.SatSwitch

	// CRASH before status persists → the new leader re-pushes the same unchanged state. The crash
	// means the status write — which carries lastPushedSatSwitchDigest — never persisted, so the new
	// leader reads it empty and MUST re-carry the switch. The crash-idempotency contract (#230 item 6)
	// therefore still holds: an un-recorded switch is re-sent (only a DURABLY recorded intent is gated
	// off the heartbeat, #207).
	cc.Status.LastPushedSatSwitchDigest = ""
	pushed2, marker2, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed2 || mock.RuntimeCalls != 2 {
		t.Fatalf("re-push #2: pushed=%v err=%v calls=%d", pushed2, err, mock.RuntimeCalls)
	}
	if mock.LastRuntime.SatSwitch == nil {
		t.Fatal("re-push #2 must re-carry the pending satSwitch")
	}
	sw2 := *mock.LastRuntime.SatSwitch

	// The re-push is an EXACT idempotent duplicate: same epoch-keyed marker, the identical satSwitch (same
	// k_mac, target ephemeris, epoch), and a byte-identical whole frame — a repeated same-epoch push, not a new
	// delta. (Whether OCUDU dedups it by epoch is the runtime's concern, not asserted here.)
	if marker1 != marker2 {
		t.Fatalf("re-push marker must be identical; got %q then %q", marker1, marker2)
	}
	if !reflect.DeepEqual(sw1, sw2) {
		t.Fatalf("re-push satSwitch must be an identical duplicate; got %+v then %+v", sw1, sw2)
	}
	if len(mock.RuntimeHistory) != 2 {
		t.Fatalf("expected 2 recorded runtime frames; got %d", len(mock.RuntimeHistory))
	}
	if !reflect.DeepEqual(mock.RuntimeHistory[0], mock.RuntimeHistory[1]) {
		t.Fatalf("re-push runtime frame (incl. satSwitch) must be byte-identical; got %+v then %+v",
			mock.RuntimeHistory[0], mock.RuntimeHistory[1])
	}
}
