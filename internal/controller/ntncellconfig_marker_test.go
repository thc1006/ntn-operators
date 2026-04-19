package controller

import (
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
