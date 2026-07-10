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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestReconcileTriggerPredicate pins the two guarantees the predicate must hold
// simultaneously: it FILTERS status-only / annotation-only self-writes (so the
// B-1/I-6/I-7 hot loops stay fixed) but PASSES spec changes and deletionTimestamp
// transitions (so finalizer cleanup is never dropped — the regression a bare
// GenerationChangedPredicate would introduce).
func TestReconcileTriggerPredicate(t *testing.T) {
	p := reconcileTriggerPredicate()
	del := metav1.Now()

	base := func() *ntnv1alpha1.NTNCellConfig {
		return &ntnv1alpha1.NTNCellConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "c",
				Namespace:   "ns",
				Generation:  1,
				Annotations: map[string]string{"k": "v1"},
			},
		}
	}

	tests := []struct {
		name string
		mut  func(newObj *ntnv1alpha1.NTNCellConfig)
		want bool
	}{
		{
			name: "status-only self-write is filtered (same gen, no deletion)",
			mut:  func(n *ntnv1alpha1.NTNCellConfig) { n.Status.AppliedKoffset = 42 },
			want: false,
		},
		{
			name: "annotation-only change is filtered",
			mut:  func(n *ntnv1alpha1.NTNCellConfig) { n.Annotations["k"] = "v2" },
			want: false,
		},
		{
			name: "spec change (generation bump) passes",
			mut:  func(n *ntnv1alpha1.NTNCellConfig) { n.Generation = 2 },
			want: true,
		},
		{
			name: "deletionTimestamp set passes (finalizer trigger)",
			mut:  func(n *ntnv1alpha1.NTNCellConfig) { n.DeletionTimestamp = &del },
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldObj := base()
			newObj := base()
			tc.mut(newObj)
			got := p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj})
			if got != tc.want {
				t.Fatalf("Update predicate = %v, want %v", got, tc.want)
			}
		})
	}

	// Create must always pass (initial reconcile).
	if !p.Create(event.CreateEvent{Object: base()}) {
		t.Error("Create event must pass the predicate")
	}
	// Delete must always pass (NotFound cleanup path).
	if !p.Delete(event.DeleteEvent{Object: base()}) {
		t.Error("Delete event must pass the predicate")
	}
}
