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

// Package metrics defines custom Prometheus metrics for NTN Operators.
// Metrics are registered with the controller-runtime global registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// FailoverTotal counts failover events by slice, source path, and target path.
	FailoverTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_failover_total",
			Help: "Total number of failover events.",
		},
		[]string{"slice", "from_path", "to_path"},
	)

	// SatellitePassAvailable indicates whether a satellite pass window is active (1) or not (0).
	SatellitePassAvailable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_satellite_pass_available",
			Help: "Whether a satellite pass window is currently active (1=yes, 0=no).",
		},
		[]string{"ephemeris"},
	)

	// GroundStationHealth reports ground station condition status (1=True, 0=False, -1=Unknown).
	GroundStationHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_ground_station_health",
			Help: "Ground station condition status (1=True, 0=False, -1=Unknown).",
		},
		[]string{"station", "condition"},
	)

	// ConfigApplyErrorsTotal counts NTN cell config application failures.
	ConfigApplyErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_config_apply_errors_total",
			Help: "Total number of NTN cell config apply errors.",
		},
		[]string{"config", "provider"},
	)

	// GPFetchDuration tracks GP data fetch duration in seconds.
	GPFetchDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ntn_operators_gp_fetch_duration_seconds",
			Help:    "Duration of GP data fetch operations.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
		},
		[]string{"source_type", "status"},
	)

	// GPSatelliteCount reports the number of satellites in the latest GP fetch.
	GPSatelliteCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_gp_satellite_count",
			Help: "Number of satellites from the latest GP fetch.",
		},
		[]string{"ephemeris"},
	)

	// ReaderQueryDuration measures how long a single PromQL fetch made
	// through a pkg/slice/metrics Reader takes, split by source
	// ("prometheus") and outcome ("success" or "error"). A higher-level
	// Reader.Read call may perform several such fetches; this histogram
	// is observed once per fetch, not once per Read, so dashboards
	// should aggregate accordingly. Used to catch slow-path Prometheus
	// instances before the 2 s per-query timeout starts biting
	// reconciles.
	ReaderQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "ntn_metrics_reader_query_duration_seconds",
			Help: "Duration of a single PromQL fetch by a metrics reader, in seconds.",
			// Upper bucket matches the default per-query timeout
			// (pkg/slice/metrics.defaultPrometheusTimeout = 2s); any
			// observation landing above it is already in +Inf and an
			// outlier worth inspecting directly.
			Buckets: []float64{0.001, 0.005, 0.010, 0.025, 0.050, 0.100, 0.250, 0.500, 1.0, 2.0},
		},
		[]string{"source", "outcome"},
	)

	// ReaderErrorsTotal counts metrics-read failures by reason. Populated
	// by the Prometheus reader; the annotation reader never errors on
	// correctly-formed input. Cardinality bound: #sources × #reasons,
	// currently 1 × 4 = 4 series.
	ReaderErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_metrics_reader_errors_total",
			Help: "Count of metrics reader errors by source and reason.",
		},
		[]string{"source", "reason"},
	)

	// ReaderStaleUsedTotal counts reconciles served from the stale-value
	// cache per NTNSlice. A non-zero derivative is the operator's signal
	// that the underlying source has been degraded long enough to matter.
	//
	// Cardinality bound: one series per NTNSlice that has ever been
	// served stale, keyed by the stable {namespace, name} pair. The
	// series is explicitly removed via DeletePartialMatch inside
	// pkg/slice/metrics.Provider.Evict when the reconciler observes
	// NotFound for a CR, so the in-process series count tracks the
	// operator's current working set rather than growing monotonically.
	ReaderStaleUsedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_metrics_reader_stale_value_used_total",
			Help: "Count of reconciles per slice in which the reader served a stale cached value.",
		},
		[]string{"namespace", "name"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		FailoverTotal,
		SatellitePassAvailable,
		GroundStationHealth,
		ConfigApplyErrorsTotal,
		GPFetchDuration,
		GPSatelliteCount,
		ReaderQueryDuration,
		ReaderErrorsTotal,
		ReaderStaleUsedTotal,
	)
	// Note: logging here is intentionally omitted because init() runs
	// before controller-runtime's logger is configured. Registration
	// success is verified by the metrics_test.go suite.
}
