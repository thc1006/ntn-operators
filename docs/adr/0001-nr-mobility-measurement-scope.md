---
adr: 1
title: Separate NR SIB11 idle/inactive measurements from connected-mode MeasConfig
status: accepted
date: 2026-04-19
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: ["previous revision of ADR 0001"]
superseded_by: []
implementation: []
tracking: ["https://github.com/thc1006/ntn-operators/issues/47"]
---

# ADR 0001 — Separate NR SIB11 idle/inactive measurements from connected-mode `MeasConfig`

## Decision summary

The project will not use “SIB11 measurement configuration” as an umbrella term
for NR mobility. Two different 3GPP mechanisms are tracked separately:

1. **SIB11 / `MeasIdleConfigSIB`** for measurements performed in
   `RRC_IDLE` and `RRC_INACTIVE`.
2. **Dedicated `MeasConfig`** for connected-mode measurement objects, report
   configurations and measurement identities.

SIB2/SIB3/SIB4 cell-selection and reselection settings are a third, distinct
surface. They may be exposed only when the chosen provider demonstrably maps
them to over-the-air behavior.

## Context

The earlier issue and ADR grouped `MeasObjectNR`, `ReportConfigNR`, `MeasId`,
`QuantityConfig` and events A1–A6/B1–B2 under SIB11. That is a standards error.

TS 38.331 defines SIB11 around `MeasIdleConfigSIB`. Connected-mode
`MeasObjectNR`, `ReportConfigNR` and `MeasId` belong to `MeasConfig`, normally
delivered through dedicated RRC signaling. SIB2/SIB3/SIB4 instead govern
selection and reselection behavior.

At the repository baseline reviewed on 2026-07-31, `ntn-operators` has no API
or provider translation that honestly implements either SIB11
`MeasIdleConfigSIB` or general connected-mode `MeasConfig`. A future upstream
provider capability must be verified at an exact commit before API fields are
added.

## Decision drivers

- Standards-correct terminology.
- No CRD field that silently has no wire-level effect.
- Provider-neutral modeling without pretending all providers expose the same
  control surface.
- Additive evolution where possible.
- Separate idle/inactive measurement, connected mobility and reselection
  because they have different lifecycle and validation rules.

## Decision

### 1. Split the work into three capability tracks

**Track A — Idle/inactive measurement**

Working capability name: `IdleMeasurementPolicy`.

Scope includes SIB11 `MeasIdleConfigSIB`, supported carrier lists, validity and
reporting behavior used by UEs in `RRC_IDLE`/`RRC_INACTIVE`.

**Track B — Connected-mode measurement**

Working capability name: `ConnectedMeasurementPolicy`.

Scope includes `MeasObjectNR`, `ReportConfigNR`, `MeasId`, quantity
configuration, measurement gaps and events A1–A6/B1–B2.

**Track C — Cell selection/reselection**

Working capability name: `NeighborReselectionPolicy`.

Scope includes the provider-supported subset of SIB2/SIB3/SIB4/SIB5. It must
not be described as connected-mode handover.

### 2. Do not predeclare unsupported fields

No `NTNCellConfig` field is added merely because it exists in 3GPP. A field is
eligible only after:

- the provider has a stable input or runtime interface;
- translation is deterministic;
- an integration test proves the setting reaches the provider;
- an over-the-air or provider-level assertion proves it is consumed.

### 3. Use capability discovery

Provider implementations SHOULD publish capabilities such as:

- `IdleMeasurementSIB11`;
- `ConnectedMeasConfig`;
- `NRCellReselection`;
- `EUTRACellReselection`.

Unsupported requested behavior MUST fail admission where statically knowable,
or set a dedicated `False` condition with reason `ProviderUnsupported`. Parent
`ConfigApplied=True` must not mask an unsupported nested feature.

### 4. Correct issue taxonomy

Issue #47 should be rewritten or superseded. It must not claim that
`MeasObjectNR` and `ReportConfigNR` are “carried in SIB11”.

## Invariants

- SIB11 is never used as a synonym for `MeasConfig`.
- Reselection support is never represented as connected-mode handover support.
- A configured field cannot result in a successful applied condition unless a
  provider consumed it.
- Provider support claims are pinned to a commit/tag and a test.

## Alternatives

**One large `neighborMobility` block.** Rejected because it hides three
different RRC mechanisms and makes capability reporting ambiguous.

**Speculative preview fields.** Rejected because a preview label does not stop
users from assuming operational effect.

**One new CRD per ASN.1 structure.** Deferred. The API should model operator
intent, not mirror every ASN.1 type mechanically.

## Failure modes

- Provider upgrades remove a key: detect through schema-drift/integration tests.
- A condition stays true after capability loss: recompute capability conditions
  on every relevant generation/provider change.
- Users confuse idle measurements with handover: API docs and conditions must
  name the RRC state and mechanism.

## Test plan

- Standards terminology lint for prohibited phrases such as
  “SIB11 MeasObjectNR”.
- Provider capability matrix tests.
- Negative test: unsupported connected measurement cannot yield
  `ConfigApplied=True` without a companion failure.
- Translation tests for every exposed SIB2/SIB3/SIB4 field.
- Provider integration test against a pinned upstream image.
- Upgrade test where a capability disappears or changes name.

## References

- 3GPP/ETSI TS 38.331, NR RRC:
  https://www.etsi.org/deliver/etsi_ts/138300_138399/138331/
- 3GPP specifications portal:
  https://www.3gpp.org/dynareport/38331.htm
- Repository issue:
  https://github.com/thc1006/ntn-operators/issues/47
