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

// TestAntiFlapAt_StaleReplayDoesNotConfirm: one degraded observation re-served N times under the
// SAME observation identity (a metrics-source outage re-serving its last-good value, or a
// status-conflict re-reconcile of the same read) must NOT satisfy N-consecutive confirmation.
// Before the identity gate, each call advanced the counter, so one degraded observation replayed
// three times tripped N=3 — defeating the "absorb a single-sample blip" contract.
func TestAntiFlapAt_StaleReplayDoesNotConfirm(t *testing.T) {
	cfg := AntiFlapConfig{ConfirmationSamples: 3}
	obs := now // ONE observation identity, re-served below
	var st AntiFlapState
	for i := 1; i <= 3; i++ {
		var res FailoverResult
		res, st = EvaluateFailoverWithAntiFlapAt(context.Background(), PathTerrestrial, triggers,
			afDegraded, true, afSwitchbackDelay, now, 0, cfg, st, obs)
		if res.Decision != DecisionStay {
			t.Fatalf("re-serve %d of the SAME degraded observation must not fail over, got %s (%s)", i, res.Decision, res.Reason)
		}
	}
	if st.ConsecutiveDegraded != 1 {
		t.Fatalf("one observation re-served 3x must count as 1 confirmation, got %d", st.ConsecutiveDegraded)
	}
}

// TestAntiFlapAt_CountsDistinctObservationsOnly walks the whole identity contract in one streak: a
// new observation advances the counter, re-serving it holds, and three DISTINCT degraded
// observations are required to confirm the failover.
func TestAntiFlapAt_CountsDistinctObservationsOnly(t *testing.T) {
	cfg := AntiFlapConfig{ConfirmationSamples: 3}
	var st AntiFlapState
	step := func(obs time.Time) FailoverResult {
		var res FailoverResult
		res, st = EvaluateFailoverWithAntiFlapAt(context.Background(), PathTerrestrial, triggers,
			afDegraded, true, afSwitchbackDelay, now, 0, cfg, st, obs)
		return res
	}

	o1 := now
	step(o1) // new observation → 1/3
	if st.ConsecutiveDegraded != 1 {
		t.Fatalf("first observation: want 1, got %d", st.ConsecutiveDegraded)
	}
	step(o1)
	step(o1) // same observation re-read twice → still 1/3
	if st.ConsecutiveDegraded != 1 {
		t.Fatalf("re-reading the same observation must not increment; got %d", st.ConsecutiveDegraded)
	}
	step(now.Add(time.Second)) // a new observation → 2/3
	if st.ConsecutiveDegraded != 2 {
		t.Fatalf("a new observation must increment; got %d", st.ConsecutiveDegraded)
	}
	res := step(now.Add(2 * time.Second)) // a third distinct observation → 3/3 → failover
	if st.ConsecutiveDegraded != 3 || res.Decision != DecisionFailover {
		t.Fatalf("third distinct degraded observation must confirm; count=%d decision=%s",
			st.ConsecutiveDegraded, res.Decision)
	}
}

// TestAntiFlapAt_ZeroObservationCountsEveryCall pins the back-compat contract: a zero observationAt
// (the plain EvaluateFailoverWithAntiFlap path the pure-evaluator unit tests use) disables the
// identity gate, so every call is a fresh sample — the pre-existing semantics are unchanged.
func TestAntiFlapAt_ZeroObservationCountsEveryCall(t *testing.T) {
	cfg := AntiFlapConfig{ConfirmationSamples: 3}
	var st AntiFlapState
	var res FailoverResult
	for range 3 {
		res, st = EvaluateFailoverWithAntiFlapAt(context.Background(), PathTerrestrial, triggers,
			afDegraded, true, afSwitchbackDelay, now, 0, cfg, st, time.Time{})
	}
	if st.ConsecutiveDegraded != 3 || res.Decision != DecisionFailover {
		t.Fatalf("zero observationAt must count every call; count=%d decision=%s", st.ConsecutiveDegraded, res.Decision)
	}
}
