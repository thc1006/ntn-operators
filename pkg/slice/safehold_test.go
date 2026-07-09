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

import "testing"

// TestEvaluateSafeHold pins the fail-static contract: quality-driven transitions
// are suppressed (hold current path), but the orbital pass-end signal still forces
// a switchback off a satellite whose pass has ended. Metrics are intentionally NOT
// an input — the decision must not depend on (possibly stale) telemetry.
func TestEvaluateSafeHold(t *testing.T) {
	tests := []struct {
		name         string
		currentPath  PathType
		satAvailable bool
		wantDecision Decision
		wantTarget   PathType
	}{
		{
			name:         "satellite + pass ended -> switch back to terrestrial (orbital honored)",
			currentPath:  PathSatellite,
			satAvailable: false,
			wantDecision: DecisionSwitchback,
			wantTarget:   PathTerrestrial,
		},
		{
			name:         "satellite + pass active -> hold satellite (no quality-driven switchback)",
			currentPath:  PathSatellite,
			satAvailable: true,
			wantDecision: DecisionStay,
			wantTarget:   PathSatellite,
		},
		{
			name:         "terrestrial + satellite available -> hold terrestrial (no quality-driven failover)",
			currentPath:  PathTerrestrial,
			satAvailable: true,
			wantDecision: DecisionStay,
			wantTarget:   PathTerrestrial,
		},
		{
			name:         "terrestrial + satellite unavailable -> hold terrestrial",
			currentPath:  PathTerrestrial,
			satAvailable: false,
			wantDecision: DecisionStay,
			wantTarget:   PathTerrestrial,
		},
		{
			name:         "unavailable path is held (not spuriously moved)",
			currentPath:  PathUnavailable,
			satAvailable: true,
			wantDecision: DecisionStay,
			wantTarget:   PathUnavailable,
		},
		{
			name:         "unknown path normalizes to terrestrial",
			currentPath:  PathType("garbage"),
			satAvailable: false,
			wantDecision: DecisionStay,
			wantTarget:   PathTerrestrial,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateSafeHold(tc.currentPath, tc.satAvailable)
			if got.Decision != tc.wantDecision {
				t.Errorf("Decision = %q, want %q", got.Decision, tc.wantDecision)
			}
			if got.TargetPath != tc.wantTarget {
				t.Errorf("TargetPath = %q, want %q", got.TargetPath, tc.wantTarget)
			}
			if got.Reason == "" {
				t.Error("Reason must be set for observability")
			}
		})
	}
}
