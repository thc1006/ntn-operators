---
adr: 8
title: Model satellite-path contact opportunity at constellation-pool level
status: accepted
date: 2026-07-30
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation: []
tracking: []
---

# ADR 0008 — Model satellite-path contact opportunity at constellation-pool level

## Decision summary

For an `NTNSlice` that references a multi-member `SatelliteEphemeris`, contact
opportunity is true when at least one eligible member has a current,
deliverable pass window.

This condition is named and documented as a **contact opportunity**, not as
actual slice service availability. Actual availability requires a future
slice-to-cell binding and evidence that the relevant cell is aligned,
configured, active and carrying service.

## Context

LEO service naturally hands over among constellation members. Pinning a slice
path to one NORAD would make the path unavailable whenever that member sets.

The current `SatellitePathSpec` references an ephemeris pool and does not name
a cell or selected member. Therefore it can answer “is a usable member
overhead?” but cannot answer “is my serving cell using that member?”.

## Decision drivers

- Constellation semantics rather than single-satellite semantics.
- Per-member freshness.
- No false equivalence between geometry and delivered service.
- Deterministic candidate reporting for later binding.
- Safe behavior while the binding layer is absent.

## Decision

### Opportunity condition

Use `ContactOpportunityAvailable`.

It is true only when a candidate satisfies all of:

- active AOS/LOS window for a referenced ground station;
- source element epoch within the freshness bound;
- propagated-state input hash matches current source/selector;
- pass prediction is current for its input hash;
- candidate was not dropped by truncation;
- any declared policy filters match.

### Candidate status

Record a deterministic candidate:

- NORAD ID;
- satellite display name;
- ground-station reference;
- AOS/LOS;
- source epoch;
- propagated epoch;
- validity deadline;
- selection reason.

Selection is deterministic, for example earliest LOS after policy priority.
Ties use stable identifiers.

### Service condition

`SliceServiceAvailable` must remain `Unknown` or absent until the API has an
explicit cell/cell-group binding and a provider supplies service evidence.

Production failover policy may use contact opportunity as one input, but it
must not claim that a data path is already active.

### Idle and stale behavior

- no current window: false, `NoActiveContact`;
- predictions unavailable: unknown, `PredictionUnavailable`;
- all candidates stale: false, `AllCandidatesStale`;
- truncated/ambiguous policy inputs: unknown or false according to whether a
  safe candidate can still be proven.

## Invariants

- One fresh member can keep pool opportunity true.
- A stale sibling does not block a fresh candidate.
- No candidate means false/unknown, never guessed.
- Opportunity does not set service availability.
- Candidate output is stable for identical inputs.

## Alternatives

**Pin slice to one NORAD.** Rejected for constellation handover.

**Infer cell selection from `NTNCellConfig` globally.** Rejected until an
explicit binding exists.

**Keep the generic name `SatelliteAvailable`.** Rejected because it overstates
what the controller knows.

## Failure modes

- stale windows with true condition: gate on hash and timestamp.
- selected member not used by a cell: service remains unknown and binding
  condition fails.
- status flapping among equal candidates: deterministic tie-break.
- source truncation hides best candidate: expose truncation and conservative
  condition behavior.

## Test plan

- one fresh member and one unavailable member;
- all stale;
- input hash mismatch;
- prediction missing;
- deterministic ties;
- truncated set;
- opportunity true never sets service true;
- future binding test verifies selected cell/member match.

## References

- ADR 0005.
- ADR 0006.
- Current API:
  https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/ntnslice_types.go
