---
adr: 3
title: Distribute CRDs and examples as Nephio kpt packages
status: accepted
date: 2026-04-19
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation: []
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/164"
---

# ADR 0003 — Distribute CRDs and examples as Nephio kpt packages

## Decision summary

Publish two sibling kpt packages inside this repository:

1. `ntn-operators-crds` — generated CRDs only.
2. `ntn-workloads-sample` — example custom resources and safe package
   customization.

The Kubernetes Go types and `config/crd/bases` remain canonical. Nephio files
are generated distribution artifacts and CI rejects drift.

## Context

CRDs have a cluster-level lifecycle; workload examples have a
namespace/site/tenant lifecycle. Combining them forces consumers to install
sample objects whenever they install CRDs and makes PackageVariant workflows
harder to reason about.

The operator Deployment itself continues to ship through Helm, OLM or direct
manifests. kpt packages do not become a second operator installer.

## Decision drivers

- One canonical CRD source.
- Renderable packages without a live Porch installation.
- Reproducible, pinned KRM functions.
- Clear separation of platform API and workload instances.
- No special publishing privileges assumed from project governance roles.

## Decision

### Package 1: `ntn-operators-crds`

Contains:

- `Kptfile`;
- README;
- generated CRD YAML copied from `config/crd/bases`.

No mutator pipeline is required.

### Package 2: `ntn-workloads-sample`

Contains:

- `Kptfile`;
- README;
- representative CRs;
- only necessary namespace/label mutators.

KRM function images MUST be pinned by digest. Tags may be retained as comments
for human readability but are not execution identity.

### Tool installation

`make nephio-install-tools` must:

- select supported OS/architecture;
- download from an official release;
- verify a published checksum;
- install to a repository-local tool directory;
- never require privileged system installation.

### Synchronization

`make nephio-sync` copies or regenerates from canonical paths. CI runs the
command and fails if `git diff --exit-code` is non-empty.

Semantic CRD equivalence must be checked across:

- `config/crd/bases`;
- Helm CRDs;
- OLM bundle manifests;
- Nephio CRD package.

Do not rely only on filenames or a partial checksum map.

### What is not decided

- No PackageVariant is shipped for a consumer’s repository layout.
- No Porch bootstrap is embedded.
- No custom validator KRM function is introduced until a concrete
  cross-resource validation requirement cannot be handled by the API server.
- Upstream contribution to the Nephio catalog remains precondition-gated.

## Invariants

- Editing a Nephio copy directly is invalid.
- Every executable function image has a digest.
- Rendering is deterministic and leaves the source package clean.
- CRD installation does not create sample workload instances.
- Package documentation states the supported Nephio/kpt baseline rather than
  claiming indefinite forward compatibility.

## Security and supply chain

- Verify tool checksums.
- Pin GitHub Actions by commit SHA.
- Pin KRM functions by OCI digest.
- Record SBOM/signature/provenance status where upstream provides it.
- Do not treat a digest as proof of publisher identity; it only fixes content.
- Keep mutable credentials out of package files.

## Test plan

- `kpt pkg tree` for both packages.
- `kpt fn render` with no network mutation after images are resolved.
- server-side dry-run against the declared minimum Kubernetes version.
- schema/sample coverage.
- generated-copy drift check.
- digest-pin checker.
- reproducibility test: two renders produce identical output.
- negative test: a direct edit to a copied CRD fails CI.

## Rollout

1. Freeze canonical source paths.
2. Add sync and verify targets.
3. Add packages.
4. Add digest/checksum enforcement.
5. Run render/apply on a real cluster.
6. Seek an external consumer before proposing upstream catalog inclusion.

## References

- Nephio documentation:
  https://docs.nephio.org/
- Nephio release notes:
  https://docs.nephio.org/docs/release-notes/
- Porch:
  https://github.com/nephio-project/porch
- kpt package get:
  https://kpt.dev/reference/cli/pkg/get/
- kpt function render:
  https://kpt.dev/reference/cli/fn/render/
- Nephio catalog:
  https://github.com/nephio-project/catalog
