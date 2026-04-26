# ntn-operators-crds

CustomResourceDefinitions for [ntn-operators](https://github.com/thc1006/ntn-operators), packaged as a [Nephio](https://nephio.org) R6-compatible kpt package.

This package installs four CRDs on a Kubernetes cluster. It does not install the operator binary itself — see the repository root for Helm and OLM options.

## Contents

| File | Purpose |
|---|---|
| `Kptfile` | Package metadata. No mutation pipeline — CRDs are installed as-is. |
| `satelliteephemeris-crd.yaml` | `SatelliteEphemeris` — satellite ephemeris tracking, SGP4 propagation, pass-window prediction. |
| `groundstationlifecycles-crd.yaml` | `GroundStationLifecycle` — ground-station edge node lifecycle (firmware OTA, health, hardware match). |
| `ntncellconfigs-crd.yaml` | `NTNCellConfig` — 3GPP TS 38.331 NTN-Config-r17 cell parameters (ephemeris, k-offset, TA, polarization, k_mac). |
| `ntnslices-crd.yaml` | `NTNSlice` — geo-satellite resilient slice with failover policy, QoS mapping, billing, and session continuity fields. |

The CRD YAMLs are regenerated from `api/v1alpha1/*_types.go` via `controller-gen` and synced into this package by `make nephio-sync`. Do not edit them by hand.

## Prerequisites

- Kubernetes 1.29+ (CEL validation is used extensively, and is GA from 1.29)
- `kpt` v1.0.0-beta.55+ for package consumption (`kpt version`)
- Cluster-admin permissions on the target cluster (CRD install is cluster-scoped)

## Consume

### Quick one-off install

```bash
kpt pkg get https://github.com/thc1006/ntn-operators.git/nephio/packages/ntn-operators-crds@main ntn-operators-crds
kubectl apply -f ntn-operators-crds/
```

### Render through kpt (no mutation, but verifies freshness)

```bash
kpt pkg get https://github.com/thc1006/ntn-operators.git/nephio/packages/ntn-operators-crds@main ntn-operators-crds
kpt fn render ntn-operators-crds
kpt live init ntn-operators-crds
kpt live apply ntn-operators-crds --reconcile-timeout=2m
```

### On a Nephio management cluster (Porch)

Create a `PackageVariant` in your deployment repo that draws from this blueprint:

```yaml
apiVersion: config.porch.kpt.dev/v1alpha1
kind: PackageVariant
metadata:
  name: ntn-operators-crds-edge01
spec:
  upstream:
    repo: thc1006-ntn-operators
    package: nephio/packages/ntn-operators-crds
    revision: main
  downstream:
    repo: deploy-edge01
    package: ntn-operators-crds
  adoptionPolicy: adoptExisting
  deletionPolicy: delete
```

The CRDs land cluster-wide, and per-cluster customization (namespace scoping of operator, labels, cluster name) is handled by the companion `ntn-workloads-sample` package.

## Uninstall

```bash
kubectl delete -f ntn-operators-crds/
```

Warning: deleting a CRD cascade-deletes every CustomResource of that kind. If you want to preserve existing CRs before deleting the CRDs, export them first (`kubectl get -A <kind> -o yaml > backup.yaml`) or adopt a GitOps workflow (ArgoCD/Flux) that retains the CRDs separately from the CRs.

## Version compatibility

| ntn-operators tag | Nephio release tested | K8s tested |
|---|---|---|
| `main` | R6 (Porch v1.5.6) | 1.29 - 1.35 |
| `v0.1.0` | (not validated on Nephio) | 1.29+ |

## License

Apache License 2.0 — see the [top-level LICENSE](https://github.com/thc1006/ntn-operators/blob/main/LICENSE) in the ntn-operators repository.
