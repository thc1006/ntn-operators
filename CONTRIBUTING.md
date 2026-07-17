# Contributing to NTN K8s Operators

Thank you for your interest in contributing!

## Development Setup

```bash
# Prerequisites: Go 1.26.5+, kubebuilder v4.13+, kubectl, Kind

# Build & Test
make generate    # Generate DeepCopy methods
make manifests   # Generate CRD, RBAC, and webhook YAML
make lint        # Run golangci-lint
make test        # Unit + envtest tests
make test-e2e    # E2E tests on Kind cluster
make docs        # Generate API reference from CRDs
make ko-build    # Build container image with ko (local)
```

## CRD Design Principles

1. **Declarative** — describe desired state, controller reconciles
2. **Provider-agnostic** — same CRD works with different NTN backends
3. **CEL-validated** — CRD-level validation without webhook infrastructure
4. **Observable** — custom Prometheus metrics for all domain events
5. **GitOps-ready** — all config is YAML, ArgoCD/Flux friendly

## Pull Request Guidelines

- Run `make lint && make test` before opening a PR
- Follow existing code style (enforced by golangci-lint)
- Add tests for new features (target ≥80% coverage)
- Update `docs/api-reference.md` if CRD types change (`make docs`)
- Update `CHANGELOG.md` with your changes
