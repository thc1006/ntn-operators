# Architecture Decision Records

**This directory is the single current authority for every architecture decision
in `ntn-operators`.** Read this index at the start of any ADR-related or design
work, then the specific ADR. An ADR is the current decision, not a chronological
scratchpad — [`ADR_STYLE_GUIDE.md`](ADR_STYLE_GUIDE.md) defines the required YAML
front matter and structure, and [`SOURCES.md`](SOURCES.md) is the verification
register (normative standard → official docs → upstream → repo → vendor).

Governance is machine-checked:

- `checks/check_adr_metadata.py` — front-matter presence, required keys, unique
  numbers, `adr` == filename, status enum, date formats.
- `hack/check-adr-index.sh` — index membership + repo-internal link/anchor
  resolution (never fetches external URLs, so it cannot go flaky).
- Both run as `make adr-lint` and as the **ADR lint** CI job on `docs/adr/**`.

## Index

| ADR | Title | Status | Relations | Tracking |
|-----|-------|--------|-----------|----------|
| [0001](0001-nr-mobility-measurement-scope.md) | Separate NR SIB11 idle/inactive measurements from connected-mode `MeasConfig` | accepted | supersedes prior 0001 | #47 |
| [0002](0002-runtime-config-interface.md) | Hybrid bootstrap and runtime NTN configuration interface | accepted | supersedes ConfigMap-only assumption | #37 · impl #177 |
| [0003](0003-nephio-integration.md) | Distribute CRDs and examples as Nephio kpt packages | accepted | — | #164 |
| [0005](0005-cluster-orchestration.md) | Place `ntn-operators` above provider-native per-cell control | accepted | — | #162 · impl #177/#189 |
| [0006](0006-decouple-propagation-from-pass-prediction.md) | Decouple propagation heartbeat from pass prediction | accepted | — | #234 · impl #256 |
| [0007](0007-durable-omm-cache.md) | Durable last-good OMM cache for restart and leader failover | accepted | — | impl #345 (hashed name, legacy adoption, metrics) · storage-mode follow-up |
| [0008](0008-constellation-pool-availability.md) | Model satellite-path contact opportunity at constellation-pool level | accepted | — | impl #TBD (reasons + `status.contactCandidate`) · slice-to-cell binding follow-up |
| [0009](0009-remote-control-credential-boundary.md) | Layered authorization boundary for remote-control credentials | accepted | superseded-in-part by **0011** | #251, #299 · wire-level E2E impl #332 |
| [0010](0010-v1alpha2-secure-defaults-and-duration-validation.md) | v1alpha2 secure-by-default transport and duration admission validation | proposed | — | #214, #315, #311, #299 |
| [0011](0011-remote-control-credential-grant.md) | Owner-issued grant for remote-control credentials | proposed | supersedes **0009** admission-only end state | #251, #298, #329 (closed by #332) |
| [0012](0012-ground-station-antenna-health-probe.md) | Evidence-based ground-station antenna health and readiness | proposed | supersedes node-exists→`AntennaReady=True` | #68 · #335 removed the fabricated `True`; the probe itself is unimplemented |

## Reserved / gaps

- **0004** — intentionally absent. It was not part of the reviewed set; do not
  reuse the number for unrelated work until its original decision is recovered.

## Adding or amending an ADR

1. Take the next free number (0004 is reserved, not free).
2. Name the file `NNNN-kebab-title.md`; start with the YAML front matter required
   by [`ADR_STYLE_GUIDE.md`](ADR_STYLE_GUIDE.md), then `# ADR NNNN — Title`.
3. Add a row to this index in the same PR.
4. An amendment that reverses a load-bearing decision must be a consolidated
   replacement, not a paragraph appended after the old one — set `superseded_by`
   on the old ADR and `supersedes` on the new.
5. Run `make adr-lint`.
