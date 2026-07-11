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

// TestReconcile_InertTriggers_EpisodePerGeneration guards that the InertTriggers
// Warning follows the generation-aware episode policy the other failure events use,
// not a bare into-False transition. A spec that swaps one inert trigger for another
// bumps the generation while the condition stays False/InertTriggers; the operator
// must re-warn once for that new episode. The sequence:
//
//	gen1 inert (latency)     -> 1 event, False/InertTriggers, ObservedGeneration=1
//	same gen, reconcile      -> 0 events (steady state)
//	gen2 inert (packetLoss)  -> 1 new event, ObservedGeneration=2
func TestReconcile_InertTriggers_EpisodePerGeneration(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)

	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: "default"},
	}
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default", Generation: 1},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "chunghwa-telecom", APN: "internet", Priority: "primary"},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "oneweb-constellation",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:        []string{"latency > 200"}, // inert: latency has no source below
				SwitchbackDelay: metav1.Duration{Duration: 60 * time.Second},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(nsObj, eph).
		WithStatusSubresource(nsObj, eph).Build()

	// FRESH metrics (so the engine runs), but BOTH latency and packetLoss are missing
	// (no configured source) — so whichever of the two the spec references is inert.
	fr := fakeReader{res: slicemetrics.Result{
		Metrics: slice.Metrics{
			RSRP: -80, LatencyMs: 20, PacketLossPercent: 0.1,
			LatencyMissing: true, PacketLossMissing: true,
		},
		Stale:       false,
		LastFreshAt: fixedNow,
	}}
	rec := events.NewFakeRecorder(20)
	r := &NTNSliceReconciler{
		Client: cli, Scheme: sch, Recorder: rec,
		Now:            func() time.Time { return fixedNow },
		ReaderProvider: fakeReaderProvider{reader: fr},
	}
	key := client.ObjectKeyFromObject(nsObj)

	reconcileAndCountInert := func() int {
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		n := 0
		for drained := false; !drained; {
			select {
			case ev := <-rec.Events:
				if strings.Contains(ev, "InertTriggers") {
					n++
				}
			default:
				drained = true
			}
		}
		return n
	}
	condOG := func() (metav1.ConditionStatus, string, int64) {
		u := &ntnv1alpha1.NTNSlice{}
		if err := cli.Get(context.Background(), key, u); err != nil {
			t.Fatal(err)
		}
		c := meta.FindStatusCondition(u.Status.Conditions, ntnv1alpha1.ConditionTriggersReady)
		if c == nil {
			t.Fatal("TriggersReady condition missing")
		}
		return c.Status, c.Reason, c.ObservedGeneration
	}
	swapTrigger := func(expr string, gen int64) {
		u := &ntnv1alpha1.NTNSlice{}
		if err := cli.Get(context.Background(), key, u); err != nil {
			t.Fatal(err)
		}
		u.Spec.FailoverPolicy.Triggers = []string{expr}
		u.Generation = gen
		if err := cli.Update(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}

	// 1) gen1 inert latency -> one event, False/InertTriggers, OG=1.
	if n := reconcileAndCountInert(); n != 1 {
		t.Fatalf("gen1 inert: want 1 InertTriggers event, got %d", n)
	}
	if s, reason, og := condOG(); s != metav1.ConditionFalse || reason != "InertTriggers" || og != 1 {
		t.Fatalf("gen1 condition: want False/InertTriggers/OG=1, got %s/%s/%d", s, reason, og)
	}
	// 2) same generation, same inert set -> steady state, no new event.
	if n := reconcileAndCountInert(); n != 0 {
		t.Fatalf("steady state: want 0 events, got %d", n)
	}
	// 3) gen2 swaps latency for packetLoss (still inert, still False/InertTriggers,
	// but a new generation) -> exactly one new event.
	swapTrigger("packetLoss > 5", 2)
	if n := reconcileAndCountInert(); n != 1 {
		t.Fatalf("gen2 new inert episode: want 1 event, got %d", n)
	}
	if s, reason, og := condOG(); s != metav1.ConditionFalse || reason != "InertTriggers" || og != 2 {
		t.Fatalf("gen2 condition: want False/InertTriggers/OG=2, got %s/%s/%d", s, reason, og)
	}
}
