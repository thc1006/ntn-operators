---
adr: 5
title: Place ntn-operators above provider-native per-cell control
status: accepted
date: 2026-06-06
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation:
  - "https://github.com/thc1006/ntn-operators/pull/177"
  - "https://github.com/thc1006/ntn-operators/pull/189"
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/162"
---

# ADR 0005 — Place `ntn-operators` above provider-native per-cell control

## Decision summary

The operator owns cluster/fleet intent and continuity. Providers own the
translation and execution of per-cell commands.

The shipped v0.6 scope is:

- runtime `ntn_config_update`;
- propagated ephemeris delivery;
- one `SatelliteEphemeris` fan-out to multiple `NTNCellConfig` objects.

Cell activation, slice-to-cell binding, ground-station-to-cell binding and
end-to-end session continuity remain separate unimplemented capabilities. This
ADR does not claim they have shipped.

## Context

OCUDU provides a native per-cell NTN configuration surface. Reimplementing
those primitives in the operator is not a durable differentiation. Kubernetes
adds value by declaring relationships among ephemeris sources, cells, ground
stations, slices and lifecycle policy, and by restoring desired state after
failure.

The current data model still lacks an explicit binding from an `NTNSlice` to
the cell or cell group that will carry it. Therefore a pool-level contact
opportunity is not yet proof that the actual serving cell is configured for
the overhead satellite.

## Decision drivers

- Complement, not fork, provider-native control.
- Keep provider-specific protocols out of public CRDs.
- Reconcile desired state after pod/process restart.
- Preserve honesty about the difference between opportunity, configuration and
  service delivery.
- Make fan-out bounded and observable.

## Decision

### Ownership boundary

`ntn-operators` owns:

- desired orbital source and freshness policy;
- fan-out to referencing cells;
- fleet relationships and policy;
- durable status and retry;
- cross-provider capability modeling.

A provider owns:

- wire protocol;
- field translation;
- command acknowledgement;
- provider-specific capability discovery.

### Current accepted scope

1. `SatelliteEphemeris` computes fresh states.
2. Referencing `NTNCellConfig` objects are enqueued through a field index/watch.
3. Each cell selects a deterministic NORAD member.
4. The provider pushes a runtime update and records delivery status.

### Deferred scope

The following require new decisions and implementation:

- `cell_lock`/`cell_unlock` activation lifecycle;
- slice-to-cell/cell-group binding;
- ground-station-to-cell binding;
- overhead-member selection and handover;
- multi-satellite path priority;
- UPF/session migration.

### No false service claim

The following states are distinct:

- `FailoverReady` (Reason `ConstellationMemberAvailable`, the pool contact opportunity);
- `CellConfigurationSelected`;
- `RuntimeConfigDelivered`;
- `ProviderCellActive`;
- `SliceServiceAvailable`.

No controller may infer the latter from the former without evidence for every
intermediate state.

## Invariants

- One source can enqueue N consumers without unbounded list scans.
- A stale source blocks only the affected member/cell.
- A cell never guesses a satellite when selection is ambiguous.
- Provider command failure does not invalidate the persisted desired state, but
  it does invalidate delivery/service conditions.
- Upstream claims are pinned to a commit or release tag.

## Alternatives

**Remain a per-cell YAML generator.** Rejected; provider upstream owns that
surface.

**Build a fully generic Crossplane-style provider framework immediately.**
Deferred until a second real provider proves the abstraction.

**Infer serving cell from pool opportunity.** Rejected; this is the exact
missing binding.

## Failure modes

- Fan-out storms: use indexes, predicates, concurrency limits and metrics.
- One bad cell blocks all cells: reconcile independently.
- Non-idempotent switch command repeats: use transition-specific markers.
- Provider schema drift: pin integration fixtures to provider commits.
- Opportunity exists but no cell is aligned: expose a failed/unknown binding
  condition, never service available.

## Test plan

- N-cell fan-out with bounded API calls.
- One failed cell does not block siblings.
- ambiguous satellite selection fails closed.
- provider restart causes safe replay.
- stale element set blocks affected delivery.
- no `SliceServiceAvailable=True` without explicit cell binding and provider
  evidence.
- scale test at representative fleet cardinality.

## References

- OCUDU:
  https://gitlab.com/ocudu/ocudu
- ADR 0002.
- ADR 0006.
- ADR 0008.
