# ADR 0006 — Decouple the propagation heartbeat from pass prediction

- Status: **Proposed** (design only; do NOT implement before @thc1006 sign-off — the SGP4→reconciler area was unfrozen 2026-07-19 for design work only)
- Date: 2026-07-19
- Deciders: @thc1006
- Related issues: [#234](https://github.com/thc1006/ntn-operators/issues/234) (this decision), [#232](https://github.com/thc1006/ntn-operators/issues/232) (EPIC), [#233](https://github.com/thc1006/ntn-operators/issues/233) (horizon cap + ctx, CLOSED — the mitigation this builds on), [#235](https://github.com/thc1006/ntn-operators/issues/235) (epoch sampled at the propagation instant), [#179](https://github.com/thc1006/ntn-operators/issues/179)/[#188](https://github.com/thc1006/ntn-operators/issues/188) (propagation vs fetch cadence, event churn)
- Builds on: ADR 0005 (cluster-scope orchestration — the SatelliteEphemeris→NTNCellConfig fan-out that delivers the epoch), the WO-20 event/status episode-gate discipline.

## Context

`SatelliteEphemerisReconciler.Reconcile` (`internal/controller/satelliteephemeris_controller.go`) runs, on every reconcile of a tracked ephemeris:

1. acquire OMMs (fetch, or serve from cache),
2. `handlePassPrediction(ctx, eph, result, now)` — the pass-window sweep (`:352`),
3. `propagateStates(ctx, eph, result, r.now()+propagationEpochLead)` — the fresh SGP4 ECEF + epoch for the runtime push (`:374`),
4. a **single** `Status().Update(ctx, eph)` (`:387`) that flushes both the pass windows and the fresh `propagatedStates`,
5. `RequeueAfter: min(effectiveInterval, propagationRefreshInterval)` (`:448`, `propagationRefreshInterval = 3m`).

The runtime-push epoch the consumer (`NTNCellConfig`) delivers to the gNB comes from `propagatedStates`. It carries `propagationEpochLead = 5m` of validity ahead of the propagation instant (`:461`), and the consumer's stale guard refuses an epoch at or before `now + epochSkewMargin` (`10s`). So the epoch heartbeat stays alive only if a fresh `propagatedStates` is published, and reaches the consumer, roughly every `propagationEpochLead` at worst.

Two properties of the current shape couple that heartbeat to the pass-prediction sweep:

- **The sweep is inline, before the epoch write.** `propagateStates` and the `Status().Update` run only after `handlePassPrediction` returns. `handlePassPrediction` and `propagateStates` share no data (both read `result.OMMs`; pass prediction sets `NextPassWindows` / the `PassesPredicted` condition in memory, propagation sets `PropagatedStates` in memory; the caller flushes both). So the ordering is incidental, not required, but it means every reconcile pays `T_pass` before the epoch is republished.
- **`T_pass` is bounded only by the horizon, not by a short deadline.** #233 clamps `passPrediction.horizon` to `maxPassHorizon = 7d` and threads `ctx` into `ephemeris.PredictPasses` so the sweep cancels on shutdown / leader-loss. It does **not** put a short per-reconcile wall-clock timeout on the sweep (unlike `groundstationlifecycle` which wraps its work in a 5s `context.WithTimeout`). The cost is `O(horizon × satellites × ground stations)`; for a large fleet over a multi-day horizon it can run into seconds or more.

The consequence, and the substance of #234:

- **Cadence stretch (the stated problem).** The interval between successive epoch republications is `propagationRefreshInterval + reconcile_duration`, and `reconcile_duration` includes `T_pass` on every cycle. Continuity holds only while `propagationRefreshInterval + T_fetch + T_pass + T_propagate < propagationEpochLead − epochSkewMargin`, i.e. roughly `T_pass + (fetch+propagate) < ~2m` — and tighter still, because the consumer-side reconcile plus WebSocket push also has to complete before the old epoch expires, and that latency comes out of the same budget. For a large fleet where the sweep approaches that budget, the delivered epoch can expire before the next refresh and the consumer refuses the push for the rest of the window. `propagationEpochLead = 5m` and the 500-window cap absorb this for typical fleets, so it is **not a current outage**, but it is a scaling cliff.
- **Latency.** Even below that cliff, the fresh epoch is published `T_pass` later than it could be.
- **Structural fragility.** On a `ctx` cancel from the sweep the reconcile returns at `:357` **before** `propagateStates`, so that cycle publishes no fresh epoch. This is benign under its intended trigger (shutdown / leader-loss, where not refreshing is correct), but it makes the epoch heartbeat structurally dependent on pass prediction completing, and the `:355` comment already anticipates a future per-reconcile timeout as a trigger — under which the early return would starve the heartbeat.

Pass prediction does not need to run at the propagation cadence. `NextPassWindows` holds absolute AOS/LOS times used for contact planning, which tolerate a far coarser refresh than the 3-minute epoch heartbeat, yet the sweep — the `O(horizon × satellites × ground stations)` cost, `T_pass` — is paid on all ~20 reconciles/hour. (For a single satellite/ground-station stream the produced windows change only when a pass rolls off, ~every 90 min; across a large fleet the aggregate roll-off rate is much higher. The point is not that the output is always identical — at scale it usually differs — but that the *expensive sweep* runs 20×/hour to serve planning data that would be fine at a fraction of that.)

## Decision drivers

- **D1. The runtime-push epoch is the load-bearing, latency-sensitive output**; pass windows are planning data with minute-to-hour horizons. They should not share a cadence, and the fast one should never wait on the slow one.
- **D2. No status-write or event-churn regression.** WO-20 gates events on a persisted transition and avoids no-op writes; the fix must preserve that (issue acceptance).
- **D3. Minimise blast radius.** SatelliteEphemeris is a shipped, public controller (v0.6/v0.7). Prefer a change that keeps one reconcile loop and one owner of status over a second controller writing the same object.
- **D4. #233 already caps and cancels the sweep**, so the remaining gap is structural (where the sweep sits relative to the heartbeat), not "the sweep is unbounded".

## Options

### Option A — Reorder + split the write (minimal)

Propagate first, publish the epoch, then run pass prediction as a best-effort tail:

```
propagate → Status().Update             # epoch: fires every heartbeat cycle (the epoch advances by
                                        #   design; this IS the heartbeat — never gate it away)
pass sweep → Status().Update IF changed # pass windows: only when they actually changed
```

- Fixes latency (epoch written before the sweep) and the `ctx`-cancel starvation (epoch already persisted before the sweep can fail). The epoch write is unchanged from today (one heartbeat write per cycle — `propagationEpochLead` forces the epoch to advance, so it is never a no-op and must not be guarded away). The *second* write is new and guarded: `NextPassWindows` holds absolute `AOS`/`LOS` (`metav1.Time`).
- **Does not remove `T_pass` from the cadence.** The sweep still runs every cycle, so `reconcile_duration` still carries `T_pass` and the cadence-stretch cliff (`T_pass + fetch + propagate < ~2m`) remains. It hardens the heartbeat against the sweep's *failure*, not its *cost*.
- **The guard does not stop churn at scale.** For a single stream the windows are stable between roll-offs (~90 min), so the guarded pass write is rare and total churn stays ≈ today's one-write-per-cycle. But across a large fleet the aggregate window set turns over on roughly the propagation cadence (many streams each rolling a window off), so write B fires most cycles and Option A roughly *doubles* the writes — in exactly the large-fleet regime #234 targets. So Option A regresses D2 at scale; it is a small-deployment fix.

### Option B — Move pass prediction to a lower cadence (recommended)

Keep one reconcile loop, but run the sweep only when due:

```
propagate → Status().Update                      # epoch heartbeat: every propagationRefreshInterval (3m); no sweep
if pass configured AND now >= nextPassDue:
    pass sweep → Status().Update IF changed       # every passPredictionInterval; off the heartbeat path
    record lastPassPrediction = now
RequeueAfter min(effectiveInterval, propagationRefreshInterval, timeUntilNextPass)  # each term > 0
```

- The heartbeat cycle is pure fast-propagate: `reconcile_duration` no longer carries `T_pass`, so the inter-publish cadence is `~propagationRefreshInterval` regardless of fleet size. The cadence-stretch cliff is removed, not just narrowed.
- Includes Option A's epoch-first ordering. On a sweep-due cycle there are two writes: the epoch first (always), then the pass write if the windows changed. The controller-runtime client writes the server's new `resourceVersion` back into the object after the first `Status().Update`, so the second uses the fresh RV; the two writes are only ~1-in-5 cycles (a sweep-due cycle), not every cycle. If an external edit lands in the sub-millisecond gap the pass write 409-conflicts — the controller aborts and the work-queue retries with backoff (never an in-reconcile `RetryOnConflict` loop), and because the epoch went first a pass-write conflict never loses the heartbeat.
- Pays the expensive sweep ~4×/hour (every `passPredictionInterval`) instead of ~20×/hour: a ~5× cut in sweep compute that holds at any fleet size (it is about how often `T_pass` is paid, not whether the output changed), and pass writes drop to ≤ one per `passPredictionInterval`, so unlike Option A the churn does not regress at scale.
- Cost: one persisted timestamp (`status.lastPassPredictionTime`) so "due" survives leader failover (an in-memory map would cost one extra sweep after failover and need its own cleanup on delete — the status field is cleaner), a dual-cadence requeue, and `NextPassWindows` up to `passPredictionInterval` stale (acceptable: passes are minutes-to-hours out and used for planning, not the push).

### Option C — Do nothing, rely on #233

`propagationEpochLead = 5m` + the 500-window cap + #233's horizon cap keep typical fleets safe today. Rejected: leaves the documented scaling cliff and the structural fragility in place; the issue asks for the structural fix and #234 is the tracked owner.

### Also considered and rejected

- **Run the sweep in a goroutine so the reconcile returns immediately.** Rejected. It breaks the controller-runtime model: a background goroutine writing `Status` concurrently with reconciles races on `resourceVersion`, escapes the work-queue's rate limiting and leader-election guarantees, and can run after leadership is lost. The idiomatic shape is the opposite — do one step and return, and let `RequeueAfter` schedule the next step (controller-runtime / Kubebuilder guidance). Option B is that shape.
- **A second controller reconciling the same SatelliteEphemeris for passes only.** Rejected under D3: two controllers writing one object's `Status` means two owners and `resourceVersion` conflicts, for no benefit over a single loop that runs the sweep on its own cadence.

## Decision

**Option B**, with Option A's epoch-first ordering and guarded split writes as its inner mechanics. Rationale: it is the only option that removes `T_pass` from the epoch cadence (the actual stated failure at large fleet), it satisfies the no-churn constraint (D2) via the naturally lower pass-write frequency rather than a guard that stops helping at scale, and it keeps a single reconcile loop and a single status owner (D3). Option A is retained only as a **staging step for small deployments**: where the fleet is small enough that both `T_pass` stays well under the continuity budget *and* the pass-window set is stable between roll-offs (so write B stays rare), the reorder + guarded split alone fixes the latency and the `ctx`-cancel starvation. At the large-fleet scale #234 actually targets Option A helps neither the cadence nor the churn, so implement Option B directly there.

`passPredictionInterval` should be a small, explicit constant (candidate: 15m — well under the ~90 min pass recurrence so a window is recomputed before it rolls off, and far above the 3m heartbeat) with room to make it spec-configurable later if operators want tighter planning granularity. The exact value is an implementation-time choice, pinned by a test, not fixed here.

## Consequences

Positive:
- The runtime-push epoch cadence is independent of fleet size and pass-prediction cost; the scaling cliff in #234 is closed.
- The epoch heartbeat no longer skips a refresh when the sweep is slow or cancelled.
- Fewer redundant sweeps and fewer pass-status writes.

Negative / watch-items:
- A new persisted status field (`lastPassPredictionTime`) and a dual-cadence requeue add reconcile-flow complexity; the requeue math must be covered by a test so a regression cannot silently collapse the two cadences back into one.
- `NextPassWindows` is refreshed less often; must confirm no consumer treats it as real-time (GroundStationLifecycle binding is the consumer to check).
- Two status writes per sweep-due cycle instead of one; must confirm the WO-20 event gates still fire exactly once per real transition and that a second write does not re-emit propagation events.
- The pass write also enqueues the SatelliteEphemeris→NTNCellConfig fan-out watchers (ADR 0005). They no-op — the push is dedup'd by `runtimeEphemerisPushMarker` when the epoch is unchanged — but it is a wasted wake ~per sweep-due cycle. If it matters, narrow the fan-out's watch predicate to `propagatedStates`/epoch changes; otherwise accept it as bounded.
- The `timeUntilNextPass` term in the requeue **must stay strictly positive**: a zero or negative `RequeueAfter` is silently ignored by controller-runtime and would disable the heartbeat (the #237 continuity lesson). After a due sweep, `lastPassPrediction` resets to `now` so the term is `passPredictionInterval > 0`; when pass prediction is not configured the term is simply omitted from the `min()`, never passed as `0`.
- `status.lastPassPredictionTime` is an additive, controller-owned status field: backward-compatible (no user-facing schema change) but it needs a CRD regen (`make manifests`) and a deepcopy regen.

## Test plan

- `TestReconcile_SlowPassPredictionDoesNotDelayPropagatedStates` (from #234): a slow/blocked sweep must not delay or skip the `propagatedStates` write.
- `TestReconcile_PassPredictionCancelledStillWritesEpoch`: a `ctx`-cancelled sweep must still have published the fresh epoch (the reorder).
- `TestReconcile_PassPredictionRunsOnLowerCadence`: over N heartbeat intervals the sweep runs ~`N·propagationRefreshInterval/passPredictionInterval` times, not every cycle; epoch is written every cycle.
- Churn guard: no second status write and no pass event on a cycle where the pass windows did not change (extends the existing WO-20 episode-gate tests).

## Open questions

1. `lastPassPredictionTime` as a status field vs an in-memory per-object timestamp (survives failover vs one extra post-failover sweep). Leaning status field for determinism.
2. Should `passPredictionInterval` be spec-configurable now, or a constant until an operator asks? Leaning constant, pinned by a test.
3. Is any current consumer of `NextPassWindows` latency-sensitive enough to object to a 15m refresh? (GroundStationLifecycle binding review.)
