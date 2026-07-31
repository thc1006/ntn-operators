# Securing the runtime NTN push (`remoteControl.tls`)

OCUDU's `remote_control` WebSocket server is **plaintext and unauthenticated**. The
operator's runtime push (`ntn_config_update`) can speak `wss://` with a bearer token and
mutual TLS — but only if something in front of the gNB terminates that TLS. This page is
the missing half: what to deploy, what the Secret must contain, and how to tell whether it
is working.

If you do not configure `remoteControl.tls`, the operator dials plain `ws://`. That is only
safe when the endpoint is pod-local (a sidecar) or otherwise unreachable from the rest of
the cluster.

## Shape

```
NTNCellConfig.spec.provider.remoteControl.endpoint
        │  wss://  + Authorization: Bearer  + client certificate
        ▼
   nginx sidecar :8443   ── terminates TLS, verifies the client cert, checks the bearer
        │  ws://127.0.0.1:8001   (pod loopback — no Service, not routable)
        ▼
   OCUDU remote_control  ── plaintext, unauthenticated, as shipped
```

Keeping the proxy in the **same pod** is the point: the plaintext port never leaves the
pod's network namespace, so there is no unauthenticated path to the gNB for anything else
in the cluster to find.

Deployable manifests: [`config/samples/remote-control-tls/gnb-with-tls-sidecar.yaml`](../config/samples/remote-control-tls/gnb-with-tls-sidecar.yaml).
Matching CR: [`config/samples/ntn_v1alpha1_ntncellconfig_remotecontrol_tls.yaml`](../config/samples/ntn_v1alpha1_ntncellconfig_remotecontrol_tls.yaml).

## The credential Secret

Lives in the **NTNCellConfig's own namespace** and must be opted in by its owner:

```bash
kubectl -n <ns> create secret generic gnb-remote-control-cred \
  --from-file=ca.crt=ca.crt \        # CA that signed the sidecar's server certificate
  --from-file=tls.crt=client.crt \   # mode=mtls only
  --from-file=tls.key=client.key \   # mode=mtls only
  --from-literal=token='<bearer>'    # optional shared secret

kubectl -n <ns> label secret gnb-remote-control-cred \
  ntn.operators.dev/remote-control-credential=true
```

| Key | When | Notes |
|---|---|---|
| `ca.crt` | required whenever `token` is set | Pins the destination. Without it the token would be sent to whatever the **public** roots vouch for, and `endpoint` is caller-controlled. |
| `token` | optional | Sent as `Authorization: Bearer`, only ever over TLS. |
| `tls.crt` / `tls.key` | `mode: mtls` | Client certificate the sidecar verifies. |

The label is an **opt-in by the Secret's owner**, not an authorization boundary: any
NTNCellConfig in the namespace may reference any labelled Secret. See
[ADR-0009](adr/0009-remote-control-credential-confused-deputy.md) for the boundary, the
admin endpoint allow-list (`--remote-control-allowed-endpoint-hosts`) and the opt-in
admission policy (`credentialRefPolicy.enable`).

## Certificates

The sidecar's server certificate SAN **must cover the endpoint host**. The operator derives
`ServerName` from `endpoint` unless `tls.serverName` overrides it, so a certificate issued
for the pod IP or a bare name will fail verification.

```bash
# SAN for a Service-addressed sidecar
subjectAltName = DNS:gnb-proxy.<ns>.svc, DNS:gnb-proxy.<ns>.svc.cluster.local
```

Give the client certificate `extendedKeyUsage = clientAuth`, or nginx's
`ssl_verify_client` will reject it.

## Verifying it works

```bash
# 1. The push succeeded
kubectl -n <ns> get ntncellconfig <name> \
  -o jsonpath='{range .status.conditions[?(@.type=="EphemerisPushed")]}{.status} {.reason} {.message}{"\n"}{end}'
# → True Pushed norad=… epoch=… digest=…

# 2. The proxy saw a real upgrade, not a rejection
kubectl -n <ns> logs deploy/gnb -c proxy | grep -oE '"GET / HTTP/1.1" [0-9]{3}'
# → 101   upgrade accepted
# → 400   no client certificate (mTLS rejected it)
# → 401   bearer missing or wrong
```

A `401` here means the operator reached the proxy and was refused — the credential is
wrong, not the route. A `400` means `mode` is not `mtls`, or the client certificate is
missing/untrusted.

## Recovery after rotating the credential

The operator reads the Secret **uncached** on every real push, but it does **not watch
Secrets**. Editing the Secret therefore triggers nothing by itself. A cell whose push is
failing recovers on its own low-frequency poll; a cell that is *succeeding* picks up a
rotated credential on its next real push, which the referenced SatelliteEphemeris drives on
its ~3-minute propagation heartbeat.

Measured on a live 1.36.3 cluster: with the bearer deliberately broken and then repaired,
the cell recovered **166 s** after the Secret was fixed — the ephemeris heartbeat, not a
per-cell retry. Issue #298 records why that bound is accepted rather than closed with a
Secret watch.

## Egress

If you enable the operator's NetworkPolicy (`networkPolicy.enable`), put the **proxy's**
port in `networkPolicy.gnbPorts` — not OCUDU's plaintext port, which the operator never
dials in this shape. The default is `[8001]`, so a sidecar on `8443` is silently
unreachable until you change it.

> Not verified here: this repository's own dev cluster runs Flannel, which does not
> implement NetworkPolicy, so policy objects are accepted and never enforced. Confirm the
> rule on a cluster with a policy-capable CNI (Calico, Cilium, Antrea) before relying on it.

## What has been verified

End to end on Kubernetes 1.36.3, with the operator running in-cluster from this
repository's image:

- mTLS enforced — a dial without a client certificate is refused (`400`)
- the bearer is genuinely transmitted and checked — without it, `401`
- the handshake upgrades (`101`) and reaches the plaintext backend
- a well-formed `ntn_config_update` arrives, carrying SGP4-propagated ECEF from a real
  CelesTrak fetch (ISS: |r| ≈ 6.80e6 m, |v| ≈ 7.34 km/s)
- the operator resumes pushing after a restart
- breaking and repairing the bearer produces the recovery behaviour described above
