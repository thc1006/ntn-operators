---
adr: 7
title: Durable last-good OMM cache for restart and leader failover
status: accepted
date: 2026-07-30
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation:
  - "internal/controller/satelliteephemeris_ommcache_persist.go"
tracking: []
---

# ADR 0007 — Durable last-good OMM cache for restart and leader failover

## Decision summary

Persist the tracked last-good OMM subset in a per-`SatelliteEphemeris`,
owner-referenced Kubernetes object so a cold leader can continue propagation
during an upstream outage.

Use a **hash-suffixed collision-resistant name**, not prefix truncation alone.
Default storage is ConfigMap for public catalogs; operators must be able to
choose Secret or Disabled for private/licensed element sets.

## Context

The in-memory cache preserves continuity during a fetch outage only while the
same process remains active. Status contains propagated ECEF output, not the
orbital elements needed to propagate a later epoch.

The current implementation uses a ConfigMap and validates digest, fetch
identity and owner UID. Its name truncation can map two long resource names to
the same ConfigMap. The UID check prevents wrong restore, but one resource can
lose persistence. That is an availability bug, not merely cosmetic.

Element sets are often public, but “OMM” does not mean “public”. Space-Track
access and private catalogs can carry contractual or operational restrictions.

## Decision drivers

- Cold failover continuity.
- Existing freshness rules remain authoritative.
- No cluster-wide ConfigMap/Secret informer.
- Collision-resistant ownership.
- Configurable data classification.
- Best-effort persistence must be observable.

## Decision

### Name

Use:

```text
<readable-prefix>-<hash>-omm-cache
```

The hash is at least 128 bits derived from namespace, name and UID. The result
must fit the DNS/subdomain limit.

A migration path checks the old truncated name once, validates UID/fetch key,
then adopts or rewrites to the new name.

### Payload

Persist only the selected and capped OMM set used by propagation. Store:

- serialized OMMs;
- SHA-256 digest;
- source/fetch identity;
- owner UID;
- original fetch time;
- count;
- ETag and Last-Modified where applicable;
- schema version.

### Storage mode

Operator configuration:

```yaml
ommCache:
  mode: ConfigMap # ConfigMap | Secret | Disabled
```

- `ConfigMap`: suitable only when the operator classifies element sets as
  non-sensitive.
- `Secret`: same validation and lifecycle, encrypted-at-rest subject to cluster
  configuration.
- `Disabled`: no durable outage continuity.

Credentials are never copied.

### Restore

Restore only when:

- owner UID matches;
- fetch identity matches;
- digest matches;
- schema version is understood;
- payload parses and validates;
- freshness rules allow its use.

The original fetch time is preserved. Restore cannot make old data fresh.

### API access

Use an uncached reader for one-off gets. Writes use normal client methods.
Required verbs are minimal and mode-dependent. Owner reference provides GC.

### Observability

Metrics and conditions/logs cover:

- persist success/failure;
- restore success/refusal reason;
- payload age;
- digest mismatch;
- oversize skip;
- legacy-name migration;
- cache mode.

A persist failure does not fail live reconciliation, but repeated failure must
be visible.

## Invariants

- Cache restore never bypasses source-epoch freshness.
- Two resources cannot intentionally share a cache object.
- A delete/recreate cannot adopt the old object without UID validation.
- Conditional validators are coupled to the same payload.
- No credential data is persisted.
- Best-effort failure is observable.

## Alternatives

**Status payload.** Rejected due to churn and public API pollution.

**Prefix truncation only.** Rejected due to deterministic collisions.

**Always ConfigMap.** Rejected because catalog visibility is deployment
specific.

**Dedicated cache CRD.** Rejected; recovery state is not user desired state.

## Test plan

- persist/restore round trip;
- digest/fetchKey/UID/schema mismatch;
- two 253-character names sharing a prefix produce different objects;
- legacy-name migration;
- Secret/ConfigMap/Disabled modes;
- oversize behavior;
- original timestamp/freshness preservation;
- 304 conditional-fetch restore path;
- HA outage failover E2E;
- metrics for every refusal/failure category.

## References

- Kubernetes object size/ConfigMap behavior:
  https://kubernetes.io/docs/concepts/configuration/configmap/
- Kubernetes Secrets:
  https://kubernetes.io/docs/concepts/configuration/secret/
- Space-Track:
  https://www.space-track.org/
- CelesTrak:
  https://celestrak.org/
