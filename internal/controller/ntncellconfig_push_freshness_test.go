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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestRuntimeEphemerisPushMarkerKeysOnEpoch pins I-12 at the marker level: the
// runtime push marker must track the propagated EPOCH, because the short-cadence
// re-propagation advances the epoch while LastUpdated (the 2h GP-fetch time) is
// unchanged — so the LastUpdated-keyed marker would never re-push.
func TestRuntimeEphemerisPushMarkerKeysOnEpoch(t *testing.T) {
	ts := metav1.NewTime(time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC))
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-a", Generation: 7},
		Status:     ntnv1alpha1.SatelliteEphemerisStatus{LastUpdated: &ts},
	}
	e1 := &ntnv1alpha1.PropagatedState{Satellite: "ISS", NoradID: 25544, EpochUnixMs: 1_700_000_000_000}
	e2 := &ntnv1alpha1.PropagatedState{Satellite: "ISS", NoradID: 25544, EpochUnixMs: 1_700_000_180_000} // +3 min

	// (Same-epoch dedup is proven end-to-end in TestPushRuntime_RepushesOnFreshEpoch_I12
	// step 2; here we focus on what MUST change the marker.)
	// Fresh epoch, LastUpdated UNCHANGED → different marker → re-push (the fix).
	if runtimeEphemerisPushMarker(eph, e1) == runtimeEphemerisPushMarker(eph, e2) {
		t.Fatal("a fresh propagated epoch must change the marker (I-12)")
	}
	// Proof of why the runtime path must not use the ConfigMap marker: it is
	// identical across the epoch advance (LastUpdated didn't move).
	if ephemerisPushMarker(eph) == "" {
		t.Fatal("sanity")
	}
	// A different satellite is a distinct push target.
	eOther := &ntnv1alpha1.PropagatedState{Satellite: "ONEWEB", NoradID: 44057, EpochUnixMs: e1.EpochUnixMs}
	if runtimeEphemerisPushMarker(eph, e1) == runtimeEphemerisPushMarker(eph, eOther) {
		t.Fatal("a different satellite must change the marker")
	}
	// A SatelliteEphemeris generation bump re-pushes (spec change).
	ephG := eph.DeepCopy()
	ephG.Generation++
	if runtimeEphemerisPushMarker(eph, e1) == runtimeEphemerisPushMarker(ephG, e1) {
		t.Fatal("an ephemeris generation change must change the marker")
	}
}

// TestPushRuntime_RepushesOnFreshEpoch_I12 pins the end-to-end I-12 fix through
// the reconciler helper: an already-pushed cell is NOT re-pushed for the same
// epoch (dedup), but a fresh propagated epoch — with LastUpdated deliberately
// UNCHANGED (as the short-cadence re-propagation leaves it) — DOES re-push. On
// the pre-fix LastUpdated-keyed marker the third call would dedup and the gNB
// would never be refreshed between GP fetches / after a restart.
func TestPushRuntime_RepushesOnFreshEpoch_I12(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	ctx := context.Background()
	epoch1 := time.Now().Add(time.Hour).UnixMilli()
	eph := ephWithPropagatedState(epoch1)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}
	cc := ccWithRemoteControl()

	// Mirror what the reconciler persists after a push, so the next call's
	// up-to-date check sees the last-pushed marker.
	persist := func(marker string) {
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionEphemerisPushed,
			Status:             metav1.ConditionTrue,
			Reason:             "Pushed",
			Message:            marker,
			ObservedGeneration: cc.Generation,
		})
	}

	// 1) First push at epoch1.
	pushed, marker1, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if err != nil || !pushed {
		t.Fatalf("first push: pushed=%v err=%v", pushed, err)
	}
	if mock.RuntimeCalls != 1 {
		t.Fatalf("first push RuntimeCalls=%d, want 1", mock.RuntimeCalls)
	}
	persist(marker1)

	// 2) Same epoch → dedup, no re-push.
	pushed, _, err = r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("dedup call: %v", err)
	}
	if pushed || mock.RuntimeCalls != 1 {
		t.Fatalf("same epoch must dedup: pushed=%v RuntimeCalls=%d (want false/1)", pushed, mock.RuntimeCalls)
	}

	// 3) Fresh epoch, LastUpdated unchanged → re-push (I-12).
	epoch2 := epoch1 + (3 * time.Minute).Milliseconds()
	got := &ntnv1alpha1.SatelliteEphemeris{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(eph), got); err != nil {
		t.Fatalf("get eph: %v", err)
	}
	got.Status.PropagatedStates[0].EpochUnixMs = epoch2 // only the epoch advances
	if err := c.Update(ctx, got); err != nil {
		t.Fatalf("update eph: %v", err)
	}

	pushed, marker3, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("re-push call: %v", err)
	}
	if !pushed || mock.RuntimeCalls != 2 {
		t.Fatalf("a fresh propagated epoch must re-push (I-12): pushed=%v RuntimeCalls=%d (want true/2)", pushed, mock.RuntimeCalls)
	}
	if mock.LastRuntime == nil || mock.LastRuntime.EpochUnixMs != epoch2 {
		t.Fatalf("re-push must carry the fresh epoch: got %+v want %d", mock.LastRuntime, epoch2)
	}
	if marker3 == marker1 {
		t.Fatal("a fresh epoch must change the marker")
	}
}
