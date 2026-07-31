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
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestReconcile_RefreshIntervalClamped_EmitsOncePerEpisode guards the episode gate
// on the clamp Warning: an out-of-bounds refreshInterval reschedules on the ~3-minute
// propagation cadence, so without a gate the Warning would fire ~480 times/day. It
// must warn ONCE per episode (per spec) instead. The clamp is evaluated in Step 2
// before the fetch, so it fires regardless of the fetch outcome; a fetch error here
// just exercises that the RefreshIntervalClamped condition still persists (via
// handleFetchError's status write), which is what makes the second reconcile a no-op.
func TestReconcile_RefreshIntervalClamped_EmitsOncePerEpisode(t *testing.T) {
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default"},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type:            "CelesTrak",
				URL:             "https://celestrak.org/x",
				RefreshInterval: metav1.Duration{Duration: 1 * time.Hour}, // below the 2h minimum
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()

	rec := events.NewFakeRecorder(20)
	r := &SatelliteEphemerisReconciler{
		Client:   cli,
		Scheme:   sch,
		Recorder: rec,
		Fetcher:  &mockGPFetcher{err: errors.New("simulated GP source outage")},
	}

	key := client.ObjectKeyFromObject(eph)
	for range 2 {
		// The fetch error surfaces as a reconcile error; irrelevant to the clamp
		// assertion (which fires in Step 2, before the fetch).
		_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	}

	clampEvents := 0
	for drained := false; !drained; {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, "RefreshIntervalClamped") {
				clampEvents++
			}
		default:
			drained = true
		}
	}
	if clampEvents != 1 {
		t.Fatalf("expected exactly ONE RefreshIntervalClamped event across two reconciles, got %d", clampEvents)
	}

	updated := &ntnv1alpha1.SatelliteEphemeris{}
	if err := cli.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	c := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionRefreshIntervalClamped)
	if c == nil || c.Status != metav1.ConditionTrue || c.Reason != "BelowMinimum" {
		t.Fatalf("RefreshIntervalClamped condition must be True/BelowMinimum, got %+v", c)
	}
}

// TestReconcile_RefreshIntervalClamped_RecoveryContract checks the condition
// contract — True/BelowMinimum when clamped, False/WithinBounds (NEVER absent) when
// valid, so a consumer can tell "evaluated, no clamp" from "not yet reconciled" — and
// the episode transitions across invalid -> valid -> invalid spec edits (one event
// each clamp, none on recovery).
func TestReconcile_RefreshIntervalClamped_RecoveryContract(t *testing.T) {
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default", Generation: 1},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/x",
				RefreshInterval: metav1.Duration{Duration: 1 * time.Hour}, // invalid (< 2h)
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	rec := events.NewFakeRecorder(20)
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: rec, Fetcher: &mockGPFetcher{err: errors.New("outage")}}
	key := client.ObjectKeyFromObject(eph)

	reconcileAndCountClampEvents := func() int {
		_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		n := 0
		for drained := false; !drained; {
			select {
			case ev := <-rec.Events:
				if strings.Contains(ev, "RefreshIntervalClamped") {
					n++
				}
			default:
				drained = true
			}
		}
		return n
	}
	cond := func() *metav1.Condition {
		u := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(context.Background(), key, u); err != nil {
			t.Fatal(err)
		}
		return meta.FindStatusCondition(u.Status.Conditions, ntnv1alpha1.ConditionRefreshIntervalClamped)
	}
	setInterval := func(d time.Duration, gen int64) {
		u := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(context.Background(), key, u); err != nil {
			t.Fatal(err)
		}
		u.Spec.Source.RefreshInterval = metav1.Duration{Duration: d}
		u.Generation = gen
		if err := cli.Update(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}

	// 1) invalid -> True/BelowMinimum + one event.
	if n := reconcileAndCountClampEvents(); n != 1 {
		t.Fatalf("initial clamp: want 1 event, got %d", n)
	}
	if c := cond(); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "BelowMinimum" {
		t.Fatalf("clamped state must be True/BelowMinimum, got %+v", c)
	}
	// 2) valid -> False/WithinBounds (NOT absent) + zero events.
	setInterval(4*time.Hour, 2)
	if n := reconcileAndCountClampEvents(); n != 0 {
		t.Fatalf("recovery: want 0 events, got %d", n)
	}
	if c := cond(); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "WithinBounds" {
		t.Fatalf("valid interval must record False/WithinBounds (never absent), got %+v", c)
	}
	// 3) invalid again -> True + a second event (the recovery made this a real transition).
	setInterval(1*time.Hour, 3)
	if n := reconcileAndCountClampEvents(); n != 1 {
		t.Fatalf("re-clamp: want 1 event, got %d", n)
	}
	if c := cond(); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("re-clamp: want True, got %+v", c)
	}
}

// TestReconcile_RefreshIntervalClamped_NoEventBeforePersist is the #311 regression: the
// clamp Warning is strictly emit-after-persist. A status-update CONFLICT that discards the
// reconcile must NOT emit the Warning (the condition never persisted) — the previous inline
// pre-persist emission fired it here, so a retry produced a duplicate — and the retry then
// emits it exactly once. This closes the last pre-persist exception to the WO-20 invariant.
func TestReconcile_RefreshIntervalClamped_NoEventBeforePersist(t *testing.T) {
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default"},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/x",
				RefreshInterval: metav1.Duration{Duration: 1 * time.Hour}, // below the 2h minimum
			},
		},
	}
	failNextStatusWrite := true
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sr string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if failNextStatusWrite {
					failNextStatusWrite = false // fail the FIRST status write only
					return errors.New("simulated status-update conflict")
				}
				return c.SubResource(sr).Update(ctx, obj, opts...)
			},
		}).Build()

	rec := events.NewFakeRecorder(20)
	r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: rec, Fetcher: &mockGPFetcher{err: errors.New("outage")}}
	key := client.ObjectKeyFromObject(eph)

	countClamp := func() int {
		n := 0
		for drained := false; !drained; {
			select {
			case ev := <-rec.Events:
				if strings.Contains(ev, "RefreshIntervalClamped") {
					n++
				}
			default:
				drained = true
			}
		}
		return n
	}

	// Reconcile 1: the status write conflicts, so the reconcile is discarded. Emit-after-persist
	// means the clamp Warning must NOT fire here (the pre-persist emission this fixes would have).
	_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	if n := countClamp(); n != 0 {
		t.Fatalf("a conflicted (discarded) reconcile must emit NO clamp Warning, got %d — pre-persist emission?", n)
	}
	// Reconcile 2: the write succeeds, so the clamp Warning fires exactly once (no duplicate).
	_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	if n := countClamp(); n != 1 {
		t.Fatalf("the retry after a conflict must emit exactly one clamp Warning, got %d", n)
	}
}
