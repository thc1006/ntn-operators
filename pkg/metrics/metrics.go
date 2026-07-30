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
	// FailoverTotal is keyed by namespace+slice (NTNSlice is namespaced, so the
	// slice name alone is not unique) plus the source/target paths.
	FailoverTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_failover_total",
			Help: "Total number of failover events.",
		},
		[]string{"namespace", "slice", "from_path", "to_path"},
	)

	// SatellitePassAvailable indicates whether a satellite pass window is active
	// (1) or not (0). Keyed by namespace+ephemeris: the ephemeris ref is resolved
	// within the NTNSlice's namespace, so the same ref name in two namespaces is
	// two distinct ephemerides and must not share a series.
	SatellitePassAvailable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_satellite_pass_available",
			Help: "Whether a satellite pass window is currently active (1=yes, 0=no).",
		},
		[]string{"namespace", "ephemeris"},
	)

	// GroundStationHealth reports ground station condition status (1=True, 0=False, -1=Unknown).
	// Keyed by namespace+station: GroundStationLifecycle is namespaced, so the same
	// CR name in two namespaces is two distinct resources and must not share a
	// series. (#183; supersedes the earlier namespace+"."+name composite `station`
	// value, whose bare-name delete never matched → a permanent series leak.)
	GroundStationHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_ground_station_health",
			Help: "Ground station condition status (1=True, 0=False, -1=Unknown).",
		},
		[]string{"namespace", "station", "condition"},
	)

	// ConfigApplyErrorsTotal counts NTN cell config application failures.
	// Keyed by namespace+config: NTNCellConfig is namespaced, so keying by config
	// name alone conflated same-named CRs across namespaces (and wiped them on
	// delete). Mirrors the #178/#184 namespace fix (#183).
	ConfigApplyErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_config_apply_errors_total",
			Help: "Total number of NTN cell config apply errors.",
		},
		[]string{"namespace", "config", "provider"},
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
	// Keyed by namespace+ephemeris: SatelliteEphemeris is namespaced, so the same
	// CR name in two namespaces is two distinct resources and must not share a
	// series (nor wipe each other's on delete). Mirrors the #178 namespace fix.
	GPSatelliteCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_gp_satellite_count",
			Help: "Number of satellites from the latest GP fetch.",
		},
		[]string{"namespace", "ephemeris"},
	)

	// GPDeepSpaceRejectedCount reports how many TRACKED element sets the latest
	// reconcile rejected as deep-space (orbital period >= 225 min) because the
	// near-earth SGP4 propagator cannot handle them (findings.md B-5; v1.0 is
	// LEO-only). It is the durable, alertable signal that a fleet's MEO/GEO birds
	// are being dropped — the UnsupportedOrbitRegime event is transition-gated and
	// ages out of etcd, so alerting keys on this gauge (> 0). Same namespace+
	// ephemeris keying and delete-on-CR-removal as GPSatelliteCount.
	GPDeepSpaceRejectedCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_gp_deep_space_rejected_count",
			Help: "Number of tracked element sets rejected as deep-space (unsupported orbit regime) in the latest reconcile.",
		},
		[]string{"namespace", "ephemeris"},
	)

	// EphemerisEpochStaleCount reports how many tracked element sets have an epoch
	// older than the freshness bound (findings.md I-17/I-19). It is the durable,
	// alertable pairing of the EphemerisEpochStale condition — a fleet propagating
	// stale elements pushes a drifting ECEF into SIB19. Alert on > 0. Same
	// namespace+ephemeris keying and delete-on-CR-removal as GPSatelliteCount.
	EphemerisEpochStaleCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_ephemeris_epoch_stale_count",
			Help: "Number of tracked element sets whose epoch is older than the freshness bound in the latest reconcile.",
		},
		[]string{"namespace", "ephemeris"},
	)

	// EphemerisPropagationFailedCount is the number of tracked element sets that failed SGP4
	// propagation to ECEF in the latest reconcile (OMM->TLE conversion, propagation, or ECEF range
	// validation). These satellites are dropped from propagatedStates, so without this signal a
	// non-propagatable element set looks like a generic downstream PayloadMissing.
	EphemerisPropagationFailedCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_ephemeris_propagation_failed_count",
			Help: "Number of tracked element sets that failed SGP4 propagation to ECEF in the latest reconcile.",
		},
		[]string{"namespace", "ephemeris"},
	)

	// GPFetchReady is 1 when the latest reconcile obtained usable GP data (a fresh
	// fetch, a 304 Not Modified, or a served cache) and 0 when it could not (fetcher
	// setup failed, the fetch errored with no cache to fall back on, or an insecure
	// URL was refused). It is the alertable companion to GPSatelliteCount:
	// `gp_satellite_count == 0` cannot fire on a cold start that has NEVER fetched
	// (the series is absent, and PromQL `== 0` never matches an absent series), but
	// this gauge is set to 0 on that first failed reconcile, so `gp_fetch_ready == 0`
	// for N minutes catches a GP pipeline that has never come up. Same
	// namespace+ephemeris keying and delete-on-CR-removal as GPSatelliteCount.
	GPFetchReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_gp_fetch_ready",
			Help: "1 if the latest GP reconcile obtained usable element-set data (fresh/304/cache), 0 if it failed.",
		},
		[]string{"namespace", "ephemeris"},
	)

	// EphemerisPushErrorsTotal counts runtime NTN-ephemeris push failures to the
	// gNB, split by reason (findings.md I-19). ConfigApplyErrorsTotal covers only
	// the static ConfigMap apply path, leaving the v0.6 runtime WebSocket push
	// (#176/#177) with no error signal to alert on. Keyed by namespace+config.
	EphemerisPushErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_ephemeris_push_errors_total",
			Help: "Total runtime NTN ephemeris push failures to the gNB, by reason.",
		},
		[]string{"namespace", "config", "reason"},
	)

	// EphemerisPushReady is 1 when the CR's runtime NTN ephemeris push to the gNB is
	// currently healthy (pushed, already up to date, or no push failure this cycle)
	// and 0 when the last push failed. EphemerisPushErrorsTotal is a per-failure
	// COUNTER, so its rate alert (`increase(...[15m]) > 0`) only sustains while a
	// TRANSIENT push keeps tight-requeuing every minute; a PERMANENT reason
	// (EphemerisRefNotFound / EphemerisPayloadMissing / EphemerisStale /
	// ProviderPushRejected) does not requeue and increments the counter only once, so
	// the rate alert decays after the window. This gauge holds 0 across the whole
	// outage regardless of requeue, so `ephemeris_push_ready == 0` for N minutes
	// alerts on the permanent push failures the counter cannot (issue #216). Emitted
	// only for CRs that configure a runtime push; keyed namespace+config, deleted on
	// CR removal (and cleared when a CR stops configuring a push).
	EphemerisPushReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ntn_operators_ephemeris_push_ready",
			Help: "1 if the CR's runtime NTN ephemeris push to the gNB is currently healthy, 0 if the last push failed.",
		},
		[]string{"namespace", "config"},
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
			Name: "ntn_operators_reader_query_duration_seconds",
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
	// currently 1 × 5 = 5 series (query_error, empty_vector,
	// ambiguous_vector, non_finite, unsupported_type).
	ReaderErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_reader_errors_total",
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
			Name: "ntn_operators_reader_stale_value_used_total",
			Help: "Count of reconciles per slice in which the reader served a stale cached value.",
		},
		[]string{"namespace", "name"},
	)

	// EphemerisMapperFallbackTotal counts times the SatelliteEphemeris->NTNCellConfig mapper
	// fell back to a full namespace scan because the spec.ephemerisRef index was unavailable.
	// A persistent nonzero rate in production means the field index is not registered (an
	// O(namespace) perf regression that the V(1) fallback log would otherwise hide at the
	// default verbosity).
	EphemerisMapperFallbackTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ntn_operators_ephemeris_mapper_index_fallback_total",
			Help: "Times the ephemeris->cell mapper fell back to a full namespace scan (spec.ephemerisRef index unavailable).",
		},
		[]string{"namespace"},
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
		GPDeepSpaceRejectedCount,
		EphemerisEpochStaleCount,
		EphemerisPropagationFailedCount,
		GPFetchReady,
		EphemerisPushErrorsTotal,
		EphemerisPushReady,
		ReaderQueryDuration,
		ReaderErrorsTotal,
		ReaderStaleUsedTotal,
		EphemerisMapperFallbackTotal,
	)
	// Note: logging here is intentionally omitted because init() runs
	// before controller-runtime's logger is configured. Registration
	// success is verified by the metrics_test.go suite.
}
