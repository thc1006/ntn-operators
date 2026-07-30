# ADR 0007 — Durable last-good OMM cache for restart / leader-failover outage continuity

- Status: **Accepted** (implemented on `feat/omm-persist-restart-continuity`)
- Date: 2026-07-30
- Deciders: @thc1006
- Builds on: ADR 0006 (decouple propagation from pass prediction — the propagation heartbeat this cache feeds), ADR 0005 (cluster-scope orchestration — the SatelliteEphemeris → NTNCellConfig runtime push that consumes the propagated states)

## Context

`SatelliteEphemerisReconciler` fetches GP/OMM element sets from CelesTrak/Space-Track, parses them to `sgp4.OMM`, and re-propagates to fresh ECEF on the ~3-minute propagation heartbeat so `status.propagatedStates` (and the downstream `ntn_config_update` runtime push) always carry an epoch in the near future. To stay polite to the upstream it fetches at most every `minRefreshInterval` (2 h) and, on a fetch failure, **serves the last-good OMMs from an in-memory cache** (`ommCache sync.Map`) so a source outage does not stall SIB19 — this is the I-18 continuity behaviour.

The gap: **that cache is in-memory only.** A process restart or a leader-election failover starts the new active reconciler with an empty cache. `status.propagatedStates` holds the ECEF *output* of a past propagation but **not** the orbital *elements* (the SGP4 input), so status alone cannot re-propagate to a new epoch. Concretely: during a sustained upstream outage, if the current leader dies, the standby (which never reconciled, so has an empty cache) takes over with nothing to propagate. Once the last-pushed epoch expires (~`propagationEpochLead`, 5 min), SIB19 goes stale for the rest of the outage even though the operator is healthy — a single-process-memory dependency in an otherwise HA design.

## Decision Drivers

- **D1. HA correctness.** Active-passive HA (ADR/#230) promises continuity across a leader loss. An in-memory-only cache silently breaks that promise precisely when it matters (leader loss *during* an outage).
- **D2. Do not weaken the freshness contract.** Restart continuity must not become a way to resurrect arbitrarily stale data; the existing window / backoff / `maxEpochAge` gates must still apply.
- **D3. Minimal blast radius.** The live fetch/propagate path must not regress; persistence is an enhancement, best-effort, and off the hot path.
- **D4. No new RBAC surface or cluster-wide caching.** Match the codebase's existing "uncached one-off read" discipline (the Space-Track Secret is read via `APIReader` to avoid a cluster-wide Secret informer).

## Options considered

- **A — Status subresource (store elements in `status`).** Rejected: bloats status with raw OMM fields for every tracked NORAD, couples a large data blob to the frequently-written status object, and status is the wrong home for controller-private recovery state.
- **B — Per-CR owner-ref'd ConfigMap (CHOSEN).** One `<name>-omm-cache` ConfigMap per SatelliteEphemeris holding the tracked OMMs as JSON, owner-ref'd for garbage collection.
- **C — Secret instead of ConfigMap.** Rejected as the default (see visibility rationale); kept as a documented follow-up if an operator classifies element sets as sensitive.
- **D — Dedicated CRD for the cache.** Rejected: a CRD is desired-state API; this is internal recovery state, not user-facing spec.
- **E — External store (etcd lease / object store).** Rejected: new infra dependency for a problem the API server already solves.

## Decision

Adopt **Option B**. On every successful fetch the reconciler writes the tracked, capped OMM set to a per-CR ConfigMap; on a cold reconcile (empty in-memory cache) it hydrates the cache from that ConfigMap before deciding fetch-vs-serve.

### Design

- **Minimal payload.** Persist only `FilterOMMs(result.OMMs, spec.satellites.noradIDs)` capped at `maxPropagatedStates` (128) — the same set `propagateStates` would use, never the full upstream response. `sgp4.OMM`'s CelesTrak JSON tags let it round-trip losslessly through `sgp4.ParseOMMs` (its `EPOCH` is a string, so no time-precision loss).
- **Identity + integrity metadata** (annotations): source identity (`fetchInputKey` = source type+URL), original fetch time (RFC3339Nano), payload sha256 digest, and owner UID. Restore refuses unless the digest is intact **and** the fetchKey and UID match the live object — so a hand-edit, a source change, or a delete-recreate never restores wrong or orphaned data.
- **Conditional-GET validators** (annotations, CelesTrak only): the origin's `ETag` and `Last-Modified`. On cold-start restore they are re-seeded into the fetcher (`SeedConditionalCache`) so the first fetch this process makes is a conditional GET (`If-None-Match` / `If-Modified-Since`) — a `304 Not Modified` instead of a full re-download, which is politer to CelesTrak's usage policy after a restart/failover. Only the **validators** are re-seeded, never the body: the fetcher's OMM cache is keyed by URL and shared across every CR fetching that URL, whereas a durable entry holds only that CR's filtered subset. A resulting cold-start `304` thus carries no body, and `obtainOMMs` re-serves the restoring CR's own cache — so continuity holds with `NotModified` semantics rather than collapsing to zero states. The validators are kept in lockstep with the body (cleared when the origin stops sending them), so a restore never seeds a stale validator.
- **Freshness preserved.** The restored entry keeps its ORIGINAL fetch time, so the normal window / backoff / `maxEpochAge` gates apply exactly as for a warm cache. Restore removes the cold-start cliff and nothing more; it cannot resurrect data the freshness gates would reject.
- **GC via owner reference.** The ConfigMap is `SetControllerReference`'d to its SatelliteEphemeris, so k8s garbage-collects it on CR deletion. The controller therefore needs `configmaps` `get;create;update` — **no delete** verb.
- **No resourceVersion churn.** Persist no-ops when the payload digest is unchanged, so the ~2 h fetch cadence does not rewrite the ConfigMap every cycle (consistent with the #204-G3 no-op-write discipline).
- **Uncached reads.** Both the persist Get-before-write and the restore Get go through `APIReader` (falling back to the cached client only when unwired, e.g. tests), so no cluster-wide ConfigMap informer is started and `configmaps list;watch` is not required. On a cold leader this also reads the ConfigMap the previous leader wrote even before the informer cache would have synced.
- **Size bound.** Payloads over `maxOMMCacheBytes` (900 KiB, under the 1 MiB ConfigMap limit) skip persistence (logged); the live warm cache is unaffected. Unreachable at the 128-state cap today, kept as a defensive guard.

### Visibility: ConfigMap, not Secret

Orbital element sets for tracked satellites are **public** data (they come from public CelesTrak/Space-Track catalogs), so a ConfigMap exposes nothing sensitive — and `NTNCellConfig` already persists derived ephemeris to a ConfigMap, so this adds no new exposure class. Space-Track *credentials* remain in their Secret and are never written here (only the resulting elements are). If a specific deployment classifies its element sets as sensitive (e.g. a private/supplemental catalog), migrating the cache to a Secret is a mechanical follow-up: same keys, same validation, `Secret` in place of `ConfigMap`.

## Consequences

**Positive**
- Restart / leader-failover continuity through a sustained upstream outage: a cold process re-propagates from the last-good elements instead of falling off the "nothing to propagate" cliff.
- No new infra, no new cluster-wide caching, minimal RBAC (`configmaps get;create;update`).
- Best-effort and off the hot path: a persist/restore failure degrades to today's behaviour, never fails a reconcile.

**Negative / limitations**
- One extra ConfigMap per SatelliteEphemeris (small; GC'd with the CR).
- **Name truncation edge:** CR names are bounded to the 253-char k8s limit by truncation. Two CR names longer than 243 chars sharing a 243-char prefix in the same namespace would map to the same cache ConfigMap; the restore UID gate prevents wrong-data restore, so the only effect is that the losing CR forgoes restart-continuity. Acceptable given k8s names are ≤253 and such collisions are pathological.
- Persistence lags the in-memory cache by one successful fetch (the cache is written after a fetch, not on every propagation) — restart continuity is bounded by `maxEpochAge` from the last *fetch*, which is the intended freshness bound anyway.

## Testing

- **Unit** (`satelliteephemeris_ommcache_persist_test.go`): persist/restore round-trip incl. nanosecond fetch-time and digest integrity; restore refuses digest-corrupt / missing-label / empty / UID-mismatch / fetchKey-mismatch inputs; no-op on identical digest; owner-ref for GC; oversize skip; name truncation; payload filter+cap; and a cold-start integration test that reconciles a fresh reconciler (empty cache) against a simulated outage and asserts the epoch keeps advancing — it fails if the restore hook is removed.
- **E2E acceptance** (`TestHAOutageContinuityAcrossFailover`, tag `e2e_ha`): warm from the in-cluster mock, scale the mock to zero (total outage), verify the epoch advances, kill the current leader, and verify the cold new leader keeps advancing the epoch beyond `propagationEpochLead` and stays in the future (runtime-push ready). This is the acceptance gate for "outage continuity solved".

## References

- `internal/controller/satelliteephemeris_ommcache_persist.go` (implementation)
- `internal/controller/satelliteephemeris_controller.go` — `acquireOMMs` (restore hook), `obtainOMMs` (persist hook + fetch-failure fallback)
- ADR 0006 (propagation heartbeat), #230 (active-passive HA)
