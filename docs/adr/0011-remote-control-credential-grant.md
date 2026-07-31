---
adr: 11
title: Owner-issued grant for remote-control credentials
status: proposed
date: 2026-07-31
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: ["ADR 0009 preference for admission authorization as the complete end state"]
superseded_by: []
implementation: []
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/251"
  - "https://github.com/thc1006/ntn-operators/issues/298"
  - "https://github.com/thc1006/ntn-operators/issues/329"
---

# ADR 0011 — Owner-issued grant for remote-control credentials

## Decision summary

Add a namespaced `RemoteControlCredentialGrant` owned beside the credential.
When strong enforcement is enabled, runtime use requires a grant matching the
credential, consumer identity, destination and transport mode.

This complements admission authorization; it provides revocation and binding
that a point-in-time requester check cannot provide.

## Context

Admission can prove that the actor submitting a CR may read a Secret at that
moment. It cannot:

- re-evaluate a stored CR after RBAC is withdrawn;
- let the credential owner bind use to one gNB;
- trigger reconcile when consent is withdrawn.

A Secret informer would require broad list/watch access and still would not
model owner intent. A separate consent object is watchable without increasing
Secret permissions.

## Decision drivers

- Secret-owner controlled consent.
- Immediate reconcile on deletion/narrowing.
- Bind to immutable consumer identity where desired.
- Bind to exact destination and mode.
- Default-off compatibility followed by an intentional secure profile.
- No credential material in the grant.

## Decision

Illustrative API:

```yaml
apiVersion: ntn.operators.dev/v1alpha1
kind: RemoteControlCredentialGrant
metadata:
  name: gnb-a-credential
  namespace: ran-team
spec:
  secretRef:
    name: gnb-a-tls
  allowedConsumers:
    - apiGroup: ntn.operators.dev
      kind: NTNCellConfig
      name: cell-a
      uid: 6f1c... # optional but recommended
  allowedTargets:
    - host: gnb-a.ran-team.svc
      port: 8443
  allowedModes: [mtls]
  credentialRevision: "2"
```

### Scope and ownership

- Grant and Secret are in the same namespace.
- Cross-namespace reference is not introduced by this ADR.
- Name is required; UID prevents delete/recreate name reuse when set.
- target matching uses canonical host and port after syntax normalization but
  before DNS resolution.
- the admin destination allow-list remains an outer bound.

### Enforcement

Manager flag:

```text
--require-credential-grant=true
```

When true, a credentialed push is refused unless at least one grant permits the
full tuple.

Do not implement “enforce only when a grant exists for this Secret”; an attacker
could omit the grant.

Refusal occurs before Secret read and uses a uniform low-information condition.

### Watch/index design

Index grants by Secret name and consumers. Grant create/update/delete enqueues
affected `NTNCellConfig` objects.

Deletion tombstones and update old/new consumer sets must both enqueue, so a
narrowing update cannot leave a removed consumer healthy.

The watch does not require Secret list/watch.

### Credential rotation

Changing Secret content alone does not change the grant. The optional
`credentialRevision` is an explicit owner-controlled trigger and may be folded
into the runtime authorization marker.

This does not claim instant Secret-content rotation unless the deployment
updates the grant revision or a future narrowly-authorized Secret watch exists.

## Invariants

- No grant contains token, key, certificate or CA bytes.
- Deleting/narrowing a grant enqueues previously authorized consumers.
- grant authorization and admin destination allow-list are intersected.
- UID mismatch denies when UID is specified.
- disabling grant enforcement is an explicit admin choice and observable.
- grant denial occurs before Secret read/dial.

## Alternatives

**Admission only.** Retained as baseline, insufficient for ongoing consent.

**Secret annotations.** Rejected; weak schema, patch-only concern and no
independent lifecycle/status.

**Gateway API `ReferenceGrant` directly.** Rejected because it cannot express
endpoint and mode and targets cross-namespace references.

**Secret informer.** Rejected for broad RBAC and missing intent model.

## Failure modes

- grant cache not synced: readiness must not become true before informer sync.
- delete event misses consumer: test tombstone and old-object paths.
- DNS rebinding: grant is not an IP firewall; admin policy and NetworkPolicy
  remain required.
- UID omitted: document name-reuse risk.
- Secret rotated but revision unchanged: report the accepted detection bound.

## Observability

- `CredentialGrantAuthorized` condition;
- denial counters by reason;
- grant revision in debug-level structured logs;
- no Secret values or raw certificate data;
- startup metric indicating whether enforcement is active.

## Test plan

- exact tuple allow/deny;
- UID mismatch and delete/recreate;
- grant deletion/narrowing immediately reconciles healthy deduplicated cell;
- target and mode mismatch;
- admin allow-list intersection;
- Secret never read on denial;
- cache-sync/readiness behavior;
- rotation with and without revision bump;
- envtest with restricted tenant and credential owner identities;
- wss/mTLS E2E.

## References

- ADR 0009.
- ADR 0010.
- Gateway API ReferenceGrant:
  https://gateway-api.sigs.k8s.io/api-types/referencegrant/
- Kubernetes authorization:
  https://kubernetes.io/docs/reference/access-authn-authz/authorization/
