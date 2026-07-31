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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider/ocudu"
)

// legacyConfigMap is a pre-atomic-reference artifact: the operator's management labels and
// its config key, but NO owner reference — the state an older version left behind when its
// best-effort EnsureOwnership second write did not land (#210's root cause).
func legacyConfigMap(ns, crName string, labeled bool, data string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ocudu.ConfigMapNameFor(crName), Namespace: ns},
	}
	if labeled {
		cm.Labels = map[string]string{
			"app.kubernetes.io/managed-by": "ntn-operators",
			"app.kubernetes.io/component":  "ocudu-ntn-config",
		}
	}
	if data != "" {
		cm.Data = map[string]string{"geo_ntn.yml": data}
	}
	return cm
}

// TestFinalizer_DeletesLegacyUnownedConfigMap walks the deletion-first path that #210 left
// uncovered. Adoption lives in ApplyCellConfig, but the reconciler runs handleFinalizer
// FIRST, so a CR deleted before any successful post-upgrade reconcile never reaches Apply:
//
//	old operator creates the ConfigMap → its EnsureOwnership write fails → no owner ref →
//	upgrade → CR deleted before the first reconcile adopts it → cleanup skipped it →
//	finalizer removed → CR gone, ConfigMap orphaned with nothing left to reclaim it.
//
// The finalizer must now reclaim exactly the object class ApplyCellConfig would have adopted.
func TestFinalizer_DeletesLegacyUnownedConfigMap(t *testing.T) {
	const ns, name = "ntn-system", "legacy-cell"
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	cc := &ntnv1alpha1.NTNCellConfig{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns, UID: types.UID("uid-" + name),
		Finalizers:        []string{"ntn.operators.dev/configmap-cleanup"},
		DeletionTimestamp: &metav1.Time{Time: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)},
	}}
	legacy := legacyConfigMap(ns, name, true, "left by an older operator version")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc, legacy).Build()
	r := &NTNCellConfigReconciler{Client: c, Scheme: scheme}
	prov := ocudu.NewProvider(c)

	done, _, err := r.handleFinalizer(context.Background(), cc, prov)
	if !done {
		t.Fatal("handleFinalizer must take over on a deletion-timestamped CR")
	}
	if err != nil {
		t.Fatalf("finalization failed: %v", err)
	}

	got := &corev1.ConfigMap{}
	getErr := c.Get(context.Background(), types.NamespacedName{Name: ocudu.ConfigMapNameFor(name), Namespace: ns}, got)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("the legacy ConfigMap leaked: it must be reclaimed on the deletion path, got err=%v", getErr)
	}
	if controllerutil.ContainsFinalizer(cc, "ntn.operators.dev/configmap-cleanup") {
		t.Error("the finalizer must be removed once cleanup succeeded")
	}
}

// A ConfigMap the operator cannot prove is its own must SURVIVE the finalizer. Deleting on
// labels alone would let a squatter be destroyed by an unrelated CR's deletion, and an
// unlabeled object at the same name is simply not ours. These are the cases where leaving the
// object behind is the correct outcome, not a leak.
func TestFinalizer_LeavesForeignConfigMapsIntact(t *testing.T) {
	const ns, name = "ntn-system", "legacy-cell"
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	for _, tc := range []struct {
		name string
		cm   *corev1.ConfigMap
		why  string
	}{
		{"unlabeled", legacyConfigMap(ns, name, false, "someone else's data"),
			"an unlabeled object at our name was never ours"},
		{"labeled but no config key", legacyConfigMap(ns, name, true, ""),
			"a labeled-but-empty impostor never held our config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := &ntnv1alpha1.NTNCellConfig{ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns, UID: types.UID("uid-" + name),
				Finalizers:        []string{"ntn.operators.dev/configmap-cleanup"},
				DeletionTimestamp: &metav1.Time{Time: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)},
			}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc, tc.cm.DeepCopy()).Build()
			r := &NTNCellConfigReconciler{Client: c, Scheme: scheme}

			if _, _, err := r.handleFinalizer(context.Background(), cc, ocudu.NewProvider(c)); err != nil {
				t.Fatalf("finalization failed: %v", err)
			}
			key := types.NamespacedName{Name: ocudu.ConfigMapNameFor(name), Namespace: ns}
			if err := c.Get(context.Background(), key, &corev1.ConfigMap{}); err != nil {
				t.Fatalf("%s — it must survive the finalizer, got err=%v", tc.why, err)
			}
		})
	}
}
