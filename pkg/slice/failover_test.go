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
	"strings"
	"testing"
	"time"
)

var triggers = []string{
	"rsrp < -120",
	"latency > 200",
	"packetLoss > 5",
}

var now = time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

func TestParseTrigger(t *testing.T) {
	tests := []struct {
		input   string
		metric  string
		op      string
		value   float64
		wantErr bool
	}{
		{"rsrp < -120", "rsrp", "<", -120, false},
		{"latency > 200", "latency", ">", 200, false},
		{"packetLoss > 5", "packetLoss", ">", 5, false},
		{"terrestrialRSRP <= -110", "terrestrialRSRP", "<=", -110, false},
		{"invalid", "", "", 0, true},
		{"rsrp < abc", "", "", 0, true},
	}
	for _, tc := range tests {
		trig, err := ParseTrigger(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTrigger(%q): expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTrigger(%q): %v", tc.input, err)
			continue
		}
		if trig.Metric != tc.metric || trig.Operator != tc.op || trig.Value != tc.value {
			t.Errorf("ParseTrigger(%q) = %+v, want metric=%s op=%s value=%f",
				tc.input, trig, tc.metric, tc.op, tc.value)
		}
	}
}

func TestEvaluateFailover_TerrestrialHealthy_Stay(t *testing.T) {
	result := EvaluateFailover(
		PathTerrestrial, triggers,
		Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay, got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathTerrestrial {
		t.Errorf("expected terrestrial, got %s", result.TargetPath)
	}
}

func TestEvaluateFailover_RSRPTrigger_Failover(t *testing.T) {
	result := EvaluateFailover(
		PathTerrestrial, triggers,
		Metrics{RSRP: -125, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionFailover {
		t.Errorf("expected Failover, got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathSatellite {
		t.Errorf("expected satellite, got %s", result.TargetPath)
	}
}

func TestEvaluateFailover_LatencyTrigger_Failover(t *testing.T) {
	result := EvaluateFailover(
		PathTerrestrial, triggers,
		Metrics{RSRP: -90, LatencyMs: 250, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionFailover {
		t.Errorf("expected Failover, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateFailover_PacketLossTrigger_Failover(t *testing.T) {
	result := EvaluateFailover(
		PathTerrestrial, triggers,
		Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 8},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionFailover {
		t.Errorf("expected Failover, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateFailover_NoSatellitePass_StayOnTerrestrial(t *testing.T) {
	result := EvaluateFailover(
		PathTerrestrial, triggers,
		Metrics{RSRP: -125, LatencyMs: 20, PacketLossPercent: 0.1},
		false, // no satellite pass
		60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay (no satellite), got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathTerrestrial {
		t.Errorf("expected terrestrial (degraded but only option), got %s", result.TargetPath)
	}
}

func TestEvaluateFailover_OnSatellite_TerrestrialRecovered_SwitchbackDelayNotElapsed(t *testing.T) {
	lastFailover := now.Add(-30 * time.Second) // failed over 30s ago
	result := EvaluateFailover(
		PathSatellite, triggers,
		Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}, // healthy
		true, 60*time.Second, lastFailover, now,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay (delay not elapsed), got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathSatellite {
		t.Errorf("expected satellite (waiting for switchback delay), got %s", result.TargetPath)
	}
}

func TestEvaluateFailover_OnSatellite_TerrestrialRecovered_SwitchbackDelayElapsed(t *testing.T) {
	lastFailover := now.Add(-90 * time.Second) // failed over 90s ago
	result := EvaluateFailover(
		PathSatellite, triggers,
		Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}, // healthy
		true, 60*time.Second, lastFailover, now,
	)
	if result.Decision != DecisionSwitchback {
		t.Errorf("expected Switchback, got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathTerrestrial {
		t.Errorf("expected terrestrial, got %s", result.TargetPath)
	}
}

func TestEvaluateFailover_OnSatellite_TerrestrialStillDegraded_Stay(t *testing.T) {
	result := EvaluateFailover(
		PathSatellite, triggers,
		Metrics{RSRP: -130, LatencyMs: 20, PacketLossPercent: 0.1}, // still bad
		true, 60*time.Second, now.Add(-90*time.Second), now,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay (terrestrial still bad), got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateFailover_OnSatellite_PassEnds_SwitchBack(t *testing.T) {
	result := EvaluateFailover(
		PathSatellite, triggers,
		Metrics{RSRP: -130, LatencyMs: 20, PacketLossPercent: 0.1}, // terrestrial still bad
		false, // satellite pass ended
		60*time.Second, now.Add(-90*time.Second), now,
	)
	if result.Decision != DecisionSwitchback {
		t.Errorf("expected Switchback (pass ended), got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathTerrestrial {
		t.Errorf("expected terrestrial fallback, got %s", result.TargetPath)
	}
}

func TestParseTrigger_UnknownMetric(t *testing.T) {
	_, err := ParseTrigger("packeLoss > 5") // typo
	if err == nil {
		t.Fatal("expected error for unknown metric")
	}
}

func TestEvaluateFailover_MultipleTriggers_ORLogic(t *testing.T) {
	// Only latency triggers, RSRP and packetLoss are fine.
	result := EvaluateFailover(
		PathTerrestrial, triggers,
		Metrics{RSRP: -90, LatencyMs: 300, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionFailover {
		t.Errorf("expected Failover (OR logic), got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateFailover_EmptyPath_NormalizesToTerrestrial(t *testing.T) {
	// Empty path normalized to terrestrial, healthy → stay.
	result := EvaluateFailover(
		"", triggers,
		Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay (normalized to terrestrial, healthy), got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathTerrestrial {
		t.Errorf("expected terrestrial, got %s", result.TargetPath)
	}
}

func TestParseTrigger_EmptyMetric(t *testing.T) {
	_, err := ParseTrigger("< -120")
	if err == nil {
		t.Fatal("expected error for empty metric")
	}
}

func TestEvaluateFailover_AllTriggersInvalid(t *testing.T) {
	result := EvaluateFailover(
		PathTerrestrial,
		[]string{"invalid", "also bad", "nope"},
		Metrics{RSRP: -130},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay for all invalid triggers, got %s", result.Decision)
	}
	if !strings.Contains(result.Reason, "invalid") {
		t.Errorf("expected reason to mention invalid, got %q", result.Reason)
	}
}

func TestEvaluate_AllOperatorsAndMetrics(t *testing.T) {
	m := Metrics{RSRP: -110, LatencyMs: 50, PacketLossPercent: 2.0}

	tests := []struct {
		trigger  Trigger
		expected bool
	}{
		// >= operator
		{Trigger{Metric: "rsrp", Operator: ">=", Value: -110}, true},
		{Trigger{Metric: "rsrp", Operator: ">=", Value: -109}, false},
		// <= operator
		{Trigger{Metric: "latency", Operator: "<=", Value: 50}, true},
		{Trigger{Metric: "latency", Operator: "<=", Value: 49}, false},
		// terrestrialLatency alias
		{Trigger{Metric: "terrestrialLatency", Operator: ">", Value: 40}, true},
		// terrestrialPacketLoss alias
		{Trigger{Metric: "terrestrialPacketLoss", Operator: ">", Value: 1}, true},
		// unknown metric → false
		{Trigger{Metric: "unknown", Operator: "<", Value: 0}, false},
		// unknown operator → false
		{Trigger{Metric: "rsrp", Operator: "!=", Value: 0}, false},
	}
	for _, tc := range tests {
		got := tc.trigger.Evaluate(m)
		if got != tc.expected {
			t.Errorf("Evaluate(%s %s %v) = %v, want %v",
				tc.trigger.Metric, tc.trigger.Operator, tc.trigger.Value, got, tc.expected)
		}
	}
}

func TestParseTrigger_GeOperator(t *testing.T) {
	tr, err := ParseTrigger("rsrp >= -100")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Operator != ">=" {
		t.Errorf("expected >=, got %s", tr.Operator)
	}
}

func TestParseTrigger_NoOperator(t *testing.T) {
	_, err := ParseTrigger("rsrp -120")
	if err == nil {
		t.Fatal("expected error for missing operator")
	}
}

func TestEvaluateFailover_EmptyPath_Degraded_Failover(t *testing.T) {
	// Empty path normalized to terrestrial, triggers fire, satellite available → failover.
	result := EvaluateFailover(
		"", triggers,
		Metrics{RSRP: -130, LatencyMs: 300, PacketLossPercent: 10},
		true,
		60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionFailover {
		t.Errorf("expected Failover, got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathSatellite {
		t.Errorf("expected satellite, got %s", result.TargetPath)
	}
}

func TestEvaluateFailoverWithContext_MatchesLegacyBehavior(t *testing.T) {
	metrics := Metrics{RSRP: -125, LatencyMs: 250, PacketLossPercent: 0.1}
	want := EvaluateFailover(
		PathTerrestrial, triggers, metrics,
		true, 60*time.Second, time.Time{}, now,
	)
	got := EvaluateFailoverWithContext(
		context.Background(),
		PathTerrestrial, triggers, metrics,
		true, 60*time.Second, time.Time{}, now,
	)
	if got != want {
		t.Fatalf("EvaluateFailoverWithContext mismatch: got %+v want %+v", got, want)
	}
}

// --- Hysteresis (dead-band) tests ---

func TestEvaluateWithHysteresis_Failover_UsesOriginalThreshold(t *testing.T) {
	// Trigger: rsrp < -120. RSRP = -125 → should failover (hysteresis doesn't affect failover direction).
	result := EvaluateFailoverWithHysteresis(
		context.Background(),
		PathTerrestrial, triggers,
		Metrics{RSRP: -125, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now, 10,
	)
	if result.Decision != DecisionFailover {
		t.Errorf("expected Failover, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateWithHysteresis_Switchback_InDeadBand_Stay(t *testing.T) {
	// Single trigger: rsrp < -120, hysteresis = 10 dB.
	// RSRP = -115 → above trigger (-120) but below recovery threshold (-110).
	// Should STAY on satellite (in dead-band).
	result := EvaluateFailoverWithHysteresis(
		context.Background(),
		PathSatellite, []string{"rsrp < -120"},
		Metrics{RSRP: -115, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay (in hysteresis dead-band), got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateWithHysteresis_Switchback_AboveDeadBand_Switchback(t *testing.T) {
	// Single trigger: rsrp < -120, hysteresis = 10 dB.
	// RSRP = -105 → above recovery threshold (-110).
	// Should SWITCHBACK.
	result := EvaluateFailoverWithHysteresis(
		context.Background(),
		PathSatellite, []string{"rsrp < -120"},
		Metrics{RSRP: -105, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if result.Decision != DecisionSwitchback {
		t.Errorf("expected Switchback (above dead-band), got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateWithHysteresis_ZeroMargin_SameAsOriginal(t *testing.T) {
	// With hysteresis=0, behavior should match non-hysteresis version.
	metrics := Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}
	want := EvaluateFailoverWithContext(
		context.Background(),
		PathSatellite, triggers, metrics,
		true, 60*time.Second, now.Add(-90*time.Second), now,
	)
	got := EvaluateFailoverWithHysteresis(
		context.Background(),
		PathSatellite, triggers, metrics,
		true, 60*time.Second, now.Add(-90*time.Second), now, 0,
	)
	if got.Decision != want.Decision || got.TargetPath != want.TargetPath {
		t.Errorf("zero hysteresis should match original: got %+v want %+v", got, want)
	}
}

func TestEvaluateWithHysteresis_LatencyTrigger_DeadBand(t *testing.T) {
	// Trigger: latency > 200, hysteresis = 30.
	// On satellite, latency = 185 → below trigger (200) but above recovery (170).
	// Should STAY (in dead-band).
	result := EvaluateFailoverWithHysteresis(
		context.Background(),
		PathSatellite,
		[]string{"latency > 200"},
		Metrics{RSRP: -90, LatencyMs: 185, PacketLossPercent: 0.1},
		true, 60*time.Second, now.Add(-90*time.Second), now, 30,
	)
	if result.Decision != DecisionStay {
		t.Errorf("expected Stay (latency in dead-band), got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateWithHysteresis_PassEnds_ForceSwitchback(t *testing.T) {
	// Even in dead-band, satellite pass ending forces switchback.
	result := EvaluateFailoverWithHysteresis(
		context.Background(),
		PathSatellite, []string{"rsrp < -120"},
		Metrics{RSRP: -115, LatencyMs: 20, PacketLossPercent: 0.1},
		false, // satellite pass ended
		60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if result.Decision != DecisionSwitchback {
		t.Errorf("expected Switchback (pass ended), got %s: %s", result.Decision, result.Reason)
	}
}
