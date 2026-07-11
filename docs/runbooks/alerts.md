# NOC Runbook — NTN Operators alerts

Operational response for the alerts shipped in the chart's `PrometheusRule`
(`dist/chart/templates/monitoring/prometheusrule.yaml`, rendered when
`--set prometheus.enable=true`). One section per alert:
**Fires when → Impact → Diagnose → Mitigate → Escalate**.

All alerts are `severity: warning` and namespaced — the alert labels
(`namespace`, and one of `ephemeris` / `config` / `slice` / `controller`) point
at the exact CR. Assume `NS=<namespace>` and the operator lives in
`ntn-operators-system` unless you deploy it elsewhere.

First moves for any alert:

```bash
# Which CR, and what does it say about itself?
kubectl describe <kind> <name> -n "$NS"     # conditions + Events are the fastest signal
kubectl logs deploy/ntn-operators-controller-manager -n ntn-operators-system --since=30m | grep -iE 'error|warn'
```

Metric → alert map:

| Alert | Expr | For |
|---|---|---|
| `NTNEphemerisEpochStale` | `ntn_operators_ephemeris_epoch_stale_count > 0` | 15m |
| `NTNEphemerisPushFailing` | `increase(ntn_operators_ephemeris_push_errors_total[15m]) > 0` | 15m |
| `NTNEphemerisPushNotReady` | `ntn_operators_ephemeris_push_ready == 0` | 15m |
| `NTNSliceFailoverFlapping` | `increase(ntn_operators_failover_total[15m]) > 4` | 5m |
| `NTNConfigApplyErrors` | `increase(ntn_operators_config_apply_errors_total[15m]) > 0` | 15m |
| `NTNNoSatellitesTracked` | `ntn_operators_gp_satellite_count == 0` | 30m |
| `NTNGPFetchNotReady` | `ntn_operators_gp_fetch_ready == 0` | 30m |
| `NTNDeepSpaceElementsRejected` | `ntn_operators_gp_deep_space_rejected_count > 0` | 15m |
| `NTNControllerReconcileErrors` | `sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m])) > 0.1` | 15m |

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
#   GPDataFetched=False  → the fetch itself is failing (reason: RateLimited / AuthFailed / FetchFailed)
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
| `EphemerisRefNotFound` / `EphemerisGetFailed` | the referenced SatelliteEphemeris is missing/unreadable | fix the ref, not the gNB |
| `EphemerisPayloadMissing` | no propagated state to push yet | upstream ephemeris problem |
| `EphemerisStale` | the propagated state is stale/expired | see **NTNEphemerisEpochStale** |

```bash
kubectl describe ntncellconfig <config> -n "$NS"
#   Condition EphemerisPushed (Status/Reason/Message) + Event "EphemerisPushFailed"
# For ProviderPushFailed, test gNB reachability from a debug pod:
ENDPOINT=$(kubectl get ntncellconfig <config> -n "$NS" -o jsonpath='{.spec.provider.remoteControl.endpoint}')
kubectl run netcheck --rm -it --image=nicolaka/netshoot -n "$NS" -- nc -vz ${ENDPOINT%:*} ${ENDPOINT##*:}
```

**Mitigate.**
- `ProviderPushFailed` — restore the gNB `remote_control` server / fix the
  `endpoint`. **If `networkPolicy.enable=true`, confirm the gNB port is in
  `networkPolicy.gnbPorts`** — a missing port silently blackholes the push (this
  is the exact failure the I-27 egress config prevents).
- `ProviderPushRejected` — read the gNB log for the rejection; correct the
  NTNCellConfig spec (commonly `siWindowPosition`, or an ephemeris field).
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
kubectl logs deploy/ntn-operators-controller-manager -n ntn-operators-system --since=20m \
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

## See also

- [`e2e-prometheus-metrics.md`](e2e-prometheus-metrics.md) — wiring an NTNSlice
  to a live Prometheus metrics source end-to-end.
- Rollback and upgrade procedure: [`../../RELEASING.md`](../../RELEASING.md)
  (§ Rollback) and the [CHANGELOG](../../CHANGELOG.md) Upgrade notes.
