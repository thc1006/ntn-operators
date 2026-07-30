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

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

func ommAt(norad int, epoch, name string) sgp4.OMM {
	return sgp4.OMM{NoradCatID: norad, EpochStr: epoch, ObjectName: name}
}

// TestCanonicalizeOMMs_DeterministicOrderAcrossReorder is the producer half of the silent-switch
// fix: whatever order the upstream lists satellites in, the canonical order (hence states[0], the
// implicit-selection target) must be identical — so a reordered response cannot switch the pushed
// satellite. Mutation target: dropping the sort makes the two outputs differ.
func TestCanonicalizeOMMs_DeterministicOrderAcrossReorder(t *testing.T) {
	ep := "2026-07-10T00:00:00.000000"
	ab := canonicalizeOMMs([]sgp4.OMM{ommAt(200, ep, "B"), ommAt(100, ep, "A")})
	ba := canonicalizeOMMs([]sgp4.OMM{ommAt(100, ep, "A"), ommAt(200, ep, "B")})
	if len(ab) != 2 || len(ba) != 2 {
		t.Fatalf("want 2 states each, got %d and %d", len(ab), len(ba))
	}
	if ab[0].NoradCatID != 100 || ab[1].NoradCatID != 200 {
		t.Fatalf("not sorted ascending by NORAD: %d,%d", ab[0].NoradCatID, ab[1].NoradCatID)
	}
	if ab[0].NoradCatID != ba[0].NoradCatID || ab[1].NoradCatID != ba[1].NoradCatID {
		t.Fatalf("order not stable across upstream reorder: %v vs %v",
			[]int{ab[0].NoradCatID, ab[1].NoradCatID}, []int{ba[0].NoradCatID, ba[1].NoradCatID})
	}
}

// TestCanonicalizeOMMs_KeepsLatestEpochPerNorad is the producer half of the duplicate-NORAD fix
// (e.g. a GP_HISTORY feed): a NORAD appearing more than once collapses to its LATEST element set,
// never the first-listed. Mutation target: keeping the first (or dropping the After() comparison)
// surfaces the OLD epoch.
func TestCanonicalizeOMMs_KeepsLatestEpochPerNorad(t *testing.T) {
	got := canonicalizeOMMs([]sgp4.OMM{
		ommAt(25544, "2026-07-01T00:00:00.000000", "OLD"),
		ommAt(25544, "2026-07-20T00:00:00.000000", "NEW"),
		ommAt(100, "2026-07-10T00:00:00.000000", "X"),
	})
	if len(got) != 2 {
		t.Fatalf("duplicate NORAD not collapsed: want 2, got %d", len(got))
	}
	// Sorted: 100 then 25544; the 25544 entry must be the NEWER one.
	if got[1].NoradCatID != 25544 || got[1].ObjectName != "NEW" {
		t.Fatalf("did not keep latest EPOCH for NORAD 25544: %+v", got[1])
	}
}

// TestCanonicalizeOMMs_PrefersParseableEpoch: a NORAD with one unparseable and one parseable epoch
// keeps the parseable one (so a garbage duplicate can't shadow a good element set).
func TestCanonicalizeOMMs_PrefersParseableEpoch(t *testing.T) {
	got := canonicalizeOMMs([]sgp4.OMM{
		ommAt(300, "not-a-timestamp", "BAD"),
		ommAt(300, "2026-07-05T00:00:00.000000", "GOOD"),
	})
	if len(got) != 1 || got[0].ObjectName != "GOOD" {
		t.Fatalf("did not prefer the parseable epoch: %+v", got)
	}
}

func schemeForSelection(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(sch); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return sch
}

// TestPushEphemerisUpdateIfNeeded_NilNoradMultiSat_FailsClosed is the consumer half: with no
// ephemerisNoradID and MORE THAN ONE tracked satellite, the push must fail CLOSED with
// EphemerisSelectionAmbiguous — never silently push whichever satellite is first. Mutation target:
// removing the guard lets the push proceed (pushed=true / RuntimeCalls=1).
func TestPushEphemerisUpdateIfNeeded_NilNoradMultiSat_FailsClosed(t *testing.T) {
	future := time.Now().Add(time.Hour).UnixMilli()
	eph := ephWithPropagatedState(future) // one state: NORAD 25544, input hash stamped
	eph.Status.PropagatedStates = append(eph.Status.PropagatedStates, ntnv1alpha1.PropagatedState{
		Satellite: "SAT-B", NoradID: 40000, EpochUnixMs: future,
		SourceEpochUnixMs: time.Now().Add(-time.Hour).UnixMilli(),
		ECEF:              ntnv1alpha1.EphemerisECEF{PosX: 6000000, PosY: 5000000, PosZ: 4000000},
	})
	c := fake.NewClientBuilder().WithScheme(schemeForSelection(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}

	cc := ccWithRemoteControl()
	cc.Spec.EphemerisNoradID = nil // implicit selection against two satellites → ambiguous

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if pushed {
		t.Fatal("expected fail-closed (pushed=false) for ambiguous implicit selection")
	}
	if err == nil {
		t.Fatal("expected an error for ambiguous implicit selection")
	}
	if reason := ephemerisPushConditionReason(err); reason != ephemerisReasonSelectionAmbiguous {
		t.Fatalf("reason = %q, want %q", reason, ephemerisReasonSelectionAmbiguous)
	}
	if mock.RuntimeCalls != 0 {
		t.Fatalf("ambiguous selection must not push; RuntimeCalls=%d", mock.RuntimeCalls)
	}
}

// TestPushEphemerisUpdateIfNeeded_NilNoradSingleSat_Allowed proves the guard does NOT regress the
// common single-satellite case: with one tracked satellite, no ephemerisNoradID still pushes it.
func TestPushEphemerisUpdateIfNeeded_NilNoradSingleSat_Allowed(t *testing.T) {
	future := time.Now().Add(time.Hour).UnixMilli()
	eph := ephWithPropagatedState(future) // single state: NORAD 25544
	c := fake.NewClientBuilder().WithScheme(schemeForSelection(t)).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	mock := &provider.MockProvider{}

	cc := ccWithRemoteControl()
	cc.Spec.EphemerisNoradID = nil // unambiguous: only one satellite

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("single-satellite implicit selection must be allowed, got err: %v", err)
	}
	if !pushed || mock.RuntimeCalls != 1 {
		t.Fatalf("single-satellite push should proceed: pushed=%v RuntimeCalls=%d", pushed, mock.RuntimeCalls)
	}
}
