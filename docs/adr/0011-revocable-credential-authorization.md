# ADR 0011 — Revocable per-CR credential authorization for remoteControl.tls

- Status: **Accepted** — Option A endorsed by @thc1006 (2026-07-31). Implementation is phased (below); this ADR lands ahead of the code, as ADR-0006 did for #234.
- Date: 2026-07-31
- Deciders: @thc1006
- Relates to: #251 (confused-deputy), #309 (`credentialRefPolicy` admission VAP), #219 (label/type opt-in gate), #300 (endpoint allow-list). **Extends [ADR-0009](0009-remote-control-credential-confused-deputy.md)**, which deferred the full per-CR authorization; this ADR takes it up and adds the requirement ADR-0009 did not: **revocability**.

## Context

`NTNCellConfig.spec.provider.remoteControl.tls.secretName` references a Secret the operator reads (with its own cluster-wide `secrets get`) and presents to a CR-author-controlled endpoint — a confused deputy (ADR-0009). The shipped controls:

| Layer | Control | Property |
|---|---|---|
| Admission | `credentialRefPolicy` VAP (#309): the CR writer must hold `get` on the Secret | Real per-CR authorization — **but opt-in (`enable: false`), and `CREATE`/`UPDATE` only** |
| Reconcile | Secret opt-in label (#219) | Revocable but **coarse** (removes all references in the namespace) and is the *Secret owner's* lever, not the RBAC admin's |
| Reconcile | Endpoint allow-list (#300) + egress NetworkPolicy (#317) | Bounds *where* the credential goes, not *who* may reference it |

**The gap: authorization is point-in-time, not revocable.** The VAP checks the writer's `get` permission *at admission*. If a principal creates a valid CR (they had `get` then) and their RBAC is *later revoked*, the CR still exists and the operator keeps reading the Secret and pushing the credential — the admission check never re-runs. Revoking a principal's access does **not** stop the operator. There is **no reconcile-time authorization** anywhere in the codebase (verified: zero `SubjectAccessReview`/authorizer calls in Go), and the operator does **not** record who authored a CR, so it cannot re-authorize at reconcile even if it wanted to.

A complete, revocable authorization must re-check at reconcile time. That needs two things the project lacks: a **principal to re-authorize** (provenance) and a **reconcile-time authorization check**.

## Decision drivers

1. **Revocable / continuous** — revoking a principal's Secret access must stop the operator honoring their references, without a manual CR edit.
2. **No admission webhook / cert-manager** — the project deliberately uses CEL admission policies, not webhooks (findings.md B-2). MutatingAdmissionPolicy is **GA and default-on in K8s 1.36** (the project target; beta 1.34/1.35), giving a webhook-free way to stamp provenance.
3. **Minimize new API surface** — ADR-0009 rejected a grant-CRD as disproportionate for a same-namespace, minority feature.
4. **Reuse RBAC** — an authorization answer already lives in RBAC; prefer `SubjectAccessReview` over inventing a parallel model.
5. **GitOps-compatible** — CR authors are frequently a CI/GitOps ServiceAccount; the model must authorize SA principals (`system:serviceaccount:…`), not just humans.

## Options

**Option A — reconcile-time SAR against a stamped last-writer identity (recommended).**
A `MutatingAdmissionPolicy` stamps `request.userInfo.username` into an operator-owned annotation on every `CREATE`/`UPDATE` of an NTNCellConfig that carries `remoteControl.tls` — always overwriting, so an author cannot spoof it, and excluding the operator's own SA. At reconcile, before reading the Secret, the operator issues a `SubjectAccessReview` asking whether that stamped principal may `get` the referenced Secret; on `denied` it refuses the push, sets `CredentialAuthorized=False/Revoked`, emits an event + metric, and requeues at a low rate (self-heals if re-granted).
- *Pros:* revocable (re-checked each reconcile); no new CRD; no webhook/cert-manager (MAP is GA in 1.36); reuses RBAC; SA-compatible; consistent with ADR-0009's stated preference for SAR over a grant-CRD.
- *Cons:* provenance trust rests on the mutating policy (the design's root of trust); needs the operator to hold `create subjectaccessreviews`; SAR-per-reconcile must be cached; a fail-safe is needed for legacy CRs with no stamped principal; MAP requires K8s ≥1.34 (gate) / 1.36 (default) — older clusters need a fallback.

**Option B — grant-object CRD (ReferenceGrant-style).**
A namespaced `CredentialGrant` (or reuse of a Gateway-API-style ReferenceGrant) must exist tying a Secret to the NTNCellConfig (or namespace) for the reference to be honored. The operator checks grant existence each reconcile.
- *Pros:* revocable by construction (delete grant = revoke); **no provenance needed**; a plain API read (no SAR, no username, no MAP).
- *Cons:* a 5th CRD + controller + lifecycle + 4-copy CRD sync + samples/chart — the surface ADR-0009 explicitly judged disproportionate; grant lifecycle becomes its own management burden; authorization moves out of RBAC into a bespoke object.

**Option C — reconcile-time SAR against a spec-named ServiceAccount.**
The CR names `credentialServiceAccountRef`; the operator SARs *that* SA for `get` each reconcile.
- *Pros:* explicit provenance (in spec, no MAP); revocable; SA-native.
- *Cons:* a new API field + CRD change (4-copy sync); shifts the question to "may the author name this SA?" (a second authz check to avoid a new confused deputy); more API-visible complexity than A.

## Decision

**Adopt Option A** (endorsed by @thc1006, 2026-07-31). It is the only option that is revocable, needs no new CRD, and needs no webhook/cert-manager — the three drivers that dominate here — and it extends ADR-0009's SAR preference from admission-time to reconcile-time, which is exactly what revocability requires. MutatingAdmissionPolicy being GA/default-on in 1.36 removes Option A's historical blocker (provenance without a webhook). Option B stays documented as the fallback if the provenance-trust or MAP-version constraints prove unacceptable.

## Phased implementation

Each phase is independently shippable; only the last is a breaking default change.

- **Phase 1 — lock the existing opt-in VAP (no behavior change).** An envtest with a real restricted user (holds `ntncellconfig-editor`, *not* `secrets get`) proving the `credentialRefPolicy` VAP rejects a credentialed CREATE, and an authorized user is accepted. Closes #251-1. Mechanism-agnostic; unblocks nothing risky.
- **Phase 2 — provenance.** A `MutatingAdmissionPolicy` (+ binding, gated by the same `credentialRefPolicy.enable`) stamps the authenticated principal into `ntn.operators.dev/credential-authorized-by` on CREATE/UPDATE of a CR with `remoteControl.tls`, always overwriting, excluding the operator SA. Envtest: the annotation is set from the request user and cannot be spoofed.
- **Phase 3 — reconcile-time re-authorization (the revocable core).** In `resolveRemoteControlTLS`, before the Secret read: if a stamped principal exists, issue a `SubjectAccessReview` for `get` on the Secret as that principal; on `denied` refuse the push with a `CredentialAuthorized=False/Revoked` condition + Warning event + a `credential_authorization_denied_total` metric + bounded requeue. Cache SAR results per (namespace, secret, principal) with a short TTL. Fail-safe for a missing stamp (legacy CR) is a documented, enable-gated choice (refuse vs. grace). Operator gains `create subjectaccessreviews` RBAC. Envtest: revoke a user's RBAC → the next reconcile denies and stops the push.
- **Phase 4 — make it the default + rollout.** Flip `credentialRefPolicy.enable` (and the new pieces) on by default, with a migration note, upgrade test, SECURITY.md update, and an e2e. This is the breaking change, taken only once the complete revocable system exists.

## Consequences

- Authorization becomes **continuous**: revoking a principal's Secret `get` stops the operator honoring their references within one reconcile, closing the revocability gap #251 tracks. ADR-0009's interim controls (endpoint allow-list, label/type gate) remain as defense-in-depth.
- No new CRD and no webhook/cert-manager; the cost is one MutatingAdmissionPolicy + reconcile-time SAR + a cache.
- **Risks / open questions:** (1) provenance trust concentrates in the mutating policy — it must overwrite unconditionally and exclude the operator SA, or a malicious UPDATE could re-attribute the reference. (2) MAP availability on clusters < 1.34 needs the documented Option-B fallback or a feature-gate note. (3) SAR caching TTL trades revocation latency against apiserver load. (4) The fail-safe for legacy/unstamped CRs is a real policy choice (refuse breaks existing references on upgrade; grace leaves a window) — decided at Phase 4.
