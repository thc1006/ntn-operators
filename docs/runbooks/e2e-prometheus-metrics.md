# E2E Runbook — Prometheus-sourced NTNSlice metrics

This runbook drives the MetricsSource (#67) feature end-to-end on
L340 (or any single-node Kubernetes cluster with Open5GS available).
It uses the in-tree `cmd/test-metrics-exporter` so RSRP / latency /
packet-loss values move through the full pipeline — exporter →
Prometheus → controller → NTNSlice.status — without needing a real
SDR in the loop.

## Prerequisites

- Kubernetes cluster where you can deploy to `ntn-e2e` namespace
- `kubectl` and `docker` on the PATH
- Go 1.26+ if you want to run the operator as a local process
- Open5GS running on the host (optional for §6)

## 1. Install CRDs

```bash
make install
```

This registers `NTNSlice`, `SatelliteEphemeris`, `NTNCellConfig`,
and `GroundStationLifecycle`.

## 2. Build and load images

The cluster needs the exporter image. Build it, then side-load:

```bash
docker build -f cmd/test-metrics-exporter/Dockerfile \
  -t test-metrics-exporter:latest .

# kubeadm + containerd: export to an OCI tar, import via ctr.
docker save test-metrics-exporter:latest | \
  sudo ctr -n k8s.io images import -

# kind cluster: use kind load instead.
#   kind load docker-image test-metrics-exporter:latest
```

Verify:

```bash
sudo crictl images | grep test-metrics-exporter
```

## 3. (Optional) Rebind Open5GS UPF metrics to a cluster-reachable IP

Pods cannot reach `127.0.0.7`. To let Prometheus scrape Open5GS UPF,
rebind the metrics server to the host IP that the pod network can
see (on L340 that is the eth0 / primary interface IP).

Edit `/etc/open5gs/upf.yaml`:

```yaml
  metrics:
    server:
      - address: 10.37.10.18   # was 127.0.0.7
        port: 9090
```

Restart and verify:

```bash
sudo systemctl restart open5gs-upfd
curl -s http://10.37.10.18:9090/metrics | head
```

**Security note.** Binding to a host IP exposes UPF metrics to
anything that can reach that address. Open5GS `/metrics` leaks
session IDs and SUPI-hash prefixes, so treat the port as sensitive.
In production, front this with a host-level firewall or a Service
ExternalName + NetworkPolicy. A minimal iptables fence that only
lets the pod CIDR in:

```bash
# Replace 10.244.0.0/16 with your cluster's podCIDR
# (kubectl cluster-info dump | grep podCIDR).
sudo iptables -I INPUT -p tcp --dport 9090 \
  -s 10.244.0.0/16 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 9090 -j DROP
```

Persist with `iptables-save > /etc/iptables/rules.v4` (or nftables
equivalent) so the fence survives reboot.

If you skip this step, the runbook still works — Prometheus will
scrape only the synthetic exporter, which is enough to exercise
the operator's Prometheus code path.

## 4. Deploy the fixture stack

```bash
kubectl apply -f config/samples/e2e/01-namespace.yaml
kubectl apply -f config/samples/e2e/02-test-metrics-exporter.yaml
kubectl apply -f config/samples/e2e/03-prometheus.yaml

# Optional — only if §3 is done. The scrape file is a full ConfigMap
# that supersedes the one bundled above; `kubectl apply` will swap the
# data cleanly. Follow with a /-/reload so Prometheus picks it up
# without a pod restart.
#   kubectl apply -f config/samples/e2e/06-open5gs-scrape.yaml
#   kubectl -n ntn-e2e exec deploy/prometheus -- \
#     wget -qO- --post-data= http://localhost:9090/-/reload

kubectl -n ntn-e2e wait --for=condition=Available deploy/test-metrics-exporter --timeout=60s
kubectl -n ntn-e2e wait --for=condition=Available deploy/prometheus           --timeout=60s
```

Verify the exporter is producing the expected series:

```bash
kubectl -n ntn-e2e port-forward svc/test-metrics-exporter 9090:9090 &
curl -s localhost:9090/metrics | grep ntn_e2e_rsrp_dbm
# Expected: swings between -80 and -140 every 30 s.
kill %1
```

Verify Prometheus is scraping:

```bash
kubectl -n ntn-e2e port-forward svc/prometheus 9091:9090 &
curl -s 'localhost:9091/api/v1/query?query=ntn_e2e_rsrp_dbm' | jq
kill %1
```

## 5. Create the SatelliteEphemeris with a pass window that spans now

The failover engine requires `FailoverReady=True`, which in turn
requires an active pass window in the referenced SatelliteEphemeris.
Apply the placeholder CR, then patch its status.

The `date -d "@..."` form below is GNU coreutils; macOS BSD `date`
wants a different flag. A portable alternative using Python 3 is
shown second.

```bash
kubectl apply -f config/samples/e2e/04-satellite-ephemeris.yaml

# Linux (GNU date):
NOW=$(date -u +%s)
AOS=$(date -u -d "@$NOW" +%Y-%m-%dT%H:%M:%SZ)
LOS=$(date -u -d "@$((NOW + 3600))" +%Y-%m-%dT%H:%M:%SZ)

# macOS / portable (Python 3):
#   AOS=$(python3 -c 'import datetime; print(datetime.datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%SZ"))')
#   LOS=$(python3 -c 'import datetime; print((datetime.datetime.utcnow()+datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')

kubectl patch satelliteephemeris oneweb-constellation \
  --subresource=status --type=merge -p "{\
\"status\": {\
  \"nextPassWindows\": [{\
    \"satellite\": \"oneweb-0001\",\
    \"groundStation\": \"nycu-gs\",\
    \"aos\": \"$AOS\",\
    \"los\": \"$LOS\",\
    \"maxElevation\": \"45\"\
  }],\
  \"satelliteCount\": 1\
}}"
```

## 6. Run the operator

Option A — as a local Go process (fastest iteration):

```bash
go run ./cmd --health-probe-bind-address=:8081 \
             --metrics-bind-address=0 \
             --leader-elect=false
```

Option B — `make deploy` after `make docker-build docker-push`.

## 7. Apply the NTNSlice and observe

```bash
kubectl apply -f config/samples/e2e/05-ntnslice.yaml
kubectl get ntnslice e2e-slice -o jsonpath='{.status}' | jq
```

Expected behaviour over the next ~2 minutes (30 s pass / 30 s gap):

- **Inside the pass window** (healthy RSRP = −80, latency 20 ms,
  loss 0.1 %): `status.activePathType = terrestrial`,
  `Conditions[FailoverReady] = True`, no failover events.
- **Inside the gap** (degraded RSRP = −140, latency 250 ms, loss
  7.5 %): the failover engine fires, `status.activePathType`
  becomes `satellite`, `status.failoverCount` increments, and a
  `FailoverTriggered` Warning event is recorded on the CR.
- **Back in the next pass window**, after `switchbackDelay` (60 s),
  a `Switchback` event fires and `activePathType` returns to
  `terrestrial`.

```bash
kubectl get events --field-selector involvedObject.name=e2e-slice --watch
```

## 8. Observe the operator's own Prometheus metrics

The operator exports `ntn_operators_reader_*` and
`ntn_operators_failover_total` on `:8443/metrics` (or whatever you
configured). For a quick read without TLS, use
`--metrics-secure=false --metrics-bind-address=:8080` at startup:

```bash
curl -s localhost:8080/metrics | grep ntn_operators_reader
```

## 9. Teardown

```bash
kubectl delete ntnslice e2e-slice -n default
kubectl delete satelliteephemeris oneweb-constellation -n default
kubectl delete namespace ntn-e2e
# Optional: revert /etc/open5gs/upf.yaml if §3 was done.
```

## Troubleshooting

- `FailoverReady=Unknown`, reason `MetricsReaderError`: the operator
  cannot reach the Prometheus endpoint. Check the Service URL in the
  NTNSlice spec, check the Prometheus pod logs, check NetworkPolicy.
- `FailoverReady=Unknown`, reason `MetricsUnavailable`: the reader
  reached Prometheus but the query came back empty or non-finite.
  Double-check the `queries.*` strings match the labels actually on
  the series (curl the Prometheus `/api/v1/query` endpoint).
- No failover transitions despite metrics swinging: verify the
  SatelliteEphemeris status.nextPassWindows spans "now"; without
  that the failover engine never considers the satellite available.
