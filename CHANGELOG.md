# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Per-neighbor idle-mode reselection** (#47): `NTNNeighborCell.reselectionInfo`
  carries `qHyst`, `qOffsetCell`, `sIntraSearchP`, `threshServingLowP` per
  3GPP TS 38.331 `IntraFreqCellReselectionInfo`, rendered via OCUDU's
  SIB2/SIB3 surface. SIB11 (connected-mode `MeasConfig`) is upstream-blocked
  — see `docs/adr/0001-sib11-measurement-config.md`.
- **SIB19 scheduling overrides** (#46): `CellOverrides.sibSchedule` with
  `siWindowLength`, `siPeriod`, `siWindowPosition` (previously hardcoded).
- **Table-driven TS 38.331 conformance test** mapping rendered YAML
  fragments back to their IE origin so future SIB additions extend by
  appending a row.
- **YAML string-injection defense**: renderer now `strconv.Quote`s
  user-controlled polarization strings and sanitizes the payloadType
  header-comment interpolation.
- **`neighborCells` bounded at MaxItems=32** with `+listType=map` for
  Server-Side Apply deduplication by `physicalCellID`.

### Changed (breaking — v1alpha1 pre-stable)
- **`spec.ntn.polarization`** (#45): flat `string` enum `linear|circular`
  replaced with nested `{dl, ul}` object accepting `rhcp|lhcp|linear`, per
  SIB19 `ntn-PolarizationDL-r17` / `ntn-PolarizationUL-r17` (independent
  IEs) and OCUDU's CLI11 schema. Existing CRs using the old form will be
  rejected; see `docs/migration-v0.2-v0.3.md` for the jq migration script.

### Fixed
- **Helm chart CRDs** now stay in sync with `config/crd/bases/` via the
  `manifests` make target — previously the chart shipped a stale copy and
  `helm install` of a v0.3 CR against a v0.2 chart would fail validation.

### Tracked (blocked on upstream OCUDU parser)
- **#52** — `k_mac` NTN-Config-r17 field (milestone v0.4)
- **#53** — `ta_common_drift_correction` Rel-18 field (milestone v0.4)

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
