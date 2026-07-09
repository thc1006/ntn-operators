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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// countEventsContaining non-blockingly drains a FakeRecorder and counts the queued
// events whose text contains substr. Counting (not first-match) lets a test assert
// EXACTLY one emission, catching a duplicate as well as a missing event.
func countEventsContaining(rec *events.FakeRecorder, substr string) int {
	n := 0
	for {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, substr) {
				n++
			}
		default:
			return n
		}
	}
}

// TestReportOrbitRegime verifies the UnsupportedOrbitRegime condition and the
// transition-gated event NOTE it returns (findings.md B-5). reportOrbitRegime no
// longer emits directly — it returns the note for the caller to emit only after a
// successful Status().Update (deep-review C-#4) — so the note must be non-empty
// exactly once per entry into the rejected state.
func TestReportOrbitRegime(t *testing.T) {
	r := &SatelliteEphemerisReconciler{} // no Recorder: emission is the caller's job
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	eph.Generation = 3
	ctx := context.Background()
	summary := "2 element set(s) rejected: orbital period >= 225 min requires the deep-space (SDP4) model"

	// 1. First rejection → condition True + returns the note to emit.
	note := r.reportOrbitRegime(ctx, eph, 2, summary)
	cond := meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "DeepSpaceElementsRejected" {
		t.Fatalf("first rejection: want True/DeepSpaceElementsRejected, got %+v", cond)
	}
	if cond.Message != summary {
		t.Errorf("condition message = %q, want the guard summary", cond.Message)
	}
	if cond.ObservedGeneration != 3 {
		t.Errorf("observedGeneration = %d, want 3", cond.ObservedGeneration)
	}
	if note != summary {
		t.Fatalf("first transition must return the event note, got %q", note)
	}

	// 2. Still rejected next reconcile → condition stays True, returns "" (no repeat).
	if note := r.reportOrbitRegime(ctx, eph, 2, summary); note != "" {
		t.Errorf("steady rejection must return no note (spam guard), got %q", note)
	}
	if !meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime) {
		t.Error("condition should remain True across repeated rejections")
	}

	// 3. Recovery: source now all near-earth → condition False, returns "".
	if note := r.reportOrbitRegime(ctx, eph, 0, ""); note != "" {
		t.Errorf("recovery must return no note, got %q", note)
	}
	cond = meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "AllNearEarth" {
		t.Fatalf("recovery: want False/AllNearEarth, got %+v", cond)
	}

	// 4. Re-entry after recovery → True again + returns the note again (genuine transition).
	if note := r.reportOrbitRegime(ctx, eph, 1, summary); note != summary {
		t.Errorf("re-entry must return the note again, got %q", note)
	}
}

// TestReconcile_OrbitRegimeEvent_DeferredToPersist proves the Warning fires ONLY
// after the status persists (deep-review C-#4): a failed Status().Update must not
// fire the event (it would re-fire next reconcile and defeat the transition gate);
// a successful update fires it once. This also covers the terminating-object case
// (its status update fails NotFound), so no spurious event on a dying object.
func TestReconcile_OrbitRegimeEvent_DeferredToPersist(t *testing.T) {
	sch := makeScheme(t)
	key := types.NamespacedName{Name: "eph-orbit", Namespace: "default"}

	newEph := func() *ntnv1alpha1.SatelliteEphemeris {
		return &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type:            "CelesTrak",
					URL:             "https://celestrak.org/test",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
			},
		}
	}
	// A feed with one deep-space bird (GEO) so the guard drives a first transition.
	deepFeed := func() *mockGPFetcher {
		return &mockGPFetcher{result: ephemeris.GPFetchResult{
			OMMs:           []sgp4.OMM{testISSOMM(), testGEOOMM()},
			SatelliteCount: 2,
			FetchedAt:      time.Now(),
		}}
	}

	t.Run("status update fails → no event", func(t *testing.T) {
		eph := newEph()
		cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
					return apierrors.NewInternalError(errors.New("simulated status write failure"))
				},
			}).Build()
		rec := events.NewFakeRecorder(10)
		r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: rec, Fetcher: deepFeed()}

		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err == nil {
			t.Fatal("expected the simulated status-update failure to propagate")
		}
		if n := countEventsContaining(rec, "UnsupportedOrbitRegime"); n != 0 {
			t.Errorf("no orbit-regime event may fire when the status did not persist; got %d", n)
		}
	})

	t.Run("status update succeeds → one event", func(t *testing.T) {
		eph := newEph()
		cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
		rec := events.NewFakeRecorder(10)
		r := &SatelliteEphemerisReconciler{Client: cli, Scheme: sch, Recorder: rec, Fetcher: deepFeed()}

		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if n := countEventsContaining(rec, "UnsupportedOrbitRegime"); n != 1 {
			t.Errorf("a persisted transition into the rejected state must emit exactly one Warning event, got %d", n)
		}
	})
}
