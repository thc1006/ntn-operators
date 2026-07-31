# Architecture Decision Records

One decision per file, `NNNN-kebab-title.md`. This index is the single current-truth
map of what each ADR decided, whether it still holds, and what implements or tracks it.
`hack/check-adr-index.sh` (CI job **ADR lint**) enforces the invariants at the bottom, so
a numbering collision or a dangling cross-reference cannot merge.

## Index

| ADR | Title | Status | Relations | Implemented by | Tracking | Last verified |
|-----|-------|--------|-----------|----------------|----------|---------------|
| [0001](0001-sib11-measurement-config.md) | SIB11 / neighbor measurement scope | Accepted (scope-pivoted) | — | design-only (`neighborMobility` not built; SIB2/3/4 pivot) | #47 | 2026-04-19 |
| [0002](0002-runtime-config-interface.md) | Runtime config interface (ConfigMap ↔ WebSocket) | Proposed | — | runtime push shipped — see 0005 | — | 2026-04-19 |
| [0003](0003-nephio-integration.md) | Nephio R6 package integration | Accepted | — | `config/nephio` kpt packages | — | 2026-04-24 |
| [0005](0005-cluster-orchestration.md) | Cluster-scope orchestration above OCUDU | Accepted | — | v0.6 fan-out | #162 | 2026-07-06 · OCUDU `dev` `90191bd6` |
| [0006](0006-decouple-propagation-from-pass-prediction.md) | Decouple propagation heartbeat from pass prediction | Accepted | — | PR #256 | #311 (fixed, PR #334) | 2026-07-19 |
| [0007](0007-durable-omm-cache.md) | Durable last-good OMM cache | Accepted | — | `feat/omm-persist-restart-continuity` | ConfigMap name-collision follow-up | 2026-07-30 |
| [0008](0008-constellation-pool-satellite-availability.md) | Pool-level satellite-path availability | Accepted | — | v0.7 pool condition | naming semantics | 2026-07-30 |
| [0009](0009-remote-control-credential-confused-deputy.md) | Confused-deputy boundary for `remoteControl.tls` | Accepted, amended ×2 | partly superseded-by **0011** | endpoint allow-list (#300), #309 VAP | #251, #300, #309 | 2026-07-31 |
| 0010 | Secure-by-default remote-control transport (v1alpha2) | Proposed — [PR #330](https://github.com/thc1006/ntn-operators/pull/330) | — | pending | #214/#315, #299, #251, #309, #317 | 2026-07-31 |
| 0011 | Owner-issued `remoteControl.tls` credential grant | Proposed — [PR #328](https://github.com/thc1006/ntn-operators/pull/328) | partly supersedes **0009** | pending | #298, #300, #309, #329/#332 | 2026-07-31 |

### Caveats recorded here so the index stays honest

- **0001** conflates NR **SIB11** (`MeasIdleConfigSIB`, idle/inactive) with connected-mode **`MeasConfig`** (`MeasObjectNR`/`ReportConfigNR`, dedicated RRC). The *decision* (defer speculative wrapping, exercise mobility via SIB2/3/4) stands; the taxonomy needs splitting — tracked with #47.
- **0002** predates its own implementation. The runtime path shipped as OCUDU `ntn_config_update` over `coder/websocket` (0005), not the speculative `nhooyr`/Gorilla/pooling design the ADR describes; a `Proposed → superseded-in-part` amendment is pending. Treat `BootstrapConfigApplied` and `RuntimeConfigDelivered` as distinct — the first does not imply the second.
- **0005** claims only the **v0.6 fan-out**. Fleet orchestration (cell binding, `NTNSlice` failover, ground-station→cell) is not yet built; a pool member being available is not the same as a slice's serving cell having switched to it.
- **0009** is the live authorization SSOT — read its **Net current state** block first; the original "prefer SAR, defer authz" bullet is superseded by 0011.

## Reserved / gaps

- **0004** — antenna-probe ADR maintained on a private branch (Phase B IP hold); the sequence gap on `main` is intentional, not a lost record.

## Invariants enforced by CI (`hack/check-adr-index.sh`)

For every `docs/adr/NNNN-*.md`:

1. the 4-digit `NNNN` is unique (this is the guard the two-ADR-0010 collision needed);
2. the `# ADR NNNN` title number matches the filename;
3. a `- Status:` line is present;
4. the ADR is listed in this index;
5. every relative link to another ADR file resolves;
6. every internal `#anchor` link resolves to a heading in the target file (repo-internal only — external URLs are never fetched, so a site being down cannot make this lint flaky).

## Adding an ADR

1. Take the next free number (check this index — 0004 is reserved, not free).
2. Name the file `NNNN-kebab-title.md`; open with `# ADR NNNN — Title` and a `- Status:` line.
3. Add a row here in the same PR.
4. When an ADR supersedes or amends another, edit both — the superseded one links forward, and its status stops claiming current authority.
