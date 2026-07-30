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
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

// TestLoadFlapState_GenerationChange_ResetsStreakKeepsDwell pins the spec-generation finding
// (#203-M2): confirmationSamples promises N CONSECUTIVE samples of the CURRENT policy, so a streak
// accumulated under a prior spec generation (an old trigger/threshold/N, or a changed metrics source
// or hysteresis) must NOT carry across an edit. loadFlapState resets the confirmation/recovery/
// observation clocks when the generation differs, keeping only the durable min-dwell clock (a real
// past hand-back, not an evaluation streak). Mutation: drop the `entry.generation != generation`
// branch in loadFlapState → the streak survives the edit and the ConsecutiveDegraded assertion fails.
func TestLoadFlapState_GenerationChange_ResetsStreakKeepsDwell(t *testing.T) {
	r := &NTNSliceReconciler{}
	key := types.NamespacedName{Name: "s", Namespace: "default"}
	const uid = types.UID("slice-uid-1")
	dwell := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	obs := dwell.Add(-30 * time.Second)

	// A streak accumulated under generation 1: 2/3 confirmations, a recovery/observation cursor, and a
	// real past hand-back (the durable dwell clock).
	r.storeFlapState(key, uid, 1, slice.AntiFlapState{
		ConsecutiveDegraded:   2,
		RecoveryObservedAt:    obs,
		LastCountedObservedAt: obs,
		LastSwitchback:        dwell,
	})

	// Same generation → the streak is intact (a steady-state reconcile must not reset).
	if st := r.loadFlapState(key, uid, 1); st.ConsecutiveDegraded != 2 {
		t.Fatalf("same generation must preserve the streak, got ConsecutiveDegraded=%d", st.ConsecutiveDegraded)
	}

	// Generation 2 (a spec edit) → the confirmation/recovery/observation clocks reset, dwell preserved.
	st := r.loadFlapState(key, uid, 2)
	if st.ConsecutiveDegraded != 0 {
		t.Errorf("generation change must reset ConsecutiveDegraded, got %d", st.ConsecutiveDegraded)
	}
	if !st.RecoveryObservedAt.IsZero() {
		t.Errorf("generation change must reset RecoveryObservedAt, got %v", st.RecoveryObservedAt)
	}
	if !st.LastCountedObservedAt.IsZero() {
		t.Errorf("generation change must reset the observation cursor, got %v", st.LastCountedObservedAt)
	}
	if !st.LastSwitchback.Equal(dwell) {
		t.Errorf("generation change must KEEP the durable min-dwell clock, got %v want %v", st.LastSwitchback, dwell)
	}

	// The reset was written back (loadFlapState persists it), so the entry now records generation 2: a
	// fresh streak accumulates from 0 without a spurious re-reset clobbering it.
	r.storeFlapState(key, uid, 2, slice.AntiFlapState{ConsecutiveDegraded: 1, LastSwitchback: dwell})
	if got := r.loadFlapState(key, uid, 2); got.ConsecutiveDegraded != 1 || !got.LastSwitchback.Equal(dwell) {
		t.Errorf("post-edit generation must accumulate a fresh streak, got ConsecutiveDegraded=%d dwell=%v",
			got.ConsecutiveDegraded, got.LastSwitchback)
	}
}

// TestReconcile_StatusConflict_DoesNotAdvanceConfirmation pins the status-conflict gate: the
// confirmation counter lives in shared memory and is committed (storeFlapState) only AFTER the status
// write persists, so a conflicting/retried reconcile must not advance it — otherwise a metrics-source
// blip during a status conflict could be counted more than once and reach the failover threshold on a
// retry. Here a degraded sample would drive the seeded 1/2 streak to a failover, but the status write
// is forced to CONFLICT; the in-memory streak must therefore remain exactly 1 (nothing committed).
// Mutation: move storeFlapState before persistStatusIfChanged (the pre-fix order) → the failed write
// still leaves an advanced/reset counter in memory and the "== 1" assertion fails.
func TestReconcile_StatusConflict_DoesNotAdvanceConfirmation(t *testing.T) {
	fixedNow := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: "default"},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			NextPassWindows: []ntnv1alpha1.PassWindow{{
				Satellite: "ONEWEB-0012", GroundStation: "gs",
				AOS: metav1.Time{Time: fixedNow.Add(-1 * time.Hour)},
				LOS: metav1.Time{Time: fixedNow.Add(1 * time.Hour)},
			}},
		},
	}
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue, Reason: "Predicted",
	})
	confirm := int32(2)
	nsObj := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default", UID: "slice-uid-1", Generation: 1},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant:          "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{Provider: "chunghwa-telecom", APN: "internet", Priority: "primary"},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "oneweb-constellation",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:            []string{"rsrp < -120"},
				SwitchbackDelay:     metav1.Duration{Duration: 60 * time.Second},
				ConfirmationSamples: &confirm,
			},
		},
		Status: ntnv1alpha1.NTNSliceStatus{ActivePathType: string(slice.PathTerrestrial)},
	}

	cli := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(nsObj, eph).WithStatusSubresource(nsObj, eph).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*ntnv1alpha1.NTNSlice); ok { // the failover's status write always conflicts
					return apierrors.NewConflict(
						schema.GroupResource{Group: "ntn.operators.dev", Resource: "ntnslices"},
						obj.GetName(), fmt.Errorf("forced conflict"))
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()

	r := &NTNSliceReconciler{Client: cli, Scheme: sch, Now: func() time.Time { return fixedNow }}
	key := client.ObjectKeyFromObject(nsObj)
	// Seed 1/2 confirmations under the object's identity (uid+generation), with the observation cursor
	// clear so the fresh degraded sample below advances the streak.
	r.storeFlapState(key, nsObj.UID, nsObj.Generation, slice.AntiFlapState{ConsecutiveDegraded: 1})
	// A reliable, degraded observation (rsrp -125 < -120) with a fresh source timestamp: the 2nd
	// confirmation drives a failover, whose status write the interceptor conflicts.
	r.ReaderProvider = fakeReaderProvider{reader: fakeReader{res: slicemetrics.Result{
		Metrics:     slice.Metrics{RSRP: -125, LatencyMs: 10, PacketLossPercent: 0},
		Stale:       false,
		LastFreshAt: fixedNow,
		ObservedAt:  fixedNow,
	}}}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err == nil {
		t.Fatal("expected the forced status conflict to surface as a reconcile error")
	}

	// The conflict aborted before storeFlapState, so the confirmation counter is unchanged: a retry
	// re-evaluates from 1/2, it is NOT double-counted to 2/2 (or reset) by the dropped write.
	if st := r.loadFlapState(key, nsObj.UID, nsObj.Generation); st.ConsecutiveDegraded != 1 {
		t.Fatalf("a conflicted status write must not advance the in-memory confirmation counter, "+
			"got ConsecutiveDegraded=%d, want 1", st.ConsecutiveDegraded)
	}
}
