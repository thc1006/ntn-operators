# NTN K8s Operators

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/thc1006/ntn-operators)](go.mod)

Kubernetes-native management framework for Non-Terrestrial Networks (NTN). Declaratively manage satellite constellations, ground stations, NTN cell configurations, and terrestrial-satellite failover — all through standard Kubernetes CRDs.

## Custom Resource Definitions

| CRD | Short Name | Description |
|-----|-----------|-------------|
| **SatelliteEphemeris** | `sateph` | Auto-fetches GP data (OMM JSON) from CelesTrak or SpaceTrack, runs SGP4 pass prediction |
| **GroundStationLifecycle** | `gs` | Manages edge ground station nodes — health checks, firmware OTA with timeout, K8s integration |
| **NTNCellConfig** | `ntncc` | Configures NTN gNB cells via OCUDU/srsRAN provider (generates ConfigMap with OwnerReference) |
| **NTNSlice** | `nts` | Manages terrestrial-satellite slice failover, QoS mapping, security policy, billing |

## Architecture

```mermaid
graph TB
    subgraph "Kubernetes Cluster"
        subgraph "NTN Operators"
            SE[SatelliteEphemeris<br/>Controller]
            GS[GroundStationLifecycle<br/>Controller]
            NC[NTNCellConfig<br/>Controller]
            NS[NTNSlice<br/>Controller]
        end

        subgraph "Provider Layer"
            OP[OCUDU Provider]
            CM[ConfigMap<br/>geo_ntn.yml]
        end

        subgraph "Failover Engine"
            FE[Failover State Machine]
        end

        subgraph "Observability"
            PM[Prometheus Metrics]
        end

        NC --> OP
        OP --> CM
        NS --> FE
        SE & GS & NC & NS --> PM
    end

    CL[CelesTrak API] -.-> SE
    ST[SpaceTrack API] -.-> SE
    ND[K8s Node] -.-> GS
    CM -.-> GNB[srsRAN gNB]
```

**Data sources**: SatelliteEphemeris supports CelesTrak (public, no auth) and SpaceTrack (requires credentials via K8s Secret). Both use the same OMM JSON format parsed by SGP4.

**Provider pattern**: NTNCellConfig uses a pluggable provider interface (currently OCUDU/srsRAN). OAI and Aalyria providers are planned for v0.2.

**Failover engine**: NTNSlice evaluates trigger conditions (RSRP, latency, packet loss) and manages terrestrial-satellite path switching with configurable switchback delay. QoS, security, and billing parameters are tracked per active path.

**Validation**: CRD-level CEL validation rules enforce constraints (lat/lon range, SpaceTrack requires credentials, path priority consistency, ECEF non-zero) without webhook infrastructure.

## Quick Start

### Prerequisites

- Go 1.25+
- Kubernetes 1.29+ (for CEL validation)
- kubectl
- Helm v3.16+ (optional, for Helm-based install)

### Option A: Helm Install

```bash
# Install CRDs
make install

# Deploy via Helm
helm install ntn-operators dist/chart \
  --namespace ntn-operators-system \
  --create-namespace \
  --set crd.enable=false
```

### Option B: One-command Kind Demo

```bash
hack/kind-setup.sh
# Tear down when done:
hack/kind-setup.sh teardown
```

### Option C: Run Locally

```bash
make install    # Install CRDs
make run        # Run controller locally (connects to current kubeconfig)
```

### Apply Sample Resources

```bash
# Satellite constellation tracking (CelesTrak)
kubectl apply -f config/samples/ntn_v1alpha1_satelliteephemeris.yaml

# Ground stations (Taipei + Hsinchu)
kubectl apply -f config/samples/ntn_v1alpha1_groundstationlifecycle.yaml
kubectl apply -f config/samples/ntn_v1alpha1_groundstationlifecycle_hsinchu.yaml

# NTN cell configuration (OCUDU/srsRAN)
kubectl apply -f config/samples/ntn_v1alpha1_ntncellconfig.yaml

# Terrestrial-satellite slice failover
kubectl apply -f config/samples/ntn_v1alpha1_ntnslice.yaml
```

### Verify

```bash
# Check satellite ephemeris data
kubectl get sateph
# NAME                   SATELLITES   LAST UPDATED   AGE
# oneweb-constellation   651          2m             5m

# Check ground station status
kubectl get gs
# NAME            PHASE          VENDOR     K8S       AGE
# gs-taipei-01    Provisioning   ennoconn   v1.35.1   5m

# Check NTN cell configuration
kubectl get ntncellconfigs
# NAME                PROVIDER   KOFFSET   PAYLOAD       AGE
# ntn-cell-geo-demo   ocudu      150       transparent   5m

# Check NTN slice failover
kubectl get ntnslices
# NAME                         TENANT      ACTIVE PATH   FAILOVERS   AGE
# enterprise-resilient-slice   acme-corp   terrestrial   0           5m
```

## Observability

### Prometheus Metrics

The operator exports custom metrics at `:8443/metrics`:

| Metric | Type | Description |
|--------|------|-------------|
| `ntn_operators_failover_total` | Counter | Failover events by slice, source/target path |
| `ntn_operators_satellite_pass_available` | Gauge | Satellite pass window active (1/0) |
| `ntn_operators_ground_station_health` | Gauge | Station condition status (1/0/-1) |
| `ntn_operators_config_apply_errors_total` | Counter | Cell config apply failures |
| `ntn_operators_gp_fetch_duration_seconds` | Histogram | GP data fetch duration |
| `ntn_operators_gp_satellite_count` | Gauge | Satellites from latest fetch |

## SpaceTrack Integration

To use SpaceTrack as a data source, create a Secret with your credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: spacetrack-creds
type: Opaque
stringData:
  username: your-spacetrack-username
  password: your-spacetrack-password
```

Then reference it in the SatelliteEphemeris:

```yaml
apiVersion: ntn.operators.dev/v1alpha1
kind: SatelliteEphemeris
metadata:
  name: oneweb-spacetrack
spec:
  source:
    type: SpaceTrack
    url: https://www.space-track.org/basicspacedata/query/class/gp/GROUP/oneweb/format/json
    refreshInterval: 4h
    credentials:
      name: spacetrack-creds
      key: password
```

## Development

```bash
make generate   # Generate DeepCopy methods
make manifests  # Generate CRD and RBAC YAML
make test       # Run unit + envtest tests
make lint       # Run golangci-lint
make build      # Build manager binary
make docs       # Generate API reference from CRDs
make ko-build   # Build container image with ko (local)
make ko-push    # Build and push multi-arch image
make test-e2e   # Run E2E tests on Kind cluster
```

## Project Structure

```
api/v1alpha1/           # CRD type definitions (with CEL validation rules)
internal/controller/    # Reconciler implementations
pkg/ephemeris/          # CelesTrak + SpaceTrack GP fetchers, SGP4 pass prediction
pkg/provider/ocudu/     # OCUDU/srsRAN provider (config generation)
pkg/slice/              # Failover state machine + trigger parser
pkg/netutil/            # SSRF-safe HTTP client
pkg/metrics/            # Custom Prometheus metrics
config/crd/             # Generated CRD YAML
config/samples/         # Sample CR manifests
dist/chart/             # Helm chart
hack/                   # Demo and setup scripts
docs/                   # API reference (auto-generated)
```

## Known Limitations (v0.1)

- **Metrics source**: Failover trigger metrics are read from CR annotations (`ntn.operators.dev/simulated-*`). Production UPF/Prometheus integration is planned for v0.2.
- **Antenna health**: `AntennaReady` condition is simulated as True when the node exists. Real hardware integration requires vendor-specific agents.
- **Session continuity**: The `sessionContinuity` field is tracked but not enforced at the data plane level.
- **Providers**: Only OCUDU/srsRAN is implemented. OAI and Aalyria are planned for v0.2.
- **Firmware updates**: The controller monitors node annotations for firmware versions but does not directly trigger OTA. An external agent on the node manages the actual update.

## API Reference

See [docs/api-reference.md](docs/api-reference.md) for the complete CRD field reference.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

## License

[Apache License 2.0](LICENSE)
