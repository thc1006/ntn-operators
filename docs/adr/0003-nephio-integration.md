# ADR 0003 — Nephio R6 Package Integration

- Status: **Accepted**
- Date: 2026-04-24
- Deciders: @thc1006
- Related issue: [#51](https://github.com/thc1006/ntn-operators/issues/51)
- Related governance: author is 1-of-6 Nephio TSC members (seated early 2026)

## Context

Issue #51 asked to expose `NTNSlice` (and by extension our other CRDs) as a Nephio-consumable workload package. The original framing implied integrating with "intent-based orchestration for dynamic QoS/billing adjustment", but a 2026-04-24 deep survey of Nephio R6 shows the idiomatic path is narrower and simpler than the issue description implies:

- Nephio is **kpt-native**, not Helm-native or CRD-controller-native. Workload packages are kpt packages (Kptfile + YAML) pulled through Porch into edge clusters.
- The canonical pattern for "ship CRDs as a Nephio package" already exists in `nephio-project/catalog/nephio/core/workload-crds/` — raw CRD YAMLs + a minimal Kptfile.
- Nephio R6 (released 2026-04-01, Porch v1.5.6) introduces no breaking schema changes for package authors; Kptfile remains `kpt.dev/v1`. The major change is that R6 blueprints publish KRM-function images at `ghcr.io/kptdev/krm-functions-catalog/*` (previously `gcr.io/kpt-fn/*`; both work, but ghcr is now idiomatic).

### Why integrate at all

Three distinct reasons, all load-bearing:

1. **Credibility closure**. The project's author holds a Nephio TSC seat. Outside observers reasonably expect the project to be a Nephio-consumable package. Absence of one raises "why is a TSC member not using Nephio?".
2. **Ecosystem fit**. Nephio is the LFN-sanctioned path for K8s-native telecom NF orchestration. Publishing as a Nephio package is the cheapest path to being pulled into LFN reference deployments, Sylva blueprints, Aether integration demos, and O-RAN SC handover scenarios.
3. **Deployment story**. Downstream users (partners, operators) who already run Nephio can consume our CRDs + samples via `kpt pkg get` + PackageVariant, without hand-rolling kustomize overlays.

### Upstream ground truth (verified 2026-04-24)

- Latest Nephio release: **R6** (2026-04-01). Porch **v1.5.6** bundled; patch stream **v1.5.8** current.
- PackageRevision / PackageVariant / PackageVariantSet remain the deployment model in R6. `PackageVariant` itself stays on `config.porch.kpt.dev/v1alpha1`; the newer `v1alpha2` group applies specifically to `PackageVariantSet`. Samples in this repository use `v1alpha1` for `PackageVariant` (correct and idiomatic).
- R6 blueprints migrated from `gcr.io/kpt-fn/*` to `ghcr.io/kptdev/krm-functions-catalog/*` (e.g. `set-namespace:v0.4.1`). Upstream `gcr.io/kpt-fn/` tags (set-namespace:v0.4.5, apply-replacements:v0.1.5, etc.) also remain current and functional.
- `catalog/nephio/core/workload-crds/Kptfile` is the minimal reference for "ship CRDs" packages: just `apiVersion: kpt.dev/v1`, `kind: Kptfile`, `metadata.annotations['config.kubernetes.io/local-config']='true'`, `info.description`.
- `workload.nephio.org/*` annotation namespace **could not be verified as active in R6**. Only `nephio.org/*`, `approval.nephio.org/*`, `config.porch.kpt.dev/*`, `kpt.dev/*`, `config.kubernetes.io/local-config` are observed in current catalog packages. **We will not use `workload.nephio.org/*`** until it is canonical.
- Official docs: `docs.nephio.org/docs/` (R6), `github.com/nephio-project/catalog`, `github.com/nephio-project/porch/releases`, `catalog.kpt.dev`.
- Nephio TSC seat grants no special publishing rights. PRs to `nephio-project/catalog` go through normal OWNERS-file review.

## Decision

**Ship two sibling kpt packages** in a new `nephio/packages/` directory inside this repo, each self-contained and `kpt fn render`-clean:

### Package 1 — `ntn-operators-crds`

- **Purpose**: deliver the four CRDs (SatelliteEphemeris, GroundStationLifecycle, NTNCellConfig, NTNSlice) as a kpt package. Mirrors `catalog/nephio/core/workload-crds/` exactly in shape.
- **Contents**:
  - `Kptfile` — minimal, `kpt.dev/v1`, no pipeline.
  - `README.md` — consumption instructions.
  - Four CRD YAMLs, copied from `config/crd/bases/` (single source of truth: the bases are generated from controller-gen; the package is a distribution artifact, not a second source).
- **Pipeline**: none. CRDs do not need mutation at package time.
- **Consumer flow**: `kpt pkg get <this-repo>/nephio/packages/ntn-operators-crds@<tag> <dest>` → `kubectl apply -f <dest>` to install CRDs on the management cluster (or on the edge cluster directly, if the edge hosts the operator).

### Package 2 — `ntn-workloads-sample`

- **Purpose**: deliver sample CRs (one per CRD) as a ready-to-adapt workload package. Shows how to customize per-cluster via a simple namespace/label mutation.
- **Contents**:
  - `Kptfile` — `kpt.dev/v1`, with a `pipeline.mutators` entry using **inline `configMap:` blocks** (not a separate fn-config.yaml) to keep the package self-contained:
    - `ghcr.io/kptdev/krm-functions-catalog/set-namespace:v0.4.1` — sets `metadata.namespace` on all CRs (default: `ntn-system`).
    - `ghcr.io/kptdev/krm-functions-catalog/set-labels:v0.2.1` — stamps `workload=ntn-sample`, `app.kubernetes.io/managed-by=ntn-operators`.
  - `README.md` — describes each CR, default values, customization knobs.
  - Four sample CRs, copied from `config/samples/ntn_v1alpha1_*.yaml` with an explicit `namespace: default` field added so the mutator has a value to rewrite (`config/samples/` remains single source).
- **Pipeline**: 2-step mutator chain. No validators in v1; validation deferred to K8s API server CEL rules (already defined on the CRDs themselves).
- **Consumer flow**: `kpt pkg get` → edit `Kptfile` (change `configMap` values inside the mutator entries) → `kpt fn render` → `kubectl apply -f .`. For Porch-managed clusters, wrap in a PackageVariant to apply per-cluster specialization automatically.

### What we will NOT do in v1

1. **No PackageVariant manifests shipped in this repo**. PackageVariant belongs in the consumer's deployment repo, not the blueprint repo. We document the pattern in README, but don't ship variants.
2. **No Porch cluster bootstrap**. Porch setup is documented at nephio.org; we don't duplicate. Our packages work with or without Porch (`kpt fn render` standalone or `kpt live apply`).
3. **No Helm chart expansion inside kpt**. The operator itself still ships via Helm (`dist/chart/`) and OLM (`bundle/`); the kpt packages ship CRDs and CRs only, not the operator Deployment. Operator installation remains the consumer's choice (Helm, OLM, or direct YAML).
4. **No custom KRM functions yet**. v1 uses only canonical upstream mutators. A future `ntn-validator-fn` could validate cross-CR references (NTNSlice → SatelliteEphemeris exists, etc.) but that is v2 scope.
5. **No PR to `nephio-project/catalog`**. We ship in our own repo first; community contribution to the Nephio catalog is a follow-up after we have internal E2E validation and, ideally, feedback from a Nephio TSC member sync.

## Alternatives considered

### Alternative A — Single combined package

Ship one `ntn-operators` package with both CRDs and samples inside.

**Rejected**: the `workload-crds` catalog precedent is CRDs-only. Mixing CRDs with samples in one package confuses the lifecycle (CRDs install once cluster-wide, samples deploy per-namespace per-tenant). The split mirrors community convention.

### Alternative B — PackageVariantSet-based per-cluster templating

Ship a PackageVariantSet CR alongside the packages so the Nephio mgmt cluster auto-generates per-cluster variants from a ClusterClaim selector.

**Deferred to v2**: requires assumptions about consumer's repo layout and variant selection strategy. Ship the blueprint first; let consumers choose variant strategy.

### Alternative C — Helm-chart-embedded kpt package

Use `render-helm-chart` KRM function at author time to expand our existing Helm chart into YAML, then ship that as the package.

**Rejected**: our Helm chart ships the operator itself (Deployment, RBAC, Service), not the CRDs-as-workload data plane. The kpt package domain is CRDs + CRs, and those are cleaner maintained directly from `config/crd/bases/` + `config/samples/` than via Helm round-trip.

### Alternative D — Skip Nephio integration for now

Accept the credibility cost ("TSC member but no Nephio package") and revisit post-competition.

**Rejected**: the cost of a minimal two-package scaffold is ~1-2 days; the credibility payoff for the NYCU 2026 創新創業競賽 pitch, and for longer-term LFN positioning, exceeds that cost many times over.

## Consequences

### Positive

- First K8s-native NTN Operator with published Nephio packages — reinforces "NTN + K8s + Nephio" positioning across OCUDU, 3GPP NTN WG, and CNCF narratives simultaneously.
- Downstream consumers on Nephio management clusters can pull the package with one `kpt pkg get` command.
- `kpt fn render` in CI becomes an early-warning signal for CRD/sample drift (changing a CRD field but forgetting to update the sample CR fails render).
- Pitches to judges, partners, and VCs can point to a concrete artifact ("here's our Nephio package") rather than an abstract credential ("the founder is on the TSC").

### Negative

- Adds a new build artifact (`nephio/packages/`) that must be kept in sync with `config/crd/bases/` and `config/samples/`. Mitigated by a `make nephio-sync` target that copies from canonical sources and a CI check that fails if the packages drift.
- Introduces a new CLI dependency (`kpt`) for contributors who modify the packages. Mitigated by `make nephio-install-tools` that downloads the official `kpt_linux_amd64` binary (no Go module build required).
- Binds us to Nephio R6 annotation conventions. If R7 changes them we will need to revisit the Kptfile. Low risk — R6 kept R5 conventions verbatim.

### Neutral

- No impact on the Go controller code, CRD definitions, Helm chart, or OLM bundle. Nephio integration is purely a distribution-side artifact.

## Validation plan

Tests live in `test/nephio/validate.sh`, wired through `make nephio-validate`:

1. **Package discoverability** — both `ntn-operators-crds` and `ntn-workloads-sample` directories exist under `nephio/packages/`.
2. **Kptfile syntactic validity** — `kpt pkg tree <pkg>` succeeds for both.
3. **Rendering** — `kpt fn render <pkg>` succeeds for both, non-empty output.
4. **CRD coverage** — all four expected CRD kinds appear in the CRDs package.
5. **Sample coverage** — all four expected sample CR kinds appear in the workloads package, with mutator-applied namespace and labels.
6. **K8s manifest validity** — `kubectl apply --dry-run=client -f <rendered-output>` succeeds for both (client-side dry-run avoids needing a live cluster in CI).
7. **Drift detection** — a separate `make nephio-verify-sync` compares SHA of `nephio/packages/ntn-operators-crds/*-crd.yaml` against `config/crd/bases/*.yaml`; mismatch fails CI.
8. **Supply-chain pinning (T15)** — `hack/check-kptfile-digest-pin.sh` scans every `pipeline.mutators` `image:` entry and fails if any reference is not `<registry>/<path>@sha256:<64-hex>`. Mirrors the action-SHA pinning discipline from `hack/check-action-shas.sh` (#107 / #109). Closes #114.

## Follow-ups (tracked in new issues, not in this ADR)

- v2: custom KRM function for cross-CR reference validation (NTNSlice.satellitePath.ephemerisRef exists in cluster).
- v2: PackageVariantSet example for multi-edge-site specialization (paired with a realistic `ClusterClaim` from a Nephio demo cluster).
- Post-v1: PR to `nephio-project/catalog` after E2E validation with at least one external Nephio user.
- Post-v1: GitOps bootstrap (Flux/ArgoCD) YAML in the README showing the full consumer flow.

## References

- [Nephio R6 release announcement (2026-04-01)](https://nephio.org/nephio-release-6-strengthening-stability-and-security/)
- [Nephio docs R6](https://docs.nephio.org/docs/)
- [nephio-project/catalog](https://github.com/nephio-project/catalog)
- [catalog/nephio/core/workload-crds/Kptfile (canonical minimal example)](https://raw.githubusercontent.com/nephio-project/catalog/main/nephio/core/workload-crds/Kptfile)
- [KRM Functions Catalog](https://catalog.kpt.dev/)
- [kpt fn render CLI reference](https://kpt.dev/reference/cli/fn/render/)
- [Porch releases](https://github.com/nephio-project/porch/releases)
- [Nephio governance (TSC charter)](https://github.com/nephio-project/governance)

Co-authored-by: junnncct1106 <jun.514114.ee10@nycu.edu.tw>
