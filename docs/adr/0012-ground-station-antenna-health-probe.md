---
adr: 12
title: Evidence-based ground-station antenna health and readiness
status: proposed
date: 2026-07-31
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: ["node-exists implies AntennaReady=True behavior"]
superseded_by: []
implementation: []
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/68"
---

# ADR 0012 — Evidence-based ground-station antenna health and readiness

## Decision summary

`AntennaReady=True` will require fresh structured telemetry from a
ground-station hardware agent. Kubernetes node existence proves only
`K8sNodeReady`; it does not prove antenna, drive, alignment, tracking or RF
health.

The controller separates:

- agent reachability;
- antenna/drive readiness;
- pointing alignment;
- tracking activity;
- receiver/modem lock.

Tracking and lock are evaluated relative to whether a contact is expected.
An idle antenna outside a scheduled pass is not unhealthy merely because it is
not locked.

## Context

The current reconciler sets:

```text
AntennaReady=True
Reason=AntennaOperational
```

whenever the mapped Node exists. The optional HTTP endpoint is treated as a
generic 2xx check and drives `RFLinkHealthy`, but it does not prove any hardware
state.

Real antenna-control systems expose distinct position, drive, tracking, lock
and fault states. There is no single universal vendor-neutral wire protocol for
all targeted hardware. Therefore the project defines a small read-only agent
contract and provider adapters behind it.

## Decision drivers

- Conditions must be evidence-backed.
- Absence of a probe is `Unknown`, not `True`.
- Hardware readiness must be distinct from current contact activity.
- Telemetry must be fresh, bounded and authenticated.
- Vendor-specific protocols stay behind an agent/provider boundary.
- A monitoring failure must not accidentally command hardware.

## Decision

## A. New monitoring model

Add a structured agent configuration; retain the old `monitoring.endpoint`
only as deprecated legacy reachability.

Illustrative API:

```yaml
spec:
  monitoring:
    healthCheckInterval: 30s
    timeout: 3s
    agent:
      endpoint: https://gs-agent.ground-system.svc:9443
      credentialSecretRef:
        name: gs-agent-client
      serverName: gs-agent.ground-system.svc
      maxTelemetryAge: 10s
      thresholds:
        maxPointingErrorDeg: "0.5"
        requireAlignmentCalibration: true
```

Durations receive `format: duration` and admission bounds.

The endpoint must use TLS in production. Destination allow-list, redirect
refusal, proxy disablement, timeouts and body-size limits apply.

## B. Read-only versioned agent contract

Example response:

```json
{
  "apiVersion": "health.groundstation.ntn.operators.dev/v1",
  "observedAt": "2026-07-31T07:00:00Z",
  "agent": {
    "ready": true,
    "version": "1.0.0",
    "uptimeSeconds": 12345
  },
  "antenna": {
    "mode": "TRACKING",
    "driveHealthy": true,
    "alignmentState": "CALIBRATED",
    "motorFaults": [],
    "commandedAzimuthDeg": 195.3,
    "actualAzimuthDeg": 195.2,
    "commandedElevationDeg": 42.7,
    "actualElevationDeg": 42.6,
    "pointingErrorDeg": 0.14
  },
  "tracking": {
    "expected": true,
    "active": true,
    "target": "NORAD:12345",
    "state": "LOCKED"
  },
  "rf": {
    "receiverLock": true,
    "modemLock": true,
    "signalPresent": true
  }
}
```

The endpoint is health/telemetry only. Command or firmware mutation endpoints
are out of scope and must use separate authorization.

Unknown fields are tolerated for forward compatibility; required fields for
the declared version are validated.

## C. Conditions

### `GroundStationAgentReady`

True only when:

- TLS/auth succeeds;
- schema version is supported;
- response is within body/time bounds;
- `observedAt` is within `maxTelemetryAge`;
- `agent.ready=true`.

### `AntennaReady`

True only when:

- agent ready;
- `driveHealthy=true`;
- no critical motor/drive fault;
- alignment is acceptable;
- position telemetry is valid;
- telemetry is fresh.

It does not require a live RF lock outside an expected pass.

### `AntennaAligned`

True when calibration is valid and pointing error is within threshold.
Unknown when the hardware cannot report it.

### `AntennaTracking`

- True: tracking expected and active/locked.
- False: tracking expected but acquiring timed out, lost or stopped.
- Unknown/`NotScheduled`: tracking is not expected.

### `RFLinkHealthy`

- True: contact expected and required receiver/modem lock is present.
- False: contact expected but lock/signal requirements fail.
- Unknown/`NotScheduled`: no contact is expected.
- Unknown/`TelemetryUnavailable`: agent data unavailable.

### `K8sNodeReady`

Continues to describe only the edge Node.

## D. Phase calculation

- `Running`: Node ready, agent ready and antenna ready; no critical condition
  false. Tracking/RF may be `NotScheduled`.
- `Degraded`: Node ready but antenna/agent is false, or a scheduled contact has
  failed tracking/RF.
- `Offline`: Node missing/not ready or station agent unreachable beyond the
  configured offline threshold.
- `Provisioning`: binding/agent has never become ready.
- `Updating`: firmware update in progress, with hardware health still reported.

No probe configured:

- `AntennaReady=Unknown`, reason `ProbeNotConfigured`;
- never synthesize `AntennaOperational`;
- phase is `Degraded` or `Provisioning` according to lifecycle policy, not
  `Running` solely from Node existence.

## E. Legacy endpoint

The old generic HTTP 2xx endpoint may set only
`GroundStationAgentReady`/`LegacyEndpointReachable`. It can never set
`AntennaReady=True`.

Deprecate it and remove it in a future API version after migration.

## F. Polling and status discipline

- per-request timeout is less than check interval;
- jitter polling to avoid fleet synchronization;
- update status only on semantic change or health timestamp cadence;
- `lastHealthCheck` means last successful **structured station health**
  evaluation, not Node lookup;
- preserve last known values for diagnosis but mark conditions Unknown when
  stale;
- use `observedGeneration` and standard condition transition semantics.

## Invariants

- Node existence never implies antenna operational.
- No agent/probe means Unknown.
- stale telemetry cannot produce True.
- motor fault or excessive pointing error cannot produce AntennaReady True.
- tracking/lock are not required outside an expected contact.
- health endpoint cannot issue hardware commands.
- failure messages contain no credential material.

## Alternatives

**Continue node-based simulation.** Rejected; false positive readiness.

**Generic 2xx health endpoint.** Rejected as proof of hardware state.

**Query every vendor directly from the central controller.** Rejected; creates
vendor protocol and network coupling.

**Require lock at all times.** Rejected; a ground station can be healthy while
idle between passes.

**Use Prometheus only.** Deferred as an optional adapter. Pulling a coherent,
timestamped snapshot is simpler for correctness; metrics remain useful for
observability.

## Security and networking

- use TLS and preferably mTLS;
- credentials in same-namespace Secret with authorization controls;
- allow-list endpoint before Secret read;
- no redirects and no environment proxy;
- cap response body and JSON depth;
- validate finite numeric values/ranges;
- reject telemetry timestamps too far in the future;
- NetworkPolicy permits only approved agent destinations;
- agent exposes a read-only service account/identity.

## Observability

Metrics:

- probe duration/result;
- telemetry age;
- pointing error;
- tracking state;
- lock state;
- motor fault count;
- condition transition count.

Avoid unbounded labels such as raw fault text or target names. Fault codes are
bounded enums; details stay in structured logs/status message with truncation.

## Test plan

### Unit

- no agent → Unknown;
- fresh normal telemetry → ready;
- stale/future timestamp;
- motor fault;
- alignment uncalibrated;
- pointing error over threshold;
- tracking expected lost vs not scheduled;
- receiver lock and modem lock combinations;
- NaN/Inf/out-of-range numeric input;
- oversized/malformed/unknown-version response;
- status no-op and transition times.

### Security

- destination denied before Secret read;
- TLS verification and mTLS;
- redirects refused;
- proxy ignored;
- timeout/body cap;
- uniform credential errors.

### Integration/E2E

- fake versioned agent with fault injection;
- agent restart and stale telemetry;
- Node ready while motor fault proves no false AntennaReady;
- scheduled pass transitions NotScheduled → Acquiring → Locked → Lost;
- policy-capable CNI blocks unauthorized endpoint;
- HA leader change preserves correct condition semantics.

## Rollout

1. Add agent schema and parser package with fixtures.
2. Add new conditions without changing phase.
3. Add structured probe behind an opt-in feature flag.
4. Run shadow evaluation and compare against existing behavior.
5. Change `AntennaReady` and phase semantics.
6. deprecate generic endpoint and simulated true behavior.
7. add provider adapters for real hardware agents.

## References

- Current controller behavior:
  https://github.com/thc1006/ntn-operators/blob/main/internal/controller/groundstationlifecycle_controller.go
- Current API:
  https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/groundstationlifecycle_types.go
- Kubernetes API condition conventions:
  https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- Kubernetes probes:
  https://kubernetes.io/docs/concepts/configuration/liveness-readiness-startup-probes/
- CCSDS Cross Support Service Management work:
  https://ccsds.org/review/902-11-r-1/
- NASA DSN real-time ground-station status:
  https://eyes.jpl.nasa.gov/apps/dsn-now/dsn.html
- SatService ACU documentation:
  https://satservicegmbh.de/files/satnms/doc/acu2-rmu/index.html
- SignalRange ACU status model:
  https://docs.signalrange.space/equipment/antenna-control-unit/
- GISS antenna control and built-in self-test:
  https://giss-satcom.com/en/terminals/flyaway-class-terminals/acu-antenna-control-unit
