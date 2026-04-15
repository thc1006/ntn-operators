# Contributing to NTN K8s Operators

Thank you for your interest! This project is in early development.

## Development Setup

```bash
# Prerequisites
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

# Build & Test
make generate    # Generate deepcopy and CRD manifests
make manifests   # Generate RBAC and CRD YAML
make lint        # Run golangci-lint
make test        # Unit + integration (envtest)
make test-e2e    # End-to-end with kind cluster
```

## CRD Design Principles

1. **Declarative** — describe desired state, controller reconciles
2. **Provider-agnostic** — same CRD works with different NTN backends
3. **Nephio-compatible** — follows KRM conventions, works with PackageVariant
4. **GitOps-ready** — all config is YAML, ArgoCD/Flux friendly
5. **Versionable** — v1alpha1 → v1beta1 → v1, following K8s API conventions
