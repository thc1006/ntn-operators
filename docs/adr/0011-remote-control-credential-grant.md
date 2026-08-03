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
- `apiGroup` and `kind` are constrained by CEL to exactly
  `ntn.operators.dev` / `NTNCellConfig`, the only consumer that exists. They are
  kept as fields so a second consumer is an additive change, but left open they
  would advertise a generic ReferenceGrant-style lookup the controller does not
  implement — and an operator would find that out from a grant that silently
  authorizes nothing.
- `secretRef` is name-bound. A Secret deleted and recreated under the same name
  inherits the grant unless the optional `secretRef.uid` is set (mirroring the
  consumer UID), so name reuse is a decision rather than an accident.
- `allowedModes` is restricted to `tls` and `mtls` — **not** ADR-0010's full
  transport enum. ADR-0010 §A says `plaintext` forbids TLS and credential fields,
  so a grant naming `plaintext` would authorize the use of a credential that mode
  cannot carry: an entry with no meaning, which a reader would reasonably take to
  mean the Secret owner had consented to something. If consent to a plaintext
  *destination* is ever wanted, that is transport policy (ADR-0010 §E), not a
  credential grant, and it belongs on a resource whose subject is the destination
  rather than the Secret. The grant is a v1alpha1 resource and authorizes both
  v1alpha1 and converted-v1alpha2 pushes; it is independent of the CR's transport
  encoding version.
- target matching **requires canonical input** rather than sharing a canonicalizer.
  A single shared implementation is not achievable: admission is CEL, which can
  neither call the Go canonicalizer nor mutate the value, so "applied identically
  in admission and controller" was a promise the implementation could not keep, and
  a divergence between the two would be a matching bug in the authorization path.
  CEL *can* reject non-canonical input, which is the half that is implementable:
  - host lower-case, no trailing dot;
  - port explicit and numeric;
  - IPv6 in RFC 5952 form, bracketed;
  - two entries canonicalizing to the same target are rejected as duplicates.

  The controller normalizes defensively before comparing — objects predating the
  rule exist — but the schema is the authority, so a value that reaches the
  controller has already been shown canonical. Matching is after this syntax
  normalization and before DNS resolution.
- the admin destination allow-list remains an outer bound.

### Authorizing the grant writer

"Owner-issued" is not a Kubernetes primitive: the API server knows a principal's
verbs, not a Secret's owner. Any principal able to create or patch a grant in
the namespace could otherwise self-issue one, so the secure profile requires,
alongside `--require-credential-grant`, an admission control on the grant writer:

- **required**: a ValidatingAdmissionPolicy using the CEL `authorizer` library to
  require the principal creating or patching a grant to hold `get` on
  `spec.secretRef.name` in that namespace;
- **additionally**: a dedicated RBAC role for grant write that is not implied by
  `NTNCellConfig` edit access.

These are not alternatives, and the earlier "and/or" made them read as such. RBAC
answers *may this principal write grants in this namespace* — it cannot answer
*may this principal read the specific Secret this grant points at*, which is the
entire content of the word "owner-issued". With RBAC alone, anyone who may write
any grant may issue one for a Secret they cannot read, and the resource's central
claim is false.

Verification cannot rest on the policy's own status: Kubernetes VAP **type
checking does not apply to matched custom resources or to a CRD `paramKind`**, so
an absent type warning proves nothing about whether the field paths in the
expression are right. A renamed field would silently match nothing and fail open.
The test plan therefore requires live API-server dry-run against a restricted
ServiceAccount — positive and negative — plus a field-rename mutation that must
break it, and an install check that the policy *and* its binding exist.

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

Refusal occurs before the Secret's **credential data** is read, and uses a uniform
low-information condition.

The earlier wording — "before Secret read" — could not be implemented alongside
`secretRef.uid`: verifying a UID means asking the API server about that Secret. The
two are reconciled by splitting the read:

1. fetch **metadata only** (`PartialObjectMetadata`), which returns `metadata` and
   omits `data`, and compare `uid` when the grant sets it;
2. evaluate the full tuple — consumer, target, mode, revision — against the grants;
3. only if all of it passes, perform the full `Get` for the credential bytes.

So no credential material is read for a push the grant denies, which is the
property that matters; and the invariant now states that rather than an absolute
that the UID check contradicts.

### Condition semantics and failing closed

`CredentialGrantAuthorized=True` while `--require-credential-grant=false` would
read as "this push was authorized by a grant" when nothing checked one. The
condition therefore reports:

| State | Condition |
|---|---|
| flag off | `Unknown` / `EnforcementDisabled` — observable without claiming authorization |
| flag on, tuple permitted | `True` / `GrantAuthorized` |
| flag on, no matching grant | `False`, uniform low-information reason (§ Enforcement) |

When the flag is on but the check **cannot** run, the manager fails closed rather
than degrading to unenforced: readiness is false (or startup fails) if the grant
CRD is absent, its informer cache has not synced, the manager lacks `list`/`watch`
on grants, or the required grant-writer policy and binding are not installed. An
operator who asked for enforcement and silently got none is worse off than one
whose rollout stops — they believe a boundary exists.

`secretRef.uid` and `allowedConsumers[].uid` stay **optional** in the schema, but
the secure profile documents that a grant without them is **name-bound**: a Secret
or consumer deleted and recreated under the same name inherits it. That is a
legitimate compatibility mode, not a default to leave ambiguous, so the preflight
report lists name-bound grants and the profile's guidance is to set both.

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

- Grant authorization is evaluated on a separate authorization marker checked
  independently of the push marker.

  Its derivation must be exact, because the obvious one is wrong: consumer, target,
  mode and revision are properties of the **CR**, and none of them changes when a
  grant is deleted or narrowed. A marker built from those alone would compare equal
  after a revocation, so the reconcile the watch correctly enqueued would skip the
  check it was enqueued for — the failure this section exists to prevent, moved one
  level down.

  ```text
  authzDigest = digest(
      canonical consumer tuple,
      canonical destination,
      mode,
      credentialRevision (when set),
      sorted set of every grant CURRENTLY matching the tuple, each as
          (grant UID, grant generation, spec digest)
  )
  ```

  The grant set is what makes deletion observable: removing one changes the set,
  therefore the digest. Two rules follow.

  - A reconcile triggered by a grant event **re-enumerates matching grants and
    recomputes the digest before comparing**. It never treats the persisted digest
    as current — that value is a cache of the last authorized state, not evidence
    about now.
  - Including grant `generation` and spec digest (not just UID) is what makes a
    *narrowing* visible: the set membership is unchanged, the spec is not.

  This derivation is what simultaneously gives: two matching grants, deleting one
  still permits; deleting the only match denies immediately; editing an unrelated
  grant does not re-push; narrowing consumer, target or mode denies immediately;
  and a revision bump re-reads the credential.
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
- UID mismatch denies when UID is specified, and the comparison uses a
  metadata-only read.
- disabling grant enforcement is an explicit admin choice and observable.
- grant denial occurs before the Secret's credential data is read, and before any
  dial.
- authorization uses a marker independent of the ephemeris push dedup, derived
  from the currently matching grant set and recomputed before it is trusted.
- a grant naming a mode that cannot carry a credential is not representable.
- enforcement that cannot run fails closed rather than reporting authorization.
- a single grant must match the full tuple; permissions are never assembled
  across grants.
- `allowedConsumers`, `allowedTargets` and `allowedModes` are bounded lists with
  `MinItems: 1`, so an empty list is **admission-invalid** — not deny-all. The two
  cannot both be true, and rejecting at admission is the better half: a grant that
  authorizes nothing is a mistake worth catching at write time, and "empty means
  deny" is a rule a reader has to know rather than one the API enforces. Wildcards
  are not supported.
- List topology is stated per field rather than as a blanket "set semantics", which
  Kubernetes defines for **scalar** lists and which does not give object lists the
  uniqueness it implies:

  | Field | `listType` | Key / uniqueness | `MaxItems` |
  |---|---|---|---|
  | `allowedModes` | `set` | scalar, native | 2 — the enum has two members |
  | `allowedTargets` | `map`, `listMapKeys: [host, port]` | both required, so the key is total | 16 |
  | `allowedConsumers` | `atomic` + CEL uniqueness on `name` | `uid` is optional, so it cannot be part of a map key without making two entries for one name — one with a uid, one without — legal and ambiguous | 32 |

  `allowedConsumers` stays `atomic` rather than requiring `uid` so a GitOps author
  can write the grant before the consumer exists; the CEL rule supplies the
  uniqueness the map key would have.

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
- UID comparison uses a metadata-only read: with a denying grant, no full Secret
  GET is issued (assert on the API-server audit or a counting client, not on the
  absence of a condition);
- authzDigest changes when the only matching grant is deleted, and when a matching
  grant is narrowed with its consumer/target/mode left otherwise identical — the
  two cases a CR-derived marker cannot see;
- a reconcile enqueued by a grant event recomputes the digest before comparing:
  mutate it to trust the persisted value and the deletion case must fail;
- grant-writer VAP against a live API server with a restricted ServiceAccount,
  positive and negative, plus a field-rename mutation that must break it — VAP type
  checking does not cover CRDs, so the policy's own status cannot stand in;
- policy installed without its binding: writes are admitted, so the install check
  must fail;
- `allowedModes: [plaintext]` is rejected by the schema;
- non-canonical targets (upper-case host, trailing dot, missing port, unbracketed
  IPv6) are rejected at admission, and two entries canonicalizing to one target are
  rejected as duplicates;
- empty `allowedConsumers`/`allowedTargets`/`allowedModes` is admission-invalid,
  not silently deny-all;
- two `allowedConsumers` entries with the same name — one with `uid`, one without —
  are rejected by the CEL uniqueness rule;
- enforcement enabled with the CRD absent, cache unsynced, RBAC missing, or the
  grant-writer policy absent: readiness false in each case, never unenforced;
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
