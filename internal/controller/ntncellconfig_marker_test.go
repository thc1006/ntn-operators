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
	"errors"
	"fmt"
	"testing"
	"time"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEphemerisPushMarkerUsesGenerationAndLastUpdated(t *testing.T) {
	ts := metav1.NewTime(time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC))
	ephA := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "eph-a",
			Generation:      7,
			ResourceVersion: "101",
		},
		Status: ntnv1alpha1.SatelliteEphemerisStatus{
			LastUpdated: &ts,
		},
	}
	ephB := ephA.DeepCopy()
	ephB.ResourceVersion = "202"

	if gotA, gotB := ephemerisPushMarker(ephA), ephemerisPushMarker(ephB); gotA != gotB {
		t.Fatalf("marker should ignore resourceVersion-only changes: %q != %q", gotA, gotB)
	}

	ephC := ephA.DeepCopy()
	ephC.Generation = ephA.Generation + 1
	if gotA, gotC := ephemerisPushMarker(ephA), ephemerisPushMarker(ephC); gotA == gotC {
		t.Fatalf("marker should change when generation changes: %q == %q", gotA, gotC)
	}

	ephD := ephA.DeepCopy()
	later := metav1.NewTime(ts.Add(5 * time.Minute))
	ephD.Status.LastUpdated = &later
	if gotA, gotD := ephemerisPushMarker(ephA), ephemerisPushMarker(ephD); gotA == gotD {
		t.Fatalf("marker should change when lastUpdated changes: %q == %q", gotA, gotD)
	}
}

func TestEphemerisPushConditionReason(t *testing.T) {
	t.Run("typed reason is preserved through wrapping", func(t *testing.T) {
		typedErr := newEphemerisPushError(ephemerisReasonProviderPushFailed, errors.New("push failed"))
		wrapped := fmt.Errorf("reconcile step failed: %w", typedErr)
		if got := ephemerisPushConditionReason(wrapped); got != ephemerisReasonProviderPushFailed {
			t.Fatalf("unexpected reason: got %q want %q", got, ephemerisReasonProviderPushFailed)
		}
	})

	t.Run("fallback reason for untyped errors", func(t *testing.T) {
		if got := ephemerisPushConditionReason(errors.New("plain error")); got != ephemerisReasonPushFailed {
			t.Fatalf("unexpected fallback reason: got %q want %q", got, ephemerisReasonPushFailed)
		}
	})
}

func TestConditionEpisodeChanged(t *testing.T) {
	t.Run("nil previous condition is a new episode", func(t *testing.T) {
		if !conditionEpisodeChanged(nil, metav1.ConditionFalse, ephemerisReasonRefNotFound, 3) {
			t.Fatalf("expected a nil previous condition to be a new episode")
		}
	})

	t.Run("identical status/reason/generation is the same episode", func(t *testing.T) {
		prev := &metav1.Condition{
			Status: metav1.ConditionFalse, Reason: ephemerisReasonRefNotFound, Message: "missing", ObservedGeneration: 3,
		}
		if conditionEpisodeChanged(prev, metav1.ConditionFalse, ephemerisReasonRefNotFound, 3) {
			t.Fatalf("expected identical status/reason/generation to be the same episode")
		}
	})

	t.Run("message-only change is the SAME episode", func(t *testing.T) {
		// Same status/reason/generation, only the varying error message differs — must
		// NOT start a new episode, or a flapping message would re-emit every reconcile.
		prev := &metav1.Condition{
			Status: metav1.ConditionFalse, Reason: ephemerisReasonRefNotFound,
			Message: "dial timeout after 30.001s", ObservedGeneration: 3,
		}
		if conditionEpisodeChanged(prev, metav1.ConditionFalse, ephemerisReasonRefNotFound, 3) {
			t.Fatalf("a message-only change must not be a new episode")
		}
	})

	t.Run("status change is a new episode", func(t *testing.T) {
		prev := &metav1.Condition{Status: metav1.ConditionTrue, Reason: ephemerisReasonRefNotFound, ObservedGeneration: 3}
		if !conditionEpisodeChanged(prev, metav1.ConditionFalse, ephemerisReasonRefNotFound, 3) {
			t.Fatalf("expected a status change to be a new episode")
		}
	})

	t.Run("reason change is a new episode", func(t *testing.T) {
		prev := &metav1.Condition{Status: metav1.ConditionFalse, Reason: ephemerisReasonRefNotFound, ObservedGeneration: 3}
		if !conditionEpisodeChanged(prev, metav1.ConditionFalse, ephemerisReasonPayloadMissing, 3) {
			t.Fatalf("expected a reason change to be a new episode")
		}
	})

	t.Run("generation bump is a new episode", func(t *testing.T) {
		prev := &metav1.Condition{Status: metav1.ConditionFalse, Reason: ephemerisReasonRefNotFound, ObservedGeneration: 3}
		if !conditionEpisodeChanged(prev, metav1.ConditionFalse, ephemerisReasonRefNotFound, 4) {
			t.Fatalf("expected a generation bump to be a new episode")
		}
	})
}

// TestEphemerisPushShouldRequeue lives in runtime_push_test.go; it covers the
// full no-requeue set (RefNotFound, PayloadMissing, EphemerisStale,
// ProviderPushRejected) plus the transient reasons that must requeue.
