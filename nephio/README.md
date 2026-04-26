# Nephio Integration

[ntn-operators](https://github.com/thc1006/ntn-operators) publishes its CRDs and sample workloads as [Nephio R6](https://nephio.org)-compatible kpt packages, so that Kubernetes-native NTN deployments can be consumed, versioned, and composed through the Linux Foundation Networking Nephio toolchain.

Design rationale lives in [ADR 0003](../docs/adr/0003-nephio-integration.md).

---

## Packages

This directory ships **two sibling kpt packages**. The split follows the convention of `nephio-project/catalog/nephio/core/workload-crds/`: CRDs are cluster-scoped installation artifacts, and sample CRs are namespace-scoped workload data. Consumers install the CRDs package once per cluster and instantiate the workloads package per namespace / per tenant.

| Package | Purpose | Pipeline | Details |
|---|---|---|---|
| [`packages/ntn-operators-crds`](packages/ntn-operators-crds) | Ships the 4 CRDs (`SatelliteEphemeris`, `GroundStationLifecycle`, `NTNCellConfig`, `NTNSlice`). Install once per cluster. | None (CRDs go in as-is). | [README](packages/ntn-operators-crds/README.md) |
| [`packages/ntn-workloads-sample`](packages/ntn-workloads-sample) | Ships one sample CR per kind, pre-filled with realistic LEO-constellation data. | `set-namespace` + `set-labels` from `ghcr.io/kptdev/krm-functions-catalog`. | [README](packages/ntn-workloads-sample/README.md) |

---

## Quick start

### On any machine with `kpt` installed

```bash
# 1) install ntn-operators CRDs
kpt pkg get https://github.com/thc1006/ntn-operators.git/nephio/packages/ntn-operators-crds@main ntn-operators-crds
kubectl apply -f ntn-operators-crds/

# 2) (separately) install the operator binary via Helm — see dist/chart/README.md
#    for the currently-published version; both stable and pre-release tags
#    live under the same OCI repo:
helm install ntn-operators oci://ghcr.io/thc1006/ntn-operators/charts/ntn-operators

# 3) pull, render, and apply sample workloads. kpt fn render mutates files
#    in place — that is the intended consumer flow (your local checkout
#    becomes the deployable artifact):
kpt pkg get https://github.com/thc1006/ntn-operators.git/nephio/packages/ntn-workloads-sample@main ntn-workloads-sample
kpt fn render ntn-workloads-sample    # applies set-namespace + set-labels
kubectl apply -f ntn-workloads-sample/
```

> **Repo maintainer note.** If you are running `kpt fn render` from inside the ntn-operators repo worktree (not from a `kpt pkg get` copy), render on a temp copy instead — `cp -r nephio/packages/ntn-workloads-sample /tmp/pkg && kpt fn render /tmp/pkg` — otherwise the source tree gets a `status:` block committed and the `nephio-verify-sync` drift check trips. The tests in `test/nephio/validate.sh` use this tmp-copy pattern for every in-place render (T3, T7, T10, T12).

### Via a Nephio management cluster (Porch)

Reference these packages from a PackageVariant in your deployment repo. Full CR examples:

- CRDs install: [`packages/ntn-operators-crds/README.md`](packages/ntn-operators-crds/README.md#on-a-nephio-management-cluster-porch) — shows `PackageVariant` pointing at this blueprint.
- Workload deployment: [`packages/ntn-workloads-sample/README.md`](packages/ntn-workloads-sample/README.md#on-a-nephio-management-cluster-porch) — shows per-cluster specialization with `packageContext` and an additional `apply-replacements` mutator.

---

## Contributor workflow

Everything runs from the repository root via `make`:

```bash
make nephio-install-tools    # install kpt (version pinned in Makefile) into $(go env GOPATH)/bin
make nephio-sync             # regenerate package CRDs from config/crd/bases/
make nephio-render           # run 'kpt fn render' on both packages (in-place — not for CI)
make nephio-validate         # run test/nephio/validate.sh (full suite, currently 15 assertions across Suites A/B/C)
make nephio-verify-sync      # CI drift check — fails if nephio/packages/ntn-operators-crds/*-crd.yaml diverges from config/crd/bases/
```

The test script `test/nephio/validate.sh` is the executable contract. It is hermetic (no live cluster needed) and is the single source of truth for whether the packages are releasable.

The same suite runs in CI via [`.github/workflows/nephio.yml`](../.github/workflows/nephio.yml) on every PR that touches `nephio/**`, `hack/check-kptfile-digest-pin.sh`, `test/nephio/**`, `config/crd/bases/**`, the `Makefile`, or the workflow file itself. PRs that do not touch any of those paths skip the suite by design.

### When you change a CRD

1. Edit `api/v1alpha1/*_types.go`
2. `make manifests` → regenerates `config/crd/bases/*.yaml`
3. `make nephio-sync` → copies bases into `nephio/packages/ntn-operators-crds/`
4. `make nephio-validate` → confirms everything still renders clean
5. Commit both the updated `config/crd/bases/*.yaml` and `nephio/packages/ntn-operators-crds/*-crd.yaml`

CI runs `make nephio-verify-sync` on every PR — forgetting step 3 fails the build.

### When you change a sample CR

Samples in `config/samples/ntn_v1alpha1_*.yaml` and package samples in `nephio/packages/ntn-workloads-sample/*-sample.yaml` are intentionally not byte-synced. The package samples have two extra properties:

- an explicit `namespace: default` field (so `set-namespace` has a value to rewrite)
- **no** hardcoded `workload` / `app.kubernetes.io/managed-by` labels (those are injected by `set-labels` at render time)
- `SatelliteEphemeris` only references ground stations that this package itself ships. The canonical `config/samples/ntn_v1alpha1_satelliteephemeris.yaml` references both `gs-taipei-01` and `gs-hsinchu-01` because the `config/samples/` directory also ships `ntn_v1alpha1_groundstationlifecycle_hsinchu.yaml`; the Nephio workloads package only ships `gs-taipei-01`, so the ephemeris sample is trimmed accordingly.

If you add a field to a sample, add it to both locations, then `make nephio-validate`.

### When you bump a KRM-function mutator version

Mutator images in `nephio/packages/ntn-workloads-sample/Kptfile` are pinned by `@sha256:` digest (T15). To bump a version:

1. Resolve the new digest from two independent sources before committing:

   ```bash
   docker buildx imagetools inspect ghcr.io/kptdev/krm-functions-catalog/<fn>:<new-tag>

   # Token extraction: jq if available, otherwise the python3 fallback below
   # (jq is not in the contributor prerequisites; python3 always is).
   TOKEN=$(curl -sL "https://ghcr.io/token?scope=repository:kptdev/krm-functions-catalog/<fn>:pull&service=ghcr.io" \
     | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
   curl -sLI \
     -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v2+json" \
     -H "Authorization: Bearer $TOKEN" \
     https://ghcr.io/v2/kptdev/krm-functions-catalog/<fn>/manifests/<new-tag> | grep -i docker-content-digest
   ```

2. Both must report identical `sha256:` values. Replace the `@sha256:` reference in the Kptfile and update the comment line above it with the new tag.
3. `make nephio-validate` — confirms T15 still passes and the rendered output matches expectations (T10 must show `namespace: ntn-system` after render).
4. Commit both lines (digest + comment) together so reviewers can audit the version bump in one diff.

For multi-arch images (OCI image index, e.g. `set-labels`), pin the **index** digest — `kpt fn render` resolves the per-platform manifest at runtime. Pinning a per-platform digest would break cross-arch render.

---

## Compatibility

| Component | Version |
|---|---|
| Nephio | R6 (released 2026-04-01) |
| Porch | v1.5.6+ |
| `kpt` CLI | v1.0.0-beta.55 or newer |
| Kubernetes | 1.29+ (CEL validation is GA from 1.29) |
| KRM function registry | `ghcr.io/kptdev/krm-functions-catalog` (Nephio R6 canonical; `gcr.io/kpt-fn/` tags are still current and functional) |
| KRM function pinning | `@sha256:` digest (enforced by `hack/check-kptfile-digest-pin.sh` and `test/nephio/validate.sh` T15) |

---

## Why this matters

Three reasons the integration is not cosmetic:

1. **Deployment story closure.** ntn-operators' author is a Nephio TSC member; shipping a Nephio-consumable package makes that TSC seat operational, not ornamental — Nephio users can consume us without hand-rolled kustomize overlays.
2. **Ecosystem fit.** Nephio is the LFN-sanctioned path for K8s-native telecom NF orchestration. Being on the Nephio package path is the cheapest route to inclusion in Sylva blueprints, Aether integration demos, and O-RAN SC handover scenarios.
3. **Deployment composition.** Downstream users pull our CRDs + samples through `kpt pkg get` and then compose them with other Nephio packages (5G core NFs, RIC, RAN) in the same deployment repo. GitOps tooling (Config Sync, ArgoCD, Flux) picks up the combined state without knowing NTN is involved.

---

## Limitations (v1)

- No `PackageVariantSet` manifests shipped here. Variants belong in the consumer's deployment repo; see each sub-README for example YAML.
- No Porch cluster bootstrap. Porch setup is documented at nephio.org — we don't duplicate.
- No custom KRM functions yet. A future `ntn-validator-fn` could validate cross-CR references (e.g., `NTNSlice.satellitePath.ephemerisRef` resolves to an existing `SatelliteEphemeris`). Tracked as a follow-up.
- We have not yet contributed these packages to `nephio-project/catalog` upstream. The plan is to submit after E2E validation with at least one external Nephio user confirms the packages work in their deployment flow.

---

## License

Apache License 2.0. See the [root LICENSE](../LICENSE).
