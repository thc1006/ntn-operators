# Release process

This project follows [SemVer](https://semver.org) with the pre-1.0
relaxations documented below, and uses tag-triggered automation to
publish every release artifact.

## Cadence

- **Minor releases** every 2-4 weeks, covering a milestone worth of
  features (see `gh milestone list`).
- **Patch releases** within 24-72 hours when a critical fix lands.
- **Release candidates** precede every minor release — tag
  `vX.Y.Z-rc.N` before the final `vX.Y.Z`. Soak ≥ 3 days unless the
  change set is trivial.

Main must stay releasable. Any merged PR should be such that cutting
a tag at HEAD would produce a shippable artifact.

## SemVer while pre-1.0

- Major stays at 0.
- Minor may carry breaking CRD changes with a migration note in
  CHANGELOG.
- Patch is backwards-compatible fixes only.
- 1.0 will freeze the v1alpha1 → v1beta1 transition policy and commit
  to API stability.

## Cutting a release

Pre-requisites:
- CI green on `main`.
- `gh milestone list` shows the target milestone has no blocking open
  issues (paused / aspirational items moved to the next milestone).
- `CHANGELOG.md` has a dated entry for the new version under the
  `## [X.Y.Z] - YYYY-MM-DD` heading.
- `dist/chart/Chart.yaml`, `dist/chart/values.yaml`,
  `bundle/manifests/*.clusterserviceversion.yaml`,
  `config/manager/kustomization.yaml`, and `Makefile VERSION` all
  reference the new version. (The release workflow double-bumps the
  chart version from the tag at publish time; source consistency is
  still required so `helm template` off `main` produces sensible
  output.)
- The CSV's `spec.replaces` points at the previous **published** CSV
  so OLM's upgrade graph is continuous.

Steps:

1. Open a `release/vX.Y.Z` branch with the version bumps above.
2. Run `make test lint` locally. Review the bumped CSV under
   `operator-sdk bundle validate` if available.
3. Open a PR, land it, then:
   ```
   git tag -a vX.Y.Z-rc.1 -m "vX.Y.Z release candidate 1"
   git push origin vX.Y.Z-rc.1
   ```
4. Monitor `.github/workflows/release.yml`. It will:
   - ko build + Trivy scan + push multi-arch image to GHCR
   - cosign sign (keyless OIDC)
   - generate SPDX SBOM and attest it to the image index
   - package + push the Helm chart to `oci://ghcr.io/<owner>`
   - create the GitHub Release (auto-generated notes + chart tarball
     + SBOM JSON attached)
5. Smoke-test the RC:
   - Pull the image and run against a test cluster.
   - Install the chart via OCI and verify CRDs apply cleanly.
   - If there is a running cluster with the previous version, test
     OLM upgrade via the bundle image.
6. After soak, tag the final version:
   ```
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```
7. Mark the RC GitHub Release as pre-release (should be automatic
   because of the `-rc` suffix).

## Breaking changes

While pre-1.0 a minor bump may carry a breaking CRD change. When it
does:

- Add an **Upgrade notes** section to the CHANGELOG entry spelling
  out what changed and how to migrate.
- Provide a manual migration path (kubectl patch, in-place edit,
  migration pod) — conversion webhooks are deferred until 1.0.
- Announce the break in the GitHub Release body prominently.

## Artifacts a release produces

- Git tag, annotated
- GitHub Release with auto-generated notes + chart `.tgz` + SBOM
- Multi-arch container image at `ghcr.io/thc1006/ntn-operators:vX.Y.Z`
  plus `:latest`
- cosign signature attached to the image index
- SPDX SBOM attested to the image index
- Helm chart at `oci://ghcr.io/thc1006/ntn-operators`, version
  `X.Y.Z`, appVersion `X.Y.Z`
- OLM bundle image at
  `ghcr.io/thc1006/ntn-operators-bundle:vX.Y.Z`
  (after `make bundle-build bundle-push VERSION=X.Y.Z`)

## CNCF Sandbox / OperatorHub expectations

The project targets CNCF Sandbox and OperatorHub listing. Both value
release cadence over single-release novelty — the second release
matters as much as the first. Keep cadence tight and the CHANGELOG
honest.
