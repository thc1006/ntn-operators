# NOC Runbook — NTN Operators alerts

Operational response for the alerts shipped in the chart's `PrometheusRule`
(`dist/chart/templates/monitoring/prometheusrule.yaml`, rendered when
`--set prometheus.enable=true`). One section per alert:
**Fires when → Impact → Diagnose → Mitigate → Escalate**.

All alerts are `severity: warning`. Most are namespaced — the labels
(`namespace`, and one of `ephemeris` / `config` / `slice`) point at the exact CR.
Two are not: `NTNControllerReconcileErrors` aggregates `sum by (controller)` over
`controller_runtime_reconcile_errors_total` (a metric with no `namespace` label),
so it carries only `controller`; `NTNEphemerisMapperIndexErrors` is a single
unlabeled counter (a process-level cache/index fault — the namespace/ephemeris is
in the log, not a series). Assume
`NS=<namespace>` and the operator lives in `ntn-operators-system` unless you
deploy it elsewhere; the operator Deployment is `<release>-controller-manager`
(the commands below assume a release named `ntn-operators`).

First moves for any alert:

```bash
# Which CR, and what does it say about itself?
kubectl describe <kind> <name> -n "$NS"     # conditions + Events are the fastest signal
# -l is release-independent (deploy/<release>-controller-manager works too).
# --tail=-1 overrides the selector default of 10 lines; --prefix tags each pod:
kubectl logs -l control-plane=controller-manager -n ntn-operators-system --tail=-1 --prefix=true --since=30m | grep -iE 'error|warn'
```

Metric → alert map:

| Alert | Expr | For |
|---|---|---|
| `NTNEphemerisEpochStale` | `ntn_operators_ephemeris_epoch_stale_count > 0` | 15m |
| `NTNEphemerisPushFailing` | `increase(ntn_operators_ephemeris_push_errors_total[15m]) > 0` | 15m |
| `NTNEphemerisPushNotReady` | `ntn_operators_ephemeris_push_ready == 0` | 15m |
| `NTNSliceFailoverFlapping` | `increase(ntn_operators_failover_total[15m]) > 4` | 5m |
| `NTNConfigApplyErrors` | `increase(ntn_operators_config_apply_errors_total[15m]) > 0` | 15m |
| `NTNConfigApplyNotReady` | `ntn_operators_config_apply_ready == 0` | 15m |
| `NTNNoSatellitesTracked` | `ntn_operators_gp_satellite_count == 0` | 30m |
| `NTNGPFetchNotReady` | `ntn_operators_gp_fetch_ready == 0` | 30m |
| `NTNDeepSpaceElementsRejected` | `ntn_operators_gp_deep_space_rejected_count > 0` | 15m |
| `NTNControllerReconcileErrors` | `sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m])) > 0.1` | 15m |
| `NTNEphemerisMapperIndexErrors` | `increase(ntn_operators_ephemeris_mapper_index_error_total[15m]) > 0` | 15m |

---

## NTNEphemerisEpochStale

**Fires when** a tracked element set's epoch is older than the freshness bound
while it is still being propagated. Labels: `namespace`, `ephemeris`.

**Impact.** SGP4 propagation from a stale epoch drifts. The ECEF/orbital state
this operator pushes into SIB19 (and to the gNB via the runtime push) is
therefore increasingly wrong, so UE timing-advance and Doppler pre-compensation
degrade — the practical symptom is falling NTN throughput and, eventually,
dropped sync.

**Diagnose.**

```bash
kubectl describe satelliteephemeris <ephemeris> -n "$NS"
# Conditions to read:
#   GPDataFetched=False  → the fetch itself is failing (reason: RateLimited / AuthFailed /
#                          FetchFailed / FetcherSetupFailed / InsecureURL — full set under NTNGPFetchNotReady)
#   EphemerisEpochStale=True with the offending NORAD IDs in the message
#   GPDataParsed / PassesPredicted → whether anything downstream still ran
```

```promql
# Which ephemerides are stale, and how many element sets each.
ntn_operators_ephemeris_epoch_stale_count > 0
# Are fetches erroring (the usual upstream cause)?
rate(ntn_operators_gp_fetch_duration_seconds_count{status="error"}[15m])
```

**Mitigate.**
- `GPDataFetched=False, reason=RateLimited` — CelesTrak returns HTTP 403 when you
  exceed its policy; the operator already backs off. Do **not** hand-retry;
  confirm nothing else is fetching the same URL faster than the data updates
  (~a few times/day).
- `reason=AuthFailed` — Space-Track credentials are bad or the account is
  throttled/suspended. Fix the secret; respect Space-Track's <30/min, <300/hr.
- Source healthy but epoch still stale — `spec.source.refreshInterval` is longer
  than the constellation's element cadence. Lower it.
- If `networkPolicy.enable=true`, confirm egress `443/TCP` reaches
  CelesTrak/Space-Track (it is allowed by default).

**Escalate** to the constellation/ephemeris data owner; for Space-Track, the
account owner (suspension risk).

---

## NTNEphemerisPushFailing

**Fires when** the runtime NTN ephemeris push to the gNB fails. Labels:
`namespace`, `config`, `reason`. This is the v0.6 WebSocket path
(`ntn_config_update`); `NTNConfigApplyErrors` (static ConfigMap apply) does
**not** cover it.

**Impact.** The gNB's SIB19 stops being refreshed with live orbital state, so
the RAN keeps broadcasting the last-good (aging) ephemeris. UEs lose NTN
timing/Doppler sync → degraded throughput or failed attach.

**Diagnose.** The `reason` label is the triage key:

```promql
sum by (namespace, config, reason) (increase(ntn_operators_ephemeris_push_errors_total[15m]))
```

| `reason` | Meaning | Class |
|---|---|---|
| `ProviderPushFailed` | dial/write/read to the gNB failed | transient (operator requeues) |
| `ProviderPushRejected` | gNB replied `{"error":...}` — bad config | permanent, clears on spec/ephemeris change |
| `RemoteEndpointRejected` | the WebSocket handshake was refused (redirect, 401/403, 429, 4xx) | self-heals on a 5m poll — the fix is outside the cluster |
| `EphemerisRefNotFound` / `EphemerisGetFailed` | the referenced SatelliteEphemeris is missing/unreadable | fix the ref, not the gNB |
| `EphemerisPayloadMissing` | no propagated state to push yet | upstream ephemeris problem |
| `EphemerisStale` | the propagated state is stale/expired | see **NTNEphemerisEpochStale** |

```bash
# Run in a subshell with strict mode so a failed kubectl (RBAC, a missing/renamed object, an API outage)
# STOPS the procedure instead of probing an empty target — bash -n cannot catch that, and the exit
# status of a plain sequence would otherwise be the last command's. The subshell keeps set -e out of
# your interactive session.
(
set -euo pipefail
: "${NS:?set NS to the NTNCellConfig namespace}"
: "${CONFIG:?set CONFIG to the NTNCellConfig name}"
OPERATOR_NS="${OPERATOR_NS:-ntn-operators-system}"   # override if you installed elsewhere

kubectl describe ntncellconfig "$CONFIG" -n "$NS"
#   Condition EphemerisPushed (Status/Reason/Message) + Event "EphemerisPushFailed"

# For ProviderPushFailed, test gNB reachability AS THE ACTIVE OPERATOR SEES IT. The push runs on the
# elected LEADER, so probe that pod — its node's CNI/routing/egress is what matters, not an arbitrary
# replica's. The leader is in the leader-election Lease (holderIdentity is "<pod>_<uuid>").
LEADER=$(kubectl get lease b1076767.operators.dev -n "$OPERATOR_NS" -o jsonpath='{.spec.holderIdentity}')
# During a graceful handoff (LeaderElectionReleaseOnCancel) the Lease briefly has NO holder; fail fast
# rather than probing an empty pod name — wait for the ~2s handoff and retry.
: "${LEADER:?leader Lease currently has no holder — wait for the ~2s handoff and retry}"
POD=${LEADER%%_*}
kubectl get pod "$POD" -n "$OPERATOR_NS" -o wide   # confirm the pod/node you are about to probe

ENDPOINT=$(kubectl get ntncellconfig "$CONFIG" -n "$NS" -o jsonpath='{.spec.provider.remoteControl.endpoint}')
: "${ENDPOINT:?spec.provider.remoteControl.endpoint is empty for $CONFIG}"
HOST=${ENDPOINT%:*}; HOST=${HOST#[}; HOST=${HOST%]}   # strip [ ] so [IPv6]:port works with nc
PORT=${ENDPOINT##*:}

# Re-read the leader just before probing: if it changed (a handoff between the two reads) we would be
# probing a pod that is no longer the active operator, so abort and retry. Assign first, THEN compare:
# `[ "$(kubectl ...)" = "$LEADER" ]` takes its exit status from `[`, not from kubectl, so a kubectl that
# failed AFTER emitting the holder would slip past set -e; a plain assignment carries kubectl's non-zero
# exit for set -e to act on.
CURRENT_LEADER=$(kubectl get lease b1076767.operators.dev -n "$OPERATOR_NS" -o jsonpath='{.spec.holderIdentity}')
[[ "$CURRENT_LEADER" == "$LEADER" ]] \
  || { echo "leader changed while preparing the probe; retry the procedure" >&2; exit 1; }

# Emit cleanup for THIS pod now, names baked in (%q-escaped), from INSIDE the subshell: $POD/$OPERATOR_NS
# exist only here — a subshell is a copy of the parent, so its vars never propagate out; a cleanup line
# placed after `)` would run in the parent with them UNSET, or worse with a stale $POD from the parent's
# history → deleting an unrelated pod. Printed BEFORE the probe because a failing `nc` is a normal
# diagnostic result that, under set -e, would skip anything after it. The ephemeral container stays listed
# (Terminated) until the pod is recreated — harmless, normally left as-is; to clear it, on the default
# 2-replica Helm install confirm BOTH replicas are Ready then delete the pod (deleting the leader triggers
# a ~2s lease handoff — a handoff timing, NOT an end-to-end SLA); on a single-replica / raw-kustomize
# install deleting the pod is a brief operator outage, so prefer leaving the terminated entry.
printf '\n# Optional cleanup for THIS debug pod (uncomment a line to run):\n'
printf '#   kubectl get pod -n %q -l control-plane=controller-manager -o wide   # both Ready first\n' "$OPERATOR_NS"
printf '#   kubectl delete pod -n %q %q\n' "$OPERATOR_NS" "$POD"

# Attach an ephemeral debug container to the leader pod: it shares the pod's network namespace, so it
# is governed by the operator's NetworkPolicy — a real test of the operator's egress. (Do NOT
# `kubectl run` a pod with the operator's labels: the ReplicaSet would adopt and delete it.) The image
# is digest-pinned — never :latest, since it shares the operator pod's namespace.
#
# Use --profile=restricted for a deterministic, hardened debug container (drop ALL capabilities,
# runAsNonRoot, RuntimeDefault seccomp). Without an explicit profile the kubectl default differs by
# client version (legacy through v1.35; general from v1.36, which adds an unneeded SYS_PTRACE), so the
# effective security context would otherwise depend on the operator's kubectl. The ephemeral container
# INHERITS the pod's securityContext: on the default Helm install the pod sets runAsUser 65532, so it
# runs as uid 65532 and `nc -vz` works under restricted (verified on Kind, node v1.32.2, kubectl v1.35.4).
# On the raw kustomize install (config/manager: runAsNonRoot with NO numeric user) netshoot fails with
# CreateContainerConfigError — its image declares no numeric non-root user (nicolaka/netshoot#163);
# there, use a debug image that declares a numeric non-root USER (or --custom a runAsUser).
kubectl debug -n "$OPERATOR_NS" -it "$POD" --profile=restricted \
  --image=nicolaka/netshoot:v0.16@sha256:b09d9b21381f47a79b3cbcb30da25266dc17186ea00ae65e99fdc51396f48e70 \
  -- nc -vz "$HOST" "$PORT"
)
```

**Mitigate.**
- `ProviderPushFailed` — restore the gNB `remote_control` server / fix the
  `endpoint`. **If `networkPolicy.enable=true`, confirm the gNB port is in
  `networkPolicy.gnbPorts`** — a missing port silently blackholes the push (this
  is the exact failure the I-27 egress config prevents).
- `ProviderPushRejected` — read the gNB log for the rejection; correct the
  NTNCellConfig spec (commonly `siWindowPosition`, or an ephemeris field).
- `RemoteEndpointRejected` — the endpoint answered the handshake with a non-101.
  Read the HTTP status in the condition message: 401/403 means the
  `remoteControl.tls` credential no longer matches the gNB (rotate the Secret —
  the operator picks it up on the next poll, no CR edit needed); 429 means a
  proxy is rate-limiting; a redirect or 404/405 means a proxy in front of the gNB
  is not passing `Upgrade` through. The cell retries every 5m on its own.
- `EphemerisRefNotFound` / `PayloadMissing` — fix `spec.cellID` and the
  referenced SatelliteEphemeris; then wait for a refresh to re-trigger.

**Escalate** to the RAN/gNB operator (reachability/rejection); the ephemeris
owner (stale/missing state).

---

## NTNEphemerisPushNotReady

**Expr:** `ntn_operators_ephemeris_push_ready == 0` for 15m.

The runtime NTN ephemeris push to the gNB has been failing continuously for 15m.
This is the durable companion to **NTNEphemerisPushFailing**: that alert uses
`increase(...errors_total[15m])`, which only sustains while a *transient* failure
tight-requeues each minute. A **permanent** reason that does not requeue
(`EphemerisRefNotFound`, `EphemerisPayloadMissing`, `EphemerisStale`,
`ProviderPushRejected`) increments the counter once, so this gauge — held at 0 for
the whole outage — is what fires for it.

**Triage:** read the `EphemerisPushed=False` condition on the `NTNCellConfig` for
the reason (`kubectl get ntncellconfig <config> -n "$NS" -o jsonpath='{.status.conditions}'`),
then follow the same reason table as **NTNEphemerisPushFailing**. A gauge stuck at
0 with no counter movement points at a permanent (config/state) cause, not a
transient reachability blip.

---

## NTNSliceFailoverFlapping

**Fires when** more than 4 path switches occur in 15m. Labels: `namespace`,
`slice`, `from_path`, `to_path`.

**Impact.** The slice is oscillating between terrestrial and satellite paths.
Each switch disrupts in-flight sessions (reordering, loss, re-auth); sustained
flapping degrades the very service the failover exists to protect.

**Diagnose.**

```promql
sum by (namespace, slice, from_path, to_path) (increase(ntn_operators_failover_total[15m]))
```

```bash
kubectl describe ntnslice <slice> -n "$NS"
#   status.activePathType, status.lastFailover, status.failoverCount
#   Conditions: PathActive, FailoverReady, MetricsStale
```

Flapping is almost always a metric hovering at the trigger threshold or a
degraded metric source:

```promql
# Is the reader falling back to stale values / erroring for this slice?
ntn_operators_reader_stale_value_used_total{namespace="<ns>", name="<slice>"}
sum by (reason) (increase(ntn_operators_reader_errors_total[15m]))
# Is the satellite pass itself marginal (1↔0 near a pass edge)?
ntn_operators_satellite_pass_available{namespace="<ns>"}
```

**Mitigate.**
- `MetricsStale=True` — the reader can't reach Prometheus. Fix
  `spec.metricsSource.prometheus.endpoint`, the query, or — **if
  `networkPolicy.enable=true` — add the Prometheus port to
  `networkPolicy.prometheusPorts`** (I-27).
- Genuine threshold-hugging — widen the recovery hysteresis / dwell in the
  slice's `failoverPolicy` so a metric sitting on the boundary doesn't
  retrigger. (Stronger N-of-M / dead-band / min-dwell anti-flap is tracked
  separately; until then, a wider margin is the lever.)

**Escalate** to network engineering (path policy) or the monitoring team (if the
metric source is the culprit).

---

## NTNConfigApplyErrors

**Fires when** `increase(ntn_operators_config_apply_errors_total[15m]) > 0`.
Labels: `namespace`, `config`, `provider`. This is the **static** ConfigMap
provider apply — distinct from the runtime push (`NTNEphemerisPushFailing`).

**Impact.** The gNB's bootstrap ConfigMap is not being updated, so a config
change (koffset, SIB schedule, ephemeris) never reaches the gNB's next boot.

**Diagnose.** `kubectl describe ntncellconfig <config> -n "$NS"` — conditions
`ConfigValid` (spec rejected at generation time) and `ConfigApplied`, plus the
`ApplyFailed` / `StatusCheckFailed` Events. **Mitigate** by correcting the spec
(a `ConfigValid=False` message names the field) or the provider target.
**Escalate** to whoever owns the NTNCellConfig spec.

---

## NTNConfigApplyNotReady

**Fires when** `ntn_operators_config_apply_ready == 0` for 15m. Labels:
`namespace`, `config`. This is the durable companion to **NTNConfigApplyErrors**,
and it covers strictly more: `ntn_operators_config_apply_errors_total` is
incremented **only** by an `ApplyCellConfig` failure, so three other ways the
apply can be broken increment it **zero** times and return a nil error — meaning
neither the rate alert nor `controller_runtime_reconcile_errors_total` sees them:

| `ConfigApplied` reason | What it means | Requeues? |
|---|---|---|
| `InternalError` | the provider registry is not configured (operator wiring bug) | 1m |
| `UnsupportedProvider` | `spec.provider.type` is not registered — **no requeue at all**, so a rate alert could never sustain on it | no |
| `StatusCheckFailed` | the write may have landed but the post-apply read could not verify it | 1m |

**Impact.** The gNB's bootstrap ConfigMap is not known to reflect the CR. Under
`UnsupportedProvider` nothing will retry until the spec is edited or the operator
restarts.

**Diagnose.** Read the reason off the condition — that is what distinguishes the
three cases:

```bash
kubectl get ntncellconfig "$CONFIG" -n "$NS" \
  -o jsonpath='{range .status.conditions[?(@.type=="ConfigApplied")]}{.status}{" "}{.reason}{" "}{.message}{"\n"}{end}'
```

**Mitigate.** `InternalError` — an operator-side wiring fault; check the
controller logs and restart. `UnsupportedProvider` — set `spec.provider.type` to a
registered provider (`ocudu`); the spec edit bumps the generation and re-triggers
the reconcile immediately. `StatusCheckFailed` — check the ConfigMap named by
`status.configMapRef` still exists and carries `geo_ntn.yml`; the next reconcile
re-applies it, so a persistent one points at an API-access problem.

**Escalate** to the operator owner for `InternalError`, to whoever owns the
NTNCellConfig spec for `UnsupportedProvider`.

---

## NTNNoSatellitesTracked

**Fires when** `ntn_operators_gp_satellite_count == 0` for 30m — the GP pipeline
is dark. **Impact:** no passes are predicted, so every consuming NTNSlice sees
its satellite path as unavailable. **Diagnose:** SatelliteEphemeris
`GPDataFetched` / `GPDataParsed`, the source URL, and `spec.passPrediction`'s
NORAD filter (a filter matching nothing yields zero). **Mitigate:** fix the
source or widen the filter. **Escalate** to the ephemeris data owner.

---

## NTNGPFetchNotReady

**Fires when** `ntn_operators_gp_fetch_ready == 0` for 30m — no usable element set
(fresh fetch, 304, or served cache) has been obtained. **Why this and not
NTNNoSatellitesTracked:** `gp_satellite_count == 0` cannot fire on a cold start
that has *never* fetched successfully — the counter series is absent and PromQL
`== 0` never matches an absent series. `gp_fetch_ready` is set to 0 on the first
failed reconcile, so it catches a pipeline that never came up (bad credentials
Secret, DNS/egress blocked, an insecure `http://` source refused, or the source
persistently erroring with no cache). **Diagnose:** the SatelliteEphemeris
`GPDataFetched` condition reason (`FetcherSetupFailed`, `InsecureURL`,
`FetchFailed`, `AuthFailed`, `RateLimited`), then network egress / credentials /
source URL. **Escalate** to the ephemeris data owner or the platform (egress).

---

## NTNDeepSpaceElementsRejected

**Fires when** `ntn_operators_gp_deep_space_rejected_count > 0` — MEO/GEO element
sets (period ≥ 225 min) are being dropped by the LEO-only guard. **Impact:** a
fleet expecting MEO/GEO coverage silently loses it; for a LEO-only deployment
this is informational. **Diagnose:** SatelliteEphemeris condition
`UnsupportedOrbitRegime`. **Mitigate:** expected on v1.0 (LEO-only); if you need
MEO/GEO, that is a roadmap gap, not a fault — filter those NORAD IDs out of the
source to silence the alert. **Escalate** to product/roadmap if MEO/GEO is
required.

---

## NTNControllerReconcileErrors

**Fires when** any controller's reconcile error rate exceeds 0.1/s for 15m.
Label: `controller`. **Impact:** that controller is not converging CRs to their
desired state — changes stall. **Diagnose:**

```bash
kubectl logs -l control-plane=controller-manager -n ntn-operators-system --tail=-1 --prefix=true --since=20m \
  | grep -i 'error' | grep '<controller>'
```

```promql
sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m]))
```

Common causes: RBAC gaps (a role missing a verb — check
`kubectl auth can-i`), apiserver unreachable, or one poison CR erroring every
reconcile. **Mitigate** per the logged error; if a single CR is the culprit,
`kubectl describe` it and correct or quarantine it. **Escalate** to the platform
owner for RBAC/apiserver issues.

---

## NTNEphemerisMapperIndexErrors

**Fires when** `increase(ntn_operators_ephemeris_mapper_index_error_total[15m]) > 0`
for 15m. **Unlabeled** — no `namespace` (see *Why unlabeled* below).

**Impact.** The SatelliteEphemeris→NTNCellConfig fan-out mapper's `spec.ephemerisRef`
indexed lookup is erroring, so a SatelliteEphemeris change does not immediately fan
out to the NTNCellConfigs that reference it. This is **self-healing**: each
referencing cell re-resolves the ephemeris on its own reconcile `RequeueAfter`
(1–5 min), so nothing is permanently lost — the cost is propagation *delay*, not a
stall. A sustained rate means the shared informer cache is unhealthy, which
usually also shows up as broader reconcile errors.

**Why unlabeled.** The `spec.ephemerisRef` field index is registered in
`SetupWithManager` and is startup-fatal, so at runtime this is never a *missing*
index — it is a cache/index anomaly on the controller's shared informer cache, a
process-level fault. A `namespace` label would spawn one series per namespace that
happened to fan out during the anomaly (fleet-wide cardinality) without localizing
the fault, so the metric is a single unlabeled counter and the affected
namespace/ephemeris is in the Error log instead.

**Diagnose.**

```bash
# The mapper logs the affected ephemeris + namespace with the underlying error.
kubectl logs -l control-plane=controller-manager -n ntn-operators-system --tail=-1 --prefix=true --since=20m \
  | grep -i 'indexed ephemerisRef lookup failed'
```

```promql
# One-off blip (self-heals) or a sustained rate (cache unhealthy)?
increase(ntn_operators_ephemeris_mapper_index_error_total[15m])
# Cross-check: a sick cache usually elevates reconcile errors too.
sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m]))
```

**Mitigate.** A single blip needs no action — the cells re-resolve on requeue. For
a sustained rate, treat it as informer-cache / apiserver health: check apiserver
reachability and the controller's memory/restart state (an OOM-restarting manager
resyncs its cache repeatedly). It commonly clears on a clean controller restart
once the underlying apiserver/cache issue is resolved.

**Escalate** to the platform owner (apiserver / controller-runtime cache health)
if the rate persists across a restart.

---

## See also

- [`e2e-prometheus-metrics.md`](e2e-prometheus-metrics.md) — wiring an NTNSlice
  to a live Prometheus metrics source end-to-end.
- Rollback and upgrade procedure: [`../../RELEASING.md`](../../RELEASING.md)
  (§ Rollback) and the [CHANGELOG](../../CHANGELOG.md) Upgrade notes.
