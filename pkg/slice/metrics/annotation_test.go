/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics_test

import (
	"context"
	"math"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
	"github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

func nsWithAnnotations(a map[string]string) *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default", Annotations: a},
	}
}

func TestAnnotationReader_NoAnnotations_ReturnsDefaults(t *testing.T) {
	r := metrics.NewAnnotationReader()
	got, err := r.Read(context.Background(), nsWithAnnotations(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := slice.Metrics{RSRP: -80, LatencyMs: 20, PacketLossPercent: 0.1}
	if got.Metrics != want {
		t.Errorf("defaults mismatch: got %+v want %+v", got.Metrics, want)
	}
	if got.Stale {
		t.Errorf("annotationReader must never report stale=true")
	}
}

func TestAnnotationReader_AllAnnotations_AreParsed(t *testing.T) {
	ns := nsWithAnnotations(map[string]string{
		"ntn.operators.dev/simulated-rsrp":        "-130",
		"ntn.operators.dev/simulated-latency":     "40",
		"ntn.operators.dev/simulated-packet-loss": "2.5",
	})
	got, err := metrics.NewAnnotationReader().Read(context.Background(), ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := slice.Metrics{RSRP: -130, LatencyMs: 40, PacketLossPercent: 2.5}
	if got.Metrics != want {
		t.Errorf("got %+v want %+v", got.Metrics, want)
	}
}

func TestAnnotationReader_PartialAnnotations_DefaultsForUnset(t *testing.T) {
	ns := nsWithAnnotations(map[string]string{
		"ntn.operators.dev/simulated-rsrp": "-140",
	})
	got, _ := metrics.NewAnnotationReader().Read(context.Background(), ns)
	if got.Metrics.RSRP != -140 {
		t.Errorf("rsrp override failed: got %v want -140", got.Metrics.RSRP)
	}
	if got.Metrics.LatencyMs != 20 || got.Metrics.PacketLossPercent != 0.1 {
		t.Errorf("unset annotations must use defaults, got %+v", got.Metrics)
	}
}

func TestAnnotationReader_InvalidValues_AreIgnored(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
	}{
		{"not a number", map[string]string{"ntn.operators.dev/simulated-rsrp": "not-a-number"}},
		{"empty string", map[string]string{"ntn.operators.dev/simulated-latency": ""}},
		{"NaN", map[string]string{"ntn.operators.dev/simulated-packet-loss": "NaN"}},
		{"Infinity", map[string]string{"ntn.operators.dev/simulated-rsrp": "+Inf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := metrics.NewAnnotationReader().Read(context.Background(), nsWithAnnotations(tc.values))
			if err != nil {
				t.Fatalf("invalid values must be silently ignored, got error %v", err)
			}
			if math.IsNaN(got.Metrics.RSRP) || math.IsInf(got.Metrics.RSRP, 0) {
				t.Errorf("reader leaked NaN/Inf into RSRP: %v", got.Metrics.RSRP)
			}
			if math.IsNaN(got.Metrics.LatencyMs) || math.IsInf(got.Metrics.LatencyMs, 0) {
				t.Errorf("reader leaked NaN/Inf into LatencyMs: %v", got.Metrics.LatencyMs)
			}
			if math.IsNaN(got.Metrics.PacketLossPercent) || math.IsInf(got.Metrics.PacketLossPercent, 0) {
				t.Errorf("reader leaked NaN/Inf into PacketLossPercent: %v", got.Metrics.PacketLossPercent)
			}
		})
	}
}

func TestAnnotationReader_NilNTNSlice_IsRejected(t *testing.T) {
	_, err := metrics.NewAnnotationReader().Read(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil NTNSlice, got nil")
	}
}
