# ADR 0008 — NTNSlice satellite-path availability is constellation-pool-level, not NORAD-pinned

- Status: **Accepted**
- Date: 2026-07-30
- Deciders: @thc1006
- Builds on: ADR 0005 (cluster-scope orchestration; fleet failover deferred to v0.7), ADR 0006 (pass-prediction validity contract). Relates to the #272 selection-determinism and #275 NTNSlice-freshness fixes.

## Context

`NTNSlice.spec.satellitePath` references a `SatelliteEphemeris` by `ephemerisRef` **only** — it carries no NORAD ID, no `NTNCellConfig` reference, and no ground-station selector. `NTNCellConfig.spec.ephemerisNoradID` separately selects which satellite's propagated state a *cell* broadcasts (SIB19) and pushes at runtime. The two CRs both reference the ephemeris but are not linked to each other.

A review asked, correctly, whether pass availability should be tied to the *cell's selected* NORAD: e.g. member A is overhead now, a cell selects member B, B has no pass or no deliverable state — should `NTNSlice.checkSatelliteAvailability` still report the satellite path available (it does, off A's window)? The reviewer noted this hinges on an unstated architectural assumption. This ADR states it.

## Decision

**A `SatelliteEphemeris` represents a constellation POOL, and NTNSlice satellite-path availability is POOL-LEVEL:** the path is available iff **some** tracked member is currently in an active pass window **and** that member's element set is deliverable (source epoch within `maxEpochAge` — the #275 per-member freshness gate, shared with the runtime-push `sourceEpochFresh`). NTNSlice is **deliberately not pinned to a single NORAD.**

## Rationale

- **LEO constellations serve by handover.** No single member is continuously overhead; members rise and set on the order of minutes. Pinning a slice to one NORAD would drop its satellite path on every set — the opposite of what a constellation is for. "Path viable" correctly means "a deliverable member is overhead."
- **The data model reflects this on purpose.** `satellitePath` has no NORAD/cell field; there is no slice→cell link. NTNSlice manages the terrestrial↔satellite *path* decision for a slice, not any individual cell's configuration.

## Consequences

- **Which member a given cell broadcasts — and keeping `ephemerisNoradID` current as members hand over — is `NTNCellConfig`'s / fleet-orchestration's responsibility** (roadmap; ADR 0005 defers fleet failover to v0.7). A cell pinned to a non-overhead member is a cell-configuration issue, **not** an NTNSlice availability bug: NTNSlice correctly reports the constellation viable and signals that cells should be pointed at the overhead member.
- The #275 freshness gate is **per-member** (an active window counts only if *that member's* element set is fresh), so a stale outage still fails the path closed — but it is not, and cannot be, per-cell-selection, because NTNSlice does not know any cell's selection.

## Alternatives considered

- **Pin NTNSlice to a NORAD (add `satellitePath.noradID`).** Rejected: breaks handover; a slice would lose service whenever that satellite sets.
- **Bind NTNSlice to specific `NTNCellConfig`s / their NORADs.** Deferred: this is the fleet-orchestration relationship (ADR 0005, v0.7), a design decision with its own ADR — not a bug fix. Until then the pool contract holds.

## Testing

`TestCheckSatelliteAvailability_PoolMemberOverheadKeepsPathAvailable` pins the contract: with a multi-member ephemeris where member A has an active, fresh window and member B has none, the path is reported **available** (A, a deliverable member, is overhead) regardless of any cell's `ephemerisNoradID`. The complementary all-members-stale → unavailable case is covered by `TestCheckSatelliteAvailability_SourceEpochFreshnessGate` (#275).
