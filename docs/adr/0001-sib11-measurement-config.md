# ADR 0001 — SIB11 / Neighbor Measurement Configuration Scope

- Status: **Accepted (scope-pivoted)**
- Date: 2026-04-19
- Deciders: @thc1006
- Related issue: [#47](https://github.com/thc1006/ntn-operators/issues/47)
- Related implementations in-tree: [#19](https://github.com/thc1006/ntn-operators/issues/19), [#45](https://github.com/thc1006/ntn-operators/issues/45), [#46](https://github.com/thc1006/ntn-operators/issues/46)

## Context

Issue #47 asked: *"SIB11 NR neighbor measurement configuration is not modelable from `NTNCellConfig`. Design a path for `MeasObjectNR` / `ReportConfigNR` / `MeasIdList` / `QuantityConfig`."*

The initial hypothesis was that OCUDU (the srsRAN successor on `gitlab.com/ocudu/ocudu`) already emits some `cell_cfg.meas_config` subset we could wrap in a CRD, mirroring how we mapped SIB19 in #19. An upstream survey (`dev` HEAD, 2026-04-19) falsifies that hypothesis.

### Upstream ground truth (verified against OCUDU `dev` @ 2026-04-19)

Files surveyed (raw URLs):

- `configs/geo_ntn.yml`
- `apps/units/flexible_o_du/o_du_high/du_high/du_high_config_yaml_writer.cpp`
- `apps/units/flexible_o_du/o_du_high/du_high/du_high_config_cli11_schema.cpp`
- `apps/units/flexible_o_du/o_du_high/du_high/du_high_config.h`

**Finding**: OCUDU exposes **zero** YAML keys for NR connected-mode / idle-inactive neighbor measurement. There is:

- No `meas_config`, `meas_obj_to_add_mod`, `report_config`, `meas_gap`, `meas_id`, or `quantity_config` key anywhere in the YAML writer or CLI11 schema.
- No `sib11_config` / `sib12_config` struct in `du_high_config.h`.
- No SIB11 ASN.1 encoder wiring in the gNB control-plane path. Confirmed separately by upstream discussion [srsRAN_4G#1255](https://github.com/srsran/srsRAN_4G/discussions/1255): *"SIB10/11/12 are unsupported"*.

What **is** present (idle-mode cell reselection SIBs, not connected-mode measurement):

| SIB | Struct / YAML surface | 3GPP TS 38.331 IE | Use |
|-----|-----------------------|---------------------|-----|
| SIB2 | `sib2_config` (lines 2076-2088) | `cellReselectionInfoCommon` | `q_hyst`, `thresh_serving_low_p`, `t_reselection_nr`, `s_intra_search_p`, `q_rx_lev_min`, `cell_reselection_priority` |
| SIB3 | `sib3_config` (lines 2090-2105) | `intraFreqNeighCellInfo` | `intra_freq_neigh_cell_list[].pci` + `.q_offset_cell`, `intra_freq_excluded_cell_list` |
| SIB4 | `sib4_config` (lines 2107-2133) | `interFreqCarrierFreqInfo` | `inter_freq_carrier_freq_list[].arfcn`, `.q_rx_lev_min`, `.thresh_x_high_p`, `.q_offset_freq` |
| SIB5 | `sib5_config` (lines 2135-2162) | EUTRA reselection | `carrier_freq_list_eutra`, `t_reselection_eutra` |
| SIB16 | `sib16_config` (lines 2164-2190) | Freq-prio slicing | `freq_prio_list_slicing` |

**Interpretation**. The CRD gap for SIB11 is **100%**, not a subset gap. Wrapping SIB11 today means writing schema that has no wire-ward destination. In contrast, SIB2/3/4 *are* fully wired to the OTA path but have no CRD exposure.

## Decision Drivers

- **D1. Honesty.** A CRD field that claims to influence OTA behavior but silently noops is a correctness and operability trap. Conditions alone do not rescue it — users still write YAML expecting behavior they will not see.
- **D2. Short-term operator value.** Partners (樺漢 / 零壹 / 台本) need *some* neighbor-mobility knob to exercise handover behavior in NTN pilots. Idle reselection via SIB2/3/4 is not perfect, but it is real and measurable.
- **D3. Long-term upstream alignment.** When OCUDU lands SIB11 / `MeasConfig` — likely as a community contribution we could seed (see [upstream SIB19 pattern at issue #310](https://github.com/srsran/srsRAN_Project/issues/1066)) — we want a stable CRD schema that extends rather than redesigns.
- **D4. Validation blast radius.** Whatever we add must compose cleanly with existing CEL on `NTNCellConfig` and not bloat the CRD surface area for users who don't care about mobility.

## Options Considered

### Option A — Wrap SIB11 / `MeasConfig` speculatively, gated by a `Preview` marker

Add `NTNCellConfig.spec.cellOverrides.measConfig` with `MeasObjectNR`, `ReportConfigNR` (events A1-A6), `QuantityConfig`, `MeasGapConfig`, `NTNMeasGap`. Controller emits `Condition{Type=MeasConfigApplied, Status=False, Reason=UpstreamUnsupported}` on set-but-not-reachable fields.

- ➕ Forward-compatible schema lands early. No redesign when upstream catches up.
- ➖ Fails D1 (honesty). Users write YAML, see `Applied=True` on the parent, miss the nested `Degraded=True`, assume handover works, fail in pilot.
- ➖ Schema expansion bet. If upstream lands with a different field taxonomy (e.g., OAI does) we re-cut v1alpha1 anyway.

### Option B — Separate new CRD `NTNCellMeasConfig`, reference by name

New controller, new RBAC, new envtest suite. Future-proof.

- ➕ Clean separation; mobility evolves independently of `NTNCellConfig`.
- ➖ Controller + tests + Helm plumbing for a schema we cannot actually translate to anything runtime. Pays full engineering cost upfront for zero OTA behavior change.
- ➖ Adds a referenced-resource failure mode (dangling ref) to the operator without any user benefit.

### Option C — Pivot scope: expose SIB2/3/4 idle-mode mobility under `NTNCellConfig.spec.neighborMobility`; defer true SIB11 / `MeasConfig`

Add a new top-level `neighborMobility` block that maps **only** to fields OCUDU already emits:

```yaml
spec:
  neighborMobility:
    reselection:            # → sib2_config
      qHystDb: 2
      tReselectionNrSec: 2
      sIntraSearchPdB: 56
      threshServingLowPdB: 4
    intraFreqNeighbors:     # → sib3_config.intra_freq_neigh_cell_list
      - pci: 100
        qOffsetCell: "db0"
    interFreqCarriers:      # → sib4_config.inter_freq_carrier_freq_list
      - arfcn: 632628
        qRxLevMin: -120
        threshXHighPdB: 4
        qOffsetFreqDb: 0
```

Track SIB11 / `MeasConfig` / `NTNMeasGap` as future work pinned to an "upstream unblocked" GitHub project column. When OCUDU lands the `meas_config` YAML writer, file a follow-up issue to extend `neighborMobility.measConfig`.

- ➕ Satisfies D1 (every field reaches OTA today).
- ➕ Satisfies D2 (partners get real handover knobs, even if only idle-mode).
- ➕ Satisfies D3 (adding `neighborMobility.measConfig` later is a pure additive change).
- ➖ Doesn't solve connected-mode handover. NTN-to-TN fallback still needs `MeasConfig`; unavoidable upstream dependency.
- ➖ Scope of #47 changes from "SIB11" to "idle-mode mobility (3/4) + SIB11 future work". Needs a comment on #47.

## Decision

**Option C.** Specifically:

1. **This ADR pivots #47.** No SIB11-proper CRD work today. #47 is re-scoped to track the upstream dependency and the new issue filed under #47-follow-up. Add a comment to #47 summarising this ADR and close as *won't-fix-until-upstream*; re-open when OCUDU's `du_high_config.h` gains a `meas_config`-shaped struct.
2. **File a new high-priority issue** for the SIB2/3/4 idle reselection CRD fields (proposed title: *"api: `NTNCellConfig.spec.neighborMobility` — expose SIB2 / SIB3 / SIB4 idle-mode reselection"*). Scope:
   - `neighborMobility.reselection` → `cell_cfg.sib.sib2.*`
   - `neighborMobility.intraFreqNeighbors[]` → `cell_cfg.sib.sib3.intra_freq_neigh_cell_list`
   - `neighborMobility.interFreqCarriers[]` → `cell_cfg.sib.sib4.inter_freq_carrier_freq_list`
   - Non-breaking additive change. CEL: `size(intraFreqNeighbors) <= 16`, `size(interFreqCarriers) <= 8` matching 3GPP MAX sizes.
   - Deliverable: types + deepcopy + provider template + config_test golden cases + sample YAML.
3. **Track SIB11 upstream blocker** in repo notes. Watch OCUDU TSC minutes; when NR MeasConfig lands in `du_high_config.h`, revisit and open an issue: *"api: extend `neighborMobility.measConfig` for SIB11 / connected-mode measurements"*. That issue inherits the partial design sketched in Option A.

## Consequences

### Positive

- Partners gain real, OTA-verifiable mobility behavior in the next release without waiting on upstream.
- CRD surface stays honest — every field has a wire-ward destination.
- Future SIB11 work has a clear and narrow insertion point: add one sub-field under `neighborMobility`.

### Negative

- Connected-mode NTN→TN handover still requires UE-side workarounds (e.g., UE-initiated reselection timeouts). Operators wanting full network-initiated handover must wait for upstream.
- Operators familiar with 3GPP may expect `MeasObjectNR` fields in v1alpha1 and need to read the README section on "what lands OTA today" to understand the limitation.

### Neutral

- Introduces `neighborMobility` as a new top-level field on `NTNCellConfig.spec`, parallel to `ntn`, `cellOverrides`, `provider`. Slight surface growth but conceptually clean.

## Rollout Plan

1. **(this commit)** Merge ADR. Update `MEMORY.md` with upstream-status note.
2. **Follow-up issue** filed (not in this PR; referenced from #47 comment).
3. **Implementation PR** for `neighborMobility` SIB2/3/4 fields.
4. **Watch** OCUDU upstream weekly; ADR becomes a living document if `meas_config` lands.

## Upstream Contribution Opportunities

A parallel question to this ADR: *since OCUDU has zero SIB11/MeasConfig today, where could we add value by contributing upstream rather than waiting?*

Entry points (verified 2026-04-19):

- Main repo: [gitlab.com/ocudu/ocudu](https://gitlab.com/ocudu/ocudu) (`dev` branch, public MRs accepted).
- Governance: [gitlab.com/ocudu/Governance](https://gitlab.com/ocudu/Governance) — CONTRIBUTORS.md is the authoritative gate. Per 2026-02-19 TSC minutes, the CONTRIBUTING.md MR is still being drafted by @andre; expect the first wave of external contributions to exercise and harden it.
- Chat: [LF Zulip](https://linuxfoundation.zulipchat.com/join/rld5xurthmkcw7xmqoguy25t/) — real-time with maintainers; lowest-friction place to gut-check scope before filing.
- TSC: open public meetings; minutes in LF Networking Confluence `OCUDU` space. Good way to float a design RFC.
- LF ID: required for Wiki edits; cheap to obtain via [openprofile.dev](https://openprofile.dev/).

### Ranked contribution list (highest leverage → lowest)

**Tier 1 — Direct payoff to this operator and to partners**

1. **Dynamic SIB19 ephemeris runtime push (NETCONF / signal-triggered reload)**
   - Upstream trail: srsRAN [#1066](https://github.com/srsran/srsRAN_Project/issues/1066) (closed unresolved at archival), plausibly re-opened in OCUDU as issue #310 — verify before starting.
   - What to build: a gNB-side reload path that re-packs SIB19 from the current config struct without full process restart, triggered either by a NETCONF RPC or by SIGHUP on the config file.
   - Why us: we already ship a regex-based `replaceEphemeris` workaround in `pkg/provider/ocudu/provider.go:246`; contributing the real runtime path retires that workaround and unblocks LEO handover demos for 新星飛揚 / 樺漢.
   - Effort: 2–4 weeks gNB engineering. Medium L1/L2 depth required; most touch points are C++ config + RRC packing, which are tractable from our background.

2. **NTN `MeasGapConfig` (narrow subset of full MeasConfig)**
   - Why narrow: UEs doing inter-frequency measurement on an NTN cell need longer gaps to accommodate 5–30 ms propagation delay. This is NTN-specific and does *not* require full `SIB11 / MeasObjectNR` wiring — a single `cell_cfg.meas_gap { gap_offset_ms, gap_length_ms, periodicity_ms, pre_configured_gap: true }` block unblocks real-world scenarios.
   - Why us: NTN expertise is our differentiator; a terrestrial-focused contributor is less likely to scope the NTN propagation constraints correctly.
   - Effort: 1–2 weeks. YAML writer + CLI11 schema + config struct + dedicated-signaling plumb-through. Pairs naturally with our #47 follow-up.

**Tier 2 — Broader community value, moderate scope**

3. **Enrich SIB3 / SIB4 CLI surface to full 3GPP field set**
   - Today: SIB3 exposes only `pci` + `q_offset_cell`. SIB4 exposes only `arfcn` + `q_rx_lev_min` + `thresh_x_high_p` + `q_offset_freq`.
   - Missing: `intra_freq_excluded_cell_list`, `intra_freq_neigh_cell_list_v1610`, `inter_freq_black_cell_list`, `speedStateReselectionPars`. ASN.1 encoding presumably exists; only plumbing needs to land.
   - Effort: ~1 week. Good starter MR; doesn't touch RRC state machine.

4. **SIB11 ASN.1 encoder only (no DU-high plumbing)**
   - Add `asn1::rrc_nr::SIB11` encode/decode in `lib/asn1/rrc_nr/`, leave the gNB config surface off. Lets downstream users hand-inject SIB11 via test fixtures and seeds the follow-up T3.
   - Effort: 1–2 weeks. Contained; clear test harness (golden ASN.1 bytes).

**Tier 3 — Long-horizon, not first contribution**

5. **Full SIB11 + `MeasConfig` + RRC signaling**
   - 3–6 months. Needs deep L2/L3 and connected-mode RRC state-machine work. File as a tracking issue upstream after T1/T2 lands and maintainers are familiar with our contributions.

### Recommended first MR

**Tier 1 item #2 (NTN `MeasGapConfig`)** is the sweet spot: smallest scope that matters *only* to NTN operators (our lane), narrow enough to land in one release cycle, required as a precondition for T3 anyway. File it as an OCUDU issue first, link to this ADR as design rationale, and attach a draft MR.

### What this operator commits to

- Watch OCUDU TSC minutes weekly.
- When the OCUDU issue tracker is seeded for NTN (post-launch, per 2026-01-26 TSC minutes), file issues #1–#2 above and draft MRs.
- Document any accepted MR in `docs/upstream/` with date, commit SHA, OCUDU MR link — so this repo's NTN deployments can pin against OCUDU versions known to include our contributions.

## References

- OCUDU repo: [gitlab.com/ocudu/ocudu](https://gitlab.com/ocudu/ocudu) (`dev` @ 2026-04-19)
- srsRAN_4G SIB10/11/12 unsupported: [Discussion #1255](https://github.com/srsran/srsRAN_4G/discussions/1255)
- Upstream SIB19 dynamic-update precedent: [srsRAN_Project #1066](https://github.com/srsran/srsRAN_Project/issues/1066)
- 3GPP TS 38.331 §5.5 *Measurements*, §6.2.2 *SystemInformationBlockType11*
- Related ADR (future): `0002-sib11-upstream-unlocked.md` when applicable.
