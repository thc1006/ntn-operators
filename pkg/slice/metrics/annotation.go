/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics

import (
	"context"
	"errors"
	"math"
	"strconv"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
)

// Annotation keys that annotationReader understands. Kept as unexported
// constants so callers use the reader rather than the raw strings.
const (
	annotationRSRP       = "ntn.operators.dev/simulated-rsrp"
	annotationLatency    = "ntn.operators.dev/simulated-latency"
	annotationPacketLoss = "ntn.operators.dev/simulated-packet-loss"
)

// Default healthy-path metrics returned when annotations are absent.
// Kept in sync with the previous inline defaults in NTNSliceReconciler.
var defaultMetrics = slice.Metrics{
	RSRP:              -80,
	LatencyMs:         20,
	PacketLossPercent: 0.1,
}

// annotationReader reads simulated metric values placed on the NTNSlice
// itself. It is intended for development and tests; production deployments
// should use prometheusReader.
type annotationReader struct{}

// NewAnnotationReader returns a Reader that sources metrics from annotations
// of the form ntn.operators.dev/simulated-*. It never returns Stale=true.
func NewAnnotationReader() Reader {
	return annotationReader{}
}

func (annotationReader) Read(_ context.Context, ns *ntnv1alpha1.NTNSlice) (Result, error) {
	if ns == nil {
		return Result{}, errors.New("annotationReader: nil NTNSlice")
	}
	m := defaultMetrics
	if ns.Annotations != nil {
		if v, ok := parseFinite(ns.Annotations[annotationRSRP]); ok {
			m.RSRP = v
		}
		if v, ok := parseFinite(ns.Annotations[annotationLatency]); ok {
			m.LatencyMs = v
		}
		if v, ok := parseFinite(ns.Annotations[annotationPacketLoss]); ok {
			m.PacketLossPercent = v
		}
	}
	return Result{Metrics: m}, nil
}

// parseFinite returns v, true only when s parses as a finite float64.
// Empty strings, non-numeric input, NaN, and ±Inf are all rejected.
func parseFinite(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
