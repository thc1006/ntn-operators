# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.0-rc.1] - 2026-04-21

Release candidate for 0.4.0 consolidating the v0.2 Core Hardening, v0.3
Dynamic Ephemeris, and v0.4 Production Readiness milestones into a single
publishable artifact. No breaking CRD changes vs 0.1.0 — every new field
is optional with the prior behaviour as its default.

### Added

**Dynamic ephemeris (v0.3)**
- SpaceTrack dynamic GP fetcher with Secret-backed credentials (#22, #23)
- SGP4 propagator — OMM → 3GPP ECEF conversion (#37 via #75)
- `NTNCellConfig.spec.ephemerisRef` — re-reconcile on SatelliteEphemeris update (#20)
- `NTNProvider.PushEphemerisUpdate` interface method (#20)

**Failover and metrics (v0.4)**
- Signal-hysteresis dead-band for trigger anti-flapping (#50)
- `NTNSlice.spec.metricsSource` pluggable metrics source — annotations or
  Prometheus, with per-UID stale-cache and reader-layer observability (#67)
- Synthetic `cmd/test-metrics-exporter` for deterministic E2E (#67 / #94)
- `--prometheus-allowed-endpoint-hosts` operator flag — optional SSRF
  allow-list for multi-tenant deployments (#93)
- `MetricsStale` / `EndpointNotAllowed` / `MetricsUnavailable` /
  `MetricsReaderError` failover-ready reasons, each distinct and actionable

**Production readiness (v0.4)**
- Helm NetworkPolicy template (#58)
- Controller benchmarks and `--max-concurrent-reconciles` (#59)
- Container image scanning (Trivy) + cosign signing + SPDX SBOM (#61, #62)
- Validating admission webhook for NTNSlice trigger syntax (#70)
- OLM bundle for OperatorHub publishing (#60; CSV replaces v0.1.0)

**Observability**
- `ntn_operators_reader_query_duration_seconds{source,outcome}` histogram
- `ntn_operators_reader_errors_total{source,reason}` counter (5 reasons)
- `ntn_operators_reader_stale_value_used_total{namespace,name}` counter;
  evicted on CR deletion
- Grafana dashboard (6 panels) shipped under `config/grafana/`

**API additions (all backward compatible)**
- SIB19 Stage 2/3 multi-cell NTN fields (#19 via #33 #34)
- `NTNCellConfig.spec.ntn.ephemerisOrbital` alternative (#21 via #30)
- `FailoverPolicy.hysteresisMargin` string field (#50)
- `NTNSlice.spec.metricsSource` block (#67)
- 12 CEL XValidation rules (was 7 at 0.1.0), including `MetricsSource`
  type-requires-prometheus, queries-non-empty, endpoint-URL-pattern

### Changed

- Controller decoupled from OCUDU-specific imports; `NTNProvider` interface
  gained `EnsureOwnership` / `Cleanup` methods (#72)
- Package-level structured logging across all runtime packages (#7, #39)
- NTNSlice reconciler now calls `ReaderProvider.For(ns).Read()` in place
  of the inline `readMetrics()` (#67); behaviour change is gated on
  `spec.metricsSource.type=prometheus` so existing annotation-driven CRs
  continue unchanged

### Fixed

- SpaceTrack credentials no longer interleave across concurrent reconciles (#8)
- `ConfigMapNameFor` adds hash suffix, preventing truncation collisions (#9)
- Firmware version sync drift → `UpdateInterrupted`/`Degraded` transition (#14)
- Global `os.Chdir` in test util removed (#16)
- `nodeToGroundStation` handles hashed labels for long namespace/name (#41)
- Chart.yaml stale `srsran` keyword removed (#42)
- Polarization schema aligned with OCUDU YAML + TS 38.331 enum values (#45)

### Security

- All CI action versions pinned to commit SHA (#17 via #27)
- Dependabot enabled for automated dependency updates (#63)
- SSRF allow-list for user-supplied Prometheus endpoints (#93)
- IP-level SSRF hardening lives in operator Pod NetworkPolicy, not in CRD
  schema — explicit non-goal per design doc `docs/design/metrics-source.md`

### Upgrade notes from 0.1.0

- **CRD schema is backward compatible**. Existing NTNSlice/NTNCellConfig
  CRs remain valid. No conversion webhook required.
- **NTNSlice behaviour** is unchanged unless `spec.metricsSource.type` is
  set to `prometheus`. Annotation-based simulation is still the default.
- **New operator flag** `--prometheus-allowed-endpoint-hosts` defaults to
  empty (permit-all) — existing deployments that did not set it before
  continue to work without change.
- **OLM upgrade** path is explicit: CSV `ntn-operators.v0.4.0-rc.1`
  declares `replaces: ntn-operators.v0.1.0`; the channel graph skips
  directly from 0.1.0 → 0.4.0-rc.1. No intermediate CSVs are published
  because v0.2 / v0.3 were never cut as standalone tags.

### Known deferrals

- `#49` multi-satellite failover — moved to v0.5 (engineering freeze)
- `#65` OAI gNB provider — moved to v1.0 (1-week+ scope)
- `#68` real antenna readiness probe — moved to v1.0 (requires hardware)
- `#69` SessionContinuity during failover — v1.0, paused
- `#47` / `#52` / `#53` — blocked on upstream OCUDU parser, v1.0

## [0.1.0] - 2026-04-17

### Added
- **4 CRDs**: SatelliteEphemeris, GroundStationLifecycle, NTNCellConfig, NTNSlice
- **CelesTrak GP fetcher**: OMM JSON with ETag caching, SGP4 pass prediction
- **SpaceTrack GP fetcher**: Cookie-based auth, session reuse, Secret credential reading
- **OCUDU provider**: ConfigMap generation for NTN gNB configuration
- **Failover engine**: Terrestrial-satellite path switching with switchback delay
- **QoS/Security/Billing**: Status reporting per active path in NTNSlice
- **CEL validation rules**: URL scheme, lat/lon range, path priority, ECEF non-zero, SpaceTrack credentials
- **Custom Prometheus metrics**: failover_total, satellite_pass_available, ground_station_health, config_apply_errors_total, gp_fetch_duration_seconds, gp_satellite_count
- **Firmware OTA**: Timeout detection (30min), phase stuck recovery
- **Helm chart**: CRDs, RBAC, Deployment, PodDisruptionBudget, ServiceMonitor
- **Release pipeline**: ko multi-arch build, GitHub Actions, Helm OCI push
- **Grafana dashboard**: 6 panels for all custom metrics
- **API reference**: Auto-generated from CRDs via crdoc
- **E2E tests**: Multi-CRD workflow, deletion/finalizer cleanup
- **SSRF protection**: TCP-dial-level IP validation, redirect target validation
- **Security docs**: GOVERNANCE.md, SECURITY.md, CONTRIBUTING.md

### Security
- SSRF-safe HTTP client (private IP blocking at dial level)
- Cross-namespace write prevention (provider.namespace forced to CR namespace)
- ETag cache race condition fix (sync.Map)
- ConfigMap OwnerReference for garbage collection
- CEL validation (no webhook infrastructure needed)
- Secret RBAC for SpaceTrack credentials

### Known Limitations
- Failover metrics are annotation-based (simulated); UPF/Prometheus integration planned for v0.2
- Only OCUDU provider implemented; OAI and Aalyria planned for v0.2
- AntennaReady condition simulated (requires vendor-specific hardware agent)
- SessionContinuity tracked but not enforced at data plane
- Firmware updates require external node agent for actual OTA
