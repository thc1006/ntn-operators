# ADR 0010 — Secure-by-default remote-control transport (v1alpha2)

- Status: **Proposed** (design; implementation deferred to the phased plan below — no code lands with this ADR)
- Date: 2026-07-31
- Deciders: @thc1006
- Relates to: #214 / #315 (v1alpha2 conversion + storage migration — already planned to retire the deprecated `spec.satellites.constellation` no-op field, so the version bump + conversion webhook are coming regardless; this ADR rides on that work), #299 (SSRF egress control / `ServiceReference` residual), #251 + ADR-0009 (endpoint allow-list + confused-deputy boundary), #309 (credential `ValidatingAdmissionPolicy`), #317 (egress NetworkPolicy).

## Context

`NTNCellConfig.spec.provider.remoteControl` today (v1alpha1) carries an **optional** `tls` block. When it is absent, the operator dials **plaintext `ws://`**. That is the core problem: **omission decides transport**, so forgetting `tls` silently downgrades to plaintext — the opposite of secure-by-default.

The controls added around it are all opt-in / off by default, so none of them *forces* a secure transport:

- The endpoint allow-list (#251; ADR-0009 Amendment 2 extended it to gate plaintext too) defaults to **permit-all** (empty).
- The credential `ValidatingAdmissionPolicy` (#309) is an opt-in chart artifact (`credentialRefPolicy.enable`, default `false`).
- The egress NetworkPolicy (#317) is opt-in.

Two facts make v1alpha1 unfixable in place:

1. **`tls == nil` is a load-bearing value** — it means "plaintext, deliberately." A CRD default or an in-place rejection cannot distinguish "I want plaintext" from "I forgot TLS" without breaking existing objects.
2. **The credential VAP is pinned to v1alpha1.** `dist/chart/templates/admission/credential-ref-policy.yaml` sets `matchConstraints.resourceRules[].apiVersions: ["v1alpha1"]`. A new API version would **bypass the Secret-authorization check entirely** unless it ships its own policy — verified 2026-07-31.

A v1alpha2 + conversion webhook is **already on the roadmap** (#214 / #315) to retire `spec.satellites.constellation`. The secure-transport redesign should therefore be designed *now* and land *with* that version bump, rather than motivating a separate one.

## Decision

Introduce **`v1alpha2`** with a **required** transport mode, eliminating implicit plaintext. The work is split into **MUST** (decided here) and **DEFER** (recorded here, deliberately *not* built now for proportionality — see Alternatives).

### MUST — the secure-by-default core

1. **`transport.mode` is required, no default.** Enum `{tls, mtls, plaintext}`. A missing mode is a schema rejection (**fail-closed**). Do **not** add `// +kubebuilder:default` — a default just moves the implicit-behavior problem (see Alternatives).
2. **Plaintext must be written explicitly** (`mode: plaintext`); it is never implied by omission. `kubectl explain`, GitOps diffs, and review all show plaintext as a deliberate choice.
3. **CEL structural rules** on the CRD (evaluated in-apiserver, no webhook):
   - **`address` is the single required target in the first v1alpha2.** `service` is DEFERred, so the `service` XOR `address` rule is introduced *with* that additive field — v1 ships no half-wired `service` path.
   - `tls`/`mtls` require a `tls` block; `plaintext` forbids it.
   - Raw `address` + `plaintext` is denied unless the address is a **literal loopback IP** (`127.0.0.0/8` or `::1` — never a DNS name that *resolves* to loopback, which is a rebind bypass). CEL enforces only this literal-IP shape; the "loopback plaintext is enabled" half is an operator-policy gate CEL cannot read, so it lives in MUST 7.
4. **Conversion webhook**, hub-and-spoke with `v1alpha2` as the hub, doing a **lossless round-trip**. The load-bearing rule: **`v1alpha1 tls == nil` converts to an explicit `mode: plaintext`** — never to a missing mode that defaulting could later silently upgrade to TLS. Conversion **preserves** semantics; it must not "repair" a legacy plaintext object into TLS, which would change live network behaviour during a version bump.
5. **v1alpha1 ratcheting** (admission): a *new* object or a *newly introduced* plaintext transport is denied; an *existing* plaintext object whose `remoteControl` is **unchanged** is grandfathered (`Warn` + `Audit`); a change that *widens* the plaintext attack surface (new/edited plaintext endpoint) is denied. Without this, v1alpha1 stays a complete bypass of every v1alpha2 guarantee. Mechanism: VAP `oldObject` (and/or CRD transition rules via `oldSelf` / `optionalOldSelf`) — transition rules are **not** auto-ratcheted (only non-`oldSelf` errors are), so an explicit transition rule is the deliberate way to grandfather unchanged old values while enforcing the stricter rule on new/changed ones.
6. **A v1alpha2-specific credential VAP.** The v1alpha1 policy's CEL references v1alpha1 field paths (`spec.provider.remoteControl.tls.secretName`) and matches only `v1alpha1`; v1alpha2's path differs (`…remoteControl.transport.tls.secretName`). Ship **two** policies, not one over-loaded CEL. v1alpha2's is `Deny`, `failurePolicy: Fail`, `parameterNotFoundAction: Deny` from day one (a brand-new API has no back-compat burden).
7. **Loopback plaintext is an operator-level opt-in, off by default.** The "enabled" half of rule 3 is a `--allow-loopback-plaintext` manager flag (default `false`), checked at **runtime** (reconcile) — *not* a CR field (a namespace tenant must not be able to self-grant plaintext) and *not* CEL (which cannot read a manager flag). It gates only the literal-loopback-IP addresses rule 3 already constrains, and matters only for a genuine same-Pod sidecar (see Rationale). Conversion maps a v1alpha1 loopback-plaintext object to the same explicit `mode: plaintext` + loopback `address`; the flag then governs whether the controller will *dial* it, exactly as the runtime endpoint allow-list (#251) governs every other destination.

### DEFER — recorded, not built now (proportionality per ADR-0009)

- **An *admission-time* transport allow-list at all: defer it.** The runtime flag allow-list (#251, ADR-0009 Amendment 2) already gates **every** push's destination at reconcile time — an admission-time version is a fail-fast UX nicety, not a new security control, so it is disproportionate to add now for a minority feature. If it is later wanted, the choice is a genuine tradeoff, **not** the "just use a ConfigMap" I first suggested (which was wrong): a *structured* allow-list (per-target transports) is idiomatically a **CRD** — Kubernetes guidance is ConfigMap `paramKind` for *simple key-value* and a CRD `paramKind` for *structured/validated/reusable* schemas, and a ConfigMap would force encoding the list as a string and parsing it in CEL (`params.data['x'].split(...)`), with no schema validation of the policy content. So the real later choice is "a new `RemoteControlPolicy` CRD (structured, but re-opening ADR-0009's proportionality) **vs** a flat ConfigMap that drops per-transport granularity" — both deferred; the CRD is not adopted now. A `RemoteControlPolicy` CRD is **not** the cross-namespace credential *grant* ADR-0009 rejected, but a new CRD subsystem for this minority feature triggers the same proportionality objection. (The credential VAP below needs **no** param — it uses the `authorizer` library — so this deferral does not touch the Secret-authorization MUST.)
- **`ServiceReference` target** (resolve a same-namespace `Service`, **reject `type: ExternalName`**, use the Service DNS as the default TLS `ServerName`): rejecting `ExternalName` is not optional — it is a documented confused-deputy / SSRF vector (an `ExternalName` redirects the resolved endpoint to arbitrary external DNS, the same class as Skipper `GHSA-mxxc-p822-2hx9` and Kyverno `CVE-2026-4789`; NetworkPolicy cannot even target an `ExternalName` Service, so it must be denied at admission + rechecked at runtime). This is the #299 residual and the highest-value production path, but also the part most likely to introduce conversion bugs — a `Service` target has **no lossless representation in v1alpha1** (down-conversion to `host:port` loses the Service identity + policy binding, needing a carefully round-trip-tested conversion annotation). Ship v1alpha2 with a structured **`address`** target first; add `service` as an **additive** v1alpha2 field once the conversion path is soaked (address-first, `service`-additive).

## Rationale

- **Required mode is the only true fix.** Any default — even `tls` — keeps "an omitted field decides the transport," and worse, mis-converts legacy plaintext (a `v1alpha1 tls==nil` becoming a defaulted-to-TLS v1alpha2) — a silent security-behaviour change during conversion.
- **Lossless, plaintext-preserving conversion is mandatory** because the webhook sits on the API read/write path and runs on stored-vs-requested version mismatch; "improving" a legacy object's security during conversion would change what the operator dials.
- **v1alpha1 without ratcheting is a bypass.** Grandfathering unchanged legacy objects while denying new/widened plaintext is the standard "old data grandfathered, new data must comply" pattern (CEL transition rules / VAP `oldObject`).
- **VAP is sufficient infra.** GA since Kubernetes 1.30; the chart already requires ≥1.31. VAP type-checks CEL against the matched schema, but only **best-effort**: full type-safety needs a single-GVK match, errors are surfaced in the policy *status* by an informer control loop (so they can lag policy creation), and recursive schemas are not checkable. Keep the existing server-side dry-run of rendered policies plus live positive/negative admission tests.
- **This is an established Kubernetes API pattern, not bespoke.** Gateway API's `BackendTLSPolicy` (GEP-1897) mandates that an invalid TLS config **MUST NOT fall back to unencrypted (plaintext)** — the exact "TLS intent ⇒ never silently plaintext" rule this ADR applies to remote-control — and Gateway API expresses transport as **explicit modes** (`Terminate`/`Passthrough`), not an implicit default. This aligns with OWASP Secure-by-Default (resilient out of the box without user configuration; fail-closed defaults).
- **The credential VAP needs no param** — it authorizes via the `authorizer` library (can the CR author `get` the referenced Secret), so it carries no ConfigMap/CRD param at all. The *admission-time transport allow-list* that would have needed a param is DEFERred entirely (see DEFER); nothing in the MUST set depends on a param resource.
- **Loopback is not a shortcut.** `127.0.0.1`/`::1` is the *controller-manager* Pod's loopback, not the gNB Pod's; only containers in the *same* Pod share `localhost`. The standard Helm topology is a central controller with the gNB/proxy in a separate Pod reached via a Service, so loopback plaintext must be **off by default** and only meaningful for a genuine same-Pod sidecar deployment.

## Consequences

- The conversion webhook is on the API critical path: a request needing conversion fails if the webhook is unavailable. It must be served by the existing **2-replica / PDB / leader-independent** topology (every replica serves the webhook, not only the leader), and rolled out **webhook-infra-first, then CRD-adds-v1alpha2** so the CRD never calls a not-yet-ready webhook.
- **Storage migration is phased** (served-both/storage-v1alpha1 → storage-v1alpha2 → rewrite stored objects → clean `status.storedVersions` → eventually `v1alpha1 served: false`), never in one PR, and `storedVersions` must be reduced to `["v1alpha2"]` before removing v1alpha1 storage.
- The new version + conversion extends the **4-copy CRD sync** and the CI drift checks (test-chart.yml / test-e2e.yml already enforce these).
- No behaviour changes for existing v1alpha1 deployments until they opt into the migration; v1alpha1 stays `served: true` for at least one explicit deprecation window.

## Rollout / phased plan (one PR per phase — NOT one mega-PR)

1. **This ADR.**
2. `api/v1alpha2` types + **pure conversion round-trip tests** (no storage or controller change).
3. **Conversion webhook infrastructure** (server, Service, serving cert, chart/bundle/Nephio wiring, HA tests) — CRD still v1alpha1-only.
4. **CRD serves v1alpha2** (`storage: false`) — soak conversion under GET/CREATE/UPDATE/WATCH/round-trip/rollback.
5. **v1alpha2 credential VAP (`Deny`)** + **v1alpha1 `Warn`/`Audit` plaintext ratchet** + the `--allow-loopback-plaintext` manager flag (default off). The admission-time transport allow-list is DEFERred — the runtime flag allow-list (#251) already gates every destination.
6. **Controller resolves the structured `address` target** (`service` only when it lands additively) with the layered check: runtime allow-list ∩ NetworkPolicy ∩ (for loopback plaintext) the loopback flag — intersection, never union.
7. **Storage version switch** + stored-object migration tooling.
8. **v1alpha1 deprecation ratchet** — deny new v1alpha1 objects, allow only migration edits, eventually `served: false`.

## Alternatives considered

- **Flip v1alpha1 semantics in place** (default `mode: tls`, or reject `tls == nil`): breaks existing plaintext deployments on upgrade and corrupts conversion of legacy objects. Rejected.
- **`RemoteControlPolicy` CRD** (a structured admission-time transport allow-list): not the identical object ADR-0009 rejected (that was a cross-namespace credential *grant*), but it repeats ADR-0009's disproportionate new-CRD-subsystem cost for this minority feature. Deferred per the DEFER section — the admission-time transport allow-list is deferred entirely (the runtime flag allow-list already gates destinations), and *if* built later a structured allow-list is idiomatically a CRD (Kubernetes guidance), **not** the `ConfigMap`-param this ADR first suggested, which re-opens the ADR-0009 proportionality tradeoff. Not rejected on merit — a future multi-tenant policy story could revisit it.
- **`ServiceReference` in the first v1alpha2 PR**: couples the highest-value feature to the riskiest conversion path. Deferred to an additive field (address-first).
- **Default `mode: tls`**: keeps implicit-transport behaviour and mis-converts legacy plaintext. Rejected (Decision 1/7).

## Testing (built with the implementation, not this ADR)

Schema/CEL matrix (missing/unknown mode; empty target; `plaintext`+`tls`; `tls`/`mtls` without `tls`; raw non-loopback plaintext; IPv4/IPv6 **literal** loopback accepted; a DNS name resolving to loopback **denied**; malformed IPv6; port 0/65536). Admission with a restricted ServiceAccount (CR-write-but-no-`secrets get` → Deny; grant `get` → allow; policy-not-found → Deny; v1alpha1 grandfather vs widen). Loopback flag (default off → literal-loopback plaintext still not *dialed*; flag on → dialed; flag never influences a non-loopback address). Conversion round-trips (plaintext/tls/mtls; metadata/finalizer/ownerReferences/status preserved; annotation carries **no** Secret content; a v1alpha1 client editing the projected endpoint must not have a stale annotation override its new intent; list + multi-object ConversionReview; webhook restart; cert rotation; either of the 2 replicas terminating). Storage migration + rollback. **Mutation tests** (drop required-mode; `parameterNotFoundAction: Allow`; VAP forgets v1alpha2; remove the loopback check; loopback flag default flips to on; conversion turning `nil` TLS into TLS; down-conversion annotation ignored). Kind E2E on the declared-minimum and current Kubernetes; the real OCUDU + TLS-proxy path still succeeds. **Deferred to the additive `service` field** (not the first v1alpha2 PR): Service resolution (ExternalName-reject/headless/absent-port/SAN = `<service>.<namespace>.svc`), `address`↔`service` conversion round-trips, and the `allow ExternalName` / `service`↔`address` XOR mutations.

## References (large-scale cross-check, 2026-07-31)

Authoritative sources checked against this ADR's load-bearing decisions — none contradicted a MUST; the research **strengthened** the ExternalName rejection (now CVE-backed) and **corrected** the deferred allow-list's param shape (a CRD, not the ConfigMap first suggested):

- **No plaintext fallback / explicit transport modes** — Gateway API `BackendTLSPolicy` (GEP-1897: an invalid TLS config "MUST NOT fall back to unencrypted"); OWASP *Secure by Default*. → confirms MUST 1–3.
- **Lossless hub-and-spoke conversion** — Kubebuilder multiversion tutorial + the CRD-versioning task; the hub is typically the storage version. → confirms MUST 4.
- **CEL ratcheting** — "Enforce Immutability using CEL" + KEP-2876/3488: transition rules use `oldSelf`/`optionalOldSelf` and are **not** auto-ratcheted. → confirms MUST 5.
- **VAP version matching + type-checking** — VAP reference + KEP-3488: `matchConstraints.apiVersions` matches the *request* version (so `["v1alpha1"]` does not cover v1alpha2 — verified against our own chart), and type-checking is best-effort. → confirms MUST 6 + the keep-dry-run caveat.
- **`ExternalName` SSRF / confused-deputy** — Skipper `GHSA-mxxc-p822-2hx9`, Kyverno `CVE-2026-4789`, `CVE-2020-8558`. → confirms the DEFER-ServiceReference `ExternalName`-rejection requirement.
- **Storage migration** — the storage-version-migration task + `StorageVersionMigration` / kube-storage-version-migrator: rewrite stored objects, then reduce `status.storedVersions` to `["v1alpha2"]` **before** removing v1alpha1 storage. → confirms the phased plan.
- **VAP param shape** — VAP reference: `ConfigMap` `paramKind` is for simple key-value; structured/validated params are idiomatically a CRD. → why the deferred allow-list is not "just a ConfigMap".
- **Deprecation window** — Kubernetes deprecation policy: **alpha** versions may be removed without a formal window, so keeping v1alpha1 `served` through an explicit window is a *courtesy* to existing deployments, not a policy requirement.
