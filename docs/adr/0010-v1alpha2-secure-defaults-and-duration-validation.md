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
  - "https://github.com/thc1006/ntn-operators/issues/214"
  - "https://github.com/thc1006/ntn-operators/issues/315"
  - "https://github.com/thc1006/ntn-operators/issues/311"
  - "https://github.com/thc1006/ntn-operators/issues/299"
---

# ADR 0010 — v1alpha2 secure-by-default transport and duration admission validation

## Decision summary

Introduce `v1alpha2` with two intentional breaking improvements:

1. remote-control transport mode is required and plaintext is never implied by
   omission;
2. `SatelliteEphemeris.spec.source.refreshInterval` is validated at admission
   in the supported range because the CRD already represents it as
   `format: duration` and Kubernetes CEL supports duration conversion and
   comparison.

v1alpha1 semantics remain available during a migration window. Conversion does
not silently reinterpret legacy objects.

## Context

### Remote control

In v1alpha1, an absent `tls` block means plaintext `ws://`. Omission therefore
chooses the least secure mode. Adding a default in place cannot distinguish
deliberate plaintext from a forgotten block.

### `refreshInterval`

The Go field is `metav1.Duration` and the generated schema already declares
`format: duration`. Kubernetes maps an OpenAPI string with `format: duration` to the CEL
`duration` type. A field-scoped rule therefore compares `self` directly with a
duration literal; it must not parse `self` a second time.

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

## B. `refreshInterval` validation

v1alpha2 adds:

```go
// +kubebuilder:validation:Format=duration
// +kubebuilder:validation:XValidation:rule="self >= duration('2h') && self <= duration('24h')",message="refreshInterval must be between 2h and 24h"
RefreshInterval metav1.Duration `json:"refreshInterval"`
```

The exact bounds remain constants shared with controller tests.

For v1alpha2:

- out-of-range values are rejected at admission;
- the controller does not clamp;
- `RefreshIntervalClamped` is not emitted;
- malformed durations are rejected by schema/CEL compilation/evaluation.

v1alpha1 keeps existing clamp behavior during deprecation.

## C. Migration precondition for out-of-range legacy objects

Conversion must not secretly clamp a v1alpha1 object. Before serving v1alpha2:

1. add a v1alpha1 admission ratchet preventing new or widened out-of-range
   values while grandfathering unchanged legacy values;
2. run a preflight report listing legacy out-of-range objects and the effective
   clamped value currently used by the controller;
3. require an explicit migration patch to the effective in-range value;
4. verify no remaining out-of-range v1alpha1 objects;
5. only then enable v1alpha2 serving.

This keeps conversion lossless and makes the behavior change reviewable in
GitOps history.

## D. Conversion

The conversion webhook maps:

- v1alpha1 `tls == nil` → v1alpha2 `mode: plaintext`;
- v1alpha1 TLS block → `tls` or `mtls` according to existing fields;
- v1alpha2 address target → v1alpha1 `host:port`.

It preserves metadata, finalizers, owner references and status. It never stores
Secret content in annotations.

Round-trip annotations are permitted only for information with no v1alpha1
representation and must include freshness/intent guards so an old client edit
cannot be overridden by stale annotation data. The initial address-only model
is chosen to minimize this need.

## E. Admission policies

- v1alpha2 gets its own credential VAP.
- a transport policy may use a ConfigMap parameter with
  `parameterNotFoundAction: Deny` in secure mode.
- v1alpha1 gets a ratchet that prevents new plaintext or broader plaintext
  targets while permitting required migration edits.
- policy match tests enumerate every served version.

## F. Storage migration

Order:

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

## Invariants

- Missing transport mode is rejected.
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

### Conversion

- plaintext/TLS/mTLS round trips;
- list and multi-object `ConversionReview`;
- metadata/status preservation;
- old client edits are not overridden;
- webhook restart and certificate rotation;
- no Secret content in conversion artifacts.

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
