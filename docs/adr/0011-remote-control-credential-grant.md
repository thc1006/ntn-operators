# ADR 0011 — An owner-issued grant for `remoteControl.tls` credentials

- Status: **Proposed**
- Date: 2026-07-31
- Deciders: @thc1006
- Supersedes: the **"prefer SubjectAccessReview at admission over a ReferenceGrant-style CRD"** recommendation in [ADR-0009](0009-remote-control-credential-confused-deputy.md) — see *Relationship to ADR-0009*. The interim endpoint allow-list and the shipped admission policy both stand.
- Relates to: #219 (label/type opt-in), #251 (confused deputy), #298 (rotation is bounded by the ~3m epoch cadence), #300 (admin endpoint allow-list), #309 (opt-in `ValidatingAdmissionPolicy`), #329/#332 (the credentialed-push E2E baseline this work extends).
- ⚠ **Numbering**: this grant decision is **ADR-0011**; the concurrent secure-by-default transport decision takes **ADR-0010** (layering: 0009 confused-deputy threat model → 0010 transport mode + API versioning → 0011 continuous per-credential authorization). The two are independent: transport versioning decides *how* we connect, this decides *who may consent to a credential being used*.
- ⚠ **Depends on ADR-0010 for one field**: `allowedModes` must use the SAME enum as the transport API, and the grant must ship in whichever API version that lands in. This ADR does not pre-decide that — see *Open dependency*.

## Context

Three gaps remain on `main` after #219, #251/#300, #296, #297, #309 and #313. They look separate but have one shape: **the Secret's owner cannot express, revocably, what their credential may be used for.**

**1. There is no way to withdraw consent after the fact.** #309 ships a `ValidatingAdmissionPolicy` requiring whoever writes an `NTNCellConfig` to hold `get` on the referenced Secret. That is a real boundary, but it is evaluated **at admission only** — ADR-0009's own amendment says *"Objects already stored are not re-validated; the policy applies on their next write."*

To be precise about what a grant does and does not fix, because an earlier draft of this ADR was not: a grant **does not make the original author's RBAC revocable**. Nothing re-runs that SubjectAccessReview, nothing tracks the author's identity, and removing their RBAC still invalidates nothing automatically. What a grant adds is a *different* capability — a **durable, credential-side consent object that the credential's administrator can withdraw**, without needing any rights over the consuming CR. Implementers should not read this as a requirement to track CR-creator identity.

**2. A credential is not bound to a destination.** `--remote-control-allowed-endpoint-hosts` (#300) is an **admin-global** list. Verified on `main`: there is no per-Secret endpoint binding anywhere in the tree. Any labelled Secret may therefore be paired with any allow-listed endpoint. A gNB operator who publishes a credential for *their* gNB cannot stop it being pointed at a different, equally allow-listed one.

**3. Withdrawal is not observed promptly, and is not reported at all.** Secrets are `get`-only, deliberately (an informer would need cluster-wide `list`/`watch` — see #298). So withdrawing consent is not acted on until the next real push. Measured on a live 1.36.3 cluster: a broken credential recovered **166 s** after being fixed, driven by the referenced `SatelliteEphemeris`'s ~3-minute propagation heartbeat, not by any per-cell retry.

**Correcting an earlier draft of this ADR**, which claimed this window is *"unbounded if that producer stalls"* — that contradicts #298, which this same author wrote. It is not unbounded: the currency gates run **before** the dedup early-return (#221 finding 1, pinned by `TestPushRuntime_SameMarkerBecomesStale_Blocked`), so a stalled producer trips `EphemerisStale` within `propagationEpochLead` and the push fails closed. And while dedup holds, `resolveRemoteControlTLS` is never called — **no credential is presented at all**. So the honest statement of the gap is narrower and worth stating exactly:

- a withdrawn consent stops the **next** push, up to ~3 minutes later, not immediately;
- until then the CR still reports `EphemerisPushed=True`, which is stale with respect to consent;
- there is no mechanism at all for the credential side to *cause* that re-evaluation.

This is a consent-propagation and reporting gap. It is **not** an ongoing credential-exposure window, and this ADR should not be used to argue that it is.

A failing push is already handled: `RemoteEndpointRejected`, `RemoteControlCredentialUnavailable` and `RemoteControlEndpointNotAllowed` all self-heal on the 5-minute poll. The gap is the **healthy** cell, where the dedup early-return sits ahead of `resolveRemoteControlTLS`, so nothing re-reads the credential at all.

## Decision

Introduce a namespaced CRD — working name **`RemoteControlCredentialGrant`** — created **by the Secret's owner, in the Secret's namespace**, that states which `NTNCellConfig` may use the credential, against which endpoint, in which mode.

```yaml
apiVersion: ntn.operators.dev/v1alpha1
kind: RemoteControlCredentialGrant
metadata:
  name: gnb-cred-for-cell-a
  namespace: ran-team
spec:
  secretRef:
    name: gnb-remote-control-cred
  # Which CRs may reference it. Name is required; uid pins it against name reuse
  # after a delete/recreate.
  allowedConfigs:
    - name: cell-a
      uid: 6f1c…            # optional
  # Which destinations that credential may be presented to. Exact host:port; the
  # admin allow-list (#300) remains the outer bound, this is the inner one.
  allowedEndpoints:
    - "gnb-proxy.ran-team.svc:8443"
  # Which authentication modes may use it.
  allowedModes: ["mtls"]
  # Optional: the owner bumps this when they rotate the Secret's contents. See
  # "What this does NOT solve".
  credentialRevision: "2"
```

Three consequences follow from it being an **object** rather than a check:

- **Revocable.** Deleting the grant, or narrowing it, withdraws consent. The Secret's owner acts on their own object; they need no rights over the consuming CR.
- **Watchable.** The controller watches grants and enqueues the `NTNCellConfig`s they name (a field index on `spec.secretRef.name`, mirroring the existing `spec.ephemerisRef` index). Revocation therefore triggers a reconcile instead of waiting for an unrelated heartbeat — closing gap 3 for the consent half.
- **Binding.** `allowedEndpoints` and `allowedModes` make gap 2 a property of the credential rather than of the cluster.

### Where the check runs — the part that makes the properties true

A grant watch alone does **not** deliver revocation, and an earlier draft of this ADR implied it did. `pushRuntimeEphemeris` currently runs `isEphemerisPushUpToDate` (line ~114) **before** `resolveRemoteControlTLS` (line ~163). So the sequence for a healthy cell is:

1. the grant is deleted or narrowed → the watch enqueues the `NTNCellConfig`;
2. the ephemeris marker has not changed and the generation has not changed;
3. **dedup returns early** — and the grant is never looked at.

Deleting a grant, narrowing it, or bumping `credentialRevision` would all be silently ignored until the next epoch. The decision must therefore say where authorization is evaluated, not just that grants are watched.

**Decision: grant authorization is evaluated BEFORE the dedup early-return, and its outcome participates in the dedup key.**

```
freshness / currency gates          (unchanged, still first)
→ match the grant  ────────────────  cheap: CR-local fields vs a cached grant. No Secret read.
→ dedup on (ephemerisMarker, authzDigest)
→ resolve the Secret               (unchanged: only past dedup)
→ dial / push
```

`authzDigest` is derived from the **matched grant's** UID + `resourceVersion` (which changes on any edit, including `credentialRevision`), plus the tuple that was matched. Persisted next to the existing push marker. This keeps the cheap path cheap — a steady-state reconcile still reads no Secret and still writes nothing — while making "the grant changed" a first-class reason to re-push, exactly as "the epoch changed" already is.

Rejected alternative: fold the grant into the existing ephemeris push marker. That marker is load-bearing for gNB-restart recovery and the #230 crash-idempotency argument, and #298 already rejects conflating "what the gNB holds" with "how we authenticated" for the same reason.

**Required tests** (extending the #332 fixture rather than building a new one):

| Case | Expected |
|---|---|
| healthy cell → delete the only matching grant, **no** new ephemeris epoch | condition goes False promptly, without waiting ~3 min |
| narrow `allowedEndpoints` / `allowedModes` so the tuple no longer matches | refused promptly |
| bump `credentialRevision` only | dedup bypassed, Secret re-read, push re-issued |
| edit an unrelated grant | **no** re-push (the digest must not be over-broad) |
| two matching grants, delete one | still permitted |

### Watch → enqueue mapping

Indexing grants by `spec.secretRef.name` answers "which grants reference this Secret", which is **not** the question the event handler has to answer. The mapping is explicit:

- **Add/Update**: enqueue the union of `allowedConfigs` in the **old** and **new** objects, so a config removed from the list is re-evaluated and loses access.
- **Delete**: enqueue every `allowedConfigs` entry of the deleted object.
- Entries are namespace-local (the grant lives in the Secret's namespace, which is the CR's namespace).

A second index of `NTNCellConfig` by its referenced Secret name is **not** required for this mapping and is not added.

### Multiple grants: one grant must match the whole tuple

**A single grant must match Secret + config + endpoint + mode.** Permissions are **not** assembled across grants: a grant allowing endpoint A and another allowing `mtls` must not combine into "A over mtls". Deleting any one grant revokes exactly what that grant permitted; access survives only if some *other single* grant still matches the complete tuple.

Gateway API's `ReferenceGrant` is additive/OR across grants, but each grant there authorises one complete (from, to) pair — the OR is over whole permissions, not over fields. This keeps that property while refusing field-level assembly, which is the failure mode where two individually-harmless grants add up to one nobody intended.

### Identity: what is pinned, and what is not

- `allowedConfigs[].uid` — **optional**. Present ⇒ delete/recreate under the same name does **not** inherit the grant.
- `spec.secretRef` — **name-bound, no UID**, and this is deliberate: the CR references its Secret by name too, so pinning the grant harder than the reference it authorises would make routine Secret rotation-by-recreate fail while the CR happily kept pointing at the new object. The consequence is stated rather than hidden: **deleting a Secret and recreating it under the same name inherits the existing grant.** Both recreate cases get a test.
- `spec.secretRef` is **immutable** after creation. Repointing a grant at a different Secret is a new consent decision and must be a new object.

### Who may write a grant

"Created by the Secret's owner" is a story, not a boundary — Kubernetes has no notion of Secret ownership, only verbs. Anyone with `create`/`patch` on the grant resource could otherwise issue one, which would hollow out the whole design. Enforcement is therefore part of this decision, not an afterthought:

1. A **grant-specific `ValidatingAdmissionPolicy`** requiring the principal writing the grant to hold `get` on the exact Secret in `spec.secretRef` — the same `authorizer` mechanism #309 already uses, applied to the grant instead of the CR.
2. **Separate RBAC**: the shipped `ntncellconfig-editor-role` must **not** confer grant write.
3. `--require-credential-grant` refuses to start unless (1) is installed, so the enforcement mode cannot be enabled while its own gate is missing.

Note this is a distinct policy from #309's, which checks the *NTNCellConfig* writer and is default-off. Neither implies the other.

### Schema bounds

Decided here because they change the merge semantics and the compatibility surface, not merely the implementation:

| Field | Rule |
|---|---|
| `allowedConfigs` | `MinItems: 1`, `MaxItems: 32`, `listType: map` keyed on `name` |
| `allowedEndpoints` | `MinItems: 1`, `MaxItems: 16`, `listType: set`, each `MaxLength: 261` |
| `allowedModes` | `MinItems: 1`, enum from the transport API, `listType: set` |
| empty list | **impossible** — `MinItems: 1` everywhere. An empty list must never read as "allow all" |
| wildcards | **not supported** in any field, in this version |
| `credentialRevision` | optional opaque string, `MaxLength: 64` |

### Endpoint matching is canonicalised, once

`allowedEndpoints` is an exact `host:port` match **after canonicalisation**, and the same canonicaliser is used by admission validation, the controller's match, and the tests — a rule that differs between the three is a rule that will eventually authorise something nobody approved:

- host lower-cased; a single trailing dot stripped;
- a short Service name is **not** equal to its FQDN — write what the CR writes;
- IPv6 in its canonical compressed form; IPv4-mapped IPv6 normalised to the IPv4 form;
- the port is **mandatory** and compared.

This is deliberately stricter than `pkg/netutil.EndpointAllowlist`, which compares a lower-cased hostname and ignores the port. That one is an admin's outer bound on egress; this is an owner's inner bound on a specific credential, and the two are not interchangeable.

### Open dependency

`allowedModes` must draw from the same enum as the transport work (ADR-0010), and the grant must ship in whichever API version that lands in. This ADR **does not** assume that version. Sequencing — grant first, transport first, or together — is settled with ADR-0010 before either implementation starts.

### Enforcement is admin-gated and default-off

A new flag, `--require-credential-grant` (default `false`). While it is off, grants are advisory and nothing changes. When an admin turns it on, a `remoteControl.tls` push is refused unless a grant permits the (Secret, CR, endpoint, mode) tuple.

The alternative — "enforce whenever a grant exists for that Secret" — is **rejected**: an attacker simply does not create one, and the feature would silently do nothing exactly when it matters.

Refusal reuses `RemoteControlCredentialUnavailable`, the uniform reason from #296, so the existing 5-minute self-heal applies and no new Secret oracle is opened.

## Relationship to ADR-0009

ADR-0009 recommended **SAR at admission** over a grant CRD, on the grounds that this is a same-namespace problem, that the feature is a minority deployment, and that *"applying its owner-opt-in-object machinery to a same-namespace reference is heavier than the problem warrants."*

That reasoning was sound **for the requirement as stated there** — deciding who may create the reference. It does not survive two requirements that were not on the table:

- **Revocability.** A SAR is a point-in-time check on a write. ADR-0009's amendment records that limitation itself. A grant is state, so withdrawing it is observable.
- **Endpoint binding.** A SAR answers "may this principal read this Secret?". It cannot express "…and only to reach *that* gNB". Nothing in the SAR model can.

So a grant is not a heavier way to do the same job — it does **three** jobs, and the per-object cost is amortised across all three. That is what changes the calculus, not a disagreement with ADR-0009's original analysis.

**#309 is not replaced.** It stays as the cheap default: no CRD, no controller, one declarative policy, and it stops the confused deputy at the moment of writing. The grant is the stronger, opt-in tier for multi-tenant namespaces. They compose — admission decides who may *create* the reference, the grant decides whether it may *continue* to be used, and #300's allow-list remains the admin's outer bound on destinations.

## What this does NOT solve

- **Rotating a Secret's *contents*.** Editing `token` or `tls.key` does not touch the grant, so it fires no watch. That path stays at the ~3-minute bound recorded in #298. The optional `credentialRevision` field is the escape hatch — an owner who bumps it on rotation converts a content change into a watched change — but it is opt-in and depends on the owner remembering. It is **not** a substitute for a Secret watch, and this ADR should not be read as closing #298.
- **A `patch`-capable principal.** Someone who can patch objects in the Secret's namespace can write a grant they should not. The grant moves consent to the Secret's own namespace, which is where it belongs, but namespace RBAC is still the ceiling.
- **Anything about the gNB's own authorization.** A grant governs what the operator will present, not what the gNB accepts.

## Consequences

- A fifth CRD: `make manifests bundle nephio-sync` keeps **four** copies in sync, plus `docs/api-reference.md`, chart RBAC, and samples.
- The controller gains `list`/`watch` on the new CRD — but **not** on Secrets. That is the point: consent becomes watchable without widening Secret access, which is exactly the trade #298 rejected the informer for.
- Cardinality is per (Secret, consumer) rather than per CR; in the expected deployment that is a small number.
- The grant is only meaningful when the operator can be trusted to enforce it. It is a boundary against *CR authors*, not against a compromised operator, which already holds `secrets get`.

## Alternatives considered

- **Keep SAR-only (#309 as shipped).** Rejected as the end state: it cannot revoke and cannot bind an endpoint. Kept as the default tier.
- **Gateway API `ReferenceGrant` itself.** Its schema is namespace/kind-scoped for the cross-namespace case; it has no place to express an endpoint or an auth mode, which are two of the three jobs here.
- **Put `allowedEndpoints` on the Secret as annotations.** No schema, no validation, no status, and a `patch`-only principal can edit annotations on a Secret they cannot read — the same weakness #219 already documents for the opt-in label.
- **A cluster-scoped grant.** Rejected: consent belongs next to the credential, and a cluster-scoped object would need its own authorization story.

## Follow-up, tracked separately

The credentialed-push E2E baseline is no longer hypothetical: #329 tracked it and **#332** shipped it, with four arms driving a real `ntn_config_update` frame through a TLS proxy to a plaintext backend. The revoke/narrow/revision tests above extend that fixture rather than standing up a second one.
