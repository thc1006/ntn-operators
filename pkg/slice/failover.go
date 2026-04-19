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
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
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
	Metric   string // rsrp, terrestrialRSRP, latency, terrestrialLatency, packetLoss, terrestrialPacketLoss
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
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return Trigger{}, fmt.Errorf("non-finite trigger value %q in %q", valueStr, s)
			}
			if metric == "" {
				return Trigger{}, fmt.Errorf("empty metric in trigger %q", s)
			}
			validMetrics := map[string]bool{
				"rsrp": true, "terrestrialRSRP": true,
				"latency": true, "terrestrialLatency": true,
				"packetLoss": true, "terrestrialPacketLoss": true,
			}
			if !validMetrics[metric] {
				return Trigger{}, fmt.Errorf("unknown metric %q in trigger %q", metric, s)
			}
			return Trigger{Metric: metric, Operator: op, Value: value}, nil
		}
	}
	return Trigger{}, fmt.Errorf("invalid trigger format %q: expected 'metric operator value'", s)
}

// EvaluateRecovery checks if the trigger has recovered with hysteresis margin.
// For "<"/"<=" triggers, recovery requires actual >= threshold + margin.
// For ">"/">=" triggers, recovery requires actual <= threshold - margin.
func (t Trigger) EvaluateRecovery(m Metrics, margin float64) bool {
	var actual float64
	switch t.Metric {
	case "rsrp", "terrestrialRSRP":
		actual = m.RSRP
	case "latency", "terrestrialLatency":
		actual = m.LatencyMs
	case "packetLoss", "terrestrialPacketLoss":
		actual = m.PacketLossPercent
	default:
		return true // unknown metric — assume recovered
	}

	switch t.Operator {
	case "<":
		return actual >= t.Value+margin
	case "<=":
		return actual > t.Value+margin
	case ">":
		return actual <= t.Value-margin
	case ">=":
		return actual < t.Value-margin
	default:
		return true
	}
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
//   - triggers: raw trigger expressions from spec (e.g., "rsrp < -120")
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
	return EvaluateFailoverWithContext(
		context.Background(),
		currentPath,
		triggers,
		metrics,
		satelliteAvailable,
		switchbackDelay,
		lastFailover,
		now,
	)
}

// EvaluateFailoverWithContext is the context-aware variant of EvaluateFailover.
// It emits structured package-level logs for trigger parsing and decision outcomes.
func EvaluateFailoverWithContext(
	ctx context.Context,
	currentPath PathType,
	triggers []string,
	metrics Metrics,
	satelliteAvailable bool,
	switchbackDelay time.Duration,
	lastFailover time.Time,
	now time.Time,
) FailoverResult {
	log := logr.FromContextOrDiscard(ctx)
	log.V(2).Info("evaluate failover",
		"currentPath", currentPath,
		"triggerCount", len(triggers),
		"satelliteAvailable", satelliteAvailable,
		"switchbackDelay", switchbackDelay,
	)
	finish := func(res FailoverResult) FailoverResult {
		log.V(1).Info("failover decision",
			"decision", res.Decision,
			"targetPath", res.TargetPath,
			"reason", res.Reason,
		)
		return res
	}

	// Parse and evaluate all triggers (OR logic).
	anyTriggered := false
	parseErrors := 0
	for _, triggerStr := range triggers {
		trigger, err := ParseTrigger(triggerStr)
		if err != nil {
			parseErrors++
			log.V(1).Info("invalid trigger expression", "trigger", triggerStr, "error", err.Error())
			continue
		}
		if trigger.Evaluate(metrics) {
			anyTriggered = true
			log.V(2).Info("trigger matched", "trigger", triggerStr)
			break
		}
	}

	// Normalize unknown currentPath to terrestrial.
	if currentPath != PathTerrestrial && currentPath != PathSatellite && currentPath != PathUnavailable {
		currentPath = PathTerrestrial
	}

	// Surface trigger parse errors.
	parseSuffix := ""
	if parseErrors > 0 {
		if parseErrors == len(triggers) {
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     fmt.Sprintf("All %d trigger expressions are invalid", parseErrors),
				TargetPath: currentPath,
			})
		}
		parseSuffix = fmt.Sprintf(" (%d of %d triggers had parse errors)", parseErrors, len(triggers))
	}

	switch currentPath {
	case PathTerrestrial:
		if !anyTriggered {
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial path healthy" + parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		if !satelliteAvailable {
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial degraded but no satellite pass available" + parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionFailover,
			Reason:     "Terrestrial path degraded, satellite pass available" + parseSuffix,
			TargetPath: PathSatellite,
		})

	case PathSatellite:
		// If satellite pass ended, must switch back regardless.
		if !satelliteAvailable {
			if anyTriggered {
				return finish(FailoverResult{
					Decision:   DecisionSwitchback,
					Reason:     "Satellite pass ended, falling back to degraded terrestrial" + parseSuffix,
					TargetPath: PathTerrestrial,
				})
			}
			return finish(FailoverResult{
				Decision:   DecisionSwitchback,
				Reason:     "Satellite pass ended, terrestrial recovered" + parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		if anyTriggered {
			// Terrestrial still degraded, stay on satellite.
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial still degraded, staying on satellite" + parseSuffix,
				TargetPath: PathSatellite,
			})
		}
		// Terrestrial recovered — check switchback delay.
		if !lastFailover.IsZero() && now.Sub(lastFailover) < switchbackDelay {
			return finish(FailoverResult{
				Decision: DecisionStay,
				Reason: fmt.Sprintf("Terrestrial recovered but switchback delay not elapsed (%s remaining)%s",
					switchbackDelay-now.Sub(lastFailover), parseSuffix),
				TargetPath: PathSatellite,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     "Terrestrial recovered, switchback delay elapsed" + parseSuffix,
			TargetPath: PathTerrestrial,
		})

	default:
		// Unknown or unavailable path — try terrestrial first.
		if !anyTriggered {
			return finish(FailoverResult{
				Decision:   DecisionSwitchback,
				Reason:     "Initializing on terrestrial path" + parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		if satelliteAvailable {
			return finish(FailoverResult{
				Decision:   DecisionFailover,
				Reason:     "Terrestrial unavailable, using satellite" + parseSuffix,
				TargetPath: PathSatellite,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     "Both paths degraded" + parseSuffix,
			TargetPath: PathUnavailable,
		})
	}
}

// EvaluateFailoverWithHysteresis is like EvaluateFailoverWithContext but applies
// a dead-band margin to trigger thresholds during switchback evaluation.
//
// During failover (terrestrial → satellite), triggers use their original thresholds.
// During switchback (satellite → terrestrial), triggers require the metric to clear
// the threshold by the hysteresis margin before considering recovery.
//
// Example: trigger "rsrp < -120" with hysteresisDB=10 means failover fires at
// RSRP < -120 dBm, but switchback requires RSRP >= -110 dBm (10 dB dead-band).
func EvaluateFailoverWithHysteresis(
	ctx context.Context,
	currentPath PathType,
	triggers []string,
	metrics Metrics,
	satelliteAvailable bool,
	switchbackDelay time.Duration,
	lastFailover time.Time,
	now time.Time,
	hysteresisDB float64,
) FailoverResult {
	// If not on satellite or no hysteresis, delegate to the standard evaluator.
	if currentPath != PathSatellite || hysteresisDB <= 0 {
		return EvaluateFailoverWithContext(
			ctx, currentPath, triggers, metrics,
			satelliteAvailable, switchbackDelay, lastFailover, now,
		)
	}

	log := logr.FromContextOrDiscard(ctx)
	log.V(2).Info("evaluate failover with hysteresis",
		"hysteresisMargin", hysteresisDB,
		"currentPath", currentPath,
	)
	finish := func(res FailoverResult) FailoverResult {
		log.V(1).Info("failover decision (hysteresis)",
			"decision", res.Decision,
			"targetPath", res.TargetPath,
			"reason", res.Reason,
		)
		return res
	}

	// Parse all triggers with error accounting (same as EvaluateFailoverWithContext).
	anyTriggered := false
	parseErrors := 0
	var parsed []Trigger
	for _, triggerStr := range triggers {
		trigger, err := ParseTrigger(triggerStr)
		if err != nil {
			parseErrors++
			log.V(1).Info("invalid trigger expression", "trigger", triggerStr, "error", err.Error())
			continue
		}
		parsed = append(parsed, trigger)
		if trigger.Evaluate(metrics) {
			anyTriggered = true
		}
	}

	parseSuffix := ""
	if parseErrors > 0 {
		if parseErrors == len(triggers) {
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     fmt.Sprintf("All %d trigger expressions are invalid", parseErrors),
				TargetPath: currentPath,
			})
		}
		parseSuffix = fmt.Sprintf(" (%d of %d triggers had parse errors)", parseErrors, len(triggers))
	}

	// Satellite pass ended — force switchback regardless of hysteresis.
	if !satelliteAvailable {
		if anyTriggered {
			return finish(FailoverResult{
				Decision:   DecisionSwitchback,
				Reason:     "Satellite pass ended, falling back to degraded terrestrial" + parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     "Satellite pass ended, terrestrial recovered" + parseSuffix,
			TargetPath: PathTerrestrial,
		})
	}

	if anyTriggered {
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     "Terrestrial still degraded, staying on satellite" + parseSuffix,
			TargetPath: PathSatellite,
		})
	}

	// Triggers no longer firing at original thresholds.
	// Check hysteresis: have ALL valid triggers recovered past the dead-band?
	allRecovered := true
	for _, t := range parsed {
		if !t.EvaluateRecovery(metrics, hysteresisDB) {
			allRecovered = false
			break
		}
	}

	if !allRecovered {
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason: fmt.Sprintf(
				"Terrestrial in hysteresis dead-band (margin=%g), staying on satellite%s",
				hysteresisDB, parseSuffix),
			TargetPath: PathSatellite,
		})
	}

	// Recovered past dead-band — check switchback delay.
	if !lastFailover.IsZero() && now.Sub(lastFailover) < switchbackDelay {
		return finish(FailoverResult{
			Decision: DecisionStay,
			Reason: fmt.Sprintf("Terrestrial recovered past dead-band but switchback delay not elapsed (%s remaining)%s",
				switchbackDelay-now.Sub(lastFailover), parseSuffix),
			TargetPath: PathSatellite,
		})
	}

	return finish(FailoverResult{
		Decision:   DecisionSwitchback,
		Reason:     "Terrestrial recovered past hysteresis dead-band, switchback delay elapsed" + parseSuffix,
		TargetPath: PathTerrestrial,
	})
}
