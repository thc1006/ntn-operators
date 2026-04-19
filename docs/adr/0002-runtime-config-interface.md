# ADR 0002 — Runtime NTN Configuration Interface: ConfigMap vs WebSocket Hybrid

- Status: **Proposed**
- Date: 2026-04-19
- Deciders: @thc1006
- Related issue: [#37](https://github.com/thc1006/ntn-operators/issues/37)
- Related in-tree: `pkg/provider/ocudu/provider.go:162-272` (`PushEphemerisUpdate`, `replaceEphemeris`)
- Supersedes: implicit status-quo of ConfigMap-only path established in PRs #34, #35

## Context

Today the OCUDU provider in this operator takes the following path to deliver NTN parameter updates to a running gNB:

1. Reconciler resolves `NTNCellConfig.spec` into a YAML blob via `pkg/provider/ocudu/config.go:GenerateConfig`.
2. Provider writes the blob to a ConfigMap (`pkg/provider/ocudu/provider.go:ApplyCellConfig`).
3. On a new ephemeris push, provider **string-replaces** the `ephemeris_info_ecef:` / `ephemeris_orbital:` block in place (`pkg/provider/ocudu/provider.go:replaceEphemeris`, 60+ lines of regex-adjacent string munging).
4. The gNB is assumed to reload the config (typically on restart).

This was reasonable when it was our only option. A 2026-04-19 upstream survey has changed the factual landscape:

### 2026-04-19 fact pattern

1. **OCUDU has a runtime NTN update interface** — `ocudu_ntn::ntn_configuration_manager::handle_ntn_config_update()` declared in `include/ocudu/ntn/ntn_configuration_manager.h`. The struct `ntn_cell_config_update_info` explicitly names itself *"NTN Config update message to be received over a websocket interface"*.
2. **MR !411 (merged 2026-02-27)** added SIB19 value-tag-tracked fields — including `ncells`, `moving_ref_location`, `sat_switch_with_resync` — into `ntn_cell_config_update_info`. Runtime reconfig is the upstream-blessed path, not YAML.
3. **OCUDU YAML CLI11 schema does not parse `ntn.ncells`** — the field exists in the struct but has no CLI11 subcommand (confirmed via grep over `du_high_ntn_config_cli11_schema.cpp`). Our current ConfigMap output for `neighborCells:` is accepted by the YAML reader but lands in a field no one populates from config — the value stays empty inside the gNB.
4. **Remote control WebSocket service exists today** — `apps/services/remote_control/` (per OCUDU Issue #310 §"Current State") exposes `ssb_set` + `rrm_policy_ratio_set` as non-disruptive operations. NTN update is the obvious next addition; MR !411 is the data-model step.
5. **Our existing `replaceEphemeris` is a workaround** — it edits YAML that the gNB must then *voluntarily reload*. In practice that means process restart. UE sessions drop. Not production-viable.
6. **OCUDU Issue #310 is an open design conversation** calling for a *"Cell Configuration Reconciler"* with per-cell non-disruptive updates — exactly the capability this ADR unlocks.

## Decision Drivers

- **D1. OTA correctness.** Our operator's selling point is "NTN config reaches the gNB correctly." ConfigMap static load misses `ncells` entirely today; regex-based ephemeris replacement requires process restart. Neither is OTA-correct.
- **D2. Minimal disruption.** Production NTN deployments measure service continuity in UE session survival. ConfigMap writes that force a DU restart violate the operator's value proposition — we'd be an expensive alternative to a plain Helm chart.
- **D3. Upstream alignment.** OCUDU upstream has already shipped the runtime update data model (MR !411). Building on top reduces impedance with upstream and positions us as reference implementation material for #310.
- **D4. Migration cost.** We already have a working ConfigMap path, verified in this repo's test suite. Any new path must not regress existing functionality.
- **D5. Scope of v0.x.** The operator is pre-v1alpha2. Introducing a second transport is acceptable. A full rewrite is not.

## Options Considered

### Option A — Stay on ConfigMap only

Accept the known gaps (`ncells` unreachable, ephemeris update requires restart) and invest in upstream MRs to add YAML parsers for missing fields.

- ➕ No new dependencies in the operator.
- ➕ GitOps-friendly: every change is a versioned ConfigMap.
- ➖ Fails D1 for neighbor / mobility fields — upstream may intentionally never add a YAML parser for fields that are supposed to change at runtime.
- ➖ Fails D2 — ephemeris / PCI / TA updates require DU restart.
- ➖ Maintains the `replaceEphemeris` regex code indefinitely.

### Option B — Move entirely to WebSocket

Delete the ConfigMap path; every `NTNCellConfig` change opens a WS connection to the gNB and calls `handle_ntn_config_update`.

- ➕ Fully D1 / D2-aligned. No regex. No restart.
- ➖ Breaks GitOps bootstrap — a first-boot gNB has no pre-existing config to consume; WS only updates a running process.
- ➖ Adds WS client, reconnect, backoff, auth as hard dependencies — 600+ LOC.
- ➖ One-way dependency on an upstream interface whose auth scheme and message framing are not yet stable (remote_control expanded iteratively in 2025-26).
- ➖ Sheds the provider abstraction's value — Aalyria / ST Engineering providers don't speak this WS protocol.

### Option C — Hybrid: ConfigMap for bootstrap + WebSocket for runtime (recommended)

- **ConfigMap path** continues to own first-boot / static fields. It is the source of truth when the gNB cold-starts.
- **WebSocket path** handles runtime updates for fields that OCUDU accepts dynamically: `ncells`, `ephemeris_info`, `epoch_time`, `ta_info`, `polarization`, `moving_ref_location`, `sat_switch_with_resync` (the set in MR !411's `ntn_cell_config_update_info`). `replaceEphemeris` retires.
- A new `pkg/provider/ocudu/wsclient.go` talks to the gNB's remote-control endpoint.
- The provider interface gains a `PushRuntimeUpdate` method (generalising the existing `PushEphemerisUpdate`); ConfigMap provider implements it as a **no-op** when WS is unavailable, degrading gracefully.
- Provider configuration (existing `ProviderRef`) gains an optional `remoteControlEndpoint` + `remoteControlAuth` hint.

- ➕ D1 satisfied: runtime-only fields reach OTA.
- ➕ D2 satisfied: no process restart for covered fields.
- ➕ D3 satisfied: uses upstream-blessed data model directly.
- ➕ D4 satisfied: ConfigMap path remains for bootstrap; migration is additive.
- ➖ Two code paths to reason about — offset by a clean layering (bootstrap fields vs runtime fields are disjoint per MR !411).
- ➖ Depends on WS message format stability. Mitigation: version-check the first exchanged message; fall back to ConfigMap-only if version mismatch.

## Decision

**Option C.** Scope the first implementation tightly:

1. **Retire `replaceEphemeris` as the runtime update path**, but keep the ConfigMap path as the bootstrap baseline.
2. **Define a `RuntimeUpdate` interface** in `pkg/provider` with one method `PushRuntimeUpdate(ctx, cr, update)`; generalise `EphemerisUpdate` to `RuntimeUpdate{Ephemeris, Polarization, TAInfo, NeighborCells, ...}` mirroring `ntn_cell_config_update_info`.
3. **Implement `pkg/provider/ocudu/wsclient.go`**:
   - Gorilla WebSocket client with connection pool keyed by `(namespace, endpoint)`.
   - Send framing: JSON-encoded `ntn_config_update_info` (field names must match OCUDU's Boost.PropertyTree layout; see §Risks).
   - Wait for `ntn_config_update_result` response, correlate by cell NCGI.
   - On WS failure: degrade to ConfigMap path and surface `Condition{Type=RuntimeUpdateDegraded, Status=True}`.
4. **Add `ProviderRef.RemoteControl { endpoint, authSecretRef }`** (optional). If unset, provider stays on ConfigMap path exclusively.
5. **Test matrix**: unit tests with a fake WS server; integration test with a stubbed gNB that records received messages.

**Explicitly out of scope for the first PR** (separate issues):

- Mutual TLS / token rotation for WS auth — start with plaintext or bearer token referenced via Secret; raise the bar later.
- Multi-gNB fan-out — single gNB per `NTNCellConfig` for now.
- Bootstrap-via-WS — we keep first-boot on ConfigMap indefinitely.
- Retiring ConfigMap entirely — not considered.

## Consequences

### Positive

- Runtime NTN updates no longer drop UE sessions in the fields covered by MR !411.
- `replaceEphemeris` regex code deletable once `wsclient` covers ephemeris updates.
- Operator becomes credible demonstrable evidence when engaging OCUDU Issue #310 or contributing to `remote_control` upstream.
- Unlocks future integration with OCUDU's `ssb_set` / `rrm_policy_ratio_set` via the same WS channel.

### Negative

- Introduces a TCP-level dependency on the gNB being reachable from the operator's network namespace. Needs documentation on network policy.
- Operators must now configure `remoteControlEndpoint`; migration path for existing users who don't set it = no behaviour change (ConfigMap-only).
- WS message format is implicit today — first version may need a rewrite if OCUDU formalises the schema differently.

### Neutral

- Adds `github.com/gorilla/websocket` or `nhooyr.io/websocket` as a dependency. Both are maintained; prefer the latter for `context.Context` support.

## Rollout Plan

1. **This commit**: ADR merged as a design anchor. No code.
2. **Follow-up issue filed**: *"provider/ocudu: implement WebSocket runtime update client per ADR 0002"* — blocks on upstream OCUDU stabilising the `handle_ntn_config_update` WS message format (observe `apps/services/remote_control/` stability over 2-3 weeks).
3. **Upstream reconnaissance**: draft a comment or small MR on OCUDU asking for WS message-format documentation / versioning. If OCUDU publishes a JSON schema, we adopt verbatim.
4. **Implementation PR**: `wsclient.go` + interface refactor + unit tests + degrade-to-ConfigMap fallback + docs/runbook.
5. **Deprecation**: after a full release with both paths green in partner deployments, delete `replaceEphemeris`.

## Open Questions

1. **WS auth model.** Does OCUDU require bearer tokens, mTLS, or anything yet? `apps/services/remote_control/` Boost.Beast server may accept plaintext WS today. Needs clarification before adoption in regulated deployments.
2. **Message schema stability.** MR !411 defines the C++ struct; the over-the-wire JSON layout depends on Boost.PropertyTree conventions. If it's fragile to field renames, we need a versioning handshake.
3. **Fallback policy when WS is down.** Degrade silently to ConfigMap (risk: UE drop on restart) OR surface as `Condition{Ready=False}` and block reconcile (risk: stale config pushed never reaches gNB)? Probably the former with a prominent metric.

## References

- OCUDU header: `include/ocudu/ntn/ntn_configuration_manager.h` — class `ocudu_ntn::ntn_configuration_manager`, struct `ntn_cell_config_update_info`.
- OCUDU header: `include/ocudu/ntn/ntn_configuration_manager_config.h` — `ntn_assistance_info` full field list.
- OCUDU MR [!411](https://gitlab.com/ocudu/ocudu/-/merge_requests/411) (merged 2026-02-27) — adds SIB19 tracked fields to `ntn_cell_config_update_info`.
- OCUDU Issue [#310](https://gitlab.com/ocudu/ocudu/-/issues/310) (open) — proposes a Cell Configuration Reconciler; our operator is a natural reference implementation (see §Disclosure caveat in issue body).
- Our `pkg/provider/ocudu/provider.go:162-272` — current `PushEphemerisUpdate` + `replaceEphemeris` implementation to be superseded.
- Related operator issue: [#37](https://github.com/thc1006/ntn-operators/issues/37) — dynamic ephemeris push path.
