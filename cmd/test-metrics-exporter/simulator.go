/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import "time"

// SimulatedMetrics is a single snapshot of the three failover metrics
// plus an in-pass flag useful for dashboards and logs.
//
// Values are chosen so that an NTNSlice with the default trigger set
// ("rsrp < -120", "latency > 200", "packetLoss > 5") will flip to
// satellite during the gap and back to terrestrial during the pass.
type SimulatedMetrics struct {
	RsrpDbm           float64
	LatencyMs         float64
	PacketLossPercent float64
	InPass            bool
}

// healthyMetrics are returned while the simulator believes a satellite
// pass window is active. Comfortably above the default thresholds.
var healthyMetrics = SimulatedMetrics{
	RsrpDbm:           -80,
	LatencyMs:         20,
	PacketLossPercent: 0.1,
	InPass:            true,
}

// degradedMetrics are returned during the gap between passes. Set to
// clearly cross each of the default triggers so E2E tests can drive
// failover deterministically.
var degradedMetrics = SimulatedMetrics{
	RsrpDbm:           -140,
	LatencyMs:         250,
	PacketLossPercent: 7.5,
	InPass:            false,
}

// Simulator produces a deterministic sequence of healthy / degraded
// metrics to exercise the failover engine end-to-end without a real
// SDR or radio link in the loop.
type Simulator struct {
	Start        time.Time
	PassDuration time.Duration
	GapDuration  time.Duration
}

// ValuesAt returns the simulated metrics at wall-clock time t. The
// pattern is: [Start, Start+PassDuration) is healthy, then
// [Start+PassDuration, Start+PassDuration+GapDuration) is degraded,
// repeating. Times before Start are treated as in the gap so an early
// scrape does not report a rosy picture that never actually happened.
func (s Simulator) ValuesAt(t time.Time) SimulatedMetrics {
	cycle := s.PassDuration + s.GapDuration
	if cycle <= 0 {
		return degradedMetrics
	}
	offset := t.Sub(s.Start)
	if offset < 0 {
		return degradedMetrics
	}
	within := offset % cycle
	if within < s.PassDuration {
		return healthyMetrics
	}
	return degradedMetrics
}
