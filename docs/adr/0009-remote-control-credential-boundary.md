---
adr: 9
title: Layered authorization boundary for remote-control credentials
status: accepted
date: 2026-07-31
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: ["previous amendment-accumulating revision of ADR 0009"]
superseded_by: []
implementation: []
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/251"
  - "https://github.com/thc1006/ntn-operators/issues/299"
---

# ADR 0009 — Layered authorization boundary for remote-control credentials

## Decision summary

Remote-control credential use is protected by four intersecting layers:

1. admission-time requester authorization;
2. runtime owner-issued grant when the stronger mode is enabled;
3. administrator destination allow-list;
4. network egress enforcement.

The existing Secret label/type checks are defense in depth, not authorization.
ADR 0011 defines the revocable credential grant.

## Threat model

A principal may be allowed to create or update `NTNCellConfig` but not read
Secrets or directly reach internal network targets. The operator has broader
Secret and network privileges. Without controls, the principal can use it as a
confused deputy and SSRF relay.

The attacker does not control the operator Deployment or cluster-admin policy.

## Decision drivers

- Same-namespace Secret references still require authorization.
- Existing objects need explicit treatment after RBAC changes.
- Credentials must be bound to destinations and modes.
- Destination denial must occur before Secret read.
- Controls must fail closed when enabled and remain observable.

## Decision

### Layer 1: admission requester authorization

Ship a `ValidatingAdmissionPolicy` and binding requiring the requesting
principal to have `get` on the referenced Secret.

Use the CEL `authorizer` exposed by the API server. No controller SAR RBAC or
webhook certificate is needed.

Rollout:

1. `Warn` + `Audit`;
2. review violations;
3. `Deny`.

The policy must match every served API version containing the reference.
Adding v1alpha2 without updating the policy is a security regression.

This is a point-in-time authorization check. It does not revoke stored objects.

### Layer 2: runtime credential grant

When `--require-credential-grant` is enabled, the operator requires an
owner-issued grant described by ADR 0011. The grant binds Secret, consumer,
destination and mode and is watchable/revocable.

### Layer 3: admin destination allow-list

An operator-controlled allow-list constrains all remote-control targets,
credentialed or plaintext. Empty legacy behavior may remain permit-all for
v1alpha1 compatibility, but the v1alpha2 secure profile is fail closed.

The target is checked before reading a Secret.

### Layer 4: NetworkPolicy

Ship an egress policy example that permits only required DNS/API/source/gNB
destinations. Documentation must say that enforcement depends on the CNI.

NetworkPolicy does not replace object-level authorization.

### Secret label/type gate

Continue to require explicit credential labeling and reject Kubernetes API
credential Secret types. This narrows mistakes but does not prove that a CR
author may use the credential.

## Invariants

- Denied destination means no Secret read and no dial.
- Admission policy includes all served versions.
- Missing policy parameters fail according to explicit `Fail`/`Deny`.
- Stored objects are not described as reauthorized by VAP.
- Runtime grant and admin allow-list are both required when configured.
- Errors exposed to a tenant do not reveal Secret existence/type.

## Alternatives

**Secret label only.** Rejected; namespace-wide and patchable.

**Admission authorization only.** Insufficient for revocation and endpoint
binding, retained as the baseline.

**Grant only.** Insufficient at object creation if disabled or misconfigured;
admission remains useful.

**NetworkPolicy only.** Rejected; it cannot express which credential belongs to
which CR.

## Observability

- admission audit annotations;
- runtime denial counters by non-secret reason;
- no Secret name in low-trust metrics labels;
- grant deletion/narrowing events;
- stale authorization/grant conditions;
- allow-list configuration status.

## Test plan

- CR writer without Secret `get` denied;
- granting `get` allows admission;
- every served API version is covered;
- missing policy/parameter fails closed in secure profile;
- rejected destination causes zero Secret reads;
- grant deletion revokes a healthy deduplicated cell;
- uniform errors for missing, wrong-type and unauthorized Secrets;
- NetworkPolicy E2E on a policy-capable CNI;
- mutation tests that remove each layer.

## References

- Kubernetes ValidatingAdmissionPolicy:
  https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/
- Kubernetes VAP API (`authorizer`, `oldObject`, `params`):
  https://kubernetes.io/docs/reference/kubernetes-api/admissionregistration-resources/validating-admission-policy-v1/
- Kubernetes security checklist:
  https://kubernetes.io/docs/concepts/security/application-security-checklist/
- ADR 0010.
- ADR 0011.
