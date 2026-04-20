# Design: Pluggable Metrics Source for NTNSlice (Issue #67)

Status: In Progress
Target release: v0.4
Decision owner: thc1006

## Problem

`NTNSliceReconciler.readMetrics()` currently reads RSRP / latency / packet-loss
from annotations (`ntn.operators.dev/simulated-*`). Failover decisions are
therefore driven by values that a human (or test) placed on the CR, not by
anything the radio or core actually reported. This is fine for development but
must not ship as the default behaviour for production users.

## Goals

1. Allow operators of NTNSlice to pick the metrics source per-CR:
   `annotations` (current behaviour) or `prometheus`.
2. Keep the existing 3 metrics contract (`RSRP`, `LatencyMs`,
   `PacketLossPercent`). Do not expand to RSRQ / SINR in this change — that
   requires a matching failover-policy expansion and is scheduled as a
   follow-up.
3. Backward compatible: existing NTNSlice CRs with no `metricsSource` field
   continue to read from annotations.
4. Degrade safely: a broken Prometheus endpoint must never silently resolve
   to "everything is healthy"; that would suppress failovers during an outage.

## Non-goals (explicit)

- Authentication / TLS to Prometheus. Defer to follow-up with Secret-backed
  bearer-token + custom CA. RBAC surface change not in this PR.
- Per-slice rate limiting of Prometheus queries. Controller reconcile rate
  already provides natural backpressure.
- Background pull / push-gateway mode. Sync query per reconcile is sufficient
  for the current scale target (tens of NTNSlices).
- RSRQ / SINR metrics.

## Design decisions

### D1. Error fallback: stale-value cache, not "healthy defaults"

| Scenario | Action |
|---|---|
| Query succeeds | Cache and return value. |
| Query fails, cache has a prior value | Return stale value, flag `Stale=true`. |
| Query fails, cache empty (bootstrap) | Return `ErrNoMetrics`; controller sets `FailoverReady=Unknown` and requeues. |

"Healthy defaults" on error would suppress failover exactly when operators
need it most. Stale values keep the last-known position until fresh data is
available; the condition makes the degraded observability visible.

### D2. Sync query with short timeout, not async poller

Per reconcile we issue one query with a 2 s hard timeout (configurable).
This trades latency for simplicity: no goroutine lifecycle, no cache
invalidation rules, one place for the timeout. At the current controller
throughput (≪ 1 QPS per slice) the 2 s worst case is acceptable. If Prometheus
scale becomes a concern, a background poller is a local change inside the
`prometheusReader` without disturbing callers.

### D3. Endpoint-keyed client pool

Multiple NTNSlices in the same cluster will often point at the same Prometheus
endpoint. A `sync.Map[endpoint]*v1.API` amortises TCP / TLS setup across CRs.

### D4. PromQL is configurable

We do not hardcode PromQL. Users provide their own queries for each of
`rsrpDbm`, `latencyMs`, `packetLossPercent`. This decouples us from the
gNB / UPF exporter dialect (Open5GS exporter labels are not the same as
a third-party gNB RIC exporter). The trade-off is that a typo in PromQL
produces an `ErrNoMetrics` at runtime — handled by D1 + admission.

### D5. RSRP on Open5GS — limitation acknowledged

Open5GS UPF exposes packet and session counters, not radio measurements. RSRP
is a UE → gNB report. The OCUDU gNB has no native Prometheus exporter.
Consequences for E2E on L340:

- `latencyMs` and `packetLossPercent` can be validated against real UPF
  counters.
- `rsrpDbm` requires an exporter we write ourselves. We ship a synthetic
  `test-metrics-exporter` in `cmd/test-metrics-exporter/` that emits values
  linked to the satellite pass window, so the whole pipeline can be
  exercised end-to-end. A separate issue will track a real srsRAN-log → Prom
  bridge for the OCUDU gNB.

## API

```go
// api/v1alpha1/ntnslice_types.go

type MetricsSourceType string

const (
    MetricsSourceAnnotations MetricsSourceType = "annotations"
    MetricsSourcePrometheus  MetricsSourceType = "prometheus"
)

type MetricsSource struct {
    // +kubebuilder:validation:Enum=annotations;prometheus
    // +kubebuilder:default=annotations
    Type       MetricsSourceType          `json:"type,omitempty"`
    Prometheus *PrometheusMetricsSource   `json:"prometheus,omitempty"`
}

type PrometheusMetricsSource struct {
    // +kubebuilder:validation:Pattern=`^https?://`
    Endpoint string `json:"endpoint"`

    // +kubebuilder:default="2s"
    QueryTimeout metav1.Duration `json:"queryTimeout,omitempty"`

    Queries PrometheusQueries `json:"queries"`
}

type PrometheusQueries struct {
    RsrpDbm           string `json:"rsrpDbm,omitempty"`
    LatencyMs         string `json:"latencyMs,omitempty"`
    PacketLossPercent string `json:"packetLossPercent,omitempty"`
}
```

CEL validation on `NTNSliceSpec`:

```
self.metricsSource.?type.orValue('annotations') != 'prometheus'
|| has(self.metricsSource.prometheus)
```

## Package layout

```
pkg/slice/metrics/
  reader.go         Reader interface + aliased Metrics + sentinel errors
  annotation.go     annotationReader
  annotation_test.go
  prometheus.go     prometheusReader + QueryClient interface
  prometheus_test.go
  stale.go          staleCache wrapper
  stale_test.go
  clientpool.go     endpoint → v1.API pool
  clientpool_test.go
  provider.go       Provider: NTNSlice → Reader

internal/metrics/
  metricsreader.go  Prometheus counters for observability

cmd/test-metrics-exporter/
  main.go           synthetic RSRP/latency/packet-loss exporter
```

## Controller wiring

```go
type NTNSliceReconciler struct {
    ...
    ReaderProvider metrics.Provider
}

// In Reconcile():
reader := r.ReaderProvider.For(ns)
m, stale, err := reader.Read(ctx, ns)
if errors.Is(err, metrics.ErrNoMetrics) {
    setUnknownFailoverReady()
    return requeueSoon
}
```

## Observability

New counters (in `internal/metrics`):

- `ntn_metrics_reader_query_duration_seconds{source,outcome}`
- `ntn_metrics_reader_errors_total{source,reason}`
- `ntn_metrics_reader_stale_value_used_total{slice}`

## Testing

- Unit: interface-mocked `QueryClient`; no HTTP in unit tests for readers.
- Envtest (integration): Reconciler against fake API server, with an
  in-process `httptest.Server` acting as Prometheus.
- E2E (L340): described in `docs/e2e-prometheus.md`.

## Migration

No schema migration needed. Existing NTNSlices with no `metricsSource` are
treated as `type: annotations`. A whitepaper note will advise production
users to set `type: prometheus` before cutover.
