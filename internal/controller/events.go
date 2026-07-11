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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// deferredEvent is a Kubernetes Event held until after the reconcile's status
// write persists (WO-20 / N-1), so a rolled-back Status().Update never announces
// (and, on the retry, re-announces) a transition that did not become durable.
type deferredEvent struct {
	eventType string
	reason    string
	message   string
}

// conditionEpisodeChanged reports whether setting a condition to (newStatus,
// newReason, generation) begins a NEW episode relative to old: the condition is
// absent, or its Status, Reason, or ObservedGeneration differs.
//
// It deliberately IGNORES the condition Message. A failure whose message only
// carries a varying detail — "dial timeout after 30.001s" vs "…30.013s" — is the
// same episode and must not re-emit a Warning on every reconcile; the full detail
// belongs in the structured log, not a fresh Event each cycle. (meta.SetStatusCondition
// returns true on a Message-only change, which is why event gates use this instead
// of that return value.)
//
// Callers MUST evaluate this BEFORE meta.SetStatusCondition mutates the slice: old
// is a pointer into ns.Status.Conditions that SetStatusCondition updates in place,
// so reading it afterward would always see the new values.
func conditionEpisodeChanged(old *metav1.Condition, newStatus metav1.ConditionStatus, newReason string, generation int64) bool {
	return old == nil ||
		old.Status != newStatus ||
		old.Reason != newReason ||
		old.ObservedGeneration != generation
}
