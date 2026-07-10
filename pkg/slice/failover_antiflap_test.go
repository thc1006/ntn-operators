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
	"testing"
	"time"
)

var (
	afHealthy  = Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}  // no trigger fires
	afDegraded = Metrics{RSRP: -125, LatencyMs: 20, PacketLossPercent: 0.1} // rsrp < -120 fires
)

const afSwitchbackDelay = 60 * time.Second

// TestAntiFlap_OptInPreservesImmediateFailover pins the opt-in contract: with a
// zero-value config (N<=1, dwell=0) a single degraded sample fails over
// immediately, exactly like the un-gated engine.
func TestAntiFlap_OptInPreservesImmediateFailover(t *testing.T) {
	res, _ := EvaluateFailoverWithAntiFlap(
		context.Background(), PathTerrestrial, triggers, afDegraded, true,
		afSwitchbackDelay, now, 0, AntiFlapConfig{}, AntiFlapState{},
	)
	if res.Decision != DecisionFailover {
		t.Fatalf("zero-config must fail over immediately, got %s (%s)", res.Decision, res.Reason)
	}
}

// TestAntiFlap_NConsecutiveConfirmation pins acceptance (a): a single degraded
// blip does not trip a failover; N=3 consecutive degraded samples are required,
// and any healthy sample resets the counter.
func TestAntiFlap_NConsecutiveConfirmation(t *testing.T) {
	cfg := AntiFlapConfig{ConfirmationSamples: 3}
	var st AntiFlapState
	eval := func(m Metrics) FailoverResult {
		var res FailoverResult
		res, st = EvaluateFailoverWithAntiFlap(
			context.Background(), PathTerrestrial, triggers, m, true,
			afSwitchbackDelay, now, 0, cfg, st,
		)
		return res
	}

	// One degraded blip: unconfirmed (1/3) → hold terrestrial.
	if res := eval(afDegraded); res.Decision != DecisionStay {
		t.Fatalf("blip 1/3 must not fail over, got %s (%s)", res.Decision, res.Reason)
	}
	if st.ConsecutiveDegraded != 1 {
		t.Fatalf("counter after 1 degraded = %d, want 1", st.ConsecutiveDegraded)
	}
	// A healthy sample resets the streak.
	if res := eval(afHealthy); res.Decision != DecisionStay {
		t.Fatalf("healthy sample must stay, got %s", res.Decision)
	}
	if st.ConsecutiveDegraded != 0 {
		t.Fatalf("a healthy sample must reset the counter, got %d", st.ConsecutiveDegraded)
	}
	// Now three consecutive degraded samples: fail over only on the third.
	if res := eval(afDegraded); res.Decision != DecisionStay {
		t.Fatalf("degraded 1/3 must not fail over, got %s", res.Decision)
	}
	if res := eval(afDegraded); res.Decision != DecisionStay {
		t.Fatalf("degraded 2/3 must not fail over, got %s", res.Decision)
	}
	if res := eval(afDegraded); res.Decision != DecisionFailover {
		t.Fatalf("degraded 3/3 must fail over, got %s (%s)", res.Decision, res.Reason)
	}
}

// TestAntiFlap_MinDwellBlocksRefailover pins acceptance (b): after a switchback,
// a re-failover is suppressed until the min-dwell elapses, then allowed.
func TestAntiFlap_MinDwellBlocksRefailover(t *testing.T) {
	cfg := AntiFlapConfig{ConfirmationSamples: 1, MinTerrestrialDwell: 90 * time.Second}
	// A quality-driven switchback just landed at `now`.
	st := AntiFlapState{LastSwitchback: now}
	eval := func(at time.Time) FailoverResult {
		var res FailoverResult
		res, st = EvaluateFailoverWithAntiFlap(
			context.Background(), PathTerrestrial, triggers, afDegraded, true,
			afSwitchbackDelay, at, 0, cfg, st,
		)
		return res
	}

	// 30s after switchback (< 90s dwell): degraded but held.
	if res := eval(now.Add(30 * time.Second)); res.Decision != DecisionStay {
		t.Fatalf("within min dwell must hold terrestrial, got %s (%s)", res.Decision, res.Reason)
	}
	// 120s after switchback (> 90s dwell): failover allowed.
	if res := eval(now.Add(120 * time.Second)); res.Decision != DecisionFailover {
		t.Fatalf("past min dwell must allow failover, got %s (%s)", res.Decision, res.Reason)
	}
}

// TestAntiFlap_SwitchbackTimerResetsOnRedegrade pins acceptance (c): the
// switchback delay is measured from the CONTINUOUS-recovery start, so a
// re-degradation resets it — a switchback that an absolute now-lastFailover timer
// would have fired is correctly deferred until terrestrial is stably recovered.
func TestAntiFlap_SwitchbackTimerResetsOnRedegrade(t *testing.T) {
	var st AntiFlapState // on satellite, no prior recovery streak
	eval := func(m Metrics, at time.Time) FailoverResult {
		var res FailoverResult
		res, st = EvaluateFailoverWithAntiFlap(
			context.Background(), PathSatellite, triggers, m, true,
			afSwitchbackDelay, at, 0, AntiFlapConfig{}, st,
		)
		return res
	}

	// t=0: terrestrial recovers → recovery clock starts; hold satellite (0 < 60s).
	if res := eval(afHealthy, now); res.Decision != DecisionStay {
		t.Fatalf("t=0 recovery start must hold satellite, got %s", res.Decision)
	}
	// t=30s: still recovered, 30 < 60 → hold.
	if res := eval(afHealthy, now.Add(30*time.Second)); res.Decision != DecisionStay {
		t.Fatalf("t=30s must hold satellite, got %s", res.Decision)
	}
	// t=40s: terrestrial RE-DEGRADES → recovery clock resets; hold satellite.
	if res := eval(afDegraded, now.Add(40*time.Second)); res.Decision != DecisionStay {
		t.Fatalf("t=40s re-degrade must hold satellite, got %s", res.Decision)
	}
	if !st.RecoveryObservedAt.IsZero() {
		t.Fatalf("re-degradation must reset the recovery clock")
	}
	// t=50s: recovers again → a FRESH recovery streak starts at t=50s.
	if res := eval(afHealthy, now.Add(50*time.Second)); res.Decision != DecisionStay {
		t.Fatalf("t=50s fresh recovery must hold satellite, got %s", res.Decision)
	}
	// t=90s: only 40s of continuous recovery (90-50) < 60 → STILL hold. An absolute
	// timer from the first recovery (t=0) would have switched back by now — this is
	// the bug the reset fixes.
	if res := eval(afHealthy, now.Add(90*time.Second)); res.Decision != DecisionStay {
		t.Fatalf("t=90s must still hold (timer reset on re-degrade), got %s (%s)", res.Decision, res.Reason)
	}
	// t=115s: 65s of continuous recovery (115-50) >= 60 → switch back.
	if res := eval(afHealthy, now.Add(115*time.Second)); res.Decision != DecisionSwitchback {
		t.Fatalf("t=115s must switch back after 60s continuous recovery, got %s (%s)", res.Decision, res.Reason)
	}
}
