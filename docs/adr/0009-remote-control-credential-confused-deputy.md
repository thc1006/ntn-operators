# ADR 0009 — Confused-deputy boundary for remoteControl.tls credential reads

- Status: **Accepted**, **amended 2026-07-31 (twice)** — see [Amendment (1)](#amendment-2026-07-31--the-deferral-rationale-was-wrong) and [Amendment (2)](#amendment-2026-07-31-2--the-endpoint-allow-list-now-gates-plaintext-destinations-too). The interim endpoint allow-list stands (and now gates plaintext destinations too); the deferral of the full per-CR authorization does **not**.
- Date: 2026-07-31
- Deciders: @thc1006
- ⚠ **Partly superseded by [ADR-0010](0010-remote-control-credential-grant.md)**: the *Rationale* bullet preferring a SubjectAccessReview at admission **over** a ReferenceGrant-style CRD no longer holds once revocability and endpoint binding are required — a SAR is a point-in-time check on a write and cannot express a destination. The interim endpoint allow-list and the shipped admission policy (#309) both stand; ADR-0010 adds an owner-issued grant as the opt-in stronger tier.
- Relates to: #219 (label/type opt-in gate), #251 (this confused-deputy follow-up), the N-12 runtime TLS/bearer/mTLS push. Builds on the existing SSRF allow-list (`--prometheus-allowed-endpoint-hosts`, `--ephemeris-allowed-private-hosts`) and `pkg/netutil.EndpointAllowlist`.

## Context

`NTNCellConfig.spec.provider.remoteControl.tls.secretName` references a Secret (in the CR's own namespace) holding a bearer token and/or mTLS client cert + key. The operator reads that Secret (with its `secrets get`) and authenticates to `remoteControl.endpoint` — an **arbitrary, CR-author-controlled `host:port`** (the endpoint CEL validates only *format*, not destination).

This is a **confused deputy**: the operator lends its Secret-read privilege on behalf of whoever wrote the CR.

- The shipped `ntncellconfig-editor-role` grants `create/update/patch/get` on `NTNCellConfig` but **no** `secrets get`. A principal with that role — but who cannot read the Secret — can create an NTNCellConfig that points a labelled credential at an **attacker endpoint**.
- For a **bearer** token this is direct **credential exfiltration** (the token is sent as `Authorization: Bearer`). For **mTLS** it is identity *misuse* (the operator authenticates to the attacker with its client identity; the private key is proven, not transmitted) — lower impact but still a confused deputy.

The #219 mitigation — the Secret owner must label it `ntn.operators.dev/remote-control-credential: true`, and API-credential Secret types are refused — reduces *which* Secrets can be targeted but is **not an authorization boundary**: the label is namespace-scoped, so **any** NTNCellConfig in the namespace may use **any** labelled Secret, and it does not bind the *endpoint*. The code comment on `RemoteControlTLS.secretName` already states this; #251 tracks the real fix.

## Decision

Two-part, matching the actual (same-namespace, low-adoption) shape of the problem:

1. **Ship an admin endpoint allow-list now** (this PR) as the interim boundary. `--remote-control-allowed-endpoint-hosts` gates the endpoint a **credentialed** push may target: when set, a push whose `remoteControl.tls` is present and whose endpoint host is not on the list is **refused before the referenced Secret is even read** (`RemoteControlConfigInvalid`, bounded self-heal requeue) — so no credential leaves the operator, and the refusal is identical whether or not the Secret resolves (no "Secret is valid" oracle). Empty (default) = permit-all (opt-in), so existing deployments and all plaintext pushes are unaffected. This closes the **worst** outcome — bearer exfiltration to an external host — because the destination is now admin-controlled (the CR author cannot change it), and it composes with the egress NetworkPolicy.

2. **Defer the full per-CR authorization**, and when it is built, prefer **SubjectAccessReview at admission ("the principal creating the reference must be able to read the Secret")** over a ReferenceGrant-style CRD. Rationale below.

## Rationale

- **This is same-namespace.** The Secret lives in the NTNCellConfig's own namespace. The standard same-namespace confused-deputy fix is "if you can reference the Secret, you must be able to read it" — a SAR against the *requester* at admission. That reuses existing RBAC and needs no new API surface. ReferenceGrant (Gateway API) exists for the **cross**-namespace case, where the source's RBAC cannot be checked from the sink; applying its owner-opt-in-object machinery to a same-namespace reference is heavier than the problem warrants.
- **The feature is advanced and uncommon.** OCUDU's `remote_control` server is plaintext/unauthenticated localhost by default; the safe deployment is a gNB sidecar. Credentialed (wss:///bearer/mTLS) remote control is a minority deployment. A whole grant-CRD subsystem (new CRD + controller + lifecycle + 4-copy CRD sync + samples/chart) for a minority feature is disproportionate.
- **Endpoint hardening is the highest-leverage, lowest-cost control** and is strictly beneficial under *any* eventual design: the exfiltration requires an attacker *destination*, so constraining the destination neuters it regardless of which reference-authz model lands later.
- **SAR needs infrastructure we do not have yet.** There is no admission webhook in the operator today; a SAR check requires the requester identity (`req.UserInfo`), which a reconcile does not carry — only a validating webhook does. That is real infrastructure (serving cert, `failurePolicy`, envtest wiring) and should be a deliberate, separately-reviewed step, not bundled here.

## Consequences

- The interim boundary is **opt-in** (empty allow-list permits all) to avoid breaking existing credentialed deployments on upgrade. Operators who use credentials SHOULD set `--remote-control-allowed-endpoint-hosts` to the sanctioned gNB hosts; until they do, the #219 label gate + the RBAC guidance ("do not grant `NTNCellConfig` write to principals who should not use every labelled credential") remain the only controls, as today.
- A future tightening (fail-closed default once adoption is understood) or the full SAR boundary is a follow-up on #251; this ADR does not close #251, it de-risks it and records the recommended direction.
- No API/CRD change: the endpoint field and label gate are unchanged; this adds one operator flag and one controller check.

## Alternatives considered

- **ReferenceGrant-style `RemoteControlCredentialGrant` CRD** (the reviewer's proposal: `secretRef` + `allowedNTNCellConfig{name,uid}` + `allowedEndpoint`). Strongest binding (owner opt-in, per-CR-UID, per-endpoint), but a cross-namespace pattern applied to a same-namespace, minority feature — disproportionate. Not rejected on merit for a future cross-namespace credential story; deferred as over-scoped for #251.
- **SAR admission webhook** (requester must be able to read the Secret). The recommended *full* fix; deferred because it requires building webhook infrastructure from zero. Recorded as the preferred direction for #251.
- **Do nothing beyond #219.** Rejected: the shipped editor-role makes the confused deputy reachable under the default role model, and a documented-but-unmitigated credential-exfil path is too weak for the credentialed feature.

## Testing

`TestPushEphemerisUpdateIfNeeded_RemoteControlEndpointAllowlist` pins all five arms: a credentialed push to a non-allowlisted endpoint is refused (`RemoteControlConfigInvalid`, `errRemoteControlEndpointNotAllowed`) **and the provider is never called** (the credential does not leave the operator) — the refused case provisions **no** Secret, so getting the endpoint error (rather than credential-unavailable) proves the endpoint is checked before the Secret read (no oracle); an allowlisted credentialed push proceeds; an empty allow-list permits any endpoint (opt-in); a **plaintext** push to a non-allowlisted endpoint is **also refused** (no SSRF relay through the operator); and an allowlisted plaintext push proceeds. Mutation-verified by neutering the allow-list `Check` (both attacker-endpoint cases then push).

---

## Amendment 2026-07-31 — the deferral rationale was wrong

The Rationale bullet **"SAR needs infrastructure we do not have yet"** is incorrect, and with it the decision to defer the full boundary. It reads:

> There is no admission webhook in the operator today; a SAR check requires the requester identity (`req.UserInfo`), which a reconcile does not carry — only a validating webhook does. That is real infrastructure (serving cert, `failurePolicy`, envtest wiring) and should be a deliberate, separately-reviewed step, not bundled here.

**A validating webhook is not the only thing that carries the requester identity.** `ValidatingAdmissionPolicy` — GA since Kubernetes 1.30, and this chart already requires `>=1.31` — evaluates CEL *inside kube-apiserver*, and its CEL environment exposes the Kubernetes **authorizer library**. The exact check this ADR wanted can be written declaratively:

```cel
authorizer.group('').resource('secrets')
  .namespace(request.namespace)
  .name(object.spec.provider.remoteControl.tls.secretName)
  .check('get').allowed()
```

None of the deferred cost is real for this route: no webhook deployment, **no serving certificate**, no `failurePolicy` plumbing beyond a field, and — importantly — **the operator gains no new RBAC**, because the API server performs the authorization check rather than the controller. A reconcile-time SAR would have needed `create subjectaccessreviews`; this needs nothing.

### Verified, not assumed

Checked end-to-end against a live Kubernetes **1.36.3** cluster using the artifact this chart now ships (not a hand-written approximation), with a ServiceAccount holding `ntncellconfigs` write and **no** `secrets get`:

| Case | Result |
|---|---|
| tenant without `secrets get`, CR references a labelled credential | **Forbidden** by the policy |
| same tenant after being granted `get` on that Secret | created |
| same tenant, **plaintext** CR (no `remoteControl.tls`) | created — correctly ungated |
| tenant without `secrets get`, **UPDATE** of an existing credentialed CR | **Forbidden** |

### Revised decision

1. The interim endpoint allow-list **stands** and is still worth having: it constrains the *destination*, which is orthogonal to who may *reference* the credential, and it protects the CRs that already exist.
2. The per-CR authorization boundary ships as an **opt-in chart artifact** (`credentialRefPolicy.enable`, default `false`), because turning it on is a real tightening: the policy gates admission, so stored objects are untouched until their next write, and an operator should first run it with `validationActions: [Warn, Audit]` to find violators.
3. What remains deferred is only the **default**: flipping `credentialRefPolicy.enable` to `true`, once adoption is understood. That is a much smaller open question than "build webhook infrastructure".

### What this does NOT solve

- **Objects already stored** are not re-validated; the policy applies on their next write.
- **A privileged writer acting for someone else** — a GitOps controller or CI identity with broad rights — passes the check, because the authorized principal is whoever submits the object. This is inherent to authorization-at-admission and would be equally true of a webhook; where tenants submit through such a pipeline, the pipeline is the trust boundary.
- The `patch`-without-`get` labelling vector from #219 is unchanged: this gates *who may reference* a labelled Secret, not who may label one.

---

## Amendment 2026-07-31 (2) — the endpoint allow-list now gates plaintext destinations too

The original Decision scoped `--remote-control-allowed-endpoint-hosts` to *credentialed* pushes on a "nothing to exfiltrate" rationale, leaving plaintext `ws://` pushes ungated. That conflated two orthogonal axes. The endpoint allow-list constrains the **destination** — where the operator connects — which, as the Revised decision notes, is *orthogonal to who may reference the credential*. A missing credential does not make an arbitrary destination safe: a principal who can write an `NTNCellConfig` but has narrower egress than the operator can aim a **plaintext** push at an internal host and use the operator as an **SSRF relay**, no credential required.

The allow-list now runs for **every** push (still before any Secret is read; still opt-in — an empty list permits all, so no existing deployment changes on upgrade). The error, flag help, and field doc drop the credential-only framing.

**This does not contradict the `credentialRefPolicy` result in Amendment (1)** ("plaintext CR — created, correctly ungated"). That row is about **admission** of the *credential reference*: a plaintext CR has no `secretName`, so the credential-ref policy correctly does not block its creation. The endpoint allow-list acts later, at **runtime**, on the *destination*. The two are complementary and operate on different axes at different times — admission gates *who may reference a credential*; the allow-list gates *where any push may go*. Both compose with the opt-in egress NetworkPolicy (#317 / #299), which confines the operator at the network layer but is port-based by default and so does not confine *hosts* on its own; the app-layer allow-list adds host-level destination confinement for operators who set it.
