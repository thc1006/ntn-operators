/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"testing"
	"time"
)

func TestSimulator_InPass_ReturnsHealthyMetrics(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Simulator{
		Start:        start,
		PassDuration: 10 * time.Second,
		GapDuration:  10 * time.Second,
	}
	// A moment inside the first pass window.
	got := s.ValuesAt(start.Add(5 * time.Second))
	if !got.InPass {
		t.Fatal("expected InPass=true at t=5s (pass is 0-10s)")
	}
	if got.RsrpDbm != -80 || got.LatencyMs != 20 || got.PacketLossPercent > 0.5 {
		t.Errorf("in-pass metrics should look healthy, got %+v", got)
	}
}

func TestSimulator_OutOfPass_ReturnsDegradedMetrics(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Simulator{
		Start:        start,
		PassDuration: 10 * time.Second,
		GapDuration:  10 * time.Second,
	}
	// Inside the first gap: pass is 0-10, gap is 10-20.
	got := s.ValuesAt(start.Add(15 * time.Second))
	if got.InPass {
		t.Fatal("expected InPass=false at t=15s (gap is 10-20s)")
	}
	if got.RsrpDbm > -120 {
		t.Errorf("out-of-pass RSRP should trigger the default <-120 rule, got %v", got.RsrpDbm)
	}
	if got.LatencyMs < 150 {
		t.Errorf("out-of-pass latency should be high, got %v", got.LatencyMs)
	}
	if got.PacketLossPercent < 1 {
		t.Errorf("out-of-pass packet loss should be noticeable, got %v", got.PacketLossPercent)
	}
}

func TestSimulator_BoundaryExactlyAtPassEnd_IsOutOfPass(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Simulator{
		Start:        start,
		PassDuration: 10 * time.Second,
		GapDuration:  10 * time.Second,
	}
	// At t=10s the pass should have just ended (pass is [0, 10)).
	got := s.ValuesAt(start.Add(10 * time.Second))
	if got.InPass {
		t.Error("t=passDuration must be the first moment of the gap, not still in pass")
	}
}

func TestSimulator_CyclesWrapAroundDeterministically(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Simulator{
		Start:        start,
		PassDuration: 10 * time.Second,
		GapDuration:  10 * time.Second,
	}
	// Second cycle: pass window is 20-30s.
	a := s.ValuesAt(start.Add(5 * time.Second))
	b := s.ValuesAt(start.Add(25 * time.Second)) // same offset in second pass
	if a != b {
		t.Errorf("same offset in successive pass windows must yield identical metrics: a=%+v b=%+v", a, b)
	}
}

func TestSimulator_BeforeStart_IsOutOfPass(t *testing.T) {
	// A scrape that arrives before Start (e.g., skewed clock) must not
	// index into negative modulo territory and must degrade safely.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Simulator{
		Start:        start,
		PassDuration: 10 * time.Second,
		GapDuration:  10 * time.Second,
	}
	got := s.ValuesAt(start.Add(-5 * time.Second))
	if got.InPass {
		t.Error("pre-start times must not be reported in-pass")
	}
}
