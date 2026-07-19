# NTN UL-sync timing: sizing `ntnUlSyncValidityDur` against SIB19 and the ephemeris re-push cadence

This note resolves issue #228 item **G4**: how `NTNCellConfig.spec.ntn.ntnUlSyncValidityDur` interacts
with (a) the SIB19 broadcast period and (b) the SatelliteEphemeris ephemeris re-push cadence, and what
the operator enforces versus what the deployment must size by hand.

## Background (3GPP TS 38.331, Rel-17 NTN)

SIB19 carries the NTN assistance a UE needs to pre-compensate uplink timing: the serving-satellite
**ephemeris**, **common TA**, `epochTime`, and **`ntn-UlSyncValidityDuration`**. In open-loop NTN timing
the UE autonomously computes its timing advance from its own GNSS position and the satellite position it
derives from the ephemeris, valid around `epochTime`.

Two rules govern how long that assistance is trusted:

- The UE **starts timer T430 = `ntn-UlSyncValidityDuration`, counted from the subframe indicated by
  `epochTime`** (TS 38.331 §5.2.2.4.21; a NOTE adds that re-acquiring SIB19 before it expires is done "by UE
  implementation"). On **T430 expiry** (§5.2.2.6) the UE considers UL sync lost, informs lower layers, and
  re-acquires SIB19. The actual cessation of uplink transmission once UL sync / a valid TA is lost is the
  lower-layer (MAC/PHY) behaviour — **TS 38.321 / TS 38.213**. **T430 itself is a 38.331 RRC timer, not a
  MAC timer.** `ntn-UlSyncValidityDuration` runs from **5 s to 900 s** (`s5 … s900`) in maintained Rel-17.
  (The original TS 38.331 **v17.0.0 (2022-05)** freeze topped out at `s240`; `s900` arrived in a later
  Rel-17 ASN.1 maintenance update — the same one that changed `cellSpecificKoffset` to `INTEGER(1..1023)`,
  which this operator also tracks.)
- **`epochTime` is OPTIONAL.** If present, it is an explicit, pinned reference. **If absent, the epoch is
  the ending boundary of the SI window in which SIB19 was transmitted** — i.e. it re-anchors on every
  SIB19 broadcast.

This split is the crux of the whole question.

## Two timescales, two constraints

| Timescale | Where it is set | Order of magnitude |
|---|---|---|
| SIB19 broadcast period | `cellOverrides.sibSchedule.siPeriod` (radio frames × 10 ms) | 80 ms – 5.12 s |
| Ephemeris re-push cadence | operator constant `propagationRefreshInterval` | ~3 min |
| UL-sync validity | `ntn.ntnUlSyncValidityDur` | 5 s – 900 s |

### Constraint 1 — SIB19 must carry a FRESH epoch within validity (ENFORCED, provider-scoped)

T430 counts UL-sync validity from the SIB19 `epochTime`, **not** from the moment of reception — so
re-acquiring SIB19 advances the UE's deadline only when the re-broadcast carries a **fresher**
epoch/assistance. This is therefore **not** an epoch-mode-independent invariant: it is meaningful only for a
provider whose SIB19 regeneration re-anchors the broadcast epoch each period. **OCUDU** does (it derives the
epoch from the SI-window end on every periodic regeneration — verified in Constraint 2). For such a
provider, keeping the SIB19 broadcast period shorter than the validity window is a purely local invariant on
one `NTNCellConfig`:

```
siPeriod × 10 ms  <  ntnUlSyncValidityDur
```

The controller enforces a slightly stronger form — it wants **at least two** broadcasts inside the
window (one is the bare necessary condition, but a single missed acquisition from RF fade would then
already lose sync, so two gives a one-retransmission margin):

```
2 × (siPeriod × 10 ms)  >  ntnUlSyncValidityDur   ⇒   Warning
```

When violated the reconcile sets the advisory condition **`SIB19CadenceSane=False`
(`InsufficientSIB19Margin`)** and emits a `SIB19CadenceRisk` Warning event. It does **not** block:
`ConfigApplied` stays independent. With the default `siPeriod=16` (160 ms) this never fires for any
validity ≥ 5 s; it only catches genuine misconfigurations (a large `siPeriod` paired with a tiny validity).

**An unset `ntnUlSyncValidityDur` is NOT "no validity".** The field is optional with no Kubernetes default,
but the operator's runtime `ntn_config_update` push sends **5 s** when it is unset, and OCUDU's own config
default is the same 5 s — so the UE runs a real 5 s T430. The advisory evaluates against that same effective
value (`effectiveUlSyncValidityDurSeconds`, shared with the runtime push so they cannot drift): unset +
`siPeriod=512` (5.12 s) therefore **warns**, while unset + the default `siPeriod` stays sane.

The check is **gated on the gNB's epoch mode** (`applySIB19CadenceCondition` → `sib19EpochReanchored`): it
runs only when the gNB re-anchors the broadcast epoch per SIB19 regeneration, so re-acquiring SIB19 advances
the UE's T430 deadline. The effective mode is the explicit **`spec.provider.sib19EpochMode`** override
(`reanchored` | `pinned`) if set, else the provider-type default (OCUDU, verified to re-anchor at the pinned
revision below). A deployment on a gNB build whose SIB19 epoch is **pinned** sets `sib19EpochMode: pinned`
and the operator then expresses **no opinion** (no `SIB19CadenceSane` condition) — a re-broadcast pinned
epoch does not extend validity, so `siPeriod` is not the right signal there. This is the enforceable escape
hatch: the operator cannot observe a gNB's build/version at runtime, so a changed/unverified build must be
declared, not silently trusted. The `provider.type` enum admits only `ocudu` today, so the default mode is a
live no-op; the override matters when your OCUDU build diverges from the verified re-anchoring behaviour.

### Constraint 2 — the ephemeris re-push cadence vs validity (a non-issue for OCUDU; sizing note otherwise)

This is the pairing #228 named: can a ~minutes ephemeris re-push cadence lapse a short UL-sync validity? It
depends on how the **broadcast** SIB19 `epochTime` is derived. Most of the relevant state IS observable to
the operator (an earlier draft of this note wrongly claimed it was not):

- **What the operator sends.** The ConfigMap bootstrap path emits an explicit SIB19 `epoch_time` only when
  `spec.ntn.epochTime` is set; otherwise it is omitted and the UE uses the SI-window-end implicit epoch.
  The runtime `ntn_config_update` path (whenever `provider.remoteControl` + `cellID` are set) always sends a
  **non-optional `epoch_timestamp`** equal to the propagated state's epoch.
- **What the gNB broadcasts — OCUDU (primary provider), verified against source.** OCUDU does **not** pin
  the broadcast SIB19 `epochTime` to the pushed `epoch_timestamp`. That timestamp seeds only the orbital
  propagation (`enqueue_ephemeris_info`); the broadcast `epochTime` is re-derived on OCUDU's own **periodic**
  SIB19 regeneration as the **end of the SI window** in which SIB19 is scheduled (`epoch_slot =
  next_si_win_end + 1`), and the ephemeris is re-propagated to that epoch. Crucially, OCUDU **aligns the
  regeneration timer to the SI period** (`period_ms = si_period_rf × 10 ms`), so a fresh SI-window-anchored
  epoch is recomputed **every SI period — i.e. every broadcast**. So OCUDU **re-anchors** the SIB19
  `epochTime` to the SI window on every regeneration — the validity window self-renews per SIB19 broadcast,
  exactly like the implicit-epoch case. This is what makes Constraint 1 exact rather than approximate: the
  fresh-epoch cadence the UE experiences **equals** `siPeriod`, so `2 × siPeriod < validity` is precisely
  the "≥ 2 fresh-epoch SIB19 per validity window" condition.

  > **Verified against OCUDU `dev@a11f52046ad8e701177dfbbfd89e0dc72dc997f0` (committed 2026-07-18, inspected
  > 2026-07-19):**
  > `lib/ntn/ntn_configuration_manager_impl.cpp` `periodic_ntn_config_update_task` **L423–L431**
  > (`epoch_slot = next_si_win_end + 1`; the pushed `epoch_timestamp` only feeds `enqueue_ephemeris_info`),
  > the same file **L166–L168** (regeneration timer `period_ms = si_period_rf × 10 ms`, "aligned to the SI
  > period"), and `lib/ntn/ntn_sib19_helpers.cpp` `generate_sib19_info` **L102–L104** (`epoch_time` set from
  > `epoch_slot.sfn()/subframe_index()`). **This conclusion is provider-version-sensitive: it must be
  > re-validated whenever OCUDU's NTN configuration manager or SIB19 generator is upgraded** (that code is
  > actively refactored upstream). If a future OCUDU revision were to pin the broadcast `epochTime` to the
  > pushed `epoch_timestamp`, the re-push-cadence pairing below would become live for OCUDU too.

**Consequence for OCUDU:** the ephemeris re-push cadence does **not** lapse the UL-sync validity — only the
SIB19 broadcast cadence (`siPeriod`, Constraint 1) does. That is exactly the constraint the controller
enforces, so nothing further is needed on an OCUDU deployment.

**Only for a hypothetical provider that PINS the broadcast `epochTime` to the pushed timestamp** (explicit,
non-re-anchoring) could the pinned validity lapse between pushes. Even then the safe condition is not the
simplistic `validity ≥ refresh cadence`; because the pushed epoch is forward-dated by `propagationEpochLead`
(5 min), it is

```
broadcast epoch + validity  >  next required successful push + delivery/skew margin
```

which depends on that provider's exact mapping and the live push timing. Rather than clamp or reject against
an unverified mapping, the operator treats this as a **deployment-sizing responsibility** scoped to such a
provider: keep `ntnUlSyncValidityDur` comfortably above the re-push cadence (`propagationRefreshInterval`,
~3 min). This has not been observed on OCUDU.

## Why not clamp or reject the re-push-cadence pairing?

- **For OCUDU it is a non-issue** — the SIB19 epoch re-anchors to the SI window per broadcast (above), so
  the re-push cadence never lapses UL-sync validity; enforcing anything would guard a risk that does not
  exist for the primary provider.
- **For an unverified pinning provider, no single correct threshold exists** — the safe condition is
  `broadcast epoch + validity > next push + delivery/skew margin`, not `validity ≥ cadence`, and evaluating
  it needs the provider's pinning behaviour plus the live SatelliteEphemeris cadence (cross-CRD, adjacent to
  paused fan-out work). A hard clamp/reject would misfire on OCUDU and on legitimate implicit-epoch LEO
  deployments where `s5`/`s10` are correct.
- **What we do:** enforce Constraint 1 (SIB19 cadence vs validity) — local and unambiguous, but gated on the
  gNB's epoch mode (`sib19EpochReanchored`: it runs only for a re-anchoring gNB, since re-reading a pinned
  epoch does not extend validity) — and document Constraint 2 as a sizing responsibility scoped to the
  (unverified) pinning case.

## Sources

- 3GPP TS 38.331 (NR RRC) Rel-17 — timer **T430** (§5.2.2.4.21 start = ntn-UlSyncValidityDuration from
  epochTime; §5.2.2.6 expiry → inform lower layers UL sync lost + re-acquire SIB19); NTN-Config-r17 / SIB19
  `epochTime-r17` (OPTIONAL, implicit = SI-window end), `ntn-UlSyncValidityDuration-r17` (`s5…s900`
  maintained; `s5…s240` in the v17.0.0 freeze); `SchedulingInfo.si-Periodicity` = `{rf8…rf512}` radio frames.
- TS 38.321 (MAC) / TS 38.213 (PHY) — the lower-layer cessation of uplink transmission once UL sync / a
  valid TA is lost. (T430 itself is a 38.331 RRC timer, not a MAC timer.)
- TS 38.211 §4.3.1 — radio frame = 10 ms (siPeriod unit).
- OCUDU source at `dev@a11f52046ad8e701177dfbbfd89e0dc72dc997f0` (2026-07-18, inspected 2026-07-19;
  provider-version-sensitive — revalidate on OCUDU NTN/SIB19 upgrade) — `lib/ntn/ntn_configuration_manager_impl.cpp`
  L423–L431 (`periodic_ntn_config_update_task`: epoch slot = SI-window end) and `lib/ntn/ntn_sib19_helpers.cpp`
  L102–L104 (`generate_sib19_info`: explicit-but-SI-window-derived
  epochTime): the primary provider re-anchors the SIB19 epoch per regeneration.
- 3GPP TR 38.821 / Rel-17 NR NTN UL time-synchronisation (open-loop TA, common TA, epoch/validity).
