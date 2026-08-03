//go:build e2e_wss

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Credentialed runtime-push suite (#329) for package e2e.
//
// The wss:// + bearer + mTLS push had no end-to-end coverage at all: everything below
// #206, #295, #297, #313, #318 and #322 was unit-tested against httptest, which cannot
// show that the deployment shape works. OCUDU's remote_control server is plaintext, so
// the feature only exists behind a TLS-terminating proxy — and that proxy was shipped as
// a sample nobody executed.
//
// Deliberate choices:
//
//   - The gNB stand-in is STDLIB-ONLY Go, mounted from a ConfigMap and run with the stock
//     golang image. No image build, no registry, no `kind load` — so this runs identically
//     on kind in CI and on a plain kubeadm cluster, and it needs no Docker.
//   - The nginx config is read from config/samples/remote-control-tls/, so the SAMPLE is
//     what is under test. A copy here would drift and the sample would rot unnoticed.
//   - The assertion is what crossed the wire, read back out of the plaintext backend —
//     not merely that a condition flipped.
//
// Run: make test-e2e-wss   (or: go test ./test/e2e/ -tags e2e_wss -run TestWSSCredentialedPush)
package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

const (
	wssNS       = "ntn-e2e-wss" // dedicated + ownership-labelled; never "default" (see setupNamespace)
	wssOwnerKey = "ntn.operators.dev/e2e-owner"
	wssOwnerVal = "wss-push"
	wssCell     = "e2e-wss-cell"
	wssSatEph   = "e2e-wss-eph"
	wssGNB      = "e2e-wss-gnb"
	wssSvc      = "e2e-wss-gnb"
	wssCredName = "e2e-wss-cred"
	wssNoCA     = "e2e-wss-cred-no-ca"
	wssToken    = "e2e-wss-bearer-token"
	wssNorad    = 25544
	wssGPMock   = "e2e-wss-gp"
	// The mTLS listener the sample documents, and the TLS-only listener the sample documents as
	// a two-line deletion from it. Both are served by the same pod in front of one backend.
	wssMTLSPort = 8443
	wssTLSPort  = 8444
)

// wssRunNCI makes this run's frames distinguishable from any earlier run's. The backend log is
// the evidence, and a Deployment whose spec did not change keeps its pod — so a re-run can read
// a predecessor's frames and "prove" a push that never happened. Every frame assertion is scoped
// to this value. NCI is 36 bits; this stays well inside it.
var wssRunNCI = uint64(6733824 + time.Now().UnixNano()%1_000_000)

// ---------------------------------------------------------------- helpers

func kubectl(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("kubectl", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.String(), nil
}

func kubectlApply(t *testing.T, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("apply failed: %v\n%s\nmanifest:\n%s", err, errb.String(), manifest)
	}
}

func eventuallyWSS(t *testing.T, timeout time.Duration, what string, f func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, msg := f()
		if ok {
			return
		}
		last = msg
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s; last: %s", timeout, what, last)
}

// pushedCondition returns the EphemerisPushed condition as "status/reason".
func pushedCondition(t *testing.T) string {
	t.Helper()
	out, err := kubectl(t, "-n", wssNS, "get", "ntncellconfig", wssCell, "-o",
		`jsonpath={range .status.conditions[?(@.type=="EphemerisPushed")]}{.status}/{.reason}{end}`)
	if err != nil {
		return "<none>"
	}
	return strings.TrimSpace(out)
}

// runFrames returns the frames the PLAINTEXT backend accepted FOR THIS RUN, read from the
// backend's own log so the evidence is the wire rather than the operator's opinion. Frames from
// an earlier run are filtered out by NCI — see wssRunNCI.
func runFrames(t *testing.T) []string {
	t.Helper()
	out, _ := kubectl(t, "-n", wssNS, "logs", "deploy/"+wssGNB, "-c", "gnb", "--tail=-1")
	mine := fmt.Sprintf(`"nci":%d`, wssRunNCI)
	var frames []string
	for l := range strings.SplitSeq(out, "\n") {
		if _, after, ok := strings.Cut(l, "FRAME "); ok && strings.Contains(after, mine) {
			frames = append(frames, after)
		}
	}
	return frames
}

func framesReceived(t *testing.T) int { t.Helper(); return len(runFrames(t)) }

func lastFrame(t *testing.T) string {
	t.Helper()
	f := runFrames(t)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// ---------------------------------------------------------------- certificates

type certPair struct{ certPEM, keyPEM []byte }

func mustPEM(t *testing.T, der []byte, key *ecdsa.PrivateKey) certPair {
	t.Helper()
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return certPair{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}),
	}
}

// issueCerts builds a throwaway CA, a server certificate whose SAN covers the Service DNS
// name (the operator derives ServerName from the endpoint, so a wrong SAN fails the dial),
// and a client certificate for mTLS. Generated per-run: no key material is committed.
func issueCerts(t *testing.T) (ca, server, client certPair) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("genkey: %v", err)
		}
		return k
	}
	caKey := newKey()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "e2e-wss-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create ca: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	ca = mustPEM(t, caDER, caKey)

	leaf := func(cn string, dns []string, eku x509.ExtKeyUsage) certPair {
		k := newKey()
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
			DNSNames:     dns,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create %s: %v", cn, err)
		}
		return mustPEM(t, der, k)
	}
	server = leaf(wssSvc+"."+wssNS+".svc", []string{
		wssSvc + "." + wssNS + ".svc",
		wssSvc + "." + wssNS + ".svc.cluster.local",
	}, x509.ExtKeyUsageServerAuth)
	client = leaf("ntn-operator", nil, x509.ExtKeyUsageClientAuth)
	return ca, server, client
}

// ---------------------------------------------------------------- fixtures

// freshGPJSON is one OMM with EPOCH=now, so it stays inside maxEpochAge (7d) for the whole
// run. The checked-in testdata fixture is months old and would be refused as hard-stale by
// sourceEpochFresh, which is why the runtime-push path has never been reachable in e2e.
func freshGPJSON() string {
	epoch := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000")
	return fmt.Sprintf(`[{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":%q,`+
		`"MEAN_MOTION":15.50103472,"ECCENTRICITY":0.0004364,"INCLINATION":51.6416,`+
		`"RA_OF_ASC_NODE":247.4627,"ARG_OF_PERICENTER":130.5360,"MEAN_ANOMALY":325.0288,`+
		`"EPHEMERIS_TYPE":0,"CLASSIFICATION_TYPE":"U","NORAD_CAT_ID":%d,`+
		`"ELEMENT_SET_NO":999,"REV_AT_EPOCH":47189,"BSTAR":0.00025892,`+
		`"MEAN_MOTION_DOT":0.00019394,"MEAN_MOTION_DDOT":0}]`, epoch, wssNorad)
}

// sampleNginxConf reads the nginx config out of the SHIPPED sample so the sample itself is
// under test — a copy inlined here would let the sample rot while the test stayed green. It
// parses the YAML rather than slicing the text: block indentation, quoting and escapes are the
// parser's job, and a re-indented sample must not silently yield a subtly different config.
func sampleNginxConf(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(root, "..", "..", "config", "samples", "remote-control-tls", "gnb-with-tls-sidecar.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read sample %s: %v", p, err)
	}
	var conf string
	for doc := range strings.SplitSeq(string(b), "\n---") {
		var cm struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil {
			continue // the sample may carry comment-only or non-object documents
		}
		if cm.Kind == "ConfigMap" && cm.Data["nginx.conf"] != "" {
			conf = cm.Data["nginx.conf"]
			break
		}
	}
	if conf == "" {
		t.Fatalf("sample %s no longer has a ConfigMap carrying data[\"nginx.conf\"] — "+
			"the test mounts that key, so update one to match the other", p)
	}
	conf = strings.ReplaceAll(conf, "REPLACE_WITH_TOKEN", wssToken)
	if strings.Contains(conf, "REPLACE_WITH") {
		t.Fatalf("sample nginx.conf still has an unsubstituted placeholder:\n%s", conf)
	}
	return conf
}

// proxyLogCount counts occurrences in the SIDECAR's own log. nginx writes both access and error
// logs to stdout in the official image, so this is the proxy's account of what it did — the
// point being that a refusal is observed at the proxy, not merely inferred from the operator's
// condition. Always compared as a delta across one arm.
func proxyLogCount(t *testing.T, sub string) int {
	t.Helper()
	out, _ := kubectl(t, "-n", wssNS, "logs", "deploy/"+wssGNB, "-c", "proxy", "--tail=-1")
	return strings.Count(out, sub)
}

// sampleNginxConfTLSOnly is the variant the sample itself documents:
//
//	# Drop these two lines (and use tls.mode "tls") if you only want a bearer token.
//	ssl_client_certificate /certs/ca.crt;
//	ssl_verify_client on;
//
// Deriving it here rather than writing a second config by hand means the TLS-only arm tests the
// documented instruction, not an invention of this test — and if the sample stops matching, the
// derivation fails loudly instead of silently testing something else.
func sampleNginxConfTLSOnly(t *testing.T) string {
	t.Helper()
	conf := sampleNginxConf(t)
	for _, drop := range []string{"ssl_client_certificate", "ssl_verify_client"} {
		var kept []string
		found := false
		for line := range strings.SplitSeq(conf, "\n") {
			if strings.Contains(line, drop) {
				found = true
				continue
			}
			kept = append(kept, line)
		}
		if !found {
			t.Fatalf("the sample no longer contains %q, so the TLS-only variant it documents "+
				"(%q) cannot be derived from it", drop, "drop these two lines")
		}
		conf = strings.Join(kept, "\n")
	}
	listen := fmt.Sprintf("listen %d ssl", wssMTLSPort)
	if !strings.Contains(conf, listen) {
		t.Fatalf("the sample no longer listens on %d; this test moves that listener to %d",
			wssMTLSPort, wssTLSPort)
	}
	return strings.Replace(conf, listen, fmt.Sprintf("listen %d ssl", wssTLSPort), 1)
}

// foreignCA is a throwaway CA unrelated to the one the proxy serves under. Pinning it in the
// credential must make the operator refuse the server certificate.
func foreignCA(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "e2e-wss-foreign-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatalf("create foreign ca: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ---------------------------------------------------------------- deployment

func deployGPMock(t *testing.T) {
	t.Helper()
	_, _ = kubectl(t, "-n", wssNS, "delete", "configmap", wssGPMock+"-fixture", "--ignore-not-found")
	gp := freshGPJSON()
	cmd := exec.Command("kubectl", "-n", wssNS, "create", "configmap", wssGPMock+"-fixture",
		"--from-file=gp.json=/dev/stdin")
	cmd.Stdin = strings.NewReader(gp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create gp fixture: %v: %s", err, out)
	}
	// A ConfigMap mounted through subPath never receives updates (Kubernetes documents this), and
	// this Deployment is otherwise identical between runs — so with E2E_WSS_KEEP=1 the pod keeps
	// serving the PREVIOUS run's gp.json. Same checksum-annotation pattern the gNB Deployment
	// already uses, for the same reason.
	gpSum := sha256.Sum256([]byte(gp))
	kubectlApply(t, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  replicas: 1
  selector: {matchLabels: {app: %[1]s}}
  template:
    metadata:
      labels: {app: %[1]s}
      annotations: {ntn.operators.dev/gp-fixture-sha256: "%[3]s"}
    spec:
      containers:
        - name: nginx
          image: nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de
          ports: [{containerPort: 80, name: http}]
          # Ready means the fixture is actually servable, not merely that nginx started.
          readinessProbe:
            httpGet: {path: /gp.json, port: 80}
            periodSeconds: 2
          volumeMounts:
            - {name: fixture, mountPath: /usr/share/nginx/html/gp.json, subPath: gp.json, readOnly: true}
      volumes:
        - {name: fixture, configMap: {name: %[1]s-fixture}}
---
apiVersion: v1
kind: Service
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  selector: {app: %[1]s}
  ports: [{port: 80, targetPort: 80}]
`, wssGPMock, wssNS, hex.EncodeToString(gpSum[:8])))
	if _, err := kubectl(t, "-n", wssNS, "rollout", "status", "deploy/"+wssGPMock, "--timeout=180s"); err != nil {
		t.Fatalf("gp mock not ready: %v", err)
	}
}

// deployGNB stands up the shape the sample documents: a PLAINTEXT remote_control stand-in
// bound inside the pod, with the TLS-terminating nginx sidecar in front of it. Only the TLS
// port is published — a Service on the plaintext port would hand every pod in the cluster an
// unauthenticated path to the gNB, which is the whole reason the sidecar exists.
func deployGNB(t *testing.T, ca, server certPair) {
	t.Helper()
	root, _ := os.Getwd()
	stub := filepath.Join(root, "fixtures", "gnb-remote-control-stub.go.txt")

	_, _ = kubectl(t, "-n", wssNS, "delete", "configmap",
		wssGNB+"-src", wssGNB+"-conf", wssGNB+"-conf-tls", "--ignore-not-found")
	if out, err := kubectl(t, "-n", wssNS, "create", "configmap", wssGNB+"-src", "--from-file=main.go="+stub); err != nil {
		t.Fatalf("create stub configmap: %v: %s", err, out)
	}
	conf, confTLS := sampleNginxConf(t), sampleNginxConfTLSOnly(t)
	for _, c := range []struct{ name, body string }{{wssGNB + "-conf", conf}, {wssGNB + "-conf-tls", confTLS}} {
		cmd := exec.Command("kubectl", "-n", wssNS, "create", "configmap", c.name, "--from-file=nginx.conf=/dev/stdin")
		cmd.Stdin = strings.NewReader(c.body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create %s: %v: %s", c.name, err, out)
		}
	}
	_, _ = kubectl(t, "-n", wssNS, "delete", "secret", wssGNB+"-tls", "--ignore-not-found")
	if err := createSecret(t, wssGNB+"-tls", map[string][]byte{
		"tls.crt": server.certPEM, "tls.key": server.keyPEM, "ca.crt": ca.certPEM,
	}, false); err != nil {
		t.Fatalf("create proxy tls secret: %v", err)
	}

	// nginx reads its certificates and its config ONCE, at startup, and this Deployment's spec is
	// otherwise identical between runs — so a re-run that mints a fresh CA would leave the old pod
	// serving the old chain and every dial would fail x509 verification. Observed: a second run in
	// a kept namespace failed every arm with "certificate signed by unknown authority". Rolling on
	// a checksum of exactly what nginx loads is the Helm checksum/config pattern.
	sum := sha256.Sum256(bytes.Join([][]byte{
		[]byte(conf), []byte(confTLS), ca.certPEM, server.certPEM, server.keyPEM}, nil))
	kubectlApply(t, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  replicas: 1
  selector: {matchLabels: {app: %[1]s}}
  template:
    metadata:
      labels: {app: %[1]s}
      annotations: {ntn.operators.dev/nginx-inputs-sha256: "%[4]s"}
    spec:
      containers:
        - name: gnb
          image: golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191
          command: ["go","run","/src/main.go"]
          env:
            - {name: GOCACHE, value: /tmp/gocache}
            - {name: GOFLAGS, value: -mod=mod}
            - {name: HOME,    value: /tmp}
          ports: [{containerPort: 8001}]
          # go run compiles first, so the container is Running long before anything listens;
          # without this probe, rollout status returns early and the first push races the build.
          readinessProbe:
            tcpSocket: {port: 8001}
            periodSeconds: 5
            failureThreshold: 48
          volumeMounts:
            - {name: src, mountPath: /src}
            - {name: tmp, mountPath: /tmp}
        - name: proxy-tls
          # The SAME sample config with the two client-verification lines dropped: TLS + bearer,
          # no client certificate. Without it, mode "tls" is only ever exercised as the negative
          # half of an mTLS test, so the whole TLS-plus-bearer path could break unnoticed.
          image: nginx:1.29-alpine
          ports: [{containerPort: %[6]d, name: tls}]
          readinessProbe:
            tcpSocket: {port: %[6]d}
            periodSeconds: 2
          volumeMounts:
            - {name: conf-tls, mountPath: /etc/nginx/nginx.conf, subPath: nginx.conf}
            - {name: certs,    mountPath: /certs, readOnly: true}
        - name: proxy
          image: nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de
          ports: [{containerPort: 8443, name: wss}]
          readinessProbe:
            tcpSocket: {port: 8443}
            periodSeconds: 2
          volumeMounts:
            - {name: conf,  mountPath: /etc/nginx/nginx.conf, subPath: nginx.conf}
            - {name: certs, mountPath: /certs, readOnly: true}
      volumes:
        - {name: src,   configMap: {name: %[1]s-src}}
        - {name: conf,     configMap: {name: %[1]s-conf}}
        - {name: conf-tls, configMap: {name: %[1]s-conf-tls}}
        - {name: certs, secret: {secretName: %[1]s-tls}}
        - {name: tmp,   emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata: {name: %[3]s, namespace: %[2]s}
spec:
  selector: {app: %[1]s}
  ports:
    - {name: wss, port: %[5]d, targetPort: %[5]d}
    - {name: wss-tls, port: %[6]d, targetPort: %[6]d}
`, wssGNB, wssNS, wssSvc, hex.EncodeToString(sum[:8]), wssMTLSPort, wssTLSPort))
	if _, err := kubectl(t, "-n", wssNS, "rollout", "status", "deploy/"+wssGNB, "--timeout=300s"); err != nil {
		t.Fatalf("gnb not ready: %v", err)
	}
}

func createSecret(t *testing.T, name string, data map[string][]byte, optIn bool) error {
	t.Helper()
	args := []string{"-n", wssNS, "create", "secret", "generic", name}
	files := map[string]string{}
	for k, v := range data {
		f := filepath.Join(t.TempDir(), strings.ReplaceAll(k, "/", "_"))
		if err := os.WriteFile(f, v, 0o600); err != nil {
			return err
		}
		files[k] = f
		args = append(args, "--from-file="+k+"="+f)
	}
	if out, err := kubectl(t, args...); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	if optIn {
		if out, err := kubectl(t, "-n", wssNS, "label", "secret", name,
			"ntn.operators.dev/remote-control-credential=true", "--overwrite"); err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
	}
	return nil
}

// requireManagerAllowsGPHost VERIFIES the manager configuration; it deliberately does NOT
// patch it. An earlier version appended "--ephemeris-allowed-private-hosts=<our host>" to the
// running Deployment. That flag is a plain string, so Go's flag package calls Set once per
// occurrence and the LAST one wins: the append silently REPLACED the value
// ha-ci-values.yaml sets for celestrak-mock, and TestHAOutageContinuityAcrossFailover — which
// runs after this suite — timed out with the manager rejecting the CelesTrak mock. The suite
// itself stayed green, which is exactly what makes shared-state mutation from a test dangerous.
//
// A test that runs against an already-deployed chart must not reconfigure the controller that
// every other test shares. Configure the union at deploy time and assert it here.
func requireManagerAllowsGPHost(t *testing.T) {
	t.Helper()
	want := wssGPMock + "." + wssNS + ".svc.cluster.local"
	if slices.Contains(managerFlagList(t, "--ephemeris-allowed-private-hosts"), want) {
		return
	}
	t.Fatalf("the deployed manager does not allow the GP mock host %q, so the SSRF-safe fetcher will "+
		"block it and the ephemeris will never propagate.\n"+
		"Deploy with ONE --ephemeris-allowed-private-hosts carrying the UNION of every mock host, e.g.\n"+
		"  manager.args:\n"+
		"    - --ephemeris-allowed-private-hosts=celestrak-mock.default.svc.cluster.local,%s\n"+
		"Do NOT add a second occurrence of the flag: the later value replaces the earlier one.\n"+
		"current args: %v", want, want, managerArgs(t))
}

// managerArgs reads the deployed manager's argv. Namespace-scoped on purpose: the
// control-plane=controller-manager label is not ours alone — Kueue, for one, carries it too.
func managerArgs(t *testing.T) []string {
	t.Helper()
	ns := os.Getenv("WSS_MANAGER_NAMESPACE")
	if ns == "" {
		ns = "ntn-operators-system"
	}
	// Names, not .items[0]: control-plane=controller-manager is a convention, not a unique key
	// (Kueue ships it too), so two releases or a leftover Deployment in one namespace would make
	// index 0 an arbitrary choice — and the suite would then police the wrong manager while
	// reporting success. Demand exactly one, or say which ones were found.
	names, err := kubectl(t, "-n", ns, "get", "deploy", "-l", "control-plane=controller-manager",
		"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
	if err != nil {
		t.Fatalf("cannot list Deployments in namespace %q: %v", ns, err)
	}
	var found []string
	for n := range strings.SplitSeq(strings.TrimSpace(names), "\n") {
		if n != "" {
			found = append(found, n)
		}
	}
	if want := os.Getenv("WSS_MANAGER_DEPLOYMENT"); want != "" {
		if !slices.Contains(found, want) {
			t.Fatalf("WSS_MANAGER_DEPLOYMENT=%q is not in namespace %q (found %v)", want, ns, found)
		}
		found = []string{want}
	}
	switch len(found) {
	case 1:
	case 0:
		t.Fatalf("no manager Deployment (control-plane=controller-manager) in namespace %q — deploy the "+
			"chart first, or set WSS_MANAGER_NAMESPACE", ns)
	default:
		t.Fatalf("%d Deployments carry control-plane=controller-manager in namespace %q (%v); this suite "+
			"would police an arbitrary one. Set WSS_MANAGER_DEPLOYMENT to disambiguate.", len(found), ns, found)
	}
	out, err := kubectl(t, "-n", ns, "get", "deploy", found[0],
		"-o", `jsonpath={.spec.template.spec.containers[?(@.name=="manager")].args}`)
	if err != nil {
		t.Fatalf("cannot read args of Deployment %s/%s: %v", ns, found[0], err)
	}
	var args []string
	if err := json.Unmarshal([]byte(out), &args); err != nil {
		t.Fatalf("manager args are not the JSON array kubectl usually prints (%q): %v", out, err)
	}
	return args
}

// managerFlagList returns the comma-separated value of a list flag, or nil if it is absent.
//
// It FAILS on a repeated flag rather than picking one. This helper exists because appending a
// second --ephemeris-allowed-private-hosts once broke CI: the flag is a plain string, so Go's flag
// package calls Set per occurrence and the LAST value wins, silently replacing the first. The
// first version of this guard then read the FIRST occurrence — so the check written to catch that
// regression would have passed while the manager ran on the clobbering value. A guard that reads a
// different value than the process it is guarding is worse than no guard.
// flagOccurrences returns every value the argv carries for flag. Split out from managerFlagList
// so the "repeated flag" rule is testable without a cluster — the rule is the whole point.
func flagOccurrences(args []string, flag string) []string {
	var vals []string
	for _, a := range args {
		if _, after, ok := strings.Cut(a, flag+"="); ok {
			vals = append(vals, after)
		}
	}
	return vals
}

func managerFlagList(t *testing.T, flag string) []string {
	t.Helper()
	vals := flagOccurrences(managerArgs(t), flag)
	switch len(vals) {
	case 0:
		return nil
	case 1:
		return strings.Split(vals[0], ",")
	default:
		t.Fatalf("%s appears %d times in the manager's argv (%q). It is a plain string flag, so the LAST "+
			"occurrence wins and the earlier ones are dead — which is exactly how this suite once broke "+
			"TestHAOutageContinuityAcrossFailover. Carry the union in ONE occurrence.", flag, len(vals), vals)
		return nil
	}
}

// setupNamespace creates a namespace this suite OWNS, and refuses to touch one it does not.
// The fixtures used to live in "default" under fixed names, so a re-run on a shared cluster
// could overwrite — and then delete — resources the suite never created.
func setupNamespace(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_WSS_DESTRUCTIVE") != "1" {
		t.Skip("this suite creates and then DELETES namespace " + wssNS + " on the cluster kubectl " +
			"currently points at. Run `make test-e2e-wss`, or set E2E_WSS_DESTRUCTIVE=1, to confirm " +
			"that is the cluster you meant.")
	}
	out, err := kubectl(t, "get", "ns", wssNS, "-o", `jsonpath={.metadata.labels.`+
		strings.ReplaceAll(wssOwnerKey, ".", `\.`)+`}`)
	switch {
	case err != nil: // absent — create it, owned
		kubectlApply(t, fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels: {%q: %q}
`, wssNS, wssOwnerKey, wssOwnerVal))
	case strings.TrimSpace(out) == wssOwnerVal:
		// Ours. If a previous run left it Terminating, every create below would fail with
		// "namespace is terminating"; wait it out and recreate.
		phase, _ := kubectl(t, "get", "ns", wssNS, "-o", `jsonpath={.status.phase}`)
		if strings.TrimSpace(phase) == "Terminating" {
			eventuallyWSS(t, 3*time.Minute, "previous run's namespace to finish terminating", func() (bool, string) {
				_, err := kubectl(t, "get", "ns", wssNS)
				return err != nil, "still Terminating"
			})
			kubectlApply(t, fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels: {%q: %q}\n",
				wssNS, wssOwnerKey, wssOwnerVal))
		}
	default:
		t.Fatalf("namespace %q exists but is not owned by this suite (%s=%q). Refusing to run: this suite "+
			"deletes the whole namespace on cleanup and will not do that to somebody else's resources.",
			wssNS, wssOwnerKey, strings.TrimSpace(out))
	}
}

// dumpNamespaceEvidence prints what a failed arm needs and cleanup is about to destroy.
// Best-effort throughout: this runs while something is already wrong, so a failure to collect
// must never mask the failure being collected.
func dumpNamespaceEvidence(t *testing.T) {
	t.Helper()
	t.Logf("=== FAILURE EVIDENCE for namespace %s ===", wssNS)
	for _, q := range []struct {
		what string
		args []string
	}{
		{"NTNCellConfig", []string{"-n", wssNS, "get", "ntncellconfig", wssCell, "-o", "yaml"}},
		{"SatelliteEphemeris", []string{"-n", wssNS, "get", "satelliteephemeris", wssSatEph, "-o", "yaml"}},
		{"pods", []string{"-n", wssNS, "get", "pods", "-o", "wide"}},
		{"events", []string{"-n", wssNS, "get", "events", "--sort-by=.lastTimestamp"}},
		{"proxy log", []string{"-n", wssNS, "logs", "deploy/" + wssGNB, "-c", "proxy", "--tail=80"}},
		{"gnb log", []string{"-n", wssNS, "logs", "deploy/" + wssGNB, "-c", "gnb", "--tail=80"}},
		{"gp mock log", []string{"-n", wssNS, "logs", "deploy/" + wssGPMock, "--tail=20"}},
	} {
		out, err := kubectl(t, q.args...)
		if err != nil {
			t.Logf("--- %s: unavailable (%v)", q.what, err)
			continue
		}
		t.Logf("--- %s ---\n%s", q.what, out)
	}
}

// waitCondition waits for the EXACT expected "status/reason". Waiting merely for a False
// prefix would let an arm assert while the PREVIOUS arm's reason is still on the object —
// which fails intermittently rather than never, and is the worse kind of flake.
func waitCondition(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	eventuallyWSS(t, timeout, "EphemerisPushed="+want, func() (bool, string) {
		c := pushedCondition(t)
		return c == want, c
	})
}

// ---------------------------------------------------------------- the suite

func TestWSSCredentialedPush(t *testing.T) {
	if os.Getenv("KUBECONFIG") == "" && os.Getenv("HOME") == "" {
		t.Skip("no kubeconfig available")
	}
	// Check the deployed manager BEFORE creating anything: a cluster that cannot satisfy the
	// suite should be left exactly as it was found.
	requireManagerAllowsGPHost(t)
	ca, server, client := issueCerts(t)

	setupNamespace(t)
	t.Cleanup(func() {
		// Dump BEFORE deleting: cleanup removes the namespace, so on a failure the proxy log, the
		// backend log and the CR conditions — the only three things that say WHY — would be gone
		// before CI collects diagnostics, leaving a red job with nothing to read.
		if t.Failed() {
			dumpNamespaceEvidence(t)
		}
		if os.Getenv("E2E_WSS_KEEP") == "1" {
			t.Logf("E2E_WSS_KEEP=1: leaving namespace %s up for inspection; the next run will "+
				"refuse to start until it is deleted or re-adopted", wssNS)
			return
		}
		// Delete the namespace this suite owns, rather than deleting fixed names one by one and
		// risking removing something the suite never created. Finalizers on the CR need the
		// manager alive, so clear them first — a namespace stuck Terminating would block the
		// next run's ownership check.
		_, _ = kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
			"-p", `{"metadata":{"finalizers":[]}}`)
		_, _ = kubectl(t, "delete", "ns", wssNS, "--ignore-not-found", "--wait=false")
	})

	t.Log("standing up the GP source and the gNB behind its TLS sidecar")
	deployGPMock(t)
	deployGNB(t, ca, server)

	// The credential the operator will present: CA to trust the sidecar, a client
	// certificate for mTLS, and the bearer the sidecar enforces.
	_, _ = kubectl(t, "-n", wssNS, "delete", "secret", wssCredName, wssNoCA, "--ignore-not-found")
	if err := createSecret(t, wssCredName, map[string][]byte{
		"ca.crt": ca.certPEM, "tls.crt": client.certPEM, "tls.key": client.keyPEM, "token": []byte(wssToken),
	}, true); err != nil {
		t.Fatalf("create credential secret: %v", err)
	}
	// #313: a bearer with no pinned CA must be refused BEFORE any dial.
	if err := createSecret(t, wssNoCA, map[string][]byte{
		"tls.crt": client.certPEM, "tls.key": client.keyPEM, "token": []byte(wssToken),
	}, true); err != nil {
		t.Fatalf("create no-ca secret: %v", err)
	}

	kubectlApply(t, fmt.Sprintf(`
apiVersion: ntn.operators.dev/v1alpha1
kind: SatelliteEphemeris
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  source:
    type: CelesTrak
    url: http://%[3]s.%[2]s.svc.cluster.local/gp.json
    refreshInterval: 4h
  satellites:
    noradIDs: [%[4]d]
`, wssSatEph, wssNS, wssGPMock, wssNorad))

	eventuallyWSS(t, 4*time.Minute, "the ephemeris to propagate a state", func() (bool, string) {
		out, _ := kubectl(t, "-n", wssNS, "get", "satelliteephemeris", wssSatEph,
			"-o", `jsonpath={.status.propagatedStates[*].noradID}`)
		got := strings.TrimSpace(out)
		return got != "", fmt.Sprintf("propagatedStates=%q (a stall here usually means the manager "+
			"lacks --ephemeris-allowed-private-hosts for %s.%s.svc.cluster.local)", got, wssGPMock, wssNS)
	})

	kubectlApply(t, fmt.Sprintf(`
apiVersion: ntn.operators.dev/v1alpha1
kind: NTNCellConfig
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  provider:
    type: ocudu
    namespace: %[2]s
    remoteControl:
      endpoint: "%[3]s.%[2]s.svc:%[8]d"
      tls: {mode: mtls, secretName: %[4]s}
  cellID: {plmn: "00101", nci: %[7]d}
  ephemerisRef: %[5]s
  ephemerisNoradID: %[6]d
  ntn:
    cellSpecificKoffset: 100
    ephemerisECEF: {posX: 1000000, posY: 2000000, posZ: 3000000, velX: 0, velY: 0, velZ: 0}
`, wssCell, wssNS, wssSvc, wssCredName, wssSatEph, wssNorad, wssRunNCI, wssMTLSPort))

	// --- arm 1: the whole chain works, and the PLAINTEXT backend proves it -------------
	t.Run("mtls+bearer reaches the plaintext gNB behind the proxy", func(t *testing.T) {
		waitCondition(t, "True/Pushed", 4*time.Minute)
		if n := framesReceived(t); n == 0 {
			t.Fatal("the condition says pushed but the plaintext backend received nothing — " +
				"the proxy, not the operator, is what the gNB sees")
		}
		var env struct {
			Cmd   string `json:"cmd"`
			Cells []struct {
				PLMN  string `json:"plmn"`
				NCI   uint64 `json:"nci"`
				Epoch int64  `json:"epoch_timestamp"`
			} `json:"cells"`
		}
		frame := lastFrame(t)
		if err := json.Unmarshal([]byte(frame), &env); err != nil {
			t.Fatalf("backend recorded an unparseable frame %q: %v", frame, err)
		}
		if env.Cmd != "ntn_config_update" || len(env.Cells) != 1 {
			t.Fatalf("wrong envelope on the wire: %s", frame)
		}
		if env.Cells[0].PLMN != "00101" || env.Cells[0].NCI != wssRunNCI {
			t.Errorf("frame targets the wrong cell: %+v", env.Cells[0])
		}
		if env.Cells[0].Epoch <= time.Now().UnixMilli() {
			t.Errorf("epoch %d is not in the future — OCUDU rejects past epochs", env.Cells[0].Epoch)
		}
		t.Logf("frame on the wire: %s", frame)
	})

	// --- arm 2: the bearer is genuinely checked, and a 401 is classified as retry-later -
	t.Run("wrong bearer is RemoteEndpointRejected, not a permanent rejection", func(t *testing.T) {
		before, before401 := framesReceived(t), proxyLogCount(t, `" 401 `)
		patchSecretToken(t, wssCredName, "definitely-not-the-token")
		bumpGeneration(t, "wrong-bearer")
		waitCondition(t, "False/RemoteEndpointRejected", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: a rejected handshake must not deliver a payload", before, now)
		}
		if now := proxyLogCount(t, `" 401 `); now == before401 {
			t.Errorf("the proxy logged no new 401: the condition may be reporting a rejection that "+
				"never reached the bearer check (401 count stayed at %d)", before401)
		}
		patchSecretToken(t, wssCredName, wssToken)
		bumpGeneration(t, "restore-bearer")
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	// --- arm 3: #313 — a bearer with no pinned CA is refused before any dial -----------
	// The name says "no payload", not "no dial": frame count cannot distinguish a refusal made
	// before dialling from one made after a TCP connect that the proxy then dropped. The
	// before-any-dial ordering is proven in the unit tests, which can observe the dial itself.
	t.Run("token without ca.crt delivers no payload (#313)", func(t *testing.T) {
		before := framesReceived(t)
		patchSecretName(t, wssNoCA)
		waitCondition(t, "False/RemoteControlCredentialUnavailable", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: no payload may reach the gNB when the CA is unpinned", before, now)
		}
		patchSecretName(t, wssCredName)
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	// --- arm 4: mTLS is actually enforced by the proxy ---------------------------------
	t.Run("dropping the client certificate is refused by the proxy", func(t *testing.T) {
		// nginx answers a missing client certificate with 400 in the access log. Its
		// "client sent no required SSL certificate" line is logged at info level, and the sample
		// sets no error_log directive, so the default (error) hides it — the status code is the
		// observable that does not depend on the sample's log level.
		const proxyReject = `" 400 `
		before, beforeTLS := framesReceived(t), proxyLogCount(t, proxyReject)
		patchTLSMode(t, "tls") // mode=tls presents no client certificate
		waitCondition(t, "False/RemoteEndpointRejected", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: mTLS did not actually gate the connection", before, now)
		}
		if now := proxyLogCount(t, proxyReject); now == beforeTLS {
			t.Errorf("the proxy logged no new 400: the connection may have failed before it ever "+
				"reached the proxy, and this arm would still pass (count stayed at %d)", beforeTLS)
		}
		patchTLSMode(t, "mtls")
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	// --- arm 6: the server certificate is actually verified -----------------------------
	// The happy path proves a CORRECT chain works, which a client with verification disabled
	// also does. Only a chain the client should reject separates the two, so this is the arm
	// that fails if anyone reaches for InsecureSkipVerify.
	t.Run("a credential pinning the wrong CA refuses the server certificate", func(t *testing.T) {
		before := framesReceived(t)
		patchSecretCA(t, wssCredName, foreignCA(t))
		bumpGeneration(t, "foreign-ca")
		waitCondition(t, "False/ProviderPushFailed", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: the operator accepted a server certificate signed by a CA "+
				"its credential does not pin — server verification is not being enforced", before, now)
		}
		patchSecretCA(t, wssCredName, ca.certPEM)
		bumpGeneration(t, "restore-ca")
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	// --- arm 7: TLS + bearer, no client certificate ---------------------------------------
	t.Run("mode tls succeeds against a proxy that wants no client certificate", func(t *testing.T) {
		before := framesReceived(t)
		patchEndpointAndMode(t, fmt.Sprintf("%s.%s.svc:%d", wssSvc, wssNS, wssTLSPort), "tls")
		waitCondition(t, "True/Pushed", 3*time.Minute)
		if now := framesReceived(t); now <= before {
			t.Errorf("condition says pushed but frames stayed at %d: mode tls delivered nothing", before)
		}
		patchEndpointAndMode(t, fmt.Sprintf("%s.%s.svc:%d", wssSvc, wssNS, wssMTLSPort), "mtls")
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	t.Run("endpoint outside the admin allow-list is refused before the Secret is read", func(t *testing.T) {
		runEndpointAllowlistArm(t)
	})
}

// --- arm 5: #300 — an endpoint outside the admin allow-list is refused, and the Secret is
// never read. This is the confused-deputy boundary for credentialed pushes: the check has to
// happen BEFORE the credential is resolved, or the operator has already loaded the secret it
// was supposed to refuse to use.
func runEndpointAllowlistArm(t *testing.T) {
	t.Helper()
	if len(managerFlagList(t, "--remote-control-allowed-endpoint-hosts")) == 0 {
		t.Skip("manager runs without --remote-control-allowed-endpoint-hosts, so there is no allow-list " +
			"to violate; deploy with one to cover #300")
	}
	before := framesReceived(t)
	// Point at a host that is definitely not on the list AND at a Secret that does not exist.
	// The missing Secret is what makes this arm prove its own name: with a valid credential the
	// condition would read RemoteControlEndpointNotAllowed whether the endpoint was checked
	// before or after the Secret, so the arm could not tell the two orderings apart. With no
	// Secret to resolve, a credential-first implementation must report
	// RemoteControlCredentialUnavailable instead — so the endpoint reason is only reachable if
	// the endpoint really is checked first.
	if out, err := kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
		`-p={"spec":{"provider":{"remoteControl":{"endpoint":"not-allowed.example.invalid:8443",`+
			`"tls":{"mode":"mtls","secretName":"e2e-wss-no-such-secret"}}}}}`); err != nil {
		t.Fatalf("patch endpoint: %v: %s", err, out)
	}
	waitCondition(t, "False/RemoteControlEndpointNotAllowed", 3*time.Minute)
	if now := framesReceived(t); now != before {
		t.Errorf("frames went %d -> %d: a non-allow-listed endpoint must never be dialled", before, now)
	}
	if out, err := kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
		fmt.Sprintf(`-p={"spec":{"provider":{"remoteControl":{"endpoint":"%s.%s.svc:8443",`+
			`"tls":{"mode":"mtls","secretName":"%s"}}}}}`, wssSvc, wssNS, wssCredName)); err != nil {
		t.Fatalf("restore endpoint: %v: %s", err, out)
	}
	waitCondition(t, "True/Pushed", 3*time.Minute)
}

// patchSecretCA swaps the pinned trust anchor without touching the rest of the credential.
func patchSecretCA(t *testing.T, name string, caPEM []byte) {
	t.Helper()
	if out, err := kubectl(t, "-n", wssNS, "patch", "secret", name, "--type=merge",
		fmt.Sprintf(`-p={"data":{"ca.crt":%q}}`, base64.StdEncoding.EncodeToString(caPEM))); err != nil {
		t.Fatalf("patch ca.crt on %s: %v: %s", name, err, out)
	}
}

// patchEndpointAndMode moves the cell between the two listeners. Both are the same host, so the
// admin endpoint allow-list (which matches on host) is satisfied either way.
func patchEndpointAndMode(t *testing.T, endpoint, mode string) {
	t.Helper()
	if out, err := kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
		fmt.Sprintf(`-p={"spec":{"provider":{"remoteControl":{"endpoint":%q,"tls":{"mode":%q,"secretName":%q}}}}}`,
			endpoint, mode, wssCredName)); err != nil {
		t.Fatalf("patch endpoint/mode: %v: %s", err, out)
	}
}

func patchSecretToken(t *testing.T, name, token string) {
	t.Helper()
	if out, err := kubectl(t, "-n", wssNS, "patch", "secret", name, "--type=json",
		fmt.Sprintf(`-p=[{"op":"replace","path":"/data/token","value":%q}]`,
			base64Std(token))); err != nil {
		t.Fatalf("patch token: %v: %s", err, out)
	}
}

func patchSecretName(t *testing.T, secret string) {
	t.Helper()
	if out, err := kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
		fmt.Sprintf(`-p={"spec":{"provider":{"remoteControl":{"tls":{"secretName":%q}}}}}`, secret)); err != nil {
		t.Fatalf("patch secretName: %v: %s", err, out)
	}
}

func patchTLSMode(t *testing.T, mode string) {
	t.Helper()
	if out, err := kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
		fmt.Sprintf(`-p={"spec":{"provider":{"remoteControl":{"tls":{"mode":%q}}}}}`, mode)); err != nil {
		t.Fatalf("patch tls mode: %v: %s", err, out)
	}
}

// bumpGeneration forces a prompt re-evaluation. A Secret edit alone triggers nothing —
// the operator does not watch Secrets — so without this the test would wait on the
// ~3-minute ephemeris heartbeat instead of the change under test.
func bumpGeneration(t *testing.T, marker string) {
	t.Helper()
	if out, err := kubectl(t, "-n", wssNS, "patch", "ntncellconfig", wssCell, "--type=merge",
		fmt.Sprintf(`-p={"spec":{"ntn":{"cellSpecificKoffset":%d}}}`, 100+len(marker))); err != nil {
		t.Fatalf("bump generation: %v: %s", err, out)
	}
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
