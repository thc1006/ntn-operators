# Review findings addressed by this bundle

## Blocking corrections

1. **SIB11 classification** — corrected by ADR 0001. SIB11
   `MeasIdleConfigSIB` is separated from connected-mode `MeasConfig`.
2. **Duplicate ADR 0010** — resolved by assigning credential grant ADR 0011.
3. **Secure transport contradictions** — ADR 0010 uses an address-only initial
   v1alpha2, required mode, explicit plaintext, phased conversion and migration.
4. **Duration premise** — ADR 0010 recognizes the existing
   `format: duration` schema and moves the 2h–24h rule to CEL for v1alpha2.
5. **Antenna false readiness** — ADR 0012 forbids node existence from proving
   antenna readiness and defines an evidence-backed agent contract.
6. **OMM cache name collision** — ADR 0007 replaces pure truncation with a
   hash-suffixed name and adds data-classification modes.
7. **Pool availability overclaim** — ADR 0008 calls the current signal a
   contact opportunity, not actual slice service.
8. **Runtime fallback ambiguity** — ADR 0002 separates bootstrap persistence
   from runtime delivery.
9. **Authorization fragmentation** — ADR 0009 consolidates the current layered
   model; ADR 0011 adds revocation/binding.

## Repository gaps intentionally left as tracked implementation work

- connected-mode measurement and SIB11 idle measurement;
- slice-to-cell/cell-group binding;
- cell activation/deactivation;
- true session continuity/UPF integration;
- conversion webhook and v1alpha2 migration;
- credential grant CRD/controller;
- structured ground-station hardware agent;
- policy-capable NetworkPolicy E2E;

## Landed since this bundle was written

- **Automated wss/mTLS E2E** — #332 covers the credentialed runtime push at the
  wire level (bearer rejection, unpinned CA, mTLS enforcement, endpoint
  allow-list) against a TLS-terminating proxy in front of a plaintext backend.
  It does **not** cover NetworkPolicy enforcement, which stays listed above: the
  chain being tested is not the same as every remote-control control being tested.
- **Fabricated antenna readiness** — #335 changed `AntennaReady` from a node-exists
  `True` to `Unknown/NoAntennaProbe`, which is the correction ADR 0012 requires.
  The evidence-based agent contract itself remains unimplemented (#68).
- **Six-digit NORAD regression coverage** — #351.
- **Collision-resistant OMM cache naming and legacy migration** — #345. The
  storage-mode half of ADR 0007 (`ConfigMap | Secret | Disabled`) is still open.
- **`sessionContinuity` honesty** — #352 states in the field itself that the build
  does not enforce it; enforcement remains #69.

## Claims deliberately not made

- The ADRs do not claim that proposed APIs are implemented.
- The ground-station agent contract is project-defined; no universal antenna
  health protocol was found that covers all target hardware.
- NetworkPolicy is not assumed to be enforced by every CNI.
- Conversion does not automatically migrate stored objects.
- VAP does not continuously reauthorize already stored objects.
