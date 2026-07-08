# ADR 0005 — Cluster-Scope NTN Orchestration: ntn-operators above OCUDU's native per-cell push

- Status: **Accepted** (Option B; signed off by @thc1006 and v0.6 kicked off 2026-07-09 — see the Status update below)
- Date: 2026-06-06 (Context) · promoted to Proposed 2026-07-06 (facts re-verified against OCUDU `dev` HEAD `90191bd6`)
- Deciders: @thc1006
- Related issues: [#162](https://github.com/thc1006/ntn-operators/issues/162) (v0.6 cluster-scope epic), [#176](https://github.com/thc1006/ntn-operators/issues/176) (SatelliteEphemeris SGP4→push), [#137](https://github.com/thc1006/ntn-operators/issues/137) (Crossplane-style provider — v1.0), [#52](https://github.com/thc1006/ntn-operators/issues/52) (k_mac, 7/8)
- Builds on: ADR 0002 (runtime config interface — *per-gNB*; explicitly deferred multi-gNB fan-out), ADR 0003 (Nephio integration)
- Related decision: WG-0003-NTN charter HOLD (memory `project_wg0003_ntn_decision.md`) — the cluster-orchestration scope here IS the scope a future OCUDU NTN WG charter should claim.

> **Promotion note (2026-07-06).** The original draft parked the judgement sections until after the 2026-06-17 複賽 (now past). All facts below were re-verified live against OCUDU `dev` HEAD `90191bd6` via a 4-front read-only research pass; the Options/Decision/Consequences/Rollout are now filled. Status is **Proposed** (Option B recommended). It should move to **Accepted** only when @thc1006 signs off and v0.6 is kicked off — do not begin implementation before then.

> **Status update (2026-07-09) — Accepted; the v0.6 scope has largely shipped.** Signed off by @thc1006. Rollout steps 3–4 landed in **PR #177** (merged): `PushRuntimeUpdate` targets `ntn_config_update` (!798) over the `remote_control` WS (`pkg/provider/ocudu/wsclient.go`); [#176] is closed (SatelliteEphemeris SGP4 ECEF flows into the push); and the **SatelliteEphemeris → multi-NTNCellConfig fan-out** is wired — `NTNCellConfigReconciler` watches `SatelliteEphemeris` and `ephemerisToNTNCellConfig` enqueues every referencing cell (one source → N cells). **PR #179** then decouples propagation from the GP-fetch cadence so the pushed epoch stays fresh off-cycle, i.e. the fan-out delivers continuously rather than only every ~2h. Remaining, explicitly deferred to **v0.7** (upstream-gated): cell activate/deactivate (`cell_lock`/`cell_unlock`; OCUDU !862/!863 still redesigning), `NTNSlice` fleet failover, and `GroundStationLifecycle`→cell binding. Open Question 3 (where fleet fan-out ultimately lives — extend `NTNSlice` vs a new cluster-scoped CRD) remains open and gates the v0.7 work.

## Context

### What changed since ADR 0002 (fact pattern, re-verified 2026-07-06 via `read_api`, OCUDU `dev` HEAD `90191bd6`)

ADR 0002 (2026-04-19) anticipated a runtime NTN update interface and chose a ConfigMap-bootstrap + runtime-push **hybrid**, but had to leave the runtime transport partly speculative ("WebSocket interface", message format implicit, targeting the then-unmerged MR!411). Between April and July 2026, OCUDU upstream (piotr.gawlowicz's team, plus yagoda1 / naposto / michael-cns) shipped the whole per-cell NTN substrate **and then some**:

1. **The per-cell config surface is now fully native upstream.** `du_high_ntn_config_cli11_schema.cpp` + `du_high_ntn_config_yaml_writer.cpp` carry `polarization` (rhcp/lhcp/linear), `cell_specific_koffset`, `ta_info`, `ta_report`, `ntn_ul_sync_validity_dur`, `epoch_time`, `ephemeris` (ECEF / orbital / Keplerian), Doppler compensation, `feeder_link`, `gateway_location`, `sat_switch_with_resync`, `t_service`. Source: piotr's ntn_config wave (MR !658/!660/!661/!662, merged 2026-05-08→22) + Keplerian propagator (!836, merged 2026-06-05).
2. **The runtime push interface ADR 0002 predicted materialized as MR !798** — `ntn_config_update_remote_command` (`apps/units/flexible_o_du/split_helpers/commands/ntn_config_update_remote_command.cpp`), merged 2026-05-29, with integration tests. This is the concrete realization of ADR 0002's `PushRuntimeUpdate`, and it **supersedes** the speculative MR!411 target ADR 0002 named.
3. **A month-long config-manager rewrite reshaped the model to be constellation-aware (June–July 2026).** `ntn_orbital_compute_module` + a clean `ntn_orbital_state` interface were extracted (!951); the manager was refactored around a **per-satellite context** (!944, renaming `ntn_assistance_info` → `ntn_serving_cell_config`); and a **global `ntn.satellites` pool with per-cell `satellite_idx` references** was added (!975), so cells/neighbors/sat-switch entries point at a shared satellite by index instead of inlining ephemeris. The MAC layer became NTN-aware for HARQ (!904/!929) and **Rel-19 NTN bands** landed (!1009). **Do not duplicate SGP4/Keplerian propagation** — it is owned and actively extended upstream.
4. **`k_mac` remains the one ragged edge (7/8).** Static `--k_mac` CLI + YAML keys now exist **only for the neighbor-cell and sat-switch blocks**; the **serving-cell** `k_mac` is architecturally unreachable (no static, no runtime input; struct field never set) and is broadcast-only (SIB19 `kmac_r17`, never scheduler-consumed). Our static-YAML emitter must not emit a serving-cell `k_mac`. Tracked in [#52]. Not load-bearing for this ADR.

### The transport this ADR depends on (verified 2026-07-06 — auth/persistence now concrete, not speculative)

The remote-command surface is a per-app **uWebSockets server** (`apps/services/remote_control/remote_server.cpp`):

- **Config key:** `remote_control: { enabled (default false), bind_addr (default 127.0.0.1), port (default 8001) }`. **Off by default** — the deployment must enable it.
- **Envelope:** a text WS frame `{ "cmd": "<name>", …payload… }`; reply `{"cmd":…,"timestamp":…}` or `{"error":…}`. 16 KB payload cap. Commands are fixed at boot (cannot be registered over the wire).
- **NTN push:** `{ "cmd": "ntn_config_update", "cells": [ { "plmn", "nci", "ephemeris_info": {ecef|orbital}, "cell_specific_koffset", "ta_info", "polarization", "ta_report", "ntn_ul_sync_validity_duration", "feeder_link_info", … } ], "common_ntn_config"?: {…} }` (per-cell keyed by `plmn`+`nci`).
- **Auth posture: NONE — plaintext, no token, no TLS, localhost by default.** The intended safe pattern is **sidecar co-location** (same pod, `127.0.0.1`), mirroring OCUDU's own `ocudu_o1_adapter`. Cross-pod requires `bind_addr: 0.0.0.0` + a NetworkPolicy (and ideally a mesh) — we own that security, OCUDU provides none.
- **Topology:** monolithic `gnb` exposes CU-CP **and** DU commands on one server; disaggregated CU-CP/DU each expose their own server/port — a reconciler must target the right pod per command.
- **Persistence gap (reviewer fmaroszek, MR!862):** remote commands **do not persist to the startup YAML**; on pod restart the gNB reloads from its config file and loses runtime state. This is unsolved upstream — and is a load-bearing argument for this ADR (see D6).

### The layer OCUDU deliberately does NOT own — and that others are circling

OCUDU now also ships `ocudu_netconf` (native O1/NETCONF), `ocudu_o1_adapter` (a K8s sidecar driving one Pod's config + lifecycle towards an SMO), and `ocudu_helm` (official Helm charts). So **"bring Kubernetes / O1 / cloud-native management to the RAN" is no longer a differentiator** — OCUDU does that for a single node. What no one owns is the **cluster-scope, multi-cell / multi-satellite / multi-gNB NTN lifecycle** driven by **live orbital data**. OCUDU Issue #310's call for a *"Cell Configuration Reconciler"* points at the same gap; michael-cns's CNS OIP-0001 stakes the K8s-operator-pattern cell-reconciler — but **explicitly terrestrial** (NTN "orthogonal"), proposal-stage, patent-pending. Aalyria SpaceTime models 3GPP-NTN in an open API but keeps the engine proprietary and is a gRPC SDN, not an operator. **The open-source, NTN-domain, orbital-data-driven fleet reconciler is still unoccupied** (world's-first re-sweep 2026-07-06 holds under the 6-element scope).

### Implication

ADR 0002's value proposition — "deliver NTN config to *a* gNB correctly" — is now **commoditized by upstream**. ADR 0002 explicitly listed **"Multi-gNB fan-out — single gNB per `NTNCellConfig` for now"** as out of scope. **That deferred scope is now the project's primary differentiation.** OCUDU owns the per-cell control point (and now a per-satellite pool); nobody owns the cluster-scope lifecycle layer our four CRDs were designed for.

### Where our code actually is today (verified 2026-07-06)

- The operator emits **static YAML only** — `ApplyCellConfig` renders `geo_ntn.yml` into a ConfigMap; there is **no WS / remote-command client**. `PushEphemerisUpdate` is a ConfigMap **string-replace** (Phase-1) that relies on the gNB voluntarily reloading.
- **`SatelliteEphemeris` is not wired to OCUDU:** it fetches GP/OMM and runs SGP4 (`pkg/ephemeris/propagator.go`) but the propagated ECEF is used **only for pass prediction and discarded**; `ephemerisRef` merely re-pushes the NTNCellConfig's own static ephemeris. This is the concrete first gap ([#176]).
- **`NTNCellConfig` has no activate/deactivate / cell-lock** — "deactivate" today = delete the CR → ConfigMap removed.

## Decision Drivers

- **D1. Differentiation / moat.** The defensible, "world's first" claim is the *K8s Operator pattern + 4 CRDs + controller-runtime reconciliation against 3GPP NTN, driven by orbital data*, not C++ per-cell config. Move to where the moat actually is.
- **D2. Upstream complementarity, not competition.** Build *on top of* OCUDU's per-cell push (!798), never duplicate it — the structural lesson of MR !597 (proposing what upstream owns → declined). Do not duplicate SGP4/Keplerian.
- **D3. Fill the unfilled layer.** One `SatelliteEphemeris` → fan-out to N `NTNCellConfig`s; fleet reconciliation; `NTNSlice` failover across the fleet; `GroundStationLifecycle` binding ground hardware to cells; GitOps over the constellation.
- **D4. No regression.** v0.5's ConfigMap bootstrap + per-cell path must keep working; this is additive (ConfigMap stays the bootstrap baseline, runtime push handles refresh).
- **D5. Bandwidth reality.** Small team; v0.6 must be a coherent shippable increment, not a rewrite.
- **D6. Be the persistence layer OCUDU lacks.** Because upstream remote commands don't survive a pod restart, a declarative operator that re-applies desired state on every (re)start is not just nice-to-have — it fills a gap OCUDU itself flagged (fmaroszek). This is a first-class design argument, not a side benefit.

## Options Considered

### Option A — Stay per-cell (status quo)
Keep `NTNCellConfig` 1:1 with a gNB; keep ConfigMap + `replaceEphemeris`.
- ➕ Zero new work; no new dependency on the (unauthenticated, still-evolving) WS surface.
- ➖ Commoditized by upstream; no moat; the per-cell seam is gone. `SatelliteEphemeris`'s SGP4 output stays discarded, so the headline "keep SIB19 fresh from live orbit data" loop is never closed. Effectively a slow decline. **Rejected.**

### Option B — Cluster orchestrator on top of per-cell push (recommended v0.6 increment)
- Implement ADR 0002's runtime client concretely against **!798** (`PushRuntimeUpdate` → `ntn_config_update` over the `remote_control` WS), retiring `replaceEphemeris` for covered fields while keeping ConfigMap as the bootstrap baseline.
- Close the [#176] gap: `SatelliteEphemeris`'s SGP4-propagated ECEF flows into the push, so the orbit→SIB19 loop is actually closed.
- `SatelliteEphemeris` fans out one ephemeris source to **N** `NTNCellConfig`s; the operator becomes the **fleet control-plane** over OCUDU's per-cell control point, and the **persistent source of truth** (D6) that re-applies state on pod restart.
- ➕ Satisfies D1/D2/D3/D4/D6; every piece maps to a merged upstream surface (!798) — no speculative dependency on unmerged MRs. ➖ Depends on the WS surface's schema + (absent) auth → we must own transport security (sidecar-first; NetworkPolicy for cross-pod); real bandwidth cost. Cell activate/deactivate is **explicitly out of v0.6** (see below).

### Option C — Crossplane-style NTN-ecosystem provider pattern (#137; v1.0 north star)
Generalize `provider` into a pluggable model (OCUDU, Aalyria, ST Engineering, …).
- ➕ Largest TAM; positions us as the cross-vendor NTN control plane. ➖ Largest scope; v0.7+/v1.0, not v0.6.

## Decision

**Adopt Option B for v0.6** (recommendation; Accepted on @thc1006 sign-off). A/B/C rejected/deferred as above: A is a slow decline, C is the v1.0 trajectory.

**v0.6 scope boundary (what ships):**
1. A runtime push client (`PushRuntimeUpdate`) targeting `ntn_config_update` (!798) over the `remote_control` WS, **sidecar-co-located** (localhost); ConfigMap remains bootstrap. Optional `ProviderRef.RemoteControl { endpoint, authSecretRef }` for cross-pod (guarded by NetworkPolicy guidance).
2. Close [#176]: `SatelliteEphemeris` surfaces propagated ECEF and it is pushed via (1) to the referenced `NTNCellConfig`'s cell(s), replacing the static-ephemeris re-push for the runtime path.
3. `SatelliteEphemeris` → multi-`NTNCellConfig` fan-out (one source, N cells).

**Explicitly OUT of v0.6 (roadmap, upstream-gated):**
- **Cell activate/deactivate via `cell_lock`/`cell_unlock`.** Upstream !862 (per-cell handler) conceded to a redesign on 2026-06-30 (reuse `cell_activation_routine`, CU-driven UE drain, new CU-CP logical-cell config); !863 (WS commands) is Draft and stacked on it; expected landing order !862 → !864 → !863. **Do not bind to the current `cu_cp_cell_command_handler` signatures.** Track for v0.7; the WS payload `{ "cmd": "cell_lock", "cgi": { "plmn", "nci" } }` is the only part stable enough to plan against.
- **`NTNSlice` fleet failover** and **`GroundStationLifecycle`→cell binding** (v0.7): depends on (1)–(3) landing first.
- Serving-cell `k_mac` (#52, upstream-blocked, 7/8).

## Consequences

- **Positive:** restores/strengthens the moat (D1); upstream-complementary (D2); closes the orbit→SIB19 loop that is the whole point of `SatelliteEphemeris` (D3/#176); becomes the persistence layer OCUDU lacks (D6); the cluster-orchestration scope here doubles as the **scope a future OCUDU NTN WG charter should anchor** (ties to the WG-0003-NTN HOLD); makes us the concrete reference impl for OCUDU #310.
- **Negative:** two-layer reasoning (per-cell upstream vs cluster-scope ours); a hard dependency on an **unauthenticated, still-evolving** WS surface — mitigated by sidecar-first co-location + keeping ConfigMap as the fallback baseline; real bandwidth cost.
- **Neutral:** the `provider` abstraction evolves toward the #137 pattern; the ConfigMap path becomes "bootstrap only" rather than the update mechanism.

## Rollout Plan

1. **This commit:** ADR promoted to Proposed as the v0.6 design anchor. No code yet.
2. **On sign-off / v0.6 kickoff:** promote to Accepted; open v0.6 milestone from #162.
3. **First implementation step:** refit ADR 0002's runtime client against **!798** (`PushRuntimeUpdate` + `pkg/provider/ocudu/wsclient.go`), sidecar-co-located; add integration test against an OCUDU gNB with `remote_control.enabled=true`.
4. **Then:** [#176] — `SatelliteEphemeris` propagated ECEF → push; then multi-`NTNCellConfig` fan-out.
5. **WG linkage:** the "merged NTN MR" that would strengthen a WG-0003-NTN charter (trigger (c)) should **fall out of step 3/4** (a real improvement found while integrating !798 — e.g. a doc/example/bug-fix in the `ntn_config_update` or `ntn.satellites` path), NOT a manufactured plumbing MR (which would be declined like !597 and hurt standing). Config-plumbing lane is closed (piotr owns it).

## Open Questions

1. **!798 message-schema stability** — the command is merged and tested; the config-manager rewrite (!944/!975) is still active, so watch for per-cell key churn (e.g. `satellite_idx` is static-YAML-only, not in the remote command). Pin to the tested key set; add a schema-drift check.
2. **Transport security model** — WS is plaintext/unauth. v0.6 assumes sidecar (localhost). Decide the cross-pod story: `ProviderRef.RemoteControl` + NetworkPolicy templates, or defer cross-pod to v0.7.
3. **Where does fleet fan-out live** — extend `NTNSlice`, or a new cluster-scoped CRD (relates to #137)?
4. **WG scope coupling** — should the ADR's chosen scope be written verbatim into the WG-0003-NTN charter when/if filed?

## References

- ADR 0002 (`docs/adr/0002-runtime-config-interface.md`) — the per-gNB runtime interface this ADR generalizes; deferred multi-gNB fan-out; still names the speculative MR!411 target → superseded by !798 (refit is Rollout step 3).
- ADR 0003 (`docs/adr/0003-nephio-integration.md`).
- OCUDU MR [!798](https://gitlab.com/ocudu/ocudu/-/merge_requests/798) (merged 2026-05-29) — `ntn_config_update_remote_command`, the runtime push surface.
- OCUDU config-manager rewrite: MRs [!944](https://gitlab.com/ocudu/ocudu/-/merge_requests/944), [!951](https://gitlab.com/ocudu/ocudu/-/merge_requests/951), [!975](https://gitlab.com/ocudu/ocudu/-/merge_requests/975) (per-satellite context / `ntn_orbital_state` / global `ntn.satellites`); ntn_config wave !658/!660/!661/!662; [!836](https://gitlab.com/ocudu/ocudu/-/merge_requests/836) (Keplerian propagator).
- OCUDU cell-lifecycle (roadmap): MR [!862](https://gitlab.com/ocudu/ocudu/-/merge_requests/862) (per-cell activate/deactivate, redesign in progress), [!863](https://gitlab.com/ocudu/ocudu/-/merge_requests/863) (`cell_lock`/`cell_unlock` WS, Draft).
- OCUDU transport: `apps/services/remote_control/remote_server.cpp` (uWS server); `ocudu/ocudu_elements/ocudu_oran_apps/ocudu_o1_adapter` (K8s sidecar reference pattern).
- OCUDU Issue [#310](https://gitlab.com/ocudu/ocudu/-/issues/310) — Cell Configuration Reconciler; CNS OIP-0001 (`ocudu/community/oips` MR!1) — terrestrial, patent-pending.
- Operator issues [#162](https://github.com/thc1006/ntn-operators/issues/162) (v0.6 epic), [#176](https://github.com/thc1006/ntn-operators/issues/176) (SatelliteEphemeris→push), [#137](https://github.com/thc1006/ntn-operators/issues/137), [#52](https://github.com/thc1006/ntn-operators/issues/52).
- Memory: `project_wg0003_ntn_decision.md`, `project_upstream_ocudu_backlog.md` (superseded 2026-06-06), `reference_koffset_and_crd_copies.md`, `reference_ocudu_foundation.md`.
