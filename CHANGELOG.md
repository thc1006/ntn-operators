# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-04-17

### Added
- **4 CRDs**: SatelliteEphemeris, GroundStationLifecycle, NTNCellConfig, NTNSlice
- **CelesTrak GP fetcher**: OMM JSON with ETag caching, SGP4 pass prediction
- **SpaceTrack GP fetcher**: Cookie-based auth, session reuse, Secret credential reading
- **OCUDU/srsRAN provider**: ConfigMap generation for NTN gNB configuration
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
