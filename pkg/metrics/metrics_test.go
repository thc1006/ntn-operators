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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

func TestMetricsRegistered(t *testing.T) {
	// Trigger all metrics with dummy values so Gather returns them.
	FailoverTotal.With(prometheus.Labels{"namespace": "_", "slice": "_", "from_path": "_", "to_path": "_"}).Add(0)
	SatellitePassAvailable.With(prometheus.Labels{"namespace": "_", "ephemeris": "_"}).Set(0)
	GroundStationHealth.With(prometheus.Labels{"namespace": "_", "station": "_", "condition": "_"}).Set(0)
	ConfigApplyErrorsTotal.With(prometheus.Labels{"namespace": "_", "config": "_", "provider": "_"}).Add(0)
	GPFetchDuration.With(prometheus.Labels{"source_type": "_", "status": "_"}).Observe(0)
	GPSatelliteCount.With(prometheus.Labels{"namespace": "_", "ephemeris": "_"}).Set(0)
	GPDeepSpaceRejectedCount.With(prometheus.Labels{"namespace": "_", "ephemeris": "_"}).Set(0)
	EphemerisEpochStaleCount.With(prometheus.Labels{"namespace": "_", "ephemeris": "_"}).Set(0)
	GPFetchReady.With(prometheus.Labels{"namespace": "_", "ephemeris": "_"}).Set(0)
	EphemerisPushErrorsTotal.With(prometheus.Labels{"namespace": "_", "config": "_", "reason": "_"}).Add(0)
	EphemerisPushReady.With(prometheus.Labels{"namespace": "_", "config": "_"}).Set(0)
	ReaderQueryDuration.With(prometheus.Labels{"source": "_", "outcome": "_"}).Observe(0)
	ReaderErrorsTotal.With(prometheus.Labels{"source": "_", "reason": "_"}).Add(0)
	ReaderStaleUsedTotal.With(prometheus.Labels{"namespace": "_", "name": "_"}).Add(0)

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	expected := []string{
		"ntn_operators_failover_total",
		"ntn_operators_satellite_pass_available",
		"ntn_operators_ground_station_health",
		"ntn_operators_config_apply_errors_total",
		"ntn_operators_gp_fetch_duration_seconds",
		"ntn_operators_gp_satellite_count",
		"ntn_operators_gp_deep_space_rejected_count",
		"ntn_operators_ephemeris_epoch_stale_count",
		"ntn_operators_gp_fetch_ready",
		"ntn_operators_ephemeris_push_errors_total",
		"ntn_operators_ephemeris_push_ready",
		"ntn_operators_reader_query_duration_seconds",
		"ntn_operators_reader_errors_total",
		"ntn_operators_reader_stale_value_used_total",
	}
	gathered := make(map[string]bool)
	for _, f := range families {
		gathered[f.GetName()] = true
	}
	for _, name := range expected {
		if !gathered[name] {
			t.Errorf("metric %q not found in gathered metrics", name)
		}
	}
}

func TestFailoverTotalIncrement(t *testing.T) {
	FailoverTotal.With(prometheus.Labels{
		"namespace": "default", "slice": "test-slice", "from_path": "terrestrial", "to_path": "satellite",
	}).Inc()

	m := &dto.Metric{}
	if err := FailoverTotal.With(prometheus.Labels{
		"namespace": "default", "slice": "test-slice", "from_path": "terrestrial", "to_path": "satellite",
	}).Write(m); err != nil {
		t.Fatal(err)
	}
	if m.GetCounter().GetValue() < 1 {
		t.Errorf("expected failover count >= 1, got %f", m.GetCounter().GetValue())
	}
}

func TestGPSatelliteCountGauge(t *testing.T) {
	labels := prometheus.Labels{"namespace": "test-ns", "ephemeris": "oneweb"}
	GPSatelliteCount.With(labels).Set(651)

	m := &dto.Metric{}
	if err := GPSatelliteCount.With(labels).Write(m); err != nil {
		t.Fatal(err)
	}
	if m.GetGauge().GetValue() != 651 {
		t.Errorf("expected 651, got %f", m.GetGauge().GetValue())
	}
}

func TestGPDeepSpaceRejectedCountGauge(t *testing.T) {
	labels := prometheus.Labels{"namespace": "test-ns", "ephemeris": "mixed-feed"}
	GPDeepSpaceRejectedCount.With(labels).Set(2)

	m := &dto.Metric{}
	if err := GPDeepSpaceRejectedCount.With(labels).Write(m); err != nil {
		t.Fatal(err)
	}
	if m.GetGauge().GetValue() != 2 {
		t.Errorf("expected 2, got %f", m.GetGauge().GetValue())
	}
}

func TestGPFetchDurationObserve(t *testing.T) {
	GPFetchDuration.With(prometheus.Labels{"source_type": "CelesTrak", "status": "success"}).Observe(1.5)

	// Gather from registry to verify histogram was recorded.
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		if f.GetName() == "ntn_operators_gp_fetch_duration_seconds" {
			for _, m := range f.GetMetric() {
				if m.GetHistogram().GetSampleCount() < 1 {
					t.Error("expected at least 1 observation")
				}
				found = true
			}
		}
	}
	if !found {
		t.Error("histogram metric not found in registry")
	}
}
