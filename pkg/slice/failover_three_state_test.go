/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package slice

import (
	"context"
	"strings"
	"testing"
	"time"
)

// inertPacketLoss is a metrics snapshot where packetLoss has no live source (inert),
// but rsrp/latency are healthy — so a `packetLoss > 5` trigger is unknown.
var inertPacketLoss = Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1, PacketLossMissing: true}

// TestHysteresis_AllInertTriggers_HoldsOnSatellite pins the inert-trigger
// vacuous-recovery fix: on satellite, if the only trigger is inert, the empty parsed
// set must NOT be read as "recovered" (which would switch traffic back onto a link
// whose quality is unverifiable). Unknown != recovered → HOLD.
func TestHysteresis_AllInertTriggers_HoldsOnSatellite(t *testing.T) {
	res := EvaluateFailoverWithHysteresis(
		context.Background(), PathSatellite, []string{"packetLoss > 5"}, inertPacketLoss,
		true, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if res.Decision != DecisionStay || res.TargetPath != PathSatellite {
		t.Fatalf("an all-inert trigger set must HOLD on satellite (recovery unconfirmed), got %s→%s: %s",
			res.Decision, res.TargetPath, res.Reason)
	}
}

// TestBase_AllInertTriggers_HoldsOnSatellite pins the same fix on the no-hysteresis
// (base) evaluator, which the anti-flap wrapper delegates to when margin <= 0.
func TestBase_AllInertTriggers_HoldsOnSatellite(t *testing.T) {
	res := EvaluateFailoverWithContext(
		context.Background(), PathSatellite, []string{"packetLoss > 5"}, inertPacketLoss,
		true, 60*time.Second, now.Add(-90*time.Second), now,
	)
	if res.Decision != DecisionStay || res.TargetPath != PathSatellite {
		t.Fatalf("all-inert must HOLD on satellite in the base path too, got %s→%s: %s",
			res.Decision, res.TargetPath, res.Reason)
	}
}

// TestHysteresis_AllInert_PassEnded_ForcesSwitchback confirms the hold is overridden
// by the orbital reality: if the satellite pass has physically ended, we must return
// to terrestrial even though recovery is unconfirmed (staying would black-hole traffic).
func TestHysteresis_AllInert_PassEnded_ForcesSwitchback(t *testing.T) {
	res := EvaluateFailoverWithHysteresis(
		context.Background(), PathSatellite, []string{"packetLoss > 5"}, inertPacketLoss,
		false /* pass ended */, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if res.Decision != DecisionSwitchback {
		t.Fatalf("a physically-ended pass must force switchback even with unknown triggers, got %s: %s",
			res.Decision, res.Reason)
	}
}

// TestHysteresis_MixedKnownRecoveredAndInert_Holds: rsrp has recovered past the
// dead-band, but packetLoss is inert. Recovery of the WHOLE policy is unconfirmed, so
// the conservative decision is to hold — a partial view is not a recovered view.
func TestHysteresis_MixedKnownRecoveredAndInert_Holds(t *testing.T) {
	m := Metrics{RSRP: -105, LatencyMs: 20, PacketLossPercent: 0.1, PacketLossMissing: true}
	res := EvaluateFailoverWithHysteresis(
		context.Background(), PathSatellite, []string{"rsrp < -120", "packetLoss > 5"}, m,
		true, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if res.Decision != DecisionStay {
		t.Fatalf("a mix of recovered + unknown triggers must HOLD (unknown != recovered), got %s: %s",
			res.Decision, res.Reason)
	}
}

// TestAntiFlap_DeadBandDoesNotConsumeSwitchbackDelay pins the hysteresis-aware
// recovery clock: the switchback delay must measure continuous PAST-hysteresis
// recovery, not time spent sitting in the dead-band. rsrp < -120, margin 10 →
// recovery threshold -110; delay 60s.
func TestAntiFlap_DeadBandDoesNotConsumeSwitchbackDelay(t *testing.T) {
	trg := []string{"rsrp < -120"}
	const margin = 10.0
	deadBand := Metrics{RSRP: -115, LatencyMs: 20, PacketLossPercent: 0.1}  // above -120, below -110
	recovered := Metrics{RSRP: -105, LatencyMs: 20, PacketLossPercent: 0.1} // above -110

	st := AntiFlapState{}
	var res FailoverResult

	// Sit in the dead-band longer than the switchback delay. The recovery clock must
	// stay unset — the dead-band is not recovery.
	res, st = EvaluateFailoverWithAntiFlap(context.Background(), PathSatellite, trg, deadBand, time.Time{},
		true, 60*time.Second, now, margin, AntiFlapConfig{}, st)
	if res.Decision != DecisionStay {
		t.Fatalf("dead-band must stay on satellite, got %s: %s", res.Decision, res.Reason)
	}
	if !st.RecoveryObservedAt.IsZero() {
		t.Fatal("the recovery clock must NOT start in the dead-band (H3)")
	}
	res, st = EvaluateFailoverWithAntiFlap(context.Background(), PathSatellite, trg, deadBand, time.Time{},
		true, 60*time.Second, now.Add(120*time.Second), margin, AntiFlapConfig{}, st)
	if !st.RecoveryObservedAt.IsZero() {
		t.Fatal("still in dead-band 120s later: the recovery clock must remain unset (H3)")
	}

	// Cross the margin. The clock starts NOW, so the 60s delay is not yet elapsed → hold.
	tCross := now.Add(121 * time.Second)
	res, st = EvaluateFailoverWithAntiFlap(context.Background(), PathSatellite, trg, recovered, time.Time{},
		true, 60*time.Second, tCross, margin, AntiFlapConfig{}, st)
	if res.Decision != DecisionStay {
		t.Fatalf("the switchback delay must be measured from the past-hysteresis recovery, not the "+
			"dead-band entry — expected Stay, got %s: %s", res.Decision, res.Reason)
	}
	if !st.RecoveryObservedAt.Equal(tCross) {
		t.Fatalf("the recovery clock must start at the margin cross, got %v want %v", st.RecoveryObservedAt, tCross)
	}

	// After a full switchback delay of continuous past-hysteresis recovery → switchback.
	res, _ = EvaluateFailoverWithAntiFlap(context.Background(), PathSatellite, trg, recovered, time.Time{},
		true, 60*time.Second, tCross.Add(60*time.Second), margin, AntiFlapConfig{}, st)
	if res.Decision != DecisionSwitchback {
		t.Fatalf("after switchbackDelay of continuous past-hysteresis recovery, expected Switchback, got %s: %s",
			res.Decision, res.Reason)
	}
}

// invalidExponentTrigger is syntactically well-formed and PASSES CEL admission (the
// pattern allows an unbounded exponent) yet is invalid at runtime: strconv.ParseFloat
// overflows to +Inf → ParseTrigger errors → the set is allInvalid. It is the realistic
// production input for the pass-ended-vs-allInvalid ordering test below.
const invalidExponentTrigger = "rsrp < 1e999"

// TestBase_AllInvalid_PassEnded_ForcesSwitchback is the P1 regression: an all-invalid
// trigger set on a satellite whose pass has ENDED must return to terrestrial. Before the
// fix the allInvalid guard ran BEFORE the pass-ended check and returned Stay(satellite) —
// a traffic black hole on a satellite that has physically set. Orbital reality precedes
// trigger validity. (Mutation check: revert the reorder → this Stays on satellite.)
func TestBase_AllInvalid_PassEnded_ForcesSwitchback(t *testing.T) {
	res := EvaluateFailoverWithContext(
		context.Background(), PathSatellite, []string{invalidExponentTrigger}, inertPacketLoss,
		false /* pass ended */, 60*time.Second, now.Add(-90*time.Second), now,
	)
	if res.Decision != DecisionSwitchback || res.TargetPath != PathTerrestrial {
		t.Fatalf("all-invalid triggers on an ENDED pass must switch back to terrestrial, not black-hole on "+
			"the set satellite, got %s→%s: %s", res.Decision, res.TargetPath, res.Reason)
	}
}

// TestHysteresis_AllInvalid_PassEnded_ForcesSwitchback pins the same ordering in the
// hysteresis evaluator (allInvalid guard was at line 508, pass-ended at 517).
func TestHysteresis_AllInvalid_PassEnded_ForcesSwitchback(t *testing.T) {
	res := EvaluateFailoverWithHysteresis(
		context.Background(), PathSatellite, []string{invalidExponentTrigger}, inertPacketLoss,
		false /* pass ended */, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if res.Decision != DecisionSwitchback || res.TargetPath != PathTerrestrial {
		t.Fatalf("all-invalid + ended pass must switch back to terrestrial in the hysteresis path too, "+
			"got %s→%s: %s", res.Decision, res.TargetPath, res.Reason)
	}
}

// TestPassEnded_UnknownTrigger_HonestReason pins the honest reason (Finding 3): when the
// pass ends while recovery is unverifiable (an unknown/unsourced trigger), the reason must
// NOT claim "terrestrial recovered" — the decision (leave the set satellite) is right, but
// the condition must not mislead diagnosis.
func TestPassEnded_UnknownTrigger_HonestReason(t *testing.T) {
	res := EvaluateFailoverWithHysteresis(
		context.Background(), PathSatellite, []string{"packetLoss > 5"}, inertPacketLoss,
		false /* pass ended */, 60*time.Second, now.Add(-90*time.Second), now, 10,
	)
	if res.Decision != DecisionSwitchback {
		t.Fatalf("ended pass must switch back, got %s: %s", res.Decision, res.Reason)
	}
	if !strings.Contains(res.Reason, "unconfirmed") || strings.Contains(res.Reason, "recovered") {
		t.Fatalf("pass-ended reason must be honest about unconfirmed recovery, not claim 'recovered': %q", res.Reason)
	}
}

// TestBase_UnknownTrigger_Init_PrefersSatellite pins Finding 2: initialising from an
// unavailable/unknown path with unknown terrestrial telemetry must NOT be read as
// "terrestrial healthy". A CONFIRMED-available satellite is preferred over an unproven
// terrestrial assumption.
func TestBase_UnknownTrigger_Init_PrefersSatellite(t *testing.T) {
	res := EvaluateFailoverWithContext(
		context.Background(), PathUnavailable, []string{"packetLoss > 5"}, inertPacketLoss,
		true /* satellite available */, 60*time.Second, time.Time{}, now,
	)
	if res.Decision != DecisionFailover || res.TargetPath != PathSatellite {
		t.Fatalf("init with unknown terrestrial telemetry + a confirmed satellite must use the satellite, "+
			"not init on unproven terrestrial, got %s→%s: %s", res.Decision, res.TargetPath, res.Reason)
	}
}

// TestBase_UnknownTrigger_Init_NoSatellite_FallsBackTerrestrial is the calibration of
// Finding 2: with NO satellite, we still fall back to terrestrial (the primary) rather
// than park on PathUnavailable — parking would black-hole traffic on an unproven
// assumption. The reason must be honest that quality is unconfirmed.
func TestBase_UnknownTrigger_Init_NoSatellite_FallsBackTerrestrial(t *testing.T) {
	res := EvaluateFailoverWithContext(
		context.Background(), PathUnavailable, []string{"packetLoss > 5"}, inertPacketLoss,
		false /* no satellite */, 60*time.Second, time.Time{}, now,
	)
	if res.Decision != DecisionSwitchback || res.TargetPath != PathTerrestrial {
		t.Fatalf("init with unknown telemetry and no satellite must fall back to terrestrial, not park on "+
			"unavailable, got %s→%s: %s", res.Decision, res.TargetPath, res.Reason)
	}
	if !strings.Contains(res.Reason, "unconfirmed") {
		t.Fatalf("the terrestrial-fallback reason must be honest that quality is unconfirmed: %q", res.Reason)
	}
}

// TestConfirmedRecovered_AllInvalid_NotRecovered pins the cheap half of Finding 6: an
// all-invalid (broken) policy must NOT count as a confirmed recovery (which would
// pre-start the switchback recovery clock). An EMPTY policy still trivially recovers.
func TestConfirmedRecovered_AllInvalid_NotRecovered(t *testing.T) {
	healthy := Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}
	if terrestrialConfirmedRecovered([]string{invalidExponentTrigger}, healthy, 10) {
		t.Fatal("an all-invalid policy must NOT be treated as confirmed-recovered")
	}
	if !terrestrialConfirmedRecovered(nil, healthy, 10) {
		t.Fatal("an empty policy must remain trivially confirmed-recovered")
	}
	if !terrestrialConfirmedRecovered([]string{"rsrp < -120"}, healthy, 10) {
		t.Fatal("a valid, past-margin-recovered policy must be confirmed-recovered")
	}
}

// TestAntiFlap_InitToTerrestrial_DoesNotStartDwell pins M1: initialising onto
// terrestrial from an unavailable path emits DecisionSwitchback, but it is NOT a
// quality-driven hand-back, so it must not start the min-dwell clock — otherwise a
// subsequent real degradation would be wrongly dwell-blocked.
func TestAntiFlap_InitToTerrestrial_DoesNotStartDwell(t *testing.T) {
	cfg := AntiFlapConfig{MinTerrestrialDwell: 90 * time.Second}
	healthy := Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}

	res, st := EvaluateFailoverWithAntiFlap(context.Background(), PathUnavailable, triggers, healthy, time.Time{},
		true, 60*time.Second, now, 0, cfg, AntiFlapState{})
	if res.Decision != DecisionSwitchback {
		t.Fatalf("init from unavailable → terrestrial expected Switchback, got %s: %s", res.Decision, res.Reason)
	}
	if !st.LastSwitchback.IsZero() {
		t.Fatal("init-to-terrestrial must NOT start the min-dwell clock (M1)")
	}

	// A real degradation immediately after must be free to fail over, not dwell-blocked.
	degraded := Metrics{RSRP: -130, LatencyMs: 20, PacketLossPercent: 0.1}
	res2, _ := EvaluateFailoverWithAntiFlap(context.Background(), PathTerrestrial, triggers, degraded, time.Time{},
		true, 60*time.Second, now.Add(time.Second), 0, cfg, st)
	if res2.Decision != DecisionFailover {
		t.Fatalf("a real degradation after init must fail over (not be dwell-blocked), got %s: %s",
			res2.Decision, res2.Reason)
	}
}
