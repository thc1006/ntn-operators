# ntn-workloads-sample

Ready-to-adapt sample workload CRs for [ntn-operators](https://github.com/thc1006/ntn-operators), packaged as a [Nephio](https://nephio.org) R6-compatible kpt package with a 2-step mutation pipeline.

Install the sibling [`ntn-operators-crds`](../ntn-operators-crds) package first; this one depends on those CRDs being present.

## Contents

| File | Purpose |
|---|---|
| `Kptfile` | Package metadata + `pipeline.mutators` with `set-namespace` and `set-labels`. |
| `satelliteephemeris-sample.yaml` | `SatelliteEphemeris` — OneWeb constellation fetched from CelesTrak every 4h, pass-predicting against two Taiwan ground stations. |
| `groundstationlifecycle-sample.yaml` | `GroundStationLifecycle` — Taipei ground station on Ennoconn rugged-edge hardware, K3s edge distro. |
| `ntncellconfig-sample.yaml` | `NTNCellConfig` — GEO satellite cell, OCUDU provider, transparent payload. |
| `ntnslice-sample.yaml` | `NTNSlice` — enterprise resilient slice: terrestrial primary + OneWeb failover, AES-256, per-volume/per-minute billing. |

## Pipeline

The `Kptfile` declares a two-step mutator chain that runs on `kpt fn render`:

1. **`set-namespace`** (`ghcr.io/kptdev/krm-functions-catalog/set-namespace:v0.4.1`) — rewrites every CR's `metadata.namespace` to the configured value (default: `ntn-system`).
2. **`set-labels`** (`ghcr.io/kptdev/krm-functions-catalog/set-labels:v0.2.1`) — stamps common labels on every CR:
   - `workload: ntn-sample`
   - `app.kubernetes.io/managed-by: ntn-operators`

Both images come from the Nephio R6 canonical KRM function catalog.

## Expected status on a demo cluster

The samples reference realistic NTN inputs (CelesTrak OneWeb feed, Taipei ground station on Ennoconn rugged-edge hardware, GEO cell, terrestrial+OneWeb failover slice). On a plain demo cluster without a Node labeled `ntn.operators.dev/groundstation=<ns>.gs-taipei-01`, you will see:

- `SatelliteEphemeris/oneweb-constellation` — `GPDataFetched=True`, `GPDataParsed=True`, 650+ satellites counted. Pass prediction succeeds because the sample references only `gs-taipei-01`, which this package ships.
- `GroundStationLifecycle/gs-taipei-01` — phase `Provisioning`, `K8sNodeReady=False` with reason `NodeNotFound`. This is the controller correctly reporting missing hardware, not a package defect — label one of your nodes to advance it.
- `NTNCellConfig/ntn-cell-geo-demo` — `ConfigApplied=True`, generates `ConfigMap/ocudu-ntn-ntn-cell-geo-demo` with OCUDU `geo_ntn.yml` content.
- `NTNSlice/enterprise-resilient-slice` — `PathActive=True` with `activePathType: terrestrial`, failover decision `stay` (terrestrial path healthy).

Cross-verified on K8s v1.35.4 on 2026-04-24.

## Consume

### Minimum commands

```bash
# 1. pull the package
kpt pkg get https://github.com/thc1006/ntn-operators.git/nephio/packages/ntn-workloads-sample@main ntn-workloads-sample

# 2. (optional) edit Kptfile to change target namespace/labels
$EDITOR ntn-workloads-sample/Kptfile

# 3. render (applies the mutators in place)
kpt fn render ntn-workloads-sample

# 4. apply
kubectl apply -f ntn-workloads-sample/satelliteephemeris-sample.yaml \
              -f ntn-workloads-sample/groundstationlifecycle-sample.yaml \
              -f ntn-workloads-sample/ntncellconfig-sample.yaml \
              -f ntn-workloads-sample/ntnslice-sample.yaml
```

### Using kpt live (Nephio-native)

```bash
kpt pkg get https://github.com/thc1006/ntn-operators.git/nephio/packages/ntn-workloads-sample@main ntn-workloads-sample
kpt fn render ntn-workloads-sample
kpt live init ntn-workloads-sample
kpt live apply ntn-workloads-sample --reconcile-timeout=2m
```

### On a Nephio management cluster (Porch)

Use a PackageVariant in your deployment repo to clone this blueprint and inject per-cluster context:

```yaml
apiVersion: config.porch.kpt.dev/v1alpha1
kind: PackageVariant
metadata:
  name: ntn-workloads-edge01
spec:
  upstream:
    repo: thc1006-ntn-operators
    package: nephio/packages/ntn-workloads-sample
    revision: main
  downstream:
    repo: deploy-edge01
    package: ntn-workloads-edge01
  packageContext:
    data:
      cluster-name: edge01
  pipeline:
    mutators:
      - image: ghcr.io/kptdev/krm-functions-catalog/apply-replacements:v0.1.5
        configMap: {}
```

## Customize

| What to change | Where |
|---|---|
| Target namespace | `Kptfile` → `pipeline.mutators[set-namespace].configMap.namespace` |
| Labels stamped on all CRs | `Kptfile` → `pipeline.mutators[set-labels].configMap` |
| Satellite constellation | `satelliteephemeris-sample.yaml` → `spec.satellites.constellation` + `spec.source.url` |
| Ground station hardware / location | `groundstationlifecycle-sample.yaml` → `spec.hardware`, `spec.deployment.location` |
| Cell ephemeris / payload type | `ntncellconfig-sample.yaml` → `spec.ntn` |
| Failover triggers / QoS / billing | `ntnslice-sample.yaml` → `spec.failoverPolicy`, `spec.qosMapping`, `spec.billing` |

After editing, re-run `kpt fn render` to re-apply the pipeline, then `kubectl apply` or `kpt live apply`.

## Validation

Before shipping changes to this package, run from the repository root:

```bash
make nephio-validate
```

This runs `test/nephio/validate.sh`, which verifies the package renders cleanly, produces all four expected kinds, applies the namespace mutation correctly, and passes `kubectl apply --dry-run=client` parsing.

## License

Apache License 2.0 — see the [top-level LICENSE](https://github.com/thc1006/ntn-operators/blob/main/LICENSE) in the ntn-operators repository.
