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

// Failover trigger metric names. Each satellite metric has a "terrestrial"
// counterpart that resolves to the same Metrics field; both spellings are
// accepted in trigger expressions (see ParseTrigger, Evaluate).
const (
	metricRSRP                  = "rsrp"
	metricTerrestrialRSRP       = "terrestrialRSRP"
	metricLatency               = "latency"
	metricTerrestrialLatency    = "terrestrialLatency"
	metricPacketLoss            = "packetLoss"
	metricTerrestrialPacketLoss = "terrestrialPacketLoss"
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

	// *Missing flags mark a field that was NOT sourced from real data (no
	// Prometheus query configured, no annotation set) and is therefore left at a
	// healthy placeholder. A trigger over a missing metric must NOT be evaluated
	// against that placeholder — it would silently never fire, or (for a "> good"
	// threshold) fire spuriously. The zero value is false = present, so callers
	// and tests that build Metrics from real values are unaffected (findings.md
	// I-10). terrestrialRSRP/RSRP share RSRPMissing, etc. (they read one field).
	RSRPMissing       bool
	LatencyMissing    bool
	PacketLossMissing bool
}

// metricMissing reports whether the metric this trigger reads was left at a
// placeholder (no real source). Unknown metric names are treated as not-missing;
// they are already rejected by ParseTrigger / admission CEL.
func (t Trigger) metricMissing(m Metrics) bool {
	switch t.Metric {
	case metricRSRP, metricTerrestrialRSRP:
		return m.RSRPMissing
	case metricLatency, metricTerrestrialLatency:
		return m.LatencyMissing
	case metricPacketLoss, metricTerrestrialPacketLoss:
		return m.PacketLossMissing
	default:
		return false
	}
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
				metricRSRP: true, metricTerrestrialRSRP: true,
				metricLatency: true, metricTerrestrialLatency: true,
				metricPacketLoss: true, metricTerrestrialPacketLoss: true,
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
	case metricRSRP, metricTerrestrialRSRP:
		actual = m.RSRP
	case metricLatency, metricTerrestrialLatency:
		actual = m.LatencyMs
	case metricPacketLoss, metricTerrestrialPacketLoss:
		actual = m.PacketLossPercent
	default:
		return false // unknown metric — fail-safe, assume NOT recovered
	}

	switch t.Operator {
	case "<", "<=":
		return actual >= t.Value+margin
	case ">", ">=":
		// Clamp recovery threshold to 0 for non-negative metrics
		// (e.g., packetLoss > 5 with margin 10 → threshold 0, not -5).
		recoveryThreshold := t.Value - margin
		if recoveryThreshold < 0 {
			recoveryThreshold = 0
		}
		return actual <= recoveryThreshold
	default:
		return false // unknown operator — fail-safe
	}
}

// Evaluate checks if a trigger condition is met given the current metrics.
func (t Trigger) Evaluate(m Metrics) bool {
	var actual float64
	switch t.Metric {
	case metricRSRP, metricTerrestrialRSRP:
		actual = m.RSRP
	case metricLatency, metricTerrestrialLatency:
		actual = m.LatencyMs
	case metricPacketLoss, metricTerrestrialPacketLoss:
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

// InertTriggers returns the trigger expressions whose metric has no real source
// in m (findings.md I-10). Such triggers are silently non-functional: evaluated
// against a healthy placeholder they never fire (or a ">good" threshold fires
// spuriously). The reconciler surfaces the returned list so an operator does not
// trust an armed-but-dead failover policy. Unparseable triggers are ignored here
// (they are handled by the parse-error path and rejected at admission).
func InertTriggers(triggers []string, m Metrics) []string {
	var inert []string
	for _, s := range triggers {
		t, err := ParseTrigger(s)
		if err != nil {
			continue
		}
		if t.metricMissing(m) {
			inert = append(inert, s)
		}
	}
	return inert
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

// triggerSetResult holds the result of parsing and evaluating a set of triggers.
type triggerSetResult struct {
	parsed       []Trigger
	anyTriggered bool
	parseSuffix  string
	allInvalid   bool
	// hasUnknownTrigger is true if any syntactically-valid trigger was skipped because
	// its metric has no live source (inert). Terrestrial recovery cannot be CONFIRMED
	// while a trigger is unknown, so the caller must not treat !anyTriggered as
	// "recovered" and switch back onto a link whose quality is unverifiable (I-10 /
	// the inert-trigger vacuous-recovery bug): unknown != recovered.
	hasUnknownTrigger bool
}

// parseTriggerSet parses all triggers, evaluates them (OR logic), and returns
// structured results including parse error accounting.
func parseTriggerSet(log logr.Logger, triggers []string, metrics Metrics) triggerSetResult {
	var result triggerSetResult
	parseErrors := 0
	for _, triggerStr := range triggers {
		trigger, err := ParseTrigger(triggerStr)
		if err != nil {
			// Only count/log parse errors before first match,
			// matching legacy short-circuit semantics.
			if !result.anyTriggered {
				parseErrors++
				log.V(1).Info("invalid trigger expression",
					"trigger", triggerStr, "error", err.Error())
			}
			continue
		}
		// I-10: a trigger whose metric was not sourced from real data must not be
		// evaluated against the healthy placeholder — that silently disarms it (or
		// spuriously fires a ">good" threshold). Skip it; the reconciler surfaces
		// inert triggers separately via InertTriggers.
		if trigger.metricMissing(metrics) {
			log.V(1).Info("trigger references a metric with no configured source; it is inert",
				"trigger", triggerStr, "metric", trigger.Metric)
			result.hasUnknownTrigger = true
			continue
		}
		result.parsed = append(result.parsed, trigger)
		if !result.anyTriggered && trigger.Evaluate(metrics) {
			result.anyTriggered = true
			log.V(2).Info("trigger matched", "trigger", triggerStr)
		}
	}
	if len(result.parsed) == 0 && parseErrors > 0 {
		result.allInvalid = true
	} else if parseErrors > 0 {
		result.parseSuffix = fmt.Sprintf(
			" (%d of %d triggers had parse errors)",
			parseErrors, len(triggers))
	}
	return result
}

// passEndedReason is the honest reason for the forced return to terrestrial when a
// satellite pass has physically ended. It must not claim "terrestrial recovered" when
// recovery is unverifiable (an unknown-source or all-invalid trigger set): the decision
// (leave the set satellite) is right regardless, but the condition/event must not
// mislead incident diagnosis into thinking terrestrial telemetry has recovered.
func passEndedReason(ts triggerSetResult) string {
	switch {
	case ts.anyTriggered:
		return "Satellite pass ended, falling back to degraded terrestrial"
	case ts.hasUnknownTrigger || ts.allInvalid:
		return "Satellite pass ended; returning to terrestrial (recovery unconfirmed)"
	default:
		return "Satellite pass ended, terrestrial recovered"
	}
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

	ts := parseTriggerSet(log, triggers, metrics)

	// Normalize unknown currentPath to terrestrial.
	if currentPath != PathTerrestrial && currentPath != PathSatellite && currentPath != PathUnavailable {
		currentPath = PathTerrestrial
	}

	// Orbital reality precedes everything: a satellite whose pass has physically ended
	// must return to terrestrial regardless of trigger validity or knownness — staying
	// would black-hole traffic on a set satellite. This MUST precede the allInvalid
	// guard (all-invalid triggers on an ended pass would otherwise Stay on the satellite).
	if currentPath == PathSatellite && !satelliteAvailable {
		return finish(FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     passEndedReason(ts) + ts.parseSuffix,
			TargetPath: PathTerrestrial,
		})
	}

	if ts.allInvalid {
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     fmt.Sprintf("All %d trigger expressions are invalid", len(triggers)),
			TargetPath: currentPath,
		})
	}

	switch currentPath {
	case PathTerrestrial:
		if !ts.anyTriggered {
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial path healthy" + ts.parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		if !satelliteAvailable {
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial degraded but no satellite pass available" + ts.parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionFailover,
			Reason:     "Terrestrial path degraded, satellite pass available" + ts.parseSuffix,
			TargetPath: PathSatellite,
		})

	case PathSatellite:
		// (A physically-ended pass is handled before the allInvalid guard above.)
		if ts.anyTriggered {
			// Terrestrial still degraded, stay on satellite.
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Terrestrial still degraded, staying on satellite" + ts.parseSuffix,
				TargetPath: PathSatellite,
			})
		}
		if ts.hasUnknownTrigger {
			// No trigger fired, but a trigger metric is unknown — recovery is
			// UNCONFIRMED. Hold on satellite rather than switch back onto a link whose
			// quality can't be verified; the pass-ended path above still forces a
			// return to terrestrial if the satellite physically sets.
			return finish(FailoverResult{
				Decision:   DecisionStay,
				Reason:     "Recovery unconfirmed (a trigger metric is unsourced), holding on satellite" + ts.parseSuffix,
				TargetPath: PathSatellite,
			})
		}
		// Terrestrial recovered — check switchback delay.
		if !lastFailover.IsZero() && now.Sub(lastFailover) < switchbackDelay {
			return finish(FailoverResult{
				Decision: DecisionStay,
				Reason: fmt.Sprintf("Terrestrial recovered but switchback delay not elapsed (%s remaining)%s",
					switchbackDelay-now.Sub(lastFailover), ts.parseSuffix),
				TargetPath: PathSatellite,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     "Terrestrial recovered, switchback delay elapsed" + ts.parseSuffix,
			TargetPath: PathTerrestrial,
		})

	default:
		// Unknown or unavailable path — initializing.
		if !ts.anyTriggered {
			// No trigger fired. But "no trigger fired" only means "terrestrial healthy"
			// when every trigger is KNOWN. If a trigger metric is unknown, do NOT read the
			// silence as health: prefer a CONFIRMED-available satellite; otherwise fall back
			// to terrestrial (the primary) but say so honestly — a partial view is not a
			// recovered view. We do NOT park on PathUnavailable when terrestrial is a viable
			// fallback, as that would black-hole traffic on an unproven assumption. (When a
			// real trigger IS firing, terrestrial is confirmed degraded → the paths below.)
			if ts.hasUnknownTrigger {
				if satelliteAvailable {
					return finish(FailoverResult{
						Decision:   DecisionFailover,
						Reason:     "Terrestrial quality unknown; using the confirmed satellite pass" + ts.parseSuffix,
						TargetPath: PathSatellite,
					})
				}
				return finish(FailoverResult{
					Decision:   DecisionSwitchback,
					Reason:     "Initializing on terrestrial (quality unconfirmed; no satellite pass)" + ts.parseSuffix,
					TargetPath: PathTerrestrial,
				})
			}
			return finish(FailoverResult{
				Decision:   DecisionSwitchback,
				Reason:     "Initializing on terrestrial path" + ts.parseSuffix,
				TargetPath: PathTerrestrial,
			})
		}
		if satelliteAvailable {
			return finish(FailoverResult{
				Decision:   DecisionFailover,
				Reason:     "Terrestrial unavailable, using satellite" + ts.parseSuffix,
				TargetPath: PathSatellite,
			})
		}
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     "Both paths degraded" + ts.parseSuffix,
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
// Example: trigger "rsrp < -120" with hysteresisMargin=10 means failover fires at
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
	hysteresisMargin float64,
) FailoverResult {
	// If not on satellite or no hysteresis, delegate to the standard evaluator.
	if currentPath != PathSatellite || hysteresisMargin <= 0 {
		return EvaluateFailoverWithContext(
			ctx, currentPath, triggers, metrics,
			satelliteAvailable, switchbackDelay, lastFailover, now,
		)
	}

	log := logr.FromContextOrDiscard(ctx)
	log.V(2).Info("evaluate failover with hysteresis",
		"hysteresisMargin", hysteresisMargin,
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

	ts := parseTriggerSet(log, triggers, metrics)

	// Satellite pass ended — force switchback regardless of hysteresis OR trigger
	// validity. currentPath is guaranteed PathSatellite here (line 485 delegates
	// otherwise), so this is a genuine set-pass. It MUST precede the allInvalid guard,
	// else an all-invalid trigger set on an ended pass would Stay on the set satellite
	// (a traffic black hole).
	if !satelliteAvailable {
		return finish(FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     passEndedReason(ts) + ts.parseSuffix,
			TargetPath: PathTerrestrial,
		})
	}

	if ts.allInvalid {
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     fmt.Sprintf("All %d trigger expressions are invalid", len(triggers)),
			TargetPath: currentPath,
		})
	}

	if ts.anyTriggered {
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     "Terrestrial still degraded, staying on satellite" + ts.parseSuffix,
			TargetPath: PathSatellite,
		})
	}
	if ts.hasUnknownTrigger {
		// Recovery unconfirmed: a trigger metric is unknown, so the empty/partial
		// parsed set below would vacuously pass allRecovered. Hold on satellite.
		return finish(FailoverResult{
			Decision:   DecisionStay,
			Reason:     "Recovery unconfirmed (a trigger metric is unsourced), holding on satellite" + ts.parseSuffix,
			TargetPath: PathSatellite,
		})
	}

	// Triggers no longer firing at original thresholds.
	// Check hysteresis: have ALL valid triggers recovered past the dead-band?
	allRecovered := true
	for _, t := range ts.parsed {
		if !t.EvaluateRecovery(metrics, hysteresisMargin) {
			allRecovered = false
			break
		}
	}

	if !allRecovered {
		return finish(FailoverResult{
			Decision: DecisionStay,
			Reason: fmt.Sprintf(
				"Terrestrial in hysteresis dead-band (margin=%g), staying on satellite%s",
				hysteresisMargin, ts.parseSuffix),
			TargetPath: PathSatellite,
		})
	}

	// Recovered past dead-band — check switchback delay.
	if !lastFailover.IsZero() && now.Sub(lastFailover) < switchbackDelay {
		return finish(FailoverResult{
			Decision: DecisionStay,
			Reason: fmt.Sprintf("Terrestrial recovered past dead-band but switchback delay not elapsed (%s remaining)%s",
				switchbackDelay-now.Sub(lastFailover), ts.parseSuffix),
			TargetPath: PathSatellite,
		})
	}

	return finish(FailoverResult{
		Decision:   DecisionSwitchback,
		Reason:     "Terrestrial recovered past hysteresis dead-band, switchback delay elapsed" + ts.parseSuffix,
		TargetPath: PathTerrestrial,
	})
}

// AntiFlapConfig holds the per-slice anti-flap tunables (from FailoverPolicy).
// The zero value (ConfirmationSamples 0/1, MinTerrestrialDwell 0) reproduces the
// prior immediate-failover behavior, so anti-flap is strictly opt-in.
type AntiFlapConfig struct {
	// ConfirmationSamples is N: a failover requires the terrestrial triggers to
	// fire on N CONSECUTIVE reliable samples. <=1 means no confirmation gate.
	ConfirmationSamples int
	// MinTerrestrialDwell holds terrestrial for at least this long after a
	// quality-driven switchback before another failover. 0 disables it.
	MinTerrestrialDwell time.Duration
}

// AntiFlapState is the per-slice flap-suppression state. Two of its clocks are held only
// in memory; losing them on a controller restart or leader-election handoff is safe because
// a reset re-requires N consecutive confirmations (ConsecutiveDegraded) and restarts the
// recovery clock (RecoveryObservedAt) — both of which only DELAY a switch, never advance one.
// The ONE clock whose loss ADVANCES a switch is LastSwitchback: losing it drops the
// post-switchback min-dwell, so a re-degradation within the dwell window could fail over
// earlier than the dwell intended. That axis is therefore made durable — LastSwitchback is
// mirrored to NTNSlice.status.lastSwitchbackTime by the controller and reloaded when the
// in-memory clock is absent (a cold cache after restart), so min-dwell survives a handoff.
// This matters more now that rolling updates hand leadership over routinely; the other two
// clocks are intentionally NOT persisted (their loss only biases toward holding).
type AntiFlapState struct {
	// ConsecutiveDegraded counts consecutive reliable samples on which the
	// terrestrial triggers fired. Reset to 0 by any healthy/unreliable sample.
	ConsecutiveDegraded int
	// RecoveryObservedAt is when terrestrial began its current continuous healthy
	// streak (zero while degraded). The switchback delay is measured from here, so
	// a re-degradation resets the switchback clock (a switchback then requires a
	// fresh switchbackDelay of continuous recovery — the anti-flap-correct
	// semantics; an absolute window measured from the last failover would switch
	// back onto a just-recovered, still-unstable terrestrial).
	RecoveryObservedAt time.Time
	// LastSwitchback is when the slice last switched back to terrestrial from an
	// available satellite (a quality-driven, ping-pong-prone hand-back — NOT a
	// pass-ended forced switchback). Min-dwell is measured from here.
	LastSwitchback time.Time
	// LastCountedObservedAt is the source timestamp (Result.ObservedAt) of the most recent
	// observation that advanced the anti-flap state. A degraded/recovered sample updates the
	// counters only when its ObservedAt is newer than this, so a stale-cache replay or a
	// stalled-scrape duplicate (same or older timestamp) HOLDS the state instead of falsely
	// confirming it — making confirmationSamples count DISTINCT observations, not reconcile
	// calls (#203-M3). Zero when no timestamped observation has been counted yet.
	LastCountedObservedAt time.Time
}

// terrestrialDegraded reports whether any valid, live (non-inert) trigger fires
// against the current metrics — the same OR logic parseTriggerSet uses, but
// without the logging/parse-accounting. It drives the N-consecutive counter.
func terrestrialDegraded(triggers []string, m Metrics) bool {
	for _, s := range triggers {
		t, err := ParseTrigger(s)
		if err != nil {
			continue
		}
		if t.metricMissing(m) { // I-10: inert trigger, never evaluate the placeholder
			continue
		}
		if t.Evaluate(m) {
			return true
		}
	}
	return false
}

// terrestrialConfirmedRecovered reports whether terrestrial is CONFIRMED recovered:
// every syntactically-valid trigger is known (has a live metric) AND has recovered
// past the hysteresis margin. It is deliberately stricter than !terrestrialDegraded:
//   - an unknown (inert) trigger returns false — recovery is unverifiable (C1);
//   - a known trigger still inside the hysteresis dead-band returns false, so the
//     switchback recovery clock measures only continuous PAST-hysteresis recovery,
//     not time spent in the dead-band (H3).
//
// An EMPTY policy (no triggers) is trivially recovered (true), matching the base
// evaluator's switch-back-to-terrestrial behavior for an empty policy. But a non-empty
// policy whose triggers are ALL invalid is a broken config, not a recovered link — it
// returns false so a syntactically-broken policy cannot pre-start the recovery clock
// (the base evaluator independently Stays on allInvalid). With margin <= 0 this reduces
// to !terrestrialDegraded (no dead-band).
func terrestrialConfirmedRecovered(triggers []string, m Metrics, margin float64) bool {
	sawValid := false
	for _, s := range triggers {
		t, err := ParseTrigger(s)
		if err != nil {
			continue // invalid triggers are accounted for elsewhere (allInvalid → Stay)
		}
		sawValid = true
		if t.metricMissing(m) {
			return false // unknown metric — recovery cannot be confirmed
		}
		if !t.EvaluateRecovery(m, margin) {
			return false // known but still degraded or inside the dead-band
		}
	}
	// A non-empty policy with no valid trigger is all-invalid: not a confirmed recovery.
	if len(triggers) > 0 && !sawValid {
		return false
	}
	return true
}

// EvaluateFailoverWithAntiFlap wraps EvaluateFailoverWithHysteresis with the
// in-memory anti-flap gates. It must be called ONLY with reliable metrics (the
// caller fails static via EvaluateSafeHold otherwise). It:
//
//   - tracks a consecutive-degraded counter and a continuous-recovery timestamp;
//   - measures the switchback delay from that recovery timestamp (reset on
//     re-degradation), fixing the premature-switchback flap where the absolute
//     now-lastFailover timer elapses while terrestrial has only just recovered;
//   - gates a raw failover behind N-consecutive confirmation and the
//     post-switchback min-terrestrial-dwell (both bounded — a sustained
//     degradation still fails over once the gates clear).
//
// It returns the decision and the updated state; the caller owns (and may drop)
// the state. Gating only ever downgrades a Failover to Stay on the current path;
// it never fabricates a switch.
func EvaluateFailoverWithAntiFlap(
	ctx context.Context,
	currentPath PathType,
	triggers []string,
	metrics Metrics,
	observedAt time.Time,
	satelliteAvailable bool,
	switchbackDelay time.Duration,
	now time.Time,
	hysteresisMargin float64,
	cfg AntiFlapConfig,
	st AntiFlapState,
) (FailoverResult, AntiFlapState) {
	// 1. Update the confirmation + recovery clocks from this sample. Three states:
	//    degraded (a raw trigger fires) / confirmed-recovered (all known triggers past
	//    the hysteresis margin, none unknown) / neither (dead-band or an unknown
	//    metric). The recovery clock advances ONLY while confirmed-recovered, so the
	//    switchback delay measures continuous PAST-hysteresis recovery and never counts
	//    dead-band or unknown-metric time as recovery (H3/C1).
	// Only a genuinely NEW observation advances the confirmation/recovery clocks. A replayed
	// stale-cache value or a stalled-scrape duplicate (same or older source timestamp) carries no
	// new information, so it HOLDS the counters: otherwise confirmationSamples would count reconcile
	// calls, not distinct observations, and a metrics-source outage could replay one degraded sample
	// N times into a failover (#203-M3). A zero ObservedAt (a reader that cannot supply a source
	// timestamp) falls back to counting every call, preserving the prior behavior.
	if observedAt.IsZero() || observedAt.After(st.LastCountedObservedAt) {
		switch {
		case terrestrialDegraded(triggers, metrics):
			st.ConsecutiveDegraded++
			st.RecoveryObservedAt = time.Time{} // re-degraded → reset the switchback clock
		case terrestrialConfirmedRecovered(triggers, metrics, hysteresisMargin):
			st.ConsecutiveDegraded = 0
			if st.RecoveryObservedAt.IsZero() {
				st.RecoveryObservedAt = now // a fresh continuous past-hysteresis streak begins
			}
		default:
			// Dead-band or unknown metric: neither degraded nor confirmed recovered. Break
			// the recovery streak so a later confirmed recovery must re-accumulate the full
			// switchback delay. (The evaluator independently holds on satellite here.)
			st.ConsecutiveDegraded = 0
			st.RecoveryObservedAt = time.Time{}
		}
		st.LastCountedObservedAt = observedAt
	}

	// 2. Run the base engine, measuring the switchback delay from the recovery
	//    streak start rather than the absolute last-failover time.
	res := EvaluateFailoverWithHysteresis(
		ctx, currentPath, triggers, metrics,
		satelliteAvailable, switchbackDelay, st.RecoveryObservedAt, now, hysteresisMargin,
	)

	// 3. Gate a raw failover: require N consecutive confirmations, then honor the
	//    post-switchback dwell. Both only DELAY a failover; a sustained degradation
	//    clears them and fails over. Gate only the terrestrial→satellite direction
	//    (the ping-pong-prone one); a failover FROM an unavailable/uninitialized
	//    path onto an available satellite is not a flap and must not be delayed.
	if res.Decision == DecisionFailover && currentPath == PathTerrestrial {
		switch {
		case cfg.ConfirmationSamples > 1 && st.ConsecutiveDegraded < cfg.ConfirmationSamples:
			res = FailoverResult{
				Decision:   DecisionStay,
				TargetPath: currentPath,
				Reason: fmt.Sprintf("Terrestrial degraded, failover unconfirmed (%d/%d consecutive samples)",
					st.ConsecutiveDegraded, cfg.ConfirmationSamples),
			}
		case cfg.MinTerrestrialDwell > 0 && !st.LastSwitchback.IsZero() &&
			now.Sub(st.LastSwitchback) < cfg.MinTerrestrialDwell:
			res = FailoverResult{
				Decision:   DecisionStay,
				TargetPath: currentPath,
				Reason: fmt.Sprintf("Terrestrial degraded but within min dwell after switchback (%s remaining)",
					cfg.MinTerrestrialDwell-now.Sub(st.LastSwitchback)),
			}
		}
	}

	// 4. Start the dwell clock only on a quality-driven hand-back FROM satellite
	//    (satellite still available). A pass-ended forced switchback (satellite gone)
	//    is orbital, not a ping-pong; and initialising onto terrestrial from an
	//    unavailable/unknown path also emits DecisionSwitchback but is not a hand-back —
	//    the currentPath==PathSatellite guard excludes both so neither blocks a
	//    legitimate re-failover.
	if res.Decision == DecisionSwitchback && satelliteAvailable && currentPath == PathSatellite {
		st.LastSwitchback = now
	}

	return res, st
}

// EvaluateSafeHold is the fail-static decision used when path-quality metrics are
// unreliable — the metrics source is unavailable, or the last observed value is
// stale beyond the freshness bound. Quality-driven transitions (failover on
// degradation, switchback on recovery) are suppressed: the slice holds its
// current path rather than act on unverified telemetry (the SRE "fail static"
// principle).
//
// The one transition still honored is orbital: a satellite path whose pass window
// has ended is switched back to terrestrial (the safe default), because staying on
// a satellite that has physically set would black-hole traffic. The pass-window
// signal is computed locally from ephemeris and does NOT depend on the metrics
// source, so it stays trustworthy while metrics are degraded.
//
// This fixes findings.md B-4 (no switch on arbitrarily old data) and I-8 (no
// indefinite parking on a satellite whose pass has ended when metrics are down).
func EvaluateSafeHold(currentPath PathType, satelliteAvailable bool) FailoverResult {
	// Normalize unknown currentPath to terrestrial (mirrors EvaluateFailoverWithContext).
	if currentPath != PathTerrestrial && currentPath != PathSatellite && currentPath != PathUnavailable {
		currentPath = PathTerrestrial
	}
	if currentPath == PathSatellite && !satelliteAvailable {
		return FailoverResult{
			Decision:   DecisionSwitchback,
			Reason:     "metrics unreliable; switching back to terrestrial because the satellite pass window has ended",
			TargetPath: PathTerrestrial,
		}
	}
	return FailoverResult{
		Decision:   DecisionStay,
		Reason:     "metrics unreliable; holding current path (fail static; orbital signal honored)",
		TargetPath: currentPath,
	}
}
