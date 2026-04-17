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

func TestEvaluateFailover_UnknownPath_Initialize(t *testing.T) {
	result := EvaluateFailover(
		"", triggers,
		Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1},
		true, 60*time.Second, time.Time{}, now,
	)
	if result.Decision != DecisionSwitchback {
		t.Errorf("expected Switchback (initialize), got %s: %s", result.Decision, result.Reason)
	}
	if result.TargetPath != PathTerrestrial {
		t.Errorf("expected terrestrial initialization, got %s", result.TargetPath)
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

func TestEvaluateFailover_UnknownPath_BothDegraded(t *testing.T) {
	result := EvaluateFailover(
		"", triggers,
		Metrics{RSRP: -130, LatencyMs: 300, PacketLossPercent: 10},
		false, // no satellite either
		60*time.Second, time.Time{}, now,
	)
	if result.TargetPath != PathUnavailable {
		t.Errorf("expected unavailable, got %s", result.TargetPath)
	}
}
