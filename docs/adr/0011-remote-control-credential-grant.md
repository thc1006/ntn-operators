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

- surface a durable, owner-controlled signal to stop using a credential after
  the fact (it does not re-run the original author's SubjectAccessReview, and
  this ADR does not add author-identity or author-RBAC tracking);
- let the credential owner bind use to one gNB;
- trigger reconcile when consent is withdrawn.

The grant therefore provides Secret-owner-driven consent and revocation, not
re-evaluation of the CR author's RBAC. A Secret informer would require broad
list/watch access and still would not model owner intent. A separate consent
object is watchable without increasing Secret permissions.

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
- `allowedConsumers[].name` is required; its optional `uid` prevents
  delete/recreate name reuse when set.
- `secretRef` is name-bound. A Secret deleted and recreated under the same name
  inherits the grant unless the optional `secretRef.uid` is set (mirroring the
  consumer UID), so name reuse is a decision rather than an accident.
- `allowedModes` uses ADR-0010's transport mode enum (`tls`, `mtls`,
  `plaintext`). The grant is a v1alpha1 resource and authorizes both v1alpha1
  and converted-v1alpha2 pushes; it is independent of the CR's transport
  encoding version.
- target matching uses one shared canonicalizer for host and port, applied
  identically in admission, controller matching and tests: case-folded host,
  trailing dot stripped, explicit port required, IPv6 in canonical form. Matching
  is after syntax normalization but before DNS resolution.
- the admin destination allow-list remains an outer bound.

### Authorizing the grant writer

"Owner-issued" is not a Kubernetes primitive: the API server knows a principal's
verbs, not a Secret's owner. Any principal able to create or patch a grant in
the namespace could otherwise self-issue one, so the secure profile requires,
alongside `--require-credential-grant`, an admission control on the grant writer:

- a ValidatingAdmissionPolicy requiring the principal creating or patching a
  grant to hold `get` on the referenced Secret, and/or
- a dedicated RBAC role for grant write that is not implied by `NTNCellConfig`
  edit access.

This is distinct from the default-off `credentialRefPolicy` (#309), which
authorizes the `NTNCellConfig` writer against the Secret, not the grant writer.

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

### Reconcile ordering

Grant authorization must not reuse the ephemeris push dedup. The push path
early-returns on `isEphemerisPushUpToDate` before the credential is resolved
(#298), so an enqueue from a grant event alone would hit that dedup and skip the
grant check until the next epoch marker (~3m). Two consequences follow.

- Grant authorization is evaluated on a separate authorization marker (consumer,
  target, mode and, when set, `credentialRevision`) checked independently of the
  push marker. A grant delete, narrow or revision bump invalidates that
  authorization marker and re-runs the check without waiting for a new epoch.
- The credential digest is not folded into the push marker. #298 records why:
  the push marker is load-bearing for dedup, gNB-restart recovery and
  crash-idempotency (#230), and a benign CA-bundle rotation would otherwise force
  a spurious ephemeris re-push.

No credential is presented while the push dedup holds (#298), so the enforced
guarantee is precise: a withdrawn grant refuses the next permitted push and
flips `CredentialGrantAuthorized` to false rather than interrupting a push
already in flight.

## Invariants

- No grant contains token, key, certificate or CA bytes.
- Deleting/narrowing a grant enqueues previously authorized consumers.
- grant authorization and admin destination allow-list are intersected.
- UID mismatch denies when UID is specified.
- disabling grant enforcement is an explicit admin choice and observable.
- grant denial occurs before Secret read/dial.
- authorization uses a marker independent of the ephemeris push dedup.
- a single grant must match the full tuple; permissions are never assembled
  across grants.
- `allowedConsumers`, `allowedTargets` and `allowedModes` are bounded lists
  (MinItems 1, capped MaxItems, set semantics); an empty list is deny-all and
  wildcards are not supported.

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
- consumer UID mismatch and delete/recreate;
- Secret delete/recreate under the same name, with and without `secretRef.uid`;
- grant deletion/narrowing flips `CredentialGrantAuthorized` on a healthy,
  push-deduplicated cell via the separate authorization marker, not the push
  marker;
- grant-writer authorization: a principal without `get` on the Secret cannot
  create the grant;
- target and mode mismatch;
- admin allow-list intersection;
- empty allow-list is deny-all, not wildcard;
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
- ValidatingAdmissionPolicy:
  https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/
