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

package slice

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PathType represents the active network path.
type PathType string

const (
	PathTerrestrial PathType = "terrestrial"
	PathSatellite   PathType = "satellite"
	PathUnavailable PathType = "unavailable"
)

// Decision represents the failover engine's recommendation.
type Decision string

const (
	DecisionStay       Decision = "stay"
	DecisionFailover   Decision = "failover"
	DecisionSwitchback Decision = "switchback"
)

// FailoverResult is the output of the failover evaluation.
type FailoverResult struct {
	Decision   Decision
	Reason     string
	TargetPath PathType
}

// Metrics represents the current network path quality.
// In production these come from UPF/Prometheus; for now they are
// simulated via CR annotations.
type Metrics struct {
	RSRP              float64 // dBm, e.g., -110
	LatencyMs         float64 // ms, e.g., 50
	PacketLossPercent float64 // percent, e.g., 2.5
}

// Trigger represents a parsed failover trigger expression.
type Trigger struct {
	Metric   string // rsrp, latency, packetLoss
	Operator string // <, >, <=, >=
	Value    float64
}

// ParseTrigger parses a trigger string like "rsrp < -120" into a Trigger.
func ParseTrigger(s string) (Trigger, error) {
	s = strings.TrimSpace(s)
	// Try each operator (longest first to avoid partial match).
	for _, op := range []string{"<=", ">=", "<", ">"} {
		parts := strings.SplitN(s, op, 2)
		if len(parts) == 2 {
			metric := strings.TrimSpace(parts[0])
			valueStr := strings.TrimSpace(parts[1])
			value, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				return Trigger{}, fmt.Errorf("invalid trigger value %q: %w", valueStr, err)
			}
			return Trigger{Metric: metric, Operator: op, Value: value}, nil
		}
	}
	return Trigger{}, fmt.Errorf("invalid trigger format %q: expected 'metric operator value'", s)
}

// Evaluate checks if a trigger condition is met given the current metrics.
func (t Trigger) Evaluate(m Metrics) bool {
	var actual float64
	switch t.Metric {
	case "rsrp", "terrestrialRSRP":
		actual = m.RSRP
	case "latency", "terrestrialLatency":
		actual = m.LatencyMs
	case "packetLoss", "terrestrialPacketLoss":
		actual = m.PacketLossPercent
	default:
		return false
	}

	switch t.Operator {
	case "<":
		return actual < t.Value
	case ">":
		return actual > t.Value
	case "<=":
		return actual <= t.Value
	case ">=":
		return actual >= t.Value
	default:
		return false
	}
}

// EvaluateFailover determines whether to failover, stay, or switchback.
//
// Parameters:
//   - currentPath: the currently active path
//   - triggers: parsed from spec.failoverPolicy.triggers
//   - metrics: current terrestrial path quality
//   - satelliteAvailable: whether a satellite pass window is active
//   - switchbackDelay: minimum time before switching back
//   - lastFailover: when the last failover occurred (zero if never)
//   - now: current time (injectable for testing)
func EvaluateFailover(
	currentPath PathType,
	triggers []string,
	metrics Metrics,
	satelliteAvailable bool,
	switchbackDelay time.Duration,
	lastFailover time.Time,
	now time.Time,
) FailoverResult {
	// Parse and evaluate all triggers (OR logic).
	anyTriggered := false
	for _, triggerStr := range triggers {
		trigger, err := ParseTrigger(triggerStr)
		if err != nil {
			continue // skip unparseable triggers
		}
		if trigger.Evaluate(metrics) {
			anyTriggered = true
			break
		}
	}

	switch currentPath {
	case PathTerrestrial:
		if !anyTriggered {
			return FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial path healthy",
				TargetPath: PathTerrestrial,
			}
		}
		// Triggers fired — try to failover to satellite.
		if !satelliteAvailable {
			return FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial degraded but no satellite pass available",
				TargetPath: PathTerrestrial,
			}
		}
		return FailoverResult{
			Decision:   DecisionFailover,
			Reason:     "Terrestrial path degraded, satellite pass available",
			TargetPath: PathSatellite,
		}

	case PathSatellite:
		if anyTriggered {
			// Terrestrial still degraded, stay on satellite.
			return FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial still degraded, staying on satellite",
				TargetPath: PathSatellite,
			}
		}
		// Terrestrial recovered — check switchback delay.
		if !lastFailover.IsZero() && now.Sub(lastFailover) < switchbackDelay {
			return FailoverResult{
				Decision: DecisionStay,
				Reason: fmt.Sprintf("Terrestrial recovered but switchback delay not elapsed (%s remaining)",
					switchbackDelay-now.Sub(lastFailover)),
				TargetPath: PathSatellite,
			}
		}
		return FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     "Terrestrial recovered, switchback delay elapsed",
			TargetPath: PathTerrestrial,
		}

	default:
		// Unknown or unavailable path — try terrestrial first.
		if !anyTriggered {
			return FailoverResult{
				Decision:   DecisionSwitchback,
				Reason:     "Initializing on terrestrial path",
				TargetPath: PathTerrestrial,
			}
		}
		if satelliteAvailable {
			return FailoverResult{
				Decision:   DecisionFailover,
				Reason:     "Terrestrial unavailable, using satellite",
				TargetPath: PathSatellite,
			}
		}
		return FailoverResult{
			Decision:   DecisionStay,
			Reason:     "Both paths degraded",
			TargetPath: PathUnavailable,
		}
	}
}
