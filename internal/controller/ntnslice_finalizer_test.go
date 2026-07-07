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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

func sliceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

// sliceWithRef builds an NTNSlice with a stable UID (= name) so refcounting can
// distinguish "self" from siblings.
func sliceWithRef(name, ref string, finalizer bool) *ntnv1alpha1.NTNSlice {
	s := &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name)},
		Spec: ntnv1alpha1.NTNSliceSpec{
			SatellitePath: ntnv1alpha1.SatellitePathSpec{EphemerisRef: ref},
		},
	}
	if finalizer {
		s.Finalizers = []string{sliceMetricsFinalizer}
	}
	return s
}

// deleteAndGet deletes s (the fake client sets DeletionTimestamp because s has a
// finalizer) and returns the re-fetched terminating object.
func deleteAndGet(t *testing.T, c client.Client, s *ntnv1alpha1.NTNSlice) *ntnv1alpha1.NTNSlice {
	t.Helper()
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got := &ntnv1alpha1.NTNSlice{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), got); err != nil {
		t.Fatalf("get after delete (should still exist via finalizer): %v", err)
	}
	return got
}

// A live (non-deleting) slice must get the metrics-cleanup finalizer added, and
// the finalizer handler must CONTINUE the same reconcile (done=false, no requeue)
// so the first reconcile still computes status.
func TestSliceFinalizer_AddedOnLiveSlice(t *testing.T) {
	scheme := sliceScheme(t)
	s := sliceWithRef("slice-add", "eph-add", false)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s).Build()
	r := &NTNSliceReconciler{Client: c}

	done, err := r.handleSliceFinalizer(context.Background(), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected done=false (continue reconcile) after adding the finalizer")
	}
	got := &ntnv1alpha1.NTNSlice{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, sliceMetricsFinalizer) {
		t.Error("finalizer was not added to a live slice")
	}
}

// Deleting the LAST slice referencing an ephemeris releases both its per-slice
// FailoverTotal series and the shared SatellitePassAvailable series.
func TestSliceFinalizer_DeletionReleasesUnsharedSeries(t *testing.T) {
	scheme := sliceScheme(t)
	const ref, name = "eph-solo", "slice-solo"
	ntnmetrics.SatellitePassAvailable.With(prometheus.Labels{"ephemeris": ref}).Set(1)
	ntnmetrics.FailoverTotal.With(prometheus.Labels{"namespace": "ns", "slice": name, "from_path": "a", "to_path": "b"}).Inc()
	gBefore := testutil.CollectAndCount(ntnmetrics.SatellitePassAvailable)
	fBefore := testutil.CollectAndCount(ntnmetrics.FailoverTotal)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sliceWithRef(name, ref, true)).Build()
	got := deleteAndGet(t, c, sliceWithRef(name, ref, true))
	r := &NTNSliceReconciler{Client: c}
	if done, err := r.handleSliceFinalizer(context.Background(), got); err != nil || !done {
		t.Fatalf("finalize: done=%v err=%v", done, err)
	}

	if a := testutil.CollectAndCount(ntnmetrics.SatellitePassAvailable); a != gBefore-1 {
		t.Errorf("SatellitePassAvailable not released: before=%d after=%d", gBefore, a)
	}
	if a := testutil.CollectAndCount(ntnmetrics.FailoverTotal); a != fBefore-1 {
		t.Errorf("FailoverTotal not released: before=%d after=%d", fBefore, a)
	}
	// Finalizer removed → the fake client actually deletes the object.
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(got), &ntnv1alpha1.NTNSlice{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected object removed after finalizer cleared, got err=%v", err)
	}
}

// Deleting one slice that SHARES an ephemeris with another (live) slice must KEEP
// the shared SatellitePassAvailable series; only the deleted slice's own
// FailoverTotal is released.
func TestSliceFinalizer_DeletionKeepsSharedSeries(t *testing.T) {
	scheme := sliceScheme(t)
	const ref = "eph-shared"
	ntnmetrics.SatellitePassAvailable.With(prometheus.Labels{"ephemeris": ref}).Set(1)
	gBefore := testutil.CollectAndCount(ntnmetrics.SatellitePassAvailable)

	sibling := sliceWithRef("slice-b", ref, true) // live, also references the shared ref
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sliceWithRef("slice-a", ref, true), sibling).Build()
	got := deleteAndGet(t, c, sliceWithRef("slice-a", ref, true))
	r := &NTNSliceReconciler{Client: c}
	if done, err := r.handleSliceFinalizer(context.Background(), got); err != nil || !done {
		t.Fatalf("finalize: done=%v err=%v", done, err)
	}

	if a := testutil.CollectAndCount(ntnmetrics.SatellitePassAvailable); a != gBefore {
		t.Errorf("shared SatellitePassAvailable was wrongly released (sibling still references it): before=%d after=%d", gBefore, a)
	}
}

// ephemerisMetricReferenced ignores the slice being deleted and any terminating
// slice, so two slices sharing a ref that are deleted together do not each defer
// to the other and leak the series.
func TestEphemerisMetricReferenced(t *testing.T) {
	scheme := sliceScheme(t)
	self := sliceWithRef("self", "eph", true)
	live := sliceWithRef("live", "eph", false)
	term := sliceWithRef("term", "eph", true)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(self, live, term).Build()
	// Make "term" terminating.
	if err := c.Delete(context.Background(), term); err != nil {
		t.Fatalf("delete term: %v", err)
	}
	r := &NTNSliceReconciler{Client: c}

	// A live sibling references it.
	ref, err := r.ephemerisMetricReferenced(context.Background(), "eph", self.UID)
	if err != nil || !ref {
		t.Fatalf("expected referenced=true (live sibling), got %v err=%v", ref, err)
	}
	// Remove the live sibling; only self + terminating remain.
	if err := c.Delete(context.Background(), live); err != nil {
		t.Fatalf("delete live: %v", err)
	}
	ref, err = r.ephemerisMetricReferenced(context.Background(), "eph", self.UID)
	if err != nil || ref {
		t.Fatalf("expected referenced=false (only self + terminating), got %v err=%v", ref, err)
	}
}
