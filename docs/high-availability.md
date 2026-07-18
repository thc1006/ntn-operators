# High Availability

The operator runs **active-passive**: two replicas, one elected leader that runs the
controllers, and a warm standby that takes over when the leader steps down. This document
records the design and the deliberate choices behind it — several of which look like
"missing hardening" but are correct for a leader-elected controller-runtime manager.

## Topology

| Knob | Value | Where |
|------|-------|-------|
| Replicas | 2 (chart) · 1 (raw kustomize) | `manager.replicas` (chart); `config/manager/manager.yaml` ships 1 for dev/e2e |
| Leader election | on | `--leader-elect` (default true), `cmd/main.go` |
| `LeaderElectionReleaseOnCancel` | `true` | `cmd/main.go` |
| Lease / renew / retry | 15s / 10s / 2s (explicitly pinned; same as the controller-runtime defaults) | `cmd/main.go` |
| Graceful shutdown | wait indefinitely (`GracefulShutdownTimeout` negative); `terminationGracePeriodSeconds` 30s (≥ `RenewDeadline`) | `cmd/main.go` (`gracefulShutdownTimeout`) |
| PodDisruptionBudget | `minAvailable: 1`, rendered only when `replicas > 1` | **chart only** (`podDisruptionBudget`) |
| Pod anti-affinity | soft, `topologyKey: kubernetes.io/hostname` | **chart only** (`manager.affinity`) |
| Readiness | cache-sync-gated, leadership-agnostic | `cmd/main.go` (`/readyz`) |

Every replica starts its manager and **syncs its cache before leader election**; only the
controllers (leader-election runnables) wait to start until this replica wins the lease. The
standby is a warm manager with a synced cache — not an unstarted one. (Start order in
controller-runtime v0.24.1: health server → webhooks → caches (wait for sync) → non-leader
runnables → leader election → controllers.)

## Readiness gates on cache-sync, not leadership

`/readyz` reports Ready once this replica's informer caches have synced — **regardless of
whether it is the leader**. `/healthz` is `healthz.Ping` (liveness: the process is up). Two
separate properties, both load-bearing:

- **Leadership-agnostic is mandatory.** A standby that is not the leader must still be Ready, or
  the Deployment never counts it Available and a rolling update deadlocks: the new replica can
  only become leader after the old one releases the lease, but the rollout will not tear down
  the old replica until the new one is Ready. Gating `/readyz` on leadership would wedge every
  rollout — so **never** do that.
- **Cache-sync gating is safe and wanted.** controller-runtime starts the health server *before*
  the caches sync, so a bare `healthz.Ping` `/readyz` reports Ready before (or without ever)
  syncing — a broken new replica (RBAC, CRD discovery, a wedged list/watch) would then be counted
  Available and the rollout would tear down the healthy old ones. Because every replica syncs its
  cache *before* leader election, a readyz that waits for cache-sync does **not** wait on
  leadership and does not deadlock. It is wired with a non-leader-election `Runnable`
  (`NeedLeaderElection()==false`) that flips a flag once caches have synced; `/readyz` reads it.

## Failover behavior

- **Graceful (rollout, `kubectl delete pod`, SIGTERM):** the leader releases the lease on
  shutdown (`LeaderElectionReleaseOnCancel`), so the standby can acquire it within a retry
  period (~2s) instead of waiting out the full lease duration. This is safe only because
  `main()` exits promptly after `mgr.Start()` returns — nothing runs on the critical path after
  the lease is released (controller-runtime issue #1132). **The release must never happen while a
  reconcile is still running**, or two leaders could act at once. controller-runtime releases the lease
  in a defer that runs only *after* the runnable-stop wait returns — but a *finite* `GracefulShutdownTimeout`
  lets that wait return on timeout (`runnableGroup.StopAndWait` returns on `ctx.Done()` without the
  runnables draining), which would release the lease while a hung reconcile is still doing lease-guarded
  work. So `GracefulShutdownTimeout` is set to **wait indefinitely** (a negative value): the lease is
  released only once the runnables truly stop, and a hung pod is SIGKILLed at `terminationGracePeriodSeconds`
  *without* the release defer running — degrading safely to a `LeaseDuration` failover rather than risking
  split-brain. `terminationGracePeriodSeconds` (30s across all manifests) is kept **≥ `RenewDeadline`** to give
  the release (a Get+Update bounded by `RenewDeadline`) *headroom* when the runnables stop promptly — not a
  guarantee: if little grace remains, the SIGKILL falls back safely to lease-expiry failover.
  `TestNewManagerOptionsAreSafe` pins the negative-timeout wiring and `TestManifestsGiveShutdownHeadroom`
  the grace-period floor.
- **Ungraceful (node crash, SIGKILL, network partition):** the lease is not released; the
  standby becomes *eligible* to take over after the lease expires — on the order of
  `LeaseDuration` (15s). Tightening the lease trades faster failover for more apiserver load and
  more spurious failovers under transient latency; the controller-runtime/client-go defaults (15/10/2s) are a
  good balance and are kept.

Those `~2s` / `~15s` figures are **lease-handoff** timings, not an end-to-end recovery SLA:
full recovery also includes the new leader's controller startup, work-queue processing, and the
first successful reconcile (plus apiserver and any gNB-side latency), and is not guaranteed to
complete within 2s/15s. Measure real RTO with fault injection rather than reading it off the
lease settings.

Kubernetes object reconciliation is level-based and convergent, so a reconcile cut off on the
old leader is simply re-done by the new one — no partial-write recovery is needed for CR /
ConfigMap state. The **external** runtime push (the WebSocket `ntn_config_update` to the gNB) is
**at-least-once** across a crash or leader transition: a push can succeed on the old leader
before it persists status, so the new leader may re-push. That is safe here because the push is
a set-state command keyed by epoch (`runtimeEphemerisPushMarker`) — a duplicate delivers the
same state — but the property callers must rely on is duplicate-tolerance, not exactly-once.

## PodDisruptionBudget

`minAvailable: 1` on two replicas permits a **voluntary** disruption (e.g. `kubectl drain`
for a node upgrade) to evict exactly one replica at a time, keeping the leader or a ready
standby up. It does **not** block involuntary disruptions (node crash) or Deployment rolling
updates (which delete pods directly rather than via the eviction API). A PDB with
`minAvailable: 1` on a single-replica Deployment would block all draining (the one pod can never
be evicted) — so the chart **only renders the PDB when `replicas > 1`** (`gt (int
.Values.manager.replicas) 1`). A single-replica install therefore gets no PDB and stays drainable,
rather than relying on the operator to keep `replicas` and `podDisruptionBudget.enable` in sync.

## Deploy paths

- **Helm chart (`dist/chart/`, recommended for production):** process-level active-passive
  redundancy by default — `replicas: 2`, PDB enabled (`minAvailable: 1`), soft anti-affinity.
  This is **best-effort** node spreading, not a node-failure guarantee: soft anti-affinity is a
  scheduler *preference* (both replicas can land on one node if that's all that fits), and the
  PDB only guards *voluntary* disruptions (drains/evictions) — surviving a node crash requires
  the two replicas to actually be scheduled into independent nodes / failure domains. Use hard
  anti-affinity and/or topology-spread constraints if you need that guarantee (at the cost of
  schedulability on small clusters).
- **Raw kustomize (`config/default`, `make deploy`):** a **single replica** — this is the
  dev / e2e deploy path, intentionally not HA. Deploy the Helm chart for a highly-available
  installation. (A single-replica base also keeps a `PodDisruptionBudget minAvailable: 1`
  from blocking node drains, which it would on one replica.)

## Logging

The manager logs production JSON (`zap.Options{Development: false}`) — info level, sampling,
error-level stacktraces — overridable with `--zap-devel` for local debugging.
