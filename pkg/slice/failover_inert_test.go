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
	"reflect"
	"testing"
	"time"
)

// TestInertTriggers verifies that only triggers whose metric is Missing are
// reported inert; present-metric and unparseable triggers are not (I-10).
func TestInertTriggers(t *testing.T) {
	m := Metrics{
		RSRP: -80, LatencyMs: 20, PacketLossPercent: 0.1,
		RSRPMissing: true, LatencyMissing: false, PacketLossMissing: true,
	}
	got := InertTriggers([]string{
		"rsrp < -120",    // RSRP missing → inert
		"latency > 200",  // latency present → live
		"packetLoss > 5", // packetLoss missing → inert
		"not a trigger",  // unparseable → ignored
	}, m)
	want := []string{"rsrp < -120", "packetLoss > 5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InertTriggers = %v, want %v", got, want)
	}
}

func TestInertTriggers_AllPresent(t *testing.T) {
	m := Metrics{RSRP: -80, LatencyMs: 20, PacketLossPercent: 0.1} // all Missing false
	if got := InertTriggers([]string{"rsrp < -120", "latency > 200"}, m); len(got) != 0 {
		t.Errorf("no metric missing → no inert triggers, got %v", got)
	}
}

// TestEvaluateFailover_SkipsMissingMetric_NoWrongFire is the load-bearing case:
// a "rsrp > -100" trigger fires when RSRP is HEALTHY. With RSRP missing (left at
// the -80 placeholder), evaluating it would SPURIOUSLY fire (-80 > -100). The
// engine must skip the inert trigger and NOT fail over.
func TestEvaluateFailover_SkipsMissingMetric_NoWrongFire(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	triggers := []string{"rsrp > -100"} // would fire against the -80 default

	// RSRP present (control): the trigger fires → failover to the available satellite.
	present := Metrics{RSRP: -80}
	res := EvaluateFailover(PathTerrestrial, triggers, present, true, 0, time.Time{}, now)
	if res.Decision != DecisionFailover {
		t.Fatalf("control: present metric should fire the trigger and fail over, got %+v", res)
	}

	// RSRP missing: the trigger is inert → hold on terrestrial (no spurious fire).
	missing := Metrics{RSRP: -80, RSRPMissing: true}
	res = EvaluateFailover(PathTerrestrial, triggers, missing, true, 0, time.Time{}, now)
	if res.Decision == DecisionFailover {
		t.Errorf("missing metric must not fire the trigger: got a spurious failover %+v", res)
	}
}
