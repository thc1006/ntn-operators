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

func TestEphemerisPushConditionChanged(t *testing.T) {
	t.Run("nil previous condition is treated as changed", func(t *testing.T) {
		if !ephemerisPushConditionChanged(nil, ephemerisReasonRefNotFound, "missing", 3) {
			t.Fatalf("expected condition to be marked changed when previous condition is nil")
		}
	})

	t.Run("identical previous condition is not changed", func(t *testing.T) {
		prev := &metav1.Condition{
			Status:             metav1.ConditionFalse,
			Reason:             ephemerisReasonRefNotFound,
			Message:            "missing",
			ObservedGeneration: 3,
		}
		if ephemerisPushConditionChanged(prev, ephemerisReasonRefNotFound, "missing", 3) {
			t.Fatalf("expected unchanged condition to be recognized as unchanged")
		}
	})

	t.Run("generation bump is treated as changed", func(t *testing.T) {
		prev := &metav1.Condition{
			Status:             metav1.ConditionFalse,
			Reason:             ephemerisReasonRefNotFound,
			Message:            "missing",
			ObservedGeneration: 3,
		}
		if !ephemerisPushConditionChanged(prev, ephemerisReasonRefNotFound, "missing", 4) {
			t.Fatalf("expected generation change to be treated as condition change")
		}
	})
}

func TestEphemerisPushShouldRequeue(t *testing.T) {
	if ephemerisPushShouldRequeue(ephemerisReasonRefNotFound) {
		t.Fatalf("expected EphemerisRefNotFound to avoid explicit requeue")
	}
	if !ephemerisPushShouldRequeue(ephemerisReasonProviderPushFailed) {
		t.Fatalf("expected provider push failures to keep explicit retry requeue")
	}
}
