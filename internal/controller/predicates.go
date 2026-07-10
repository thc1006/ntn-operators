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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// reconcileTriggerPredicate is the primary-resource (For) watch filter used by
// all four controllers. It exists to break the self-triggering hot loops in
// findings.md B-1/I-6/I-7: several controllers write a time-derived value
// (a push epoch, a switchback countdown, a LastHealthCheck timestamp) into
// status on every reconcile, and an unfiltered For() watch re-enqueues the
// controller on its own status write, spinning at reconcile latency instead of
// the intended RequeueAfter cadence.
//
// A bare GenerationChangedPredicate would fix the loop but INTRODUCE a
// regression: setting metadata.deletionTimestamp does not bump
// metadata.generation, so the deletion of a finalizer-bearing object arrives as
// an Update with unchanged generation and would be filtered — leaving objects
// that have settled on a no-requeue terminal path (e.g. an NTNCellConfig with a
// dangling spec.ephemerisRef) stuck in Terminating with the finalizer never
// running.
//
// So we pass an Update when EITHER the spec changed (generation bump) OR the
// deletionTimestamp transitioned (zero <-> set). Status-only and annotation-only
// self-writes bump neither and are still filtered. Create/Delete/Generic pass
// (predicate.Funcs returns true for nil funcs; predicate.Or short-circuits).
func reconcileTriggerPredicate() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				if e.ObjectOld == nil || e.ObjectNew == nil {
					return false
				}
				// Deliver the finalizer trigger: deletionTimestamp went
				// zero -> set (or, defensively, set -> zero).
				return e.ObjectOld.GetDeletionTimestamp().IsZero() !=
					e.ObjectNew.GetDeletionTimestamp().IsZero()
			},
		},
	)
}
