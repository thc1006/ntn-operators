# ntn-operators Helm Chart

Kubernetes Operators for Non-Terrestrial Network (NTN) management — satellite ephemeris, ground station lifecycle, cell configuration, and slice failover.

## Quick Start

```bash
helm install ntn-operators oci://ghcr.io/thc1006/ntn-operators/charts/ntn-operators
```

## CRDs

This chart installs four Custom Resource Definitions:

| CRD | Short Name | Description |
|-----|-----------|-------------|
| `SatelliteEphemeris` | `sateph` | GP data fetching (CelesTrak/SpaceTrack), SGP4 propagation, pass prediction |
| `GroundStationLifecycle` | `gs` | Ground station health monitoring, firmware OTA, GitOps config |
| `NTNCellConfig` | — | NTN cell parameters (SIB19, ephemeris, TA) pushed to gNB via provider |
| `NTNSlice` | `nts` | Terrestrial-satellite failover, QoS mapping, session continuity |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `manager.replicas` | int | `1` | Number of controller manager replicas |
| `manager.image.repository` | string | `ghcr.io/thc1006/ntn-operators` | Manager container image |
| `manager.image.tag` | string | `v0.4.0` | Image tag |
| `manager.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `manager.args` | list | `[--leader-elect]` | Extra args passed to the manager binary |
| `manager.env` | list | `[]` | Environment variables |
| `manager.envOverrides` | object | `{}` | Env overrides (`--set manager.envOverrides.VAR=value`) |
| `manager.imagePullSecrets` | list | `[]` | Image pull secrets |
| `manager.resources.limits.cpu` | string | `500m` | CPU limit |
| `manager.resources.limits.memory` | string | `128Mi` | Memory limit |
| `manager.resources.requests.cpu` | string | `10m` | CPU request |
| `manager.resources.requests.memory` | string | `64Mi` | Memory request |
| `manager.podSecurityContext.runAsNonRoot` | bool | `true` | Run as non-root |
| `manager.podSecurityContext.runAsUser` | int | `65532` | UID |
| `manager.podSecurityContext.runAsGroup` | int | `65532` | GID |
| `manager.affinity` | object | `{}` | Pod affinity rules |
| `manager.nodeSelector` | object | `{}` | Node selector |
| `manager.tolerations` | list | `[]` | Pod tolerations |
| `crd.enable` | bool | `true` | Install CRDs with the chart |
| `crd.keep` | bool | `true` | Keep CRDs when uninstalling |
| `rbacHelpers.enable` | bool | `false` | Install admin/editor/viewer ClusterRoles |
| `metrics.enable` | bool | `true` | Expose /metrics endpoint |
| `metrics.port` | int | `8443` | Metrics server port |
| `certManager.enable` | bool | `false` | Enable cert-manager integration |
| `prometheus.enable` | bool | `false` | Create ServiceMonitor for Prometheus |
| `podDisruptionBudget.enable` | bool | `false` | Create PodDisruptionBudget |
| `podDisruptionBudget.minAvailable` | int | `1` | Minimum available pods |
| `networkPolicy.enable` | bool | `false` | Create NetworkPolicy (requires CNI support) |

## Production Configuration

```yaml
manager:
  replicas: 2
  resources:
    limits:
      cpu: "1"
      memory: 256Mi
    requests:
      cpu: 100m
      memory: 128Mi

crd:
  keep: true

metrics:
  enable: true

prometheus:
  enable: true

podDisruptionBudget:
  enable: true
  minAvailable: 1

networkPolicy:
  enable: true
```

## Upgrading

When upgrading, CRDs are updated automatically if `crd.enable: true`. To prevent CRD deletion on uninstall, keep `crd.keep: true` (default).

```bash
helm upgrade ntn-operators oci://ghcr.io/thc1006/ntn-operators/charts/ntn-operators
```
