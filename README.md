# NTN K8s Operators

Kubernetes-native management framework for Non-Terrestrial Networks (NTN).

> **Status**: Pre-alpha. CRD API design in progress.

## What is this?

A set of Kubernetes CRDs and Controllers for declaratively managing satellite-terrestrial integrated networks:

- **SatelliteEphemeris** — TLE auto-update, SGP4 pass prediction
- **GroundStationLifecycle** — Edge station health monitoring, OTA firmware, GitOps config
- **NTNCellConfig** — NTN base station beam/frequency configuration
- **NTNSlice** — Terrestrial↔satellite failover policy, QoS mapping, billing CDR generation

## Architecture

Uses the Crossplane-style Provider pattern to abstract NTN backends:
- OCUDU/srsRAN (open-source NTN gNB)
- Aalyria Spacetime API (gRPC)
- ST Engineering iDirect / Gilat (future)

Nephio-compatible but not Nephio-dependent.

## Quick Start

```bash
# Prerequisites: Go 1.25+, kubebuilder v4.13.1, kind or K3s
make generate
make manifests
make test
make docker-build
```

## License

[Apache License 2.0](LICENSE)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md)
