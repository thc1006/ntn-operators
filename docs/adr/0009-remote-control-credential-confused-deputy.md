# ADR 0009 — Confused-deputy boundary for remoteControl.tls credential reads

- Status: **Accepted** (interim endpoint allow-list shipped here); full per-CR authorization **deferred**
- Date: 2026-07-31
- Deciders: @thc1006
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

`TestPushEphemerisUpdateIfNeeded_RemoteControlEndpointAllowlist` pins all four arms: a credentialed push to a non-allowlisted endpoint is refused (`RemoteControlConfigInvalid`, `errRemoteControlEndpointNotAllowed`) **and the provider is never called** (the credential does not leave the operator) — the refused case provisions **no** Secret, so getting the endpoint error (rather than credential-unavailable) proves the endpoint is checked before the Secret read (no oracle); an allowlisted credentialed push proceeds; an empty allow-list permits any endpoint (opt-in); and a plaintext push is never gated. Mutation-verified by neutering the `rc.TLS != nil` check (the attacker-endpoint case then pushes).
