# Runbooks

Operational procedures for running NTN Operators in production.

| Runbook | Use when |
|---|---|
| [`alerts.md`](alerts.md) | An NTN Operators alert is firing — one response section per shipped `PrometheusRule` alert (ephemeris staleness, gNB push failure, failover flapping, and the rest). |
| [`e2e-prometheus-metrics.md`](e2e-prometheus-metrics.md) | Wiring an NTNSlice to a live Prometheus metrics source and validating it end-to-end. |

Release rollback and upgrade migrations live in [`../../RELEASING.md`](../../RELEASING.md)
(§ Rollback) and the [CHANGELOG](../../CHANGELOG.md) Upgrade notes.
