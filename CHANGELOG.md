# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Config-emitter correctness and runtime NTN push, verified end-to-end against a
real OCUDU gNB (commit `0b229d35`).

### Fixed

- **OCUDU config never parsed on a real gNB.** The generated `ntn:` block sat at
  the top level; OCUDU (`config_extras_mode::error`) rejects unknown top-level
  keys. It is now nested under `cell_cfg.ntn` with the exact keys OCUDU expects
  (`pci`/`carrier_freq`, `feeder_link`, `gateway_location`, `ncells`, …).
- **Systematic value-unit mismatch.** The emitter wrote raw 3GPP codepoints where
  OCUDU expects physical SI. Positions/velocities/TA/angles/eccentricity are now
  converted (e.g. `pos_x = codepoint × 1.3 m`, angles → radians), with CRD ranges
  tightened so a codepoint can never exceed OCUDU's accepted physical range.
- **SIB19-only schedule was rejected by the gNB.** OCUDU requires a SIB with
  ID < 15 alongside SIB19; the emitter now schedules SIB2 (`schedulingInfoList`)
  before SIB19 (`schedulingInfoList2`), so the config boots and broadcasts SIB19.
- **Pass-window failover blindness (#C1):** a global propagation cap could starve
  some satellites of predicted passes; the cap is now per-satellite.
- **Prometheus series leak (#C3):** per-CR metric series are now cleaned up
  (`DeletePartialMatch`) in the finalizers on CR deletion.
- **Runtime-push stale-epoch tight loop:** a past/expired ephemeris epoch is now
  skipped (not pushed) and permanent gNB rejections no longer tight-requeue.
- **Runtime-push epoch accuracy:** the pushed ephemeris epoch is now stamped
  near-now (not up to a refresh-interval ahead). OCUDU internally re-propagates
  the state vector from its epoch, so a far-future epoch forced a long backward
  propagation and compounded LEO error; a near-now epoch keeps it short/forward.
- An unparseable gNB WebSocket reply is now a retryable failure instead of a
  silent success.

### Added

- **Runtime NTN config push (#176).** SGP4-propagated ECEF now flows to the gNB
  live via OCUDU's `ntn_config_update` remote-control WebSocket (MR !798) instead
  of being discarded: `SatelliteEphemeris.status.propagatedStates`,
  `NTNCellConfig.spec.cellID` + `spec.provider.remoteControl`, and the provider
  `PushRuntimeUpdate` transport.
- **`k_mac` (3GPP `kmac-r17`, #52)** via the runtime `sat_switch_with_resync`
  block — the only OCUDU surface that accepts it. `SatSwitchNTNConfig.kMac`
  (1..512) is delivered by the runtime push.
- **ECEF velocity range validation (#C2)** on propagated/emitted state vectors.

### Changed

- **BREAKING (v1alpha1) — `NTNCellConfig.spec.ntn.satSwitchWithResync` redesigned.**
  Replaced the inert `{targetPCI, t304}` (which mapped to nothing in OCUDU and was
  silently dropped) with OCUDU's `sat_switch_with_resync` structure
  (`ntnConfig`, `epochUnixMs`, `tServiceStartUnixMs`, `ssbTimeOffsetSubframes`,
  `gatewayLocation`). It is delivered at runtime, never emitted to static YAML.
- **BREAKING (v1alpha1) — `sibSchedule.siWindowPosition` minimum is now 2** (was 0):
  SIB19 must be scheduled after the mandatory SIB2 entry, so 0/1 produced an
  unbootable config.
- `provider.remoteControl.endpoint` now validates a bare `host:port` (no scheme
  or path): a value like `ws://host:8001` is rejected at admission instead of
  failing to dial and requeuing forever.

### Notes

- Issue #53 (`ta-CommonDriftCorrection`, Rel-18) remains tracked-only: OCUDU still
  exposes no parser hook (YAML or runtime) as of this change.

## [0.5.0-rc.1] - 2026-05-27

### Changed

**NTNCellConfig schema — OCUDU `ntn_config` alignment (#144)**
- `spec.ntn.cellSpecificKoffset` minimum tightened from `0` to `1`. The 3GPP IE
  `cellSpecificKoffset-r17` is `INTEGER(0..1023)` and permits `0`, but OCUDU
  rejects `0` (its CLI and config validation enforce 1-1023), so the CRD mirrors
  the backend rather than the spec. An explicit `cellSpecificKoffset: 0` is now
  rejected at admission; omitting the field still defaults to `150` (the typed
  zero value is dropped by `omitempty`, so only an explicit `0` reaches the
  validation).
- Field documentation now states the unit correctly: the value is in
  **milliseconds** (TS 38.213 / TS 38.300 §16.14.2). OCUDU stores
  `cell_specific_koffset` as `std::chrono::milliseconds` and converts it to
  operating-SCS slots internally, so the integer passes through this operator
  unchanged — no unit conversion. (3GPP expresses K_offset as a slot count
  assuming the 15 kHz reference SCS, where 1 slot = 1 ms; that is only how the
  IE is defined, not a conversion applied here.)
- This brings the CRD into alignment with 7 of the 8 fields in OCUDU's
  `ntn_config` struct (`ntnUlSyncValidityDur`, `polarization`, `taReport`,
  `taInfo.taCommonDrift*`, `epochTime`, `ephemeris*`, and `cellSpecificKoffset`
  were verified present and correctly rendered). The 8th field, `k_mac`, stays
  intentionally deferred pending upstream OCUDU MR!597 reassessment (tracked in
  #52); no schema change for it here.

**Dependencies / CI**
- Bumped the k8s.io stack to **0.36.0** (api, apimachinery, client-go + indirect
  apiextensions-apiserver / apiserver / component-base) with
  `sigs.k8s.io/controller-runtime` **0.23.3 → 0.24.1** (#150). controller-runtime
  0.24 deprecates `controller-runtime/pkg/scheme`, so the api package's scheme
  registration was migrated to the `k8s.io/apimachinery/pkg/runtime.NewSchemeBuilder`
  pattern (current kubebuilder v4 scaffold). No CRD/deepcopy or runtime behavior change.
- Bumped `sigstore/cosign-installer` 3.8.2 → **4.1.2**, with the cosign **binary
  pinned to `v2.6.3`** (#149). Installer v4 defaults to cosign v3.x, which would
  change the published signature/attestation format (OCI 1.1 referrers + new
  protobuf bundle) vs v0.4.0; pinning v2.x keeps the release format-consistent.
  The deliberate cosign v3 migration is tracked in #148.
- Bumped `onsi/ginkgo/v2` 2.28.1 → 2.28.2 (#133), `aquasecurity/trivy-action`
  0.35.0 → 0.36.0 (#127), `docker/login-action` 4.1.0 → 4.2.0 (#145),
  `anchore/sbom-action` 0.18.0 → 0.24.0 (#126, the syft used for the release
  SBOM), and Nephio KRM mutators `set-namespace` 0.4.5 / `set-labels` 0.2.4
  (#118 / #134).

### Added

- E2E suite can run against an existing cluster instead of always provisioning a
  Kind cluster (#135).
- OLM ClusterServiceVersion description and `certified` annotation, with
  `createdAt` aligned to the 2026-04 OperatorHub convention (#124).

## [0.4.0] - 2026-04-27

Promotion of `0.4.0-rc.1` after a 6-day soak. No CRD or controller breaking
changes vs rc.1; all additions are backward compatible.

### Added

**Nephio R6 distribution (#51 / #112)**
- `nephio/packages/ntn-operators-crds` — kpt-installable CRD-only package
- `nephio/packages/ntn-workloads-sample` — kpt-installable sample CRs with
  `set-namespace` + `set-labels` mutator pipeline
- `make nephio-{install-tools,sync,render,validate,verify-sync}` targets
- ADR 0003 — Nephio integration design rationale
- README Quick Start Option D — Nephio install path for first-time visitors

**Supply-chain hardening (#114 / #117)**
- All Kptfile pipeline mutator images pinned by `@sha256:` digest
  (`set-namespace@sha256:f930…`, `set-labels@sha256:cce5…`)
- `hack/check-kptfile-digest-pin.sh` enforces digest pinning, mirroring
  the action-SHA discipline established in `hack/check-action-shas.sh` (#109)
- `test/nephio/validate.sh` T15 wires the check into the validate suite

**CI gating (#119 / #121)**
- `.github/workflows/nephio.yml` — dedicated PR/push gate running
  `make nephio-install-tools` → `nephio-verify-sync` → `nephio-validate`
- T7 / T12 reimplemented as hermetic python-yaml structural checks
  (replaces `kubectl apply --dry-run=client`, which required cluster API
  discovery and failed on hermetic CI runners)

### Changed

- Reconciler 304 status-not-updated bug fix (#110) — SatelliteEphemeris
  controller now correctly persists status when the upstream returns 304
- `pkg/netutil/allowlist.go` — private-host SSRF allow-list, OWASP
  Case 1 hardening (#110)
- E2E uses in-cluster CelesTrak mock to remove network flake (#105 / #110)
- OMM test fixture deduplicated between unit and e2e (#111)

### CI hardening (no operator behavior change)

- E2E path filter narrowed; release.yml dry-run dispatch (#102 / #103 / #104)
- Trivy weekly scheduled scan on main (#108)
- Action SHA validator catches non-SHA refs at PR time (#107 / #109)

### Upgrade notes from 0.4.0-rc.1

- **No CRD or runtime behavior change** vs rc.1 + the #110 reconciler 304 fix
- **OLM upgrade**: `spec.skipRange` is `"<0.4.0"` (was `"<0.4.0-rc.1"`).
  Same reasoning as rc.1 — no rc.1 CSV was published to an OperatorHub
  catalog, so skipRange is the right pattern.

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
- OLM bundle for OperatorHub publishing (#60; CSV uses `spec.skipRange: "<0.4.0-rc.1"` because v0.1.0 never published a catalog entry)

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
- **OLM upgrade** path uses `spec.skipRange: "<0.4.0-rc.1"`, not an
  explicit `replaces:`, because the OLM bundle infrastructure
  landed in PR #91 (after the v0.1.0 tag) so no published
  `ntn-operators.v0.1.0` CSV exists to replace. skipRange lets this
  CSV take over from any earlier-installed bundle if one is ever
  cataloged, without requiring v0.1.0 to exist.
- **External `NTNProvider` implementers** must add three methods
  (`EnsureOwnership`, `Cleanup`, `PushEphemerisUpdate`) to satisfy
  the interface (#20, #72). In practice nobody does — only
  `pkg/provider/ocudu` implements it — but the break is real for
  anyone who built a downstream provider against v0.1.0.

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
