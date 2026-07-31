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
| 0.7.x   | Yes       |
| < 0.7.0 | No        |

## Security Measures

This project implements the following security practices:

- **SSRF prevention**: All outbound HTTP clients validate resolved IPs against private ranges at the TCP dial level, including redirect targets (see `pkg/netutil/safeclient.go`)
- **Namespace isolation**: Controllers enforce that provider operations stay within the CR's own namespace
- **CEL CRD validation**: Server-side validation rules (URL scheme, lat/lon range, credential requirements) without webhook infrastructure
- **Secret management**: SpaceTrack credentials read from K8s Secrets with a minimal RBAC of `secrets:get` only — an uncached, per-request read. The operator holds no `list` or `watch` on Secrets.
- **Credential-reference authorization** (opt-in, `credentialRefPolicy.enable`): a `ValidatingAdmissionPolicy` requiring the principal who writes an `NTNCellConfig` to hold `get` on the Secret its `spec.provider.remoteControl.tls.secretName` references. The operator reads that Secret with its own privilege and presents it to a CR-author-chosen endpoint, so without this a principal who can write the CR but not read the Secret can still cause it to be used — a confused deputy (#251, ADR-0009). Enforced inside kube-apiserver, so it needs no webhook and grants the operator no extra RBAC. **Off by default**: enabling it is a real tightening, so run it with `validationActions: [Warn, Audit]` first. Complements the admin endpoint allow-list (`--remote-control-allowed-endpoint-hosts`), which constrains the destination rather than the reference.
- **Read-only filesystem**: Container runs with `readOnlyRootFilesystem: true`
- **Non-root execution**: Container runs as UID 65532 (distroless nonroot)
- **Minimal capabilities**: All Linux capabilities are dropped
- **Dependency scanning**: Dependabot enabled for Go modules

## Disclosure Policy

We follow a coordinated disclosure model. After a fix is released, we will publish a security advisory on GitHub with credit to the reporter.
