---
adr: 13
title: Transactional data-plane actuation for NTNSlice path changes
status: proposed
date: 2026-07-31
last_verified: 2026-07-31
deciders: [thc1006]
supersedes: []
superseded_by: []
implementation: []
tracking:
  - "https://github.com/thc1006/ntn-operators/issues/49"
  - "https://github.com/thc1006/ntn-operators/issues/69"
  - "https://github.com/thc1006/ntn-operators/issues/137"
  - "https://github.com/thc1006/ntn-operators/issues/141"
  - "https://github.com/thc1006/ntn-operators/issues/162"
---

# ADR 0013 — Transactional data-plane actuation for `NTNSlice` path changes

## Status

**Proposed**

This ADR defines the architecture and correctness contract for turning an
`NTNSlice` failover **recommendation** into a verified data-plane change.

No data-plane actuator, new CRD, privileged agent, PFCP integration, route
mutation, or session-continuity guarantee is implemented merely by accepting
this ADR.

## Decision summary

`ntn-operators` will separate:

1. **policy and decision**, which determine the recommended path; from
2. **data-plane actuation**, which performs, observes, verifies, and, where
   safe, rolls back an external network change.

The core `NTNSlice` reconciler will not directly mutate host routes, BGP,
UPF/PFCP state, CNI state, Open vSwitch, VPP, eBPF maps, or vendor equipment.

Instead, it will publish a durable, revisioned desired-state object named
`PathActuation`. A separate actuator controller, selected through an
administrator-owned `DataPlaneActuator`, will reconcile that desired state
against the actual network.

An `NTNSlice` path is not considered applied merely because the policy engine
selected it. `appliedPathType` and `PathActive=True` require positive,
generation-matched verification from the actuator.

The first reference implementation will be a narrowly scoped Linux routing
actuator using rtnetlink from a separate least-privileged agent. It is a
routing failover actuator only. It will not claim 3GPP PDU-session continuity,
UPF anchor relocation, GTP-U tunnel preservation, make-before-break, or
zero-packet-loss handover.

## Context

### Current repository behavior

The current `NTNSlice` controller has a strong policy-evaluation layer:

- trigger parsing and validation;
- stale-metric handling;
- fail-static behavior when telemetry is unreliable;
- satellite-window gating;
- hysteresis, confirmation samples, switchback delay, and minimum dwell;
- persisted transition timestamps;
- event-after-status-persist discipline; and
- leader-handoff-safe handling for the state that must be durable.

However, after evaluating the policy, the controller currently treats its
decision as if it were the externally observed result:

```go
previousPath := string(currentPath)
ns.Status.ActivePathType = string(result.TargetPath)
```

It then records failover accounting and sets `PathActive`, `QoSApplied`,
`Secured`, and `BillingActive` from the selected configuration. No external
data-plane operation is required to succeed before those status claims are
made.

Consequently, the current controller is a **TN–NTN path policy and decision
controller**, not yet an end-to-end data-plane orchestrator.

### Why this is an architecture decision

Adding a data-plane actuator changes all of the following:

- the meaning of `NTNSlice.status`;
- the trust boundary between a cluster controller and network infrastructure;
- the privilege model, including possible `CAP_NET_ADMIN` or vendor credentials;
- the side-effect and idempotency model;
- behavior across controller crashes and leader changes;
- rollback safety;
- reconciliation ownership;
- API versioning;
- provider conformance;
- observability and operational claims; and
- the meaning of `sessionContinuity`.

These choices are coupled. Implementing an actuator as an ordinary helper
function would leave the project without a coherent answer to partial failure,
duplicate application, stale status, conflicting writers, or rollback.

### 3GPP responsibility boundary

3GPP session and anchor continuity is not equivalent to changing a Linux
default route.

In 5GS:

- SSC mode and PDU Session Anchor behavior are controlled by the 5G Core,
  particularly the SMF;
- make-before-break behavior requires establishment of the new path or anchor
  before release of the old one;
- UPF control uses PFCP/N4 procedures between the control-plane function and
  user-plane function; and
- GTP-U carries user-plane traffic on interfaces such as N3 and N9.

Therefore, `ntn-operators` must not claim session continuity merely because an
edge gateway route changed. The core operator must also not impersonate the
SMF by speaking raw PFCP directly unless a future, separate architecture
decision establishes ownership, interoperability, and safety for that role.

### Existing issues do not replace this ADR

Issue #69 correctly records that `sessionContinuity` is present in the API but
is not acted upon. Issue #49 concerns multiple satellite paths. Issue #141
proposes an inter-satellite-link abstraction. Issue #162 concerns broader
fleet orchestration.

Those issues identify capabilities or dependencies. None defines the
transaction, authority, status semantics, crash recovery, verification, or
rollback contract for changing the data plane.

An inter-satellite link can be one prerequisite for a regenerative-payload
design, but it is not by itself proof of PDU-session continuity or an
implementation of a 5GC path switch.

## Goals

This ADR establishes a design that:

1. keeps policy evaluation separate from privileged external mutation;
2. represents actuation as durable desired state, not a fire-and-forget call;
3. distinguishes recommended, programmed, verified, and active state;
4. is safe under at-least-once reconciliation;
5. survives process crashes and leader changes;
6. supports explicit capability discovery;
7. fails closed when a required capability is unavailable;
8. supports bounded, evidence-based rollback;
9. permits multiple actuator implementations without putting vendor logic in
   the core controller;
10. limits privileges and blast radius;
11. provides a reusable conformance and fault-injection suite; and
12. prevents documentation and status from overstating session continuity.

## Non-goals

This ADR does not:

- implement 5GC PDU-session continuity;
- define a raw PFCP client in `ntn-operators`;
- turn the core manager Pod into a router;
- replace a CNI;
- define a general SDN controller;
- guarantee zero packet loss;
- guarantee make-before-break;
- provide multi-path aggregation or load sharing;
- define satellite-to-satellite mobility;
- treat OCUDU `remote_control` configuration as proof that traffic moved;
- define a universal vendor API;
- make `sessionContinuity=true` an enforceable promise in `v1alpha1`; or
- authorize arbitrary host-network changes from tenant-authored fields.

## Terminology

### Recommendation

The path selected by the policy engine from telemetry, orbital availability,
and anti-flap state.

### Desired path

The path requested in a `PathActuation` transaction.

### Programmed path

The path for which the actuator has observed that its managed configuration is
present in the target system.

### Verified path

The path for which independent or path-specific verification has succeeded.

### Applied path

The latest verified path for the current actuation revision.

### Active traffic path

The path that real user traffic is taking. An actuator may only equate this
with the applied path when its verification contract is strong enough to
observe that fact.

### Actuator

A controller or agent that owns one class of external data-plane side effect
and reports observed state through `PathActuation.status`.

## Decision

## 1. Keep the core controller policy-only

The `NTNSlice` reconciler remains responsible for:

- reading policy inputs;
- deciding whether to stay, fail over, or switch back;
- selecting the recommended target;
- enforcing anti-flap policy;
- ensuring the satellite opportunity signal is sufficiently fresh;
- publishing a new desired actuation revision; and
- consuming actuator status.

It must not directly:

- call `ip route`;
- write rtnetlink;
- write BGP configuration;
- mutate a UPF;
- invoke PFCP;
- write eBPF maps;
- configure VPP or Open vSwitch;
- enter another Pod's network namespace; or
- store vendor credentials in `NTNSlice`.

This separation keeps the manager's privilege set independent of every
possible data-plane backend.

## 2. Introduce `DataPlaneActuator`

`DataPlaneActuator` is an administrator-authored, namespaced configuration
object that selects one actuator implementation and its typed parameters.

Illustrative API:

```yaml
apiVersion: ntn.operators.dev/v1alpha2
kind: DataPlaneActuator
metadata:
  name: edge-linux-routing
  namespace: ran-edge
spec:
  controllerName: routing.linux.ntn.operators.dev
  parametersRef:
    apiGroup: routing.ntn.operators.dev
    kind: LinuxRoutingParameters
    name: edge-gateway-a
status:
  capabilities:
    - RouteProgramming
    - TrafficVerification
    - Rollback
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      observedGeneration: 3
    - type: ResolvedRefs
      status: "True"
      reason: ResolvedRefs
      observedGeneration: 3
    - type: Ready
      status: "True"
      reason: AgentReady
      observedGeneration: 3
```

### Required fields

`spec.controllerName`

- MUST be a domain-prefixed implementation identifier.
- MUST be immutable after the actuator has dependent transactions, unless a
  migration mechanism is explicitly implemented.
- Is analogous to a controller selection key, not an executable name supplied
  by a tenant.

`spec.parametersRef`

- MUST reference a typed, schema-validated parameter object.
- MUST be same-namespace in the first implementation.
- MUST NOT reference a Secret directly as an unstructured parameter bag.
- A ConfigMap parameter object MAY be supported only for prototypes; it is not
  the production default because it lacks a purpose-built schema and safe
  evolution contract.

### Standard status conditions

`Accepted`

The implementation recognizes the class and accepts its configuration.

`ResolvedRefs`

Every referenced parameter and credential object has been resolved and
authorized.

`Ready`

The implementation and its agent are currently capable of accepting
transactions.

All conditions MUST set `observedGeneration`.

### Capability advertisement

The actuator reports a set of stable capability names. Initial names are:

- `RouteProgramming`
- `TrafficVerification`
- `AtomicSwitch`
- `MakeBeforeBreak`
- `Rollback`
- `SessionContinuity`
- `MultiPath`
- `PerFlowSteering`

Capabilities are positive claims. Absence means unsupported or unproven.

The controller MUST NOT infer `SessionContinuity` from
`RouteProgramming`, `TrafficVerification`, an ISL, a successful RAN
configuration push, or the mere presence of a UPF.

## 3. Introduce `PathActuation`

`PathActuation` is a durable desired-state and transaction-ledger object. The
core controller owns one deterministic child per `NTNSlice`.

It is long-lived and revisioned. It is not a one-shot Job and not a series of
unbounded event objects.

Illustrative API:

```yaml
apiVersion: ntn.operators.dev/v1alpha2
kind: PathActuation
metadata:
  name: enterprise-a
  namespace: ran-edge
  ownerReferences:
    - apiVersion: ntn.operators.dev/v1alpha2
      kind: NTNSlice
      name: enterprise-a
      uid: 3a6c...
spec:
  sliceRef:
    name: enterprise-a
    uid: 3a6c...
  actuatorRef:
    name: edge-linux-routing
  revision: 12
  desiredPath: satellite
  transition:
    mode: breakBeforeMake
    continuity: bestEffort
    timeout: 30s
  verification:
    mode: activeProbe
    timeout: 5s
    successThreshold: 2
  rollback:
    policy: whenSafe
status:
  observedRevision: 12
  phase: Succeeded
  previousPath: terrestrial
  programmedPath: satellite
  appliedPath: satellite
  startedAt: "2026-07-31T11:20:00Z"
  completedAt: "2026-07-31T11:20:03Z"
  evidence:
    method: activeProbe
    observedAt: "2026-07-31T11:20:03Z"
    summary: "2 consecutive probes succeeded through managed satellite route"
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      observedGeneration: 12
    - type: Programmed
      status: "True"
      reason: DesiredStateObserved
      observedGeneration: 12
    - type: Ready
      status: "True"
      reason: VerificationSucceeded
      observedGeneration: 12
```

### Required spec fields

`sliceRef`

- MUST include name and UID.
- UID prevents a same-name delete/recreate from inheriting a prior
  transaction.

`actuatorRef`

- Selects an administrator-approved `DataPlaneActuator`.
- A tenant MUST NOT be able to select an arbitrary network endpoint by placing
  it directly in `NTNSlice`.

`revision`

- MUST increase monotonically for each desired transition.
- The actuator MUST process one committed revision at a time.
- A spec update MUST NOT overwrite the evidence for an in-progress revision
  before the prior result is made durable.

`desiredPath`

- Initial values are `terrestrial` and `satellite`.
- `unavailable` is a reported observation, not a routable desired target.

`transition.mode`

Initial values:

- `breakBeforeMake`
- `makeBeforeBreak`

`makeBeforeBreak` MUST be rejected when the selected actuator does not
advertise that capability.

`transition.continuity`

Initial values:

- `disabled`
- `bestEffort`
- `required`

`required` MUST be rejected or held without mutation when the selected
actuator lacks `SessionContinuity`.

`transition.timeout`

- Bounds one revision.
- Expiry does not prove that the external mutation did not occur.
- On timeout, the actuator MUST observe the target before deciding whether to
  retry, fail, or roll back.

`verification`

- Defines the evidence contract.
- An actuator MUST not report `Ready=True` solely because an API call returned
  success.

`rollback.policy`

Initial values:

- `never`
- `whenSafe`
- `required`

`required` MUST be rejected if the actuator cannot provide rollback.

## 4. Use a transactional reconciliation state machine

The standard phases are:

```text
Pending
  -> Preparing
  -> Applying
  -> Verifying
  -> Succeeded
```

Failure paths are:

```text
Preparing | Applying | Verifying
  -> Failed
  -> RollingBack
  -> RolledBack
```

When the actual state cannot be determined:

```text
Applying | Verifying
  -> Unknown
```

`Unknown` is deliberate. It prevents the controller from converting
uncertainty into a false success or an unsafe repeated mutation.

### Phase meaning

`Pending`

The revision exists but has not been accepted by the actuator.

`Preparing`

References, capabilities, target identity, current state, and preconditions are
being resolved.

`Applying`

The actuator is changing its owned data-plane state.

`Verifying`

The desired configuration has been observed and traffic-level or
implementation-specific checks are running.

`Succeeded`

The configured verification contract succeeded for the current revision.

`Failed`

The revision is known not to have succeeded and no rollback is in progress.

`RollingBack`

The actuator is attempting to restore a previously verified state.

`RolledBack`

The prior state has been restored and verified.

`Unknown`

The external effect may or may not have occurred, or the result cannot be
verified safely.

## 5. Design for at-least-once reconciliation

Kubernetes reconciliation is at least once. The following crash is expected:

1. the actuator changes the external data plane;
2. the actuator process crashes before writing status;
3. a new process receives the same revision.

Therefore every actuator MUST implement:

1. **observe before mutate**;
2. deterministic ownership markers where the backend supports them;
3. an idempotency identity derived from `PathActuation.UID` and `revision`;
4. **read after write**;
5. no-op success when the desired state is already present and verified;
6. conflict-safe status updates; and
7. recovery from the external-effect-before-status crash window.

Blindly repeating a non-idempotent command is non-conformant.

### Revision serialization

- Only one revision for one `PathActuation` may be in `Applying` at a time.
- A newer recommendation may supersede a pending revision.
- A newer revision MUST NOT interrupt an external commit at an undefined
  point.
- After the current revision reaches a terminal or safely observable state,
  the controller reconciles the newest desired revision.
- The status MUST retain the latest completed revision and outcome.

## 6. Separate recommendation from applied state

In `v1alpha2`, `NTNSliceStatus` will distinguish:

```yaml
status:
  recommendedPathType: satellite
  appliedPathType: terrestrial
  actuationRef:
    name: enterprise-a
    revision: 12
  lastDecisionTime: ...
  lastActuationTime: ...
  decisionCount: 7
  successfulActuationCount: 5
```

### `recommendedPathType`

The latest policy-engine output.

### `appliedPathType`

The path verified by the actuator for the current or latest successful
revision.

It MUST be absent or `unknown` when no qualified actuator has proved the
state.

### Existing `v1alpha1.activePathType`

Until the API migration is complete:

- documentation MUST state that it is a legacy **decision result**, not proof
  of data-plane application;
- dashboards MUST not use it as evidence that traffic switched;
- when actuation is enabled, `PathActive=True` MUST only be emitted after the
  matching `PathActuation` has `Ready=True`;
- conversion to `v1alpha2` maps the legacy value to
  `recommendedPathType`; and
- conversion MUST NOT fabricate `appliedPathType`.

## 7. Correct misleading “applied” conditions

The current API can report `QoSApplied`, `Secured`, and `BillingActive` from
selected fields without external confirmation.

In `v1alpha2`:

- policy selection and external application MUST be separate;
- selected configuration MAY be reported as `QoSSelected`,
  `SecuritySelected`, and `BillingSelected`;
- an `Applied=True` condition requires actuator or downstream-controller
  evidence;
- a driver that only changes routes MUST NOT report QoS, encryption, charging,
  UPF, or RAN configuration as applied; and
- messages MUST name the evidence source.

## 8. Replace the ambiguous continuity Boolean in `v1alpha2`

`v1alpha1.spec.failoverPolicy.sessionContinuity` is a Boolean with a default of
`true`, but the repository does not currently enforce it.

A Boolean cannot distinguish:

- no continuity requested;
- attempt continuity when available; and
- fail closed unless continuity is guaranteed.

`v1alpha2` replaces it with:

```yaml
failoverPolicy:
  continuity:
    mode: disabled | bestEffort | required
```

Conversion:

- `false` -> `disabled`
- `true` -> `bestEffort`

Legacy `true` MUST NOT map to `required`, because the old field did not provide
an enforced guarantee.

### Required mode

When `mode: required`:

- the actuator must advertise `SessionContinuity`;
- all required references must resolve;
- any prerequisite controller must be Ready;
- the transition must fail closed or hold the current verified path when those
  conditions are unmet; and
- the system must not silently downgrade to `bestEffort`.

### Linux routing actuator

The first Linux actuator advertises:

- `RouteProgramming`
- `TrafficVerification`
- `Rollback`, when its configured previous-state contract permits it

It does not advertise:

- `SessionContinuity`
- `MakeBeforeBreak`, unless explicitly implemented and proven
- `AtomicSwitch`
- `MultiPath`

## 9. Define evidence-based verification

An actuator reports `Programmed=True` after it has read back the desired
configuration from its managed backend.

It reports `Ready=True` only after the configured verification contract
succeeds.

Permitted verification examples include:

- route and rule readback from rtnetlink;
- an active probe bound to the managed routing mark, source, interface, or
  network namespace;
- a provider API returning a generation or transaction ID that is then
  observed in operational state;
- a 5GC/SMF API reporting the expected PDU-session path, when a future driver
  has a supported API and conformance contract; or
- backend telemetry that uniquely identifies the managed path.

Insufficient evidence includes:

- the write API returned HTTP 200;
- a ConfigMap was rendered;
- the target Pod exists;
- the gNB accepted an NTN configuration command;
- the satellite is overhead;
- the controller's own status write succeeded; or
- generic cluster reachability without path binding.

### Evidence freshness

Every success record MUST include:

- revision;
- observation timestamp;
- verification method;
- bounded summary;
- actuator/controller identity; and
- observed generation.

Evidence older than the configured freshness bound cannot prove current path
state.

## 10. Use safe rollback rules

Automatic rollback is allowed only when:

1. the previous path was positively verified;
2. the previous-path evidence is still fresh enough for rollback;
3. the actuator advertises rollback;
4. rollback will not violate an orbital or administrative hard constraint;
5. no newer revision has made the previous target unsafe; and
6. the rollback operation itself is observed and verified.

When verification is inconclusive, the actuator MUST NOT oscillate blindly
between paths. It reports `Unknown`, continues observation, and applies the
bounded retry policy.

### Satellite pass ending

When an active satellite opportunity ends, the policy layer recommends
terrestrial regardless of terrestrial recovery confidence. The actuator must
attempt the safe terrestrial transition according to its contract.

The system still cannot report terrestrial as applied until verification
succeeds.

### Rollback outcome

Rollback failure is not equivalent to the original path remaining active.
Status must represent the uncertainty explicitly.

## 11. Define deletion and finalizer behavior

`PathActuation.spec.cleanupPolicy` has initial values:

- `Retain`
- `RestoreTerrestrial`

`Retain`

Deletion does not mutate the external data plane.

`RestoreTerrestrial`

The actuator attempts to restore and verify the terrestrial baseline before
allowing deletion.

A finalizer MAY be used only when external cleanup is required.

Finalizer requirements:

- cleanup must be idempotent;
- cleanup must have a documented timeout and operator recovery procedure;
- a dead external system must not create an undocumented permanent deletion
  trap;
- force-removal consequences must be documented;
- no finalizer may be added merely to preserve metrics or internal cache; and
- deletion of `NTNSlice` must not cause an unreviewed traffic cut.

The default cleanup policy for the first release is `Retain`. Restoration is an
explicit operator decision because an automatic route mutation during object
deletion can be more dangerous than leaving the last verified state intact.

## 12. Enforce least privilege and ownership boundaries

### Core manager

The core manager:

- MUST NOT run privileged;
- MUST NOT receive `CAP_NET_ADMIN`;
- MUST NOT mount host network namespaces;
- MUST NOT hold vendor data-plane credentials merely because an
  `NTNSlice` exists; and
- may create/update the owned `PathActuation` child and read actuator status.

### Actuator controller and agent

Each actuator has:

- a dedicated ServiceAccount;
- only the CRD and Secret permissions it requires;
- no broad write access to `NTNSlice.spec`;
- no permission to change another actuator's transactions;
- scoped access to its target infrastructure;
- an independent leader-election identity if multiple replicas are supported;
- restricted network egress; and
- a hardened Pod security context appropriate to its backend.

### Parameters and credentials

- Secrets are referenced, never embedded in CR specs or status.
- Credential references are same-namespace in the first release.
- An actuator must apply the project's credential-reference authorization
  policy.
- Tenant authors cannot supply arbitrary executable commands.
- Tenant authors cannot choose host paths, network namespaces, table numbers,
  or rule-priority ranges unless an administrator-owned parameter policy
  explicitly delegates bounded values.
- Endpoint allow-lists and NetworkPolicy remain defense-in-depth.

## 13. First reference actuator: Linux policy routing

The first implementation is
`routing.linux.ntn.operators.dev`.

It exists to prove the transaction model with a real, measurable data-plane
change while keeping scope narrow.

### Deployment model

A separate agent runs on the gateway node or in the explicitly selected
network namespace that owns the TN and NTN egress paths.

The agent:

- uses rtnetlink directly;
- does not shell out to `ip`, `bash`, or arbitrary commands;
- has `CAP_NET_ADMIN` only where required;
- exposes a versioned internal API or watches only its assigned transactions;
- publishes a stable agent identity and readiness;
- never places `CAP_NET_ADMIN` in the core manager; and
- records backend observations needed for idempotency and verification.

### Managed scope

`LinuxRoutingParameters` defines an administrator-controlled scope such as:

```yaml
apiVersion: routing.ntn.operators.dev/v1alpha1
kind: LinuxRoutingParameters
metadata:
  name: edge-gateway-a
  namespace: ran-edge
spec:
  nodeSelector:
    kubernetes.io/hostname: edge-gw-a
  routingTable: 201
  rulePriorityRange:
    minimum: 12000
    maximum: 12099
  trafficSelector:
    fwMark: 0x4e544e
    mask: 0xffffffff
  paths:
    terrestrial:
      gateway: 192.0.2.1
      interface: eth0
    satellite:
      gateway: 198.51.100.1
      interface: sat0
  verification:
    target: 203.0.113.10
    protocol: icmp
```

Example addresses above are documentation addresses only.

The actuator MUST:

- manage only the configured table and bounded priority range;
- reject collisions with unmanaged rules;
- not alter the main table or system default route by default;
- not flush routes it does not own;
- validate interface and gateway identity;
- snapshot the previous managed state before mutation;
- read back routes and rules;
- run a path-bound verification probe;
- avoid proxy environment variables for probes;
- bound timeouts and response sizes; and
- report the exact managed scope without exposing sensitive network details in
  metrics.

Changing the main routing table or an unbounded set of rules requires a future
explicit design amendment and admission control.

### What the Linux actuator proves

It can prove:

- the intended policy rule and route exist;
- the prior managed entry was replaced or retained as designed;
- a probe tied to the intended path succeeds; and
- rollback restores the prior managed state when supported.

It cannot by itself prove:

- UE PDU-session continuity;
- GTP-U tunnel migration;
- UPF anchor continuity;
- RAN handover completion;
- zero packet loss;
- 5GC make-before-break; or
- that every application flow followed the new path.

## 14. Future actuator families

Future drivers may include:

### FRR/BGP

Use a supported northbound or management API, not configuration-file scraping
or shell commands. The driver must observe installed operational state, not
only accepted configuration.

### VPP, Open vSwitch, or eBPF

Each requires its own typed parameter CRD, privilege analysis, ownership
markers, verification contract, and conformance profile.

### 5GC/SMF provider

A future 5GC actuator integrates through a supported SMF or vendor northbound
API.

It must not:

- use raw PFCP merely because PFCP is specified by 3GPP;
- claim to be an SMF without owning SMF state and lifecycle;
- mutate UPF state outside the 5GC's authority model; or
- infer SSC mode completion from a route change.

A separate ADR is required before implementing this driver because its
failure, security, and interoperability model differs materially from Linux
routing.

### RAN provider

RAN configuration or cell activation can be one step in a larger transition,
but must not be treated as the user-plane commit point unless the driver has
traffic-level verification.

## 15. Provider conformance model

Every actuator implementation runs a common conformance suite.

Initial conformance profiles:

### Core profile

Requires:

- class acceptance;
- reference resolution;
- revision serialization;
- observe-before-mutate;
- crash recovery;
- generation-correct conditions;
- bounded retry;
- terminal status;
- deletion semantics; and
- no false success after write-only acknowledgement.

### Verified-route profile

Adds:

- `RouteProgramming`;
- route/rule readback;
- path-bound traffic verification; and
- rollback tests when advertised.

### Session-continuity profile

Reserved for future drivers.

It requires evidence that the relevant 5GC session transition completed under
the declared continuity mode. No current reference driver satisfies this
profile.

Capabilities and profiles are tested claims, not marketing labels.

## API and condition contract

## `PathActuation` conditions

### `Accepted`

`True`

The selected actuator recognizes the request and all requested modes are
supported.

`False`

The request is invalid or asks for an unsupported required capability.

### `ResolvedRefs`

`True`

Every required reference is resolved and authorized.

`False`

A reference is missing, invalid, unauthorized, or outside allowed scope.

### `Programmed`

`True`

The actuator observed the desired managed configuration for the current
revision.

`False`

The desired state is known not to be programmed.

`Unknown`

The backend cannot currently be observed.

### `Ready`

`True`

The verification contract succeeded for the current revision.

`False`

Verification failed conclusively.

`Unknown`

Verification is incomplete, stale, or inconclusive.

### `RolledBack`

Present when rollback was attempted.

It must never replace the original failure evidence.

## `NTNSlice` conditions

### `FailoverReady`

Continues to describe whether policy inputs are sufficient to make a safe
recommendation.

It does not prove the ability to execute a data-plane transition.

### `ActuationReady`

New condition describing whether the referenced actuator is accepted, ready,
and capability-compatible.

### `PathProgrammed`

Reflects matching `PathActuation.Programmed`.

### `PathActive`

`True` only when:

- the latest relevant `PathActuation` has `Ready=True`;
- `status.observedRevision` matches the desired revision;
- its `observedGeneration` matches;
- the evidence is within its freshness bound; and
- `appliedPath` equals the desired path.

`PathActive=False` is used for conclusive failure or unavailable state.

`PathActive=Unknown` is used for in-progress, stale, or indeterminate state.

### Condition messages

Messages must state:

- desired path;
- applied path, when known;
- revision;
- actuator name;
- phase;
- verification method; and
- concise failure reason.

Messages must not contain credentials, raw tokens, private keys, complete
provider response bodies, or unbounded external strings.

## Controller interaction

The intended flow is:

```text
metrics + policy + orbital state
             |
             v
      NTNSlice decision
             |
             v
recommendedPathType
             |
             v
PathActuation.spec.revision + desiredPath
             |
             v
DataPlaneActuator implementation
             |
       observe / apply
             |
          verify
             |
             v
PathActuation.status
             |
             v
NTNSlice.appliedPathType + conditions
```

### Reconcile rules

The core controller:

1. evaluates policy;
2. updates `recommendedPathType`;
3. compares the recommendation with the latest verified applied path;
4. creates or advances `PathActuation.revision` only when a transition is
   needed;
5. does not repeatedly increment the revision for the same unresolved
   recommendation;
6. waits for matching actuator status;
7. derives applied status only from matching evidence;
8. persists status before emitting transition events or counters; and
9. continues bounded observation while the transaction is in progress.

The actuator:

1. validates the requested capabilities;
2. resolves references;
3. observes current backend state;
4. records or recovers the prior managed state;
5. applies the desired change if needed;
6. reads back the programmed state;
7. verifies traffic according to the contract;
8. rolls back only when policy permits and safety prerequisites hold;
9. persists status; and
10. emits events only after the corresponding status transition is durable.

## Conflict and multi-writer rules

- One actuator implementation owns one `controllerName`.
- One `PathActuation` references exactly one actuator.
- The core controller is the sole writer of the transaction spec.
- The selected actuator is the sole writer of its status.
- External users must not patch actuator-owned status.
- Two actuator objects must not manage overlapping Linux table/rule ownership
  ranges.
- Admission validation or runtime conflict detection must reject overlapping
  managed scopes.
- A future orchestration layer may compose multiple steps, but it must use a
  separate workflow ADR rather than allowing multiple drivers to race on one
  transaction.

## Failure matrix

| Failure | Required behavior |
|---|---|
| Policy recommendation changes before apply starts | Supersede pending revision with newest desired state |
| Recommendation changes during apply | Finish or safely observe current commit, then reconcile newest revision |
| Actuator unavailable before mutation | Hold current verified path; `ActuationReady=False/Unknown` |
| Crash before mutation | Retry safely |
| Crash after mutation before status | Observe first; no blind duplicate |
| Backend accepts write but readback disagrees | `Programmed=False` or `Unknown`; do not report active |
| Readback succeeds but traffic probe fails | `Ready=False`; evaluate safe rollback |
| Verification times out | Observe; report `Unknown`; do not infer failure or success |
| Rollback succeeds | `RolledBack=True`; applied path is prior verified path |
| Rollback fails | Applied path may be `unknown`; surface both failures |
| Satellite opportunity ends during apply | Desired revision changes to terrestrial; finish/observe safely, then execute return |
| Required continuity unsupported | Reject before mutation |
| Credential/reference unauthorized | `ResolvedRefs=False`; no external call |
| Agent and API-server connectivity partition | Agent must not create new unaudited transitions; retain last committed managed state |
| NTNSlice deleted with `Retain` | Leave external state; remove transaction after ordinary GC |
| NTNSlice deleted with restore policy | Finalizer drives bounded restore and verification |

## Observability

### Metrics

Required low-cardinality metrics:

```text
ntn_path_actuation_requests_total{
  controller,
  from_path,
  to_path,
  outcome
}

ntn_path_actuation_duration_seconds{
  controller,
  from_path,
  to_path
}

ntn_path_actuation_inflight{
  controller
}

ntn_path_actuation_rollbacks_total{
  controller,
  outcome
}

ntn_path_verification_failures_total{
  controller,
  method,
  reason
}

ntn_data_plane_actuator_ready{
  namespace,
  actuator,
  controller
}
```

Do not put the following in metric labels:

- transaction UID;
- revision;
- endpoint;
- gateway address;
- Secret name;
- token identity;
- probe target;
- session ID;
- UE identity; or
- external error text.

Those details belong in bounded status, logs, or traces with appropriate
redaction.

### Events

Emit events only on durable transitions:

- `ActuationStarted`
- `ActuationSucceeded`
- `ActuationFailed`
- `ActuationUnknown`
- `RollbackStarted`
- `RollbackSucceeded`
- `RollbackFailed`

Steady-state reconciles do not emit events.

### Logs and traces

Structured logs include:

- namespace and resource name;
- revision;
- controller name;
- phase;
- desired and observed path;
- idempotency key hash, not raw credential material;
- operation duration; and
- classified error.

A trace may span decision, actuator application, verification, and rollback.
Tracing is optional; correctness cannot depend on a trace collector.

## Security analysis

### Threats

1. A tenant causes the operator to alter host routing outside its slice.
2. A compromised actuator changes the main routing table.
3. An actuator credential is sent to a tenant-selected endpoint.
4. A replayed revision re-applies a destructive operation.
5. Two actuators claim the same route or rule.
6. A status forger makes the core controller report false success.
7. A malicious parameter object injects commands or file paths.
8. An active probe becomes an SSRF primitive.
9. A finalizer blocks deletion indefinitely.
10. A privileged agent expands the core manager's blast radius.

### Controls

- administrator-owned actuator and parameter objects;
- typed schema and CEL validation;
- same-namespace references initially;
- least-privilege RBAC;
- separate ServiceAccounts;
- privilege isolation from the core manager;
- dedicated route table and bounded rule priority;
- no arbitrary command execution;
- endpoint and probe-target allow-lists;
- NetworkPolicy;
- no proxy use for controlled probes;
- revision and UID binding;
- status ownership and admission protection;
- read-before-write and read-after-write;
- conflict detection;
- bounded finalizer recovery;
- audit events; and
- conformance tests.

## Testing strategy

## Unit tests

### State machine

Cover every legal and illegal phase transition.

Properties:

- `Succeeded` requires matching revision and successful verification;
- `Ready=True` implies `Programmed=True`;
- `RolledBack=True` does not erase original failure;
- older status cannot satisfy a newer revision;
- `required` capability absence produces no mutation;
- unknown observation never becomes success;
- retries preserve idempotency identity; and
- one revision applies at a time.

### Conversion

- `v1alpha1.sessionContinuity=false` -> `disabled`;
- `v1alpha1.sessionContinuity=true` -> `bestEffort`;
- legacy `activePathType` -> `recommendedPathType`;
- `appliedPathType` remains unknown without evidence;
- round-trip preservation for all unrelated fields; and
- no default manufactures a stronger guarantee.

### Validation

- controller names are domain-prefixed;
- references are same-namespace;
- UIDs are required where identity matters;
- revision is positive;
- unsupported combinations are rejected;
- timeout and verification limits are bounded;
- route-table and priority ranges are bounded;
- overlapping Linux scopes are rejected; and
- credentials cannot be embedded.

## Generic actuator conformance suite

Every driver runs the same fake-backend scenarios:

1. desired state absent -> apply -> observe -> verify;
2. desired state already present -> no-op success;
3. crash after external mutation before status;
4. stale status from older revision;
5. superseding pending revision;
6. backend unavailable before mutation;
7. write accepted but readback missing;
8. verification failure;
9. safe rollback;
10. unsafe rollback refusal;
11. deletion with `Retain`;
12. deletion with restore policy;
13. unauthorized reference;
14. controller identity mismatch;
15. leader handoff during each phase; and
16. status conflict after external success.

## Envtest

Verify:

- `NTNSlice` creates exactly one deterministic child;
- owner reference and UID binding;
- revision does not churn on steady reconciles;
- recommendation and applied status remain distinct;
- matching child success advances `appliedPathType`;
- mismatched generation or revision is ignored;
- events occur after durable status; and
- deletion policy behavior.

## Linux network-namespace E2E

Create isolated network namespaces with:

- one source namespace;
- terrestrial and satellite egress paths;
- distinct gateways;
- a probe destination;
- rtnetlink observation; and
- packet capture or route-bound probe assertions.

Test:

- terrestrial baseline;
- failover to satellite;
- path-bound verification;
- switchback;
- route readback;
- unrelated-route preservation;
- main-table protection;
- rule collision;
- agent restart;
- crash after mutation;
- stale revision;
- rollback;
- verification timeout;
- loss of one gateway;
- API-server partition behavior; and
- two-actuator ownership conflict.

## HA and fault injection

Inject failure at every boundary:

```text
before observe
after observe
before write
after write
before readback
after readback
before verification
after verification
before status persist
after status persist
during rollback
```

Delete or kill the leader and verify the successor observes before mutating.

## Security tests

- core manager lacks `CAP_NET_ADMIN`;
- actuator cannot write `NTNSlice.spec`;
- tenant cannot create or edit administrator actuator parameters;
- cross-namespace credential reference denied;
- arbitrary command field does not exist;
- unmanaged route deletion denied;
- unapproved probe target denied;
- HTTP redirect and proxy use disabled where applicable;
- Secret contents never appear in status or events; and
- forced finalizer removal has documented, tested consequences.

## Mutation tests

Mutations must be caught for at least:

- removing revision comparison;
- setting Ready on write success without verification;
- skipping observe-before-mutate;
- accepting `required` without capability;
- mapping legacy continuity true to required;
- treating timeout as definite failure;
- allowing main-table mutation;
- omitting UID check;
- accepting stale evidence; and
- emitting success before status persistence.

## Rollout plan

Implementation is split into reviewable PRs.

### PR 1 — ADR and claim correction

- Add this ADR.
- Update API documentation to say current `activePathType` is a decision result.
- State explicitly that `sessionContinuity` is not enforced in `v1alpha1`.
- Update issue #69 to reference this architecture.
- Do not change behavior.

### PR 2 — API types and generated artifacts

- Add `DataPlaneActuator` and `PathActuation` types.
- Add `v1alpha2` status split and continuity enum.
- Generate CRDs, deep-copy code, bundle, Helm, Nephio copies, and API reference.
- Add conversion and validation tests.
- Do not mutate a real data plane.

### PR 3 — Fake actuator and conformance suite

- Implement an in-memory fake driver.
- Implement the generic state machine.
- Add fault-injection hooks.
- Prove revision and crash behavior.

### PR 4 — Observe-only core integration

- Core controller creates and updates the child transaction.
- Default mode is `ObserveOnly`.
- `recommendedPathType` is populated.
- No privileged external mutation.
- Dashboards expose recommendation vs application.

### PR 5 — Linux routing agent

- Add typed Linux parameters.
- Implement bounded rtnetlink ownership.
- Add active path-bound verification.
- Keep feature opt-in.

### PR 6 — E2E, HA, and security gates

- Network-namespace E2E.
- Crash-window tests.
- Leader-handoff tests.
- privilege and RBAC tests.
- mutation tests.
- release-note warnings.

### PR 7 — Opt-in enforcement mode

- Add administrator switch from `ObserveOnly` to `Enforce`.
- Require accepted and ready actuator.
- Require explicit cleanup and continuity policies.
- No change to default for existing installations.

### PR 8 — Production-readiness review

Before considering a default change:

- publish measured RTO and packet-loss behavior;
- run at least one real gateway deployment;
- exercise rollback;
- validate CNI and host-network interactions;
- validate upgrades and downgrades;
- complete security review;
- document unsupported continuity guarantees; and
- obtain explicit maintainer approval.

A future 5GC actuator is not part of these PRs.

## Compatibility and migration

### `v1alpha1`

- Remains served during migration.
- Current objects do not gain an actuator implicitly.
- Existing behavior remains decision-only unless the feature is explicitly
  enabled.
- Documentation is corrected immediately.

### `v1alpha2`

- Contains the recommendation/application split.
- Uses explicit continuity modes.
- Requires an explicit actuator reference for enforcement.
- Does not convert a legacy Boolean into a hard continuity requirement.
- Does not synthesize successful applied status.
- Reuses the shared `v1alpha2` scaffolding owned by ADR 0010 (#214); it is not a
  separate version rollout.

### Storage migration

ADR 0010 (tracked in #214) is the single owner of the `v1alpha2` scaffolding:
the version bump, the conversion webhook, and the storage-version migration.
This ADR does not introduce a second or parallel `v1alpha2` rollout; its
actuation surface (`PathActuation` and the new `NTNSlice` v1alpha2 fields) is an
additive consumer of that scaffolding. Because `PathActuation` is a `v1alpha2`
kind, actuation MUST NOT be enabled before `v1alpha2` is served. Serving and
storage-version changes follow ADR 0010; conversion does not itself rewrite
stored objects.

### Downgrade

Downgrading to a version without actuator CRDs must not:

- delete external routes automatically;
- reinterpret applied status as recommendation;
- orphan a required cleanup finalizer without a runbook; or
- remove CRDs before all actuator objects and cleanup obligations are handled.

## Alternatives considered

## A. Call an in-process `DataPlaneActuator` interface directly

Rejected.

It would:

- place backend privileges in the core manager;
- couple external latency to the `NTNSlice` reconcile;
- leave no durable transaction ledger;
- complicate crash recovery;
- make provider upgrades risky; and
- allow one bad backend to affect every controller.

A Go interface may still exist inside an actuator implementation, but it is
not the system boundary.

## B. Let an external agent watch `NTNSlice` directly

Rejected.

It would blur recommendation and command, require broad access to tenant
objects, and create multiple writers interpreting policy fields independently.

The agent watches or receives only its assigned `PathActuation`.

## C. Use a fire-and-forget Kubernetes Job per switch

Rejected.

Jobs are useful for bounded batch work, but they do not naturally provide:

- continuous observation;
- idempotent external-state reconciliation;
- revision supersession;
- stable ownership;
- long-lived capability status; or
- safe rollback.

They also risk unbounded transaction-object growth.

## D. Keep status-only switching

Accepted only as `ObserveOnly`.

It is useful for policy simulation and demonstrations, but must not be called
data-plane failover.

## E. Implement raw PFCP/N4 in the core operator

Rejected.

PFCP is part of the 5GC control-to-user-plane relationship. A generic
Kubernetes operator must not bypass the SMF's session and anchor authority.

A future provider may call a supported SMF northbound API under a separate ADR.

## F. Use OCUDU `remote_control` as the actuator

Rejected as a complete solution.

Runtime RAN configuration can be a transition step, but acceptance of a config
message does not prove user-plane traffic moved or sessions were preserved.

## G. Use FRR/BGP as the first actuator

Deferred.

It is a valid future backend, but Linux policy routing gives the project a
smaller privilege and conformance surface for proving the transaction model.
FRR integration should use supported northbound APIs and operational-state
readback.

## H. Start with eBPF, VPP, or Open vSwitch

Deferred.

Each is powerful but introduces a larger implementation, privilege, and
verification surface. The architecture supports them after the core contract
is proven.

## Consequences

### Positive

- Status becomes truthful.
- Privileged mutation is isolated.
- External side effects become auditable and recoverable.
- Provider capabilities are explicit.
- Required continuity fails closed instead of silently degrading.
- The project can add real actuators without turning the core controller into a
  vendor-specific network manager.
- Fault injection and conformance become first-class.
- Marketing and operational claims align with evidence.

### Negative

- Two new API resources increase maintenance cost.
- Every installation that wants enforcement needs an actuator deployment.
- Status and dashboards become more complex.
- A real transition can take longer because verification is required.
- Rollback cannot always be automatic.
- The Linux agent requires carefully scoped network privilege.
- `v1alpha2` migration is required to cleanly correct existing status and
  continuity semantics.

### Accepted risks

- `ObserveOnly` remains useful but provides no external failover.
- A path may remain `Unknown` during partitions.
- The first actuator does not solve 5GC session continuity.
- Some external systems cannot supply atomic or strongly verifiable
  operations.
- An operator may need manual recovery when neither forward verification nor
  rollback can establish a safe state.

## Operational claims after this ADR

Before a conformant actuator is enabled, documentation should say:

> `ntn-operators` evaluates TN–NTN failover policy and records the recommended
> path. It does not yet change the production data plane.

With the Linux actuator enabled and verified, documentation may say:

> `ntn-operators` can program and verify a bounded Linux routing failover for
> the configured traffic selector.

It must not say:

> active sessions are preserved

unless a driver conforming to the session-continuity profile proves that
claim.

## AI-assisted implementation discipline

AI-assisted changes must follow the same engineering controls as human-authored
changes.

1. Every PR states the invariant it changes.
2. Generated CRDs and copied manifests are regenerated, diffed, and tested;
   generated output is not accepted merely because a tool produced it.
3. Privilege changes are reviewed separately and never expanded as an
   incidental fix.
4. External commands, shell interpolation, and untyped parameter bags are
   prohibited.
5. Tests are derived from the failure matrix, not only from the happy path.
6. At least one mutation must prove each load-bearing safety test.
7. Spec claims are pinned to primary sources and, where applicable, exact
   upstream revisions.
8. AI-generated status text and logs are checked for credential leakage and
   unbounded external input.
9. A code reviewer must be able to explain the state machine without relying
   on generated prose.
10. No model-generated claim of session continuity is accepted without
    end-to-end evidence from the responsible 5GC components.

## Acceptance criteria

This ADR is implemented only when all of the following are true:

- recommendation and applied status are separate;
- a durable revisioned transaction exists;
- core manager has no data-plane privilege;
- a selected actuator owns status;
- observe-before-mutate is enforced;
- crash-after-effect-before-status is tested;
- verification is stronger than write acknowledgement;
- capability requirements fail closed;
- continuity semantics are explicit;
- Linux actuator changes only its bounded owned scope;
- rollback behavior is specified and tested;
- events follow durable status;
- HA and fault injection are green;
- security negative tests are green;
- API migration does not fabricate applied state; and
- documentation makes no unsupported session-continuity claim.

## References

### Repository evidence

- `api/v1alpha1/ntnslice_types.go`
- `internal/controller/ntnslice_controller.go`
- `pkg/slice/failover.go`
- issue #49 — multi-satellite paths
- issue #69 — `SessionContinuity` is not enforced
- issue #137 — provider-pattern roadmap
- issue #141 — proposed inter-satellite-link abstraction
- issue #162 — cluster-scope orchestration epic
- ADR 0005 — cluster-scope orchestration
- ADR 0008 — constellation-pool availability
- ADR 0010 — secure defaults and API-version migration

Repository state reviewed at commit:

```text
fb6264b857b10d55bda5c3005667f2afe4997f54
```

### Kubernetes and Gateway API

- Kubernetes Controllers:
  https://kubernetes.io/docs/concepts/architecture/controller/
- Kubernetes Custom Resources:
  https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/
- Kubernetes Finalizers:
  https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/
- Kubernetes API conventions and status:
  https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- Gateway API `GatewayClass`:
  https://gateway-api.sigs.k8s.io/api-types/gatewayclass/
- Gateway API conditions:
  https://gateway-api.sigs.k8s.io/geps/gep-1364/
- Gateway API conformance:
  https://gateway-api.sigs.k8s.io/concepts/conformance/

### Linux and routing

- Linux kernel rtnetlink route specification:
  https://docs.kernel.org/networking/netlink_spec/rt_route.html
- Linux kernel rtnetlink rule specification:
  https://docs.kernel.org/networking/netlink_spec/rt_rule.html
- FRRouting northbound architecture:
  https://docs.frrouting.org/projects/dev-guide/en/latest/northbound/architecture.html
- FRRouting northbound gRPC:
  https://docs.frrouting.org/projects/dev-guide/en/latest/grpc.html

### 3GPP / ETSI

- ETSI TS 123 501 / 3GPP TS 23.501 — System architecture for the 5G System:
  https://www.etsi.org/deliver/etsi_ts/123500_123599/123501/
- ETSI TS 123 502 / 3GPP TS 23.502 — Procedures for the 5G System:
  https://www.etsi.org/deliver/etsi_ts/123500_123599/123502/
- ETSI TS 129 244 / 3GPP TS 29.244 — Interface between control plane and user plane nodes:
  https://www.etsi.org/deliver/etsi_ts/129200_129299/129244/
- ETSI TS 129 281 / 3GPP TS 29.281 — GTP-U:
  https://www.etsi.org/deliver/etsi_ts/129200_129299/129281/

## What would change this decision

Revisit this ADR if:

- Kubernetes introduces a standard transactional network-actuation API that
  meets the revision, verification, and rollback requirements;
- a target 5GC exposes a stable, supported, vendor-neutral northbound API with
  authoritative session-path status;
- a single actuator cannot safely own the required multi-step transition;
- real deployment evidence shows that the long-lived transaction object is
  operationally inferior to another durable pattern; or
- the project narrows permanently to policy recommendation only and explicitly
  abandons data-plane actuation.
