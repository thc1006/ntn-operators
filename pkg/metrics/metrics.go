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
			Buckets: prometheus.DefBuckets,
		},
		[]string{"source_type"},
	)

	// GPSatelliteCount reports the number of satellites in the latest GP fetch.
	GPSatelliteCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_gp_satellite_count",
			Help: "Number of satellites from the latest GP fetch.",
		},
		[]string{"ephemeris"},
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
	)
}
