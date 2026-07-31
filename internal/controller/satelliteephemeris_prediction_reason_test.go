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
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestPredictionConditionReason pins the tagged-reason extraction: a tagged error yields its
// stable Reason, an untagged (or transient) error falls back to the generic reasonPredictionFailed,
// and the tag survives further wrapping (so a caller that adds context does not lose the reason).
func TestPredictionConditionReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"untagged error → generic fallback", errors.New("boom"), reasonPredictionFailed},
		{"ground station not found", newPredictionError(reasonGroundStationNotFound, errors.New("x")), reasonGroundStationNotFound},
		{"invalid location", newPredictionError(reasonInvalidGroundStationLocation, errors.New("x")), reasonInvalidGroundStationLocation},
		{"invalid config", newPredictionError(reasonInvalidPredictionConfig, errors.New("x")), reasonInvalidPredictionConfig},
		{"computation failed", newPredictionError(reasonPredictionComputationFailed, errors.New("x")), reasonPredictionComputationFailed},
		{"tag survives outer wrapping", fmt.Errorf("outer: %w", newPredictionError(reasonGroundStationNotFound, errors.New("x"))), reasonGroundStationNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := predictionConditionReason(tc.err); got != tc.want {
				t.Fatalf("predictionConditionReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPredictionTagPreservesContextCancel guards the cancellation short-circuit in
// handlePassPrediction: a context error tagged as a prediction failure must still satisfy
// errors.Is (via Unwrap), so a shutdown/timeout is never recorded as a domain PredictionFailed.
func TestPredictionTagPreservesContextCancel(t *testing.T) {
	tagged := newPredictionError(reasonPredictionComputationFailed, fmt.Errorf("computing passes: %w", context.Canceled))
	if !errors.Is(tagged, context.Canceled) {
		t.Fatal("a tagged prediction error wrapping context.Canceled must still match errors.Is(context.Canceled)")
	}
}

// TestPredictionReasonChange_IsNewEpisode is the finding's core: because the episode gate ignores
// the Message, two DIFFERENT root causes at the SAME generation must still be distinct episodes so
// event-driven alerting sees the change — an absent ground station later created with an invalid
// latitude. The same cause at the same generation must NOT re-open an episode (no event flood).
func TestPredictionReasonChange_IsNewEpisode(t *testing.T) {
	const gen = int64(3)
	r1 := predictionConditionReason(newPredictionError(reasonGroundStationNotFound, errors.New("ground station \"gs-1\" not found")))
	cond := &metav1.Condition{
		Type:               ntnv1alpha1.ConditionPassesPredicted,
		Status:             metav1.ConditionFalse,
		Reason:             r1,
		ObservedGeneration: gen,
	}
	r2 := predictionConditionReason(newPredictionError(reasonInvalidGroundStationLocation, errors.New("invalid latitude for \"gs-1\"")))

	if !conditionEpisodeChanged(cond, metav1.ConditionFalse, r2, gen) {
		t.Fatalf("a different root cause (%s → %s) at the same generation must be a NEW episode", r1, r2)
	}
	if conditionEpisodeChanged(cond, metav1.ConditionFalse, r1, gen) {
		t.Fatal("the same reason at the same generation must NOT re-open an episode (event flood)")
	}
}
