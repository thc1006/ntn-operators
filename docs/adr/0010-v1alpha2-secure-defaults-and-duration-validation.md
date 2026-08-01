---
adr: 10
title: v1alpha2 secure-by-default transport and duration admission validation
status: proposed
date: 2026-07-31
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation: []
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/331"
  - "https://github.com/thc1006/ntn-operators/issues/214"
  - "https://github.com/thc1006/ntn-operators/issues/311"
  - "https://github.com/thc1006/ntn-operators/issues/299"
---

# ADR 0010 — v1alpha2 secure-by-default transport and duration admission validation

## Decision summary

Introduce `v1alpha2` with three intentional breaking improvements:

1. remote-control transport mode is required and plaintext is never implied by
   omission;
2. `SatelliteEphemeris.spec.source.refreshInterval` is validated at admission
   in the supported range because the CRD already represents it as
   `format: duration` and Kubernetes CEL supports duration conversion and
   comparison;
3. the deprecated `SatelliteEphemeris.spec.satellites.constellation` is dropped
   (§A.1) — tracked by #214, which this ADR previously listed without specifying.

v1alpha1 semantics remain available during a migration window. Conversion does
not silently reinterpret legacy objects.

Legacy values the secure profile will refuse — out-of-range durations and implicit
plaintext alike — are inventoried and explicitly migrated **before** v1alpha2 is
served (§C). That is what makes runtime enforcement safe to enable: after the gate
there is no such thing as a grandfathered object to distinguish.

## Context

### Remote control

In v1alpha1, an absent `tls` block means plaintext `ws://`. Omission therefore
chooses the least secure mode. Adding a default in place cannot distinguish
deliberate plaintext from a forgotten block.

### `refreshInterval`

The Go field is `metav1.Duration` and the generated schema already declares
`format: duration`. Kubernetes exposes a `format: duration` field to CEL as a
`duration`, so a field-scoped rule compares it against a duration literal.
Verified on the K8s 1.36.3 baseline: **both** `self >= duration('2h')` and the
wrapped `duration(self) >= duration('2h')` compile and enforce the bound. The
existing duration rules in `ntnslice_types.go` use the explicit `duration(self)`
form, so v1alpha2 matches that for one consistent style across the API.

The current comment that CEL cannot represent or compare this duration is no
longer a valid design premise for the chart’s supported Kubernetes baseline.
v1alpha1 accepts any syntactically valid duration and clamps it at runtime to
the effective range, emitting `RefreshIntervalClamped`. Changing that behavior
in place would break clients, so v1alpha2 is the correct boundary.

## Decision drivers

- Security-sensitive behavior must be explicit.
- API-server rejection is preferable to accepting a value and silently changing
  it later.
- Conversion must preserve user intent.
- Stored object migration is separate from conversion.
- v1alpha1 must not remain a bypass after v1alpha2 is introduced.
- Rollout must keep the conversion webhook available before the CRD calls it.

## Decision

## A. v1alpha2 remote-control model

Initial v1alpha2 supports a structured **address target only**. `Service`
references are deferred as an additive v1alpha2 field to avoid an unnecessarily
complex first down-conversion.

Illustrative shape:

```yaml
spec:
  provider:
    remoteControl:
      target:
        address:
          host: gnb-proxy.ran-system.svc
          port: 8443
      transport:
        mode: mtls
        credentialSecretRef:
          name: gnb-credential
        serverName: gnb-proxy.ran-system.svc
```

Rules:

- `transport.mode` is required; no default.
- enum: `tls`, `mtls`, `plaintext`.
- `tls` and `mtls` require the TLS/credential structure.
- `mtls` requires client certificate material at runtime.
- `plaintext` forbids TLS fields.
- controller runtime rejects plaintext unless an admin explicitly enables it;
  the secure Helm profile leaves it disabled.
- destination policy and credential authorization still apply.

A default of `tls` is rejected because it would silently convert legacy
plaintext into TLS behavior.

## A.1 Retiring `spec.satellites.constellation` (third change)

#214 was tracked by this ADR without appearing in it. It belongs here rather than
in a later version bump: the field lives on `SatelliteEphemeris`, the same CRD as
`refreshInterval`, so it rides the same conversion webhook and the same storage
migration. Dropping one inert field is not worth a second version.

It is specifiable precisely **because the field is inert**: `Constellation` is
declared in `api/v1alpha1` and read by no controller, provider or test. Nothing
selects on it; the documented replacements are `source.url` (`GROUP=`) and
`spec.satellites.noradIDs`. So retirement is a **data**-preservation problem, not
a behaviour-preservation one, and the field's own godoc already commits to the
contract:

> a v1alpha2 that drops it must ship conversion so v1alpha1<->v1alpha2 round-trips
> losslessly, plus stored-object migration and storedVersions cleanup

Decision:

- v1alpha2 does not declare `constellation`;
- v1alpha1 → v1alpha2 preserves the value in a round-trip annotation
  (`ntn.operators.dev/v1alpha1-constellation`) when non-empty, and sets nothing
  when empty — an object that never used it gains no annotation;
- v1alpha2 → v1alpha1 restores `constellation` from that annotation;
- the annotation is **not** authorization or behaviour input. Nothing reads it but
  the converter. A stale value can therefore only surface a stale label to an old
  client, never change what the operator does — which is what makes this the one
  place §D's round-trip-annotation allowance is safe to use;
- v1alpha1 keeps the field served and deprecated until v1alpha1 stops being served.

The alternative — dropping the data outright — is rejected: it is silent
destruction of user-authored spec on a version bump, and a down-conversion would
then not round-trip, which §D forbids.

## B. `refreshInterval` validation

v1alpha2 adds:

```go
// +kubebuilder:validation:Format=duration
// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('2h') && duration(self) <= duration('24h')",message="refreshInterval must be between 2h and 24h"
RefreshInterval metav1.Duration `json:"refreshInterval"`
```

The bounds **cannot literally be shared**: a kubebuilder marker is a source-code
string literal (`duration('2h')`) and the controller's are Go constants
(`minRefreshInterval = 2 * time.Hour`, `maxRefreshInterval = 24 * time.Hour`).
Nothing makes one derive from the other, so "shared constants" would be a claim no
reader could verify and no change could break loudly.

Requirement instead: **CI must fail when the two drift.** A test reads the
generated CRD, extracts the `refreshInterval` CEL rule, and compares it against the
expression built from the controller constants. Changing either side alone must
fail; the test is only meaningful if a mutation to `3h` on one side is verified to
break it.

For v1alpha2:

- out-of-range values are rejected at admission;
- the controller does not clamp;
- `RefreshIntervalClamped` is not emitted;
- malformed durations are rejected by schema/CEL compilation/evaluation.

v1alpha1 keeps existing clamp behavior during deprecation.

## C. Migration precondition for legacy values the secure profile will refuse

Two classes of legacy data become invalid or refused in v1alpha2: out-of-range
`refreshInterval`, and implicit plaintext remote control. Both get the same gate,
for the same reason — a version bump must not change behaviour silently.

### C.1 Why plaintext needs a gate rather than a provenance marker

Conversion maps v1alpha1 `tls == nil` to explicit `mode: plaintext` (§D). A user
creating a fresh v1alpha2 object with `mode: plaintext` produces a **byte-identical
object**. There is no field, and no honest way to add one, that lets the controller
tell a grandfathered object from a new one at runtime.

That forces a choice the ADR previously left open, and both branches are bad:

- enforce, and every converted legacy object stops working the moment the secure
  flag is set;
- grandfather, and new v1alpha2 plaintext passes through the same hole.

A provenance annotation stamped by the migration tool was considered and
**rejected**: it is writable by anyone who can patch the object, so it would be
authorization data living in a mutable field — the same weakness §E's ratchet
exists to avoid — and it would need a freshness guard, a signer, and a retirement
plan of its own.

The gate removes the need to distinguish them. After it, **every** plaintext
object is one an operator explicitly re-affirmed, so "grandfathered" is no longer
a category that has to survive in the data.

### C.2 The gate

Before serving v1alpha2:

1. add a v1alpha1 admission ratchet preventing new or widened out-of-range values,
   and new or broadened implicit-plaintext remote control, while grandfathering
   unchanged legacy values;
2. run a preflight report listing, per object: legacy out-of-range
   `refreshInterval` with the effective clamped value the controller currently
   uses, and every `NTNCellConfig` whose `remoteControl` has no `tls` block;
3. require an explicit migration patch — an in-range duration, and for remote
   control either a TLS mode or an explicit re-declaration of plaintext;
4. verify no remaining out-of-range and no remaining implicit-plaintext v1alpha1
   objects;
5. only then enable v1alpha2 serving; the runtime plaintext flag may be enabled
   only after this step, never before.

This keeps conversion lossless and makes the behavior change reviewable in GitOps
history — a plaintext object that survives the gate is one someone signed off on
in a commit, which is the audit trail a marker field would only have imitated.

## D. Conversion

The conversion webhook maps:

- v1alpha1 `tls == nil` → v1alpha2 `mode: plaintext`;
- v1alpha1 TLS block → `tls` or `mtls` according to existing fields;
- v1alpha2 address target → v1alpha1 `host:port`;
- v1alpha1 `constellation` (non-empty) → v1alpha2 annotation
  `ntn.operators.dev/v1alpha1-constellation`, and back (§A.1).

It preserves metadata, finalizers, owner references and status. It never stores
Secret content in annotations.

`constellation` is the one field using a round-trip annotation, and §A.1 states why
it is safe there: nothing but the converter reads it, so a stale value cannot change
operator behaviour. Round-trip annotations are otherwise permitted only for
information with no v1alpha1 representation and must include freshness/intent guards so an old client edit
cannot be overridden by stale annotation data. The initial address-only model
is chosen to minimize this need.

## E. Admission policies

- v1alpha2 gets its own credential VAP.
- the transport policy is **required** in the secure profile, not optional. The
  earlier "may use a ConfigMap parameter" contradicted this ADR's own failure-mode
  entry ("admin flag and VAP both fail closed in secure profile") and its test plan
  ("parameter missing = deny"), both of which presuppose the policy exists. An
  enforcement layer that the profile's guarantee depends on is not optional.
  Concretely, the secure profile ships and requires:
  - a `ValidatingAdmissionPolicy` **and** its `ValidatingAdmissionPolicyBinding` —
    a policy without a binding is inert, and `parameterNotFoundAction` does not
    cover a policy or binding that was never installed;
  - `paramKind` a ConfigMap, referenced by a fixed `paramRef.name` in the operator
    namespace, with `parameterNotFoundAction: Deny`;
  - a documented ConfigMap schema (the permitted transport modes, and whether
    plaintext targets are allowed at all);
  - the same Helm value that enables the runtime flag installs the policy, so the
    two cannot be configured apart;
  - on startup, the manager verifies the policy and binding exist. If the secure
    profile is on and either is missing, readiness is **false** — the manager does
    not fall back to running unenforced.
- admission and runtime are an **intersection**, not a precedence: admission decides
  what may be written, the runtime flag decides what may be dialled, and a push
  proceeds only if both allow it. Neither overrides the other.
- v1alpha1 gets a ratchet that prevents new plaintext or broader plaintext
  targets while permitting required migration edits.
- policy match tests enumerate every served version.

## F. Storage migration

Storage version, `status.storedVersions`, conversion traffic and rollback state are
**per CRD**. A single global sequence would let one CRD's storage flip while another
still has unmigrated objects, and `storedVersions` cleanup is only safe per object
kind. The two CRDs this version touches carry different changes and are gated
separately:

| CRD | v1alpha2 change | Preflight gate | Storage switch | Stored-object migration | Rollback |
|---|---|---|---|---|---|
| `NTNCellConfig` | transport target + required `transport.mode` | implicit-plaintext inventory (§C.2) cleared | after its own soak | rewrite required | tested before the switch |
| `SatelliteEphemeris` | `refreshInterval` bounds; `constellation` dropped (§A.1) | out-of-range inventory (§C.2) cleared | after its own soak | rewrite required — carries the constellation annotation | tested before the switch |
| every other CRD in the group | none | none | may stay v1alpha1-only | none | n/a |

A CRD with no v1alpha2 schema change does **not** need a v1alpha2 at all; sharing a
Go package version is not a reason to bump its served versions.

Order, applied per CRD:

1. add v1alpha2 Go types and pure conversion tests;
2. deploy HA conversion webhook, Service and certificates while CRD remains
   v1alpha1-only;
3. complete legacy refreshInterval preflight/migration;
4. serve v1alpha2 with `storage: false`;
5. soak create/get/list/watch/update and rollback;
6. update controller/admission behavior;
7. set v1alpha2 storage;
8. migrate stored objects;
9. verify and clean `status.storedVersions`;
10. deprecate and later stop serving v1alpha1.

Every replica serves conversion; webhook service is not leader-gated.

Steps 1, 2 and 10 are group-wide (types, webhook infrastructure, deprecation
announcement); steps 3 through 9 are per CRD and may complete at different times.

## Invariants

- Missing transport mode is rejected.
- No plaintext object survives the §C gate without an explicit operator edit, so
  the controller never has to tell a grandfathered object from a new one.
- Retiring `constellation` preserves the value across a v1alpha1 round trip, and
  nothing but the converter reads what it preserves.
- Generated CRD bounds and controller constants are compared by CI, not asserted
  to be shared.
- In the secure profile the transport policy and its binding must exist, or the
  manager is not ready.
- Conversion never upgrades plaintext to TLS implicitly.
- v1alpha2 never runtime-clamps refreshInterval.
- v1alpha1 out-of-range data is explicitly migrated before v1alpha2 serving.
- all served versions are covered by credential policy.
- exactly one storage version is true.
- old `storedVersions` is not removed until migration is verified.
- rollback is tested before changing storage.

## Alternatives

**Change v1alpha1 in place.** Rejected; breaks stored objects and clients.

**Default mode to TLS.** Rejected; hidden behavior and unsafe conversion.

**Clamp during conversion.** Rejected; conversion would mutate user intent and
make round trips lossy.

**Keep runtime clamp forever.** Rejected for v1alpha2; admission can express the
constraint and gives immediate feedback.

**Add `ServiceReference` in the first v1alpha2 PR.** Deferred to reduce
conversion and policy complexity.

## Failure modes

- webhook unavailable: conversion requests fail; deploy webhook first and use
  HA/PDB/readiness.
- stale v1alpha1 policy bypass: enumerate versions in tests.
- out-of-range legacy object blocks serving: preflight is a hard gate.
- storage switched too early: phased rollout and rollback test.
- TLS mode selected but trust material invalid: runtime condition false, no
  plaintext fallback.
- plaintext accidentally enabled: admin flag and VAP both fail closed in secure
  profile.

## Test plan

### Schema/CEL

- missing/unknown mode;
- TLS/mTLS/plaintext structural matrix;
- malformed address and ports;
- malformed duration;
- `1h59m59s`, `2h`, `24h`, `24h1s`;
- generated CRD includes `format: duration` and the validation rule.

### Bounds drift

- read the generated CRD, extract the `refreshInterval` CEL rule, and compare it
  with the expression derived from `minRefreshInterval`/`maxRefreshInterval`;
- mutating either side alone to `3h` must fail the test — an assertion that cannot
  fail is not a guard.

### Conversion

- plaintext/TLS/mTLS round trips;
- list and multi-object `ConversionReview`;
- metadata/status preservation;
- old client edits are not overridden;
- webhook restart and certificate rotation;
- no Secret content in conversion artifacts.

### Legacy gate

- preflight lists both classes (§C.2) and is a hard gate on serving v1alpha2;
- an object with implicit plaintext blocks the gate until edited;
- after the gate, enabling the runtime flag changes no object's outcome — every
  surviving plaintext object was re-affirmed, so there is nothing to grandfather.

### Constellation retirement

- non-empty `constellation` survives v1alpha1 -> v1alpha2 -> v1alpha1 unchanged;
- an object that never set it gains no annotation;
- deleting the annotation loses only a label no controller reads.

### Admission

- v1alpha1 and v1alpha2 both covered;
- requester authorization;
- parameter missing = deny in secure profile;
- legacy plaintext grandfathered but cannot widen;
- new plaintext denied unless explicitly allowed.

### Migration

- preflight detects every out-of-range object;
- explicit normalization;
- storage migration and `storedVersions` cleanup;
- rollback before and after storage switch.

### Secure-profile policy presence

- policy installed but binding missing: manager readiness false, not silent
  unenforced operation;
- param ConfigMap missing with `parameterNotFoundAction: Deny`: writes denied;
- admission-allows / runtime-denies and the reverse both refuse the push
  (intersection, not precedence).

### E2E

- real wss/mTLS proxy path;
- no TLS-to-plaintext fallback;
- policy-capable CNI egress denial;
- declared minimum and current Kubernetes versions.

## References

- Kubernetes CRD versioning and conversion:
  https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/
- Kubernetes storage-version migration:
  https://kubernetes.io/docs/tasks/manage-kubernetes-objects/storage-version-migration/
- Kubernetes CEL:
  https://kubernetes.io/docs/reference/using-api/cel/
- Kubernetes CRD validation:
  https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/
- ValidatingAdmissionPolicy:
  https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/
- Current field:
  https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/satelliteephemeris_types.go
- Existing duration CEL examples in the repository:
  https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/ntnslice_types.go
