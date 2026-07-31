---
adr: 2
title: Hybrid bootstrap and runtime NTN configuration interface
status: accepted
date: 2026-04-19
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: ["ConfigMap-only runtime update assumption"]
superseded_by: []
implementation:
  - "https://github.com/thc1006/ntn-operators/pull/177"
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/37"
---

# ADR 0002 — Hybrid bootstrap and runtime NTN configuration interface

## Decision summary

Keep two explicit delivery channels:

- **ConfigMap/bootstrap** for first boot, restart persistence and fields that the
  provider only reads statically.
- **Runtime command transport** for fields that the provider can update without
  process restart.

A runtime delivery failure is never reported as successful merely because the
bootstrap ConfigMap was written. Transport security is governed by ADR 0010;
credential authorization is governed by ADR 0009 and ADR 0011.

## Context

The original provider rewrote a YAML ephemeris block and relied on a future
gNB reload or restart. OCUDU later shipped the `ntn_config_update` remote
command, and the project implemented a runtime client in
`pkg/provider/ocudu/wsclient.go`.

The original ADR contained stale implementation details:

- it alternated between Gorilla and `nhooyr.io/websocket`;
- the repository now uses `github.com/coder/websocket`;
- it described a speculative message structure and connection pool;
- it suggested silent degradation to ConfigMap.

The architecture remains valid, but the delivery semantics must be exact.

## Decision drivers

- Bootstrap must work before a remote-control server is available.
- Runtime-capable updates must not require a gNB restart.
- The operator must distinguish “desired state persisted” from “hardware
  accepted the command”.
- Provider behavior must remain pluggable.
- Network and credentials are untrusted inputs.

## Decision

### Bootstrap channel

`ApplyCellConfig` renders provider bootstrap configuration and persists it in a
ConfigMap or provider-equivalent durable object.

Success means only:

> The operator persisted the bootstrap representation expected by the provider.

It does not prove that a running gNB reloaded it.

### Runtime channel

A provider implements a capability-oriented method such as:

```go
PushRuntimeUpdate(ctx context.Context, target RuntimeTarget, update RuntimeUpdate) error
```

The payload is a typed internal model. Provider packages own wire translation.
Controllers do not construct OCUDU JSON directly.

The OCUDU implementation uses `coder/websocket`, bounded context deadlines,
bounded payload/response sizes, no implicit HTTP proxy, redirect refusal and
typed handshake/result errors.

### Status model

Use independent conditions:

- `BootstrapConfigRendered`
- `BootstrapConfigPersisted`
- `RuntimeTransportReady`
- `RuntimeConfigDelivered`

`RuntimeConfigDelivered=True` requires a provider acknowledgement or a
provider-specific success criterion. A ConfigMap write cannot set it true.

When runtime transport is not configured:

- static-only objects may be `RuntimeConfigDelivered=Unknown`,
  reason `RuntimeTransportNotConfigured`;
- an object requiring a runtime-only field is `False`,
  reason `RuntimeTransportRequired`.

### Retry and idempotency

Runtime updates carry a deterministic marker derived from the effective
payload and epoch. The controller may retry after timeout or restart. Provider
commands must be treated as at-least-once unless upstream documents stronger
semantics.

A pending non-idempotent command, such as a serving-cell switch, must have its
own transition marker and must not be attached to every epoch refresh unless
upstream idempotency is proven.

### Security

- v1alpha1 legacy plaintext remains supported only for compatibility.
- v1alpha2 requires explicit transport mode (ADR 0010).
- destination allow-listing, credential authorization and grants are evaluated
  before the Secret is read and before dialing.
- NetworkPolicy is defense in depth, not an application authorization system.

## Invariants

- Bootstrap success never substitutes for runtime delivery success.
- Runtime failures are observable and requeued with bounded backoff.
- A Secret is not read for a destination rejected by policy.
- A provider-specific wire format does not leak into the public API.
- Retries do not silently repeat non-idempotent actions.

## Alternatives

**ConfigMap only.** Rejected for live orbital updates and session continuity.

**Runtime only.** Rejected because a stopped process cannot receive its initial
configuration.

**Silent ConfigMap fallback.** Rejected because it creates a false success
state.

**Long-lived shared connection pool as a requirement.** Rejected as an
architectural requirement. The provider may optimize connections later, but
correctness must not depend on pool state surviving restart.

## Observability

Metrics should separate:

- bootstrap render/persist attempts;
- runtime dial/handshake/command/ack failures;
- destination-policy denials;
- credential denials;
- deduplicated pushes;
- retry latency.

Logs must not include bearer tokens, private keys or full Secret data.

## Test plan

- Typed payload golden tests against a pinned OCUDU command contract.
- Fake WebSocket server: success, malformed response, 3xx/4xx/5xx, timeout,
  oversized response and abrupt close.
- Restart/replay idempotency.
- Non-idempotent command deduplication.
- Admission/runtime destination denial before Secret read.
- wss, bearer and mTLS kind E2E.
- Bootstrap-only path does not set runtime-delivered true.

## References

- Current runtime client:
  https://github.com/thc1006/ntn-operators/blob/main/pkg/provider/ocudu/wsclient.go
- OCUDU repository:
  https://gitlab.com/ocudu/ocudu
- coder/websocket:
  https://github.com/coder/websocket
