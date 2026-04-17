# Governance

This document describes the governance model for the NTN K8s Operators project.

## Maintainers

Maintainers are responsible for the technical direction of the project, reviewing and merging pull requests, and managing releases.

| Name | GitHub | Role |
|------|--------|------|
| Hsiu-Chi Tsai (蔡秀吉) | [@thc1006](https://github.com/thc1006) | Lead Maintainer |

## Decision Process

- **Consensus-based**: Decisions are made by discussion on GitHub Issues and Pull Requests.
- **Maintainer approval**: All code changes require approval from at least one maintainer.
- **Lazy consensus**: Proposals on Issues are accepted if there are no objections within 7 days.

## Contributing

Anyone is welcome to contribute. See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

### Becoming a Maintainer

Contributors who have demonstrated sustained, high-quality contributions may be invited to become maintainers. The existing maintainers will reach consensus before extending an invitation.

Criteria:
- Sustained contributions over 3+ months
- Demonstrated understanding of the NTN domain and Kubernetes operator patterns
- Constructive participation in code reviews and issue discussions

## Code of Conduct

This project follows the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/main/code-of-conduct.md). Violations can be reported to the maintainers.

## Releases

- Releases follow [Semantic Versioning](https://semver.org/).
- Release candidates are tagged as `-rc.N` before stable releases.
- CRD API versions follow Kubernetes conventions: `v1alpha1` -> `v1beta1` -> `v1`.
