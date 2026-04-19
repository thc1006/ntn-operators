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

func BenchmarkParseTrigger(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := ParseTrigger("rsrp < -120")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateFailover_Healthy(b *testing.B) {
	trigs := []string{"rsrp < -120", "latency > 200", "packetLoss > 5"}
	m := Metrics{RSRP: -90, LatencyMs: 20, PacketLossPercent: 0.1}
	t := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EvaluateFailoverWithContext(
			context.Background(),
			PathTerrestrial, trigs, m,
			true, 60*time.Second, time.Time{}, t,
		)
	}
}

func BenchmarkEvaluateFailover_Degraded(b *testing.B) {
	trigs := []string{"rsrp < -120", "latency > 200", "packetLoss > 5"}
	m := Metrics{RSRP: -130, LatencyMs: 20, PacketLossPercent: 0.1}
	t := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EvaluateFailoverWithContext(
			context.Background(),
			PathTerrestrial, trigs, m,
			true, 60*time.Second, time.Time{}, t,
		)
	}
}

func BenchmarkEvaluateFailoverWithHysteresis(b *testing.B) {
	trigs := []string{"rsrp < -120"}
	m := Metrics{RSRP: -115, LatencyMs: 20, PacketLossPercent: 0.1}
	t := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	lastFO := t.Add(-90 * time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EvaluateFailoverWithHysteresis(
			context.Background(),
			PathSatellite, trigs, m,
			true, 60*time.Second, lastFO, t, 10,
		)
	}
}
