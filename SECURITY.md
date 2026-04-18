# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in NTN K8s Operators, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please email: **caake2025@gmail.com**

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

## Response Timeline

- **Acknowledgment**: Within 48 hours of report
- **Assessment**: Within 7 days
- **Fix**: Targeted within 30 days for critical issues

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | Yes       |

## Security Measures

This project implements the following security practices:

- **SSRF prevention**: All outbound HTTP clients validate resolved IPs against private ranges at the TCP dial level, including redirect targets (see `pkg/netutil/safeclient.go`)
- **Namespace isolation**: Controllers enforce that provider operations stay within the CR's own namespace
- **CEL CRD validation**: Server-side validation rules (URL scheme, lat/lon range, credential requirements) without webhook infrastructure
- **Secret management**: SpaceTrack credentials read from K8s Secrets with scoped RBAC (`secrets:get;list;watch`)
- **Read-only filesystem**: Container runs with `readOnlyRootFilesystem: true`
- **Non-root execution**: Container runs as UID 65532 (distroless nonroot)
- **Minimal capabilities**: All Linux capabilities are dropped
- **Dependency scanning**: Dependabot enabled for Go modules

## Disclosure Policy

We follow a coordinated disclosure model. After a fix is released, we will publish a security advisory on GitHub with credit to the reporter.
