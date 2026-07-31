---
adr: 6
title: Decouple propagation heartbeat from pass prediction
status: accepted
date: 2026-07-19
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation:
  - "https://github.com/thc1006/ntn-operators/pull/256"
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/234"
---

# ADR 0006 — Decouple propagation heartbeat from pass prediction

## Decision summary

Publish fresh propagated states on the fast heartbeat without waiting for pass
prediction. Run pass prediction on a lower cadence and immediately when its
inputs change. Persist separate cadence and input-hash status.

## Context

Propagation produces the near-future epoch consumed by runtime push. Pass
prediction produces planning windows and has cost proportional to horizon,
satellite count and ground-station count. Running the sweep before publishing
the epoch coupled a safety-critical heartbeat to a potentially expensive
calculation.

The implemented design writes propagated state first, then performs a due pass
sweep and writes pass status separately.

## Decision drivers

- The runtime epoch must not be delayed by planning work.
- Pass windows must refresh on input change, not just elapsed time.
- Leader failover must preserve the due decision.
- Status conflicts must not roll back a successfully published epoch.
- Events must correspond to persisted transitions.

## Decision

### Fast path

Every propagation heartbeat:

1. obtain a usable last-good or fresh OMM set;
2. propagate to the target epoch;
3. persist propagated status;
4. allow downstream fan-out.

The fast path does not call pass prediction.

### Pass path

A pass sweep runs when:

- no prior successful sweep exists;
- `passPredictionInterval` elapsed;
- the stored pass-input hash differs;
- the stored completion time is in the future;
- previous pass status was invalidated.

The input hash covers:

- source identity;
- tracked satellite selector;
- pass prediction configuration;
- referenced ground-station UID and generation.

Completion time is sampled after the sweep.

### Invalidation

A single helper clears:

- windows;
- completion timestamp;
- input hash;
- `PassesPredicted`.

Every path that lacks trustworthy OMM input or resolved ground-station input
must call it.

### Conflict behavior

If the second status write conflicts, return the error and let the work queue
retry. Do not use an in-reconcile conflict loop. The epoch is already safe.

## Invariants

- No pass-prediction error or cancellation can prevent the current epoch write.
- A changed input triggers a sweep immediately.
- A future timestamp does not freeze the sweep.
- `PassesPredicted=True` implies current inputs and persisted windows agree.
- No negative or zero `RequeueAfter` is used as a timer.
- Event emission occurs only after the corresponding condition is persisted,
  except separately documented legacy exceptions.

## Alternatives

**Reorder only.** Rejected because pass cost still stretches every heartbeat.

**Second controller writing the same status.** Rejected because ownership and
conflict complexity exceed the benefit.

**Rely only on horizon cap.** Rejected because the structural coupling remains.

## Observability

Expose:

- propagation duration and failures;
- pass sweep duration and due reason;
- skipped/cancelled sweeps;
- status conflicts;
- age of last successful pass prediction;
- count of windows and truncated windows.

## Test plan

- slow sweep does not delay propagated status;
- cancelled sweep leaves fresh epoch persisted;
- lower cadence is respected;
- every input class invalidates;
- future timestamp self-heals;
- no-OMM paths clear pass status;
- second-write conflict preserves first write;
- race detector over controller and package tests;
- invariant test enumerating error exits that may leave
  `PassesPredicted=True`.

## References

- Kubernetes conditions:
  https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- controller-runtime:
  https://github.com/kubernetes-sigs/controller-runtime
