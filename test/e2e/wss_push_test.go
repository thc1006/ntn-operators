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
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	wssNS       = "default" // the celestrak mock DNS + --ephemeris-allowed-private-hosts are pinned here
	wssCell     = "e2e-wss-cell"
	wssSatEph   = "e2e-wss-eph"
	wssGNB      = "e2e-wss-gnb"
	wssSvc      = "e2e-wss-gnb"
	wssCredName = "e2e-wss-cred"
	wssNoCA     = "e2e-wss-cred-no-ca"
	wssToken    = "e2e-wss-bearer-token"
	wssNorad    = 25544
	wssGPMock   = "e2e-wss-gp"
)

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

// framesReceived counts ntn_config_update frames the PLAINTEXT backend actually accepted.
// Read from the backend's own log, so it reflects the wire, not the operator's opinion.
func framesReceived(t *testing.T) int {
	t.Helper()
	out, _ := kubectl(t, "-n", wssNS, "logs", "deploy/"+wssGNB, "-c", "gnb", "--tail=-1")
	return strings.Count(out, "FRAME ")
}

func lastFrame(t *testing.T) string {
	t.Helper()
	out, _ := kubectl(t, "-n", wssNS, "logs", "deploy/"+wssGNB, "-c", "gnb", "--tail=-1")
	var last string
	for l := range strings.SplitSeq(out, "\n") {
		if _, after, ok := strings.Cut(l, "FRAME "); ok {
			last = after
		}
	}
	return last
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

// sampleNginxConf extracts the nginx config from the SHIPPED sample so the sample itself is
// under test. A copy inlined here would let the sample rot while the test stayed green.
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
	s := string(b)
	_, body, ok := strings.Cut(s, "  nginx.conf: |")
	if !ok {
		t.Fatalf("sample no longer contains an 'nginx.conf: |' block — update this test with it")
	}
	if before, _, ok := strings.Cut(body, "\n---"); ok {
		body = before
	}
	// Strip the block indent and substitute the sample's placeholders.
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimPrefix(l, "    "))
	}
	conf := strings.Join(out, "\n")
	conf = strings.ReplaceAll(conf, "REPLACE_WITH_TOKEN", wssToken)
	if strings.Contains(conf, "REPLACE_WITH") {
		t.Fatalf("sample nginx.conf still has an unsubstituted placeholder:\n%s", conf)
	}
	return conf
}

// ---------------------------------------------------------------- deployment

func deployGPMock(t *testing.T) {
	t.Helper()
	_, _ = kubectl(t, "-n", wssNS, "delete", "configmap", wssGPMock+"-fixture", "--ignore-not-found")
	cmd := exec.Command("kubectl", "-n", wssNS, "create", "configmap", wssGPMock+"-fixture",
		"--from-file=gp.json=/dev/stdin")
	cmd.Stdin = strings.NewReader(freshGPJSON())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create gp fixture: %v: %s", err, out)
	}
	kubectlApply(t, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  replicas: 1
  selector: {matchLabels: {app: %[1]s}}
  template:
    metadata: {labels: {app: %[1]s}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.29-alpine
          ports: [{containerPort: 80, name: http}]
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
`, wssGPMock, wssNS))
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

	_, _ = kubectl(t, "-n", wssNS, "delete", "configmap", wssGNB+"-src", wssGNB+"-conf", "--ignore-not-found")
	if out, err := kubectl(t, "-n", wssNS, "create", "configmap", wssGNB+"-src", "--from-file=main.go="+stub); err != nil {
		t.Fatalf("create stub configmap: %v: %s", err, out)
	}
	cmd := exec.Command("kubectl", "-n", wssNS, "create", "configmap", wssGNB+"-conf", "--from-file=nginx.conf=/dev/stdin")
	cmd.Stdin = strings.NewReader(sampleNginxConf(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create nginx configmap: %v: %s", err, out)
	}
	_, _ = kubectl(t, "-n", wssNS, "delete", "secret", wssGNB+"-tls", "--ignore-not-found")
	if err := createSecret(t, wssGNB+"-tls", map[string][]byte{
		"tls.crt": server.certPEM, "tls.key": server.keyPEM, "ca.crt": ca.certPEM,
	}, false); err != nil {
		t.Fatalf("create proxy tls secret: %v", err)
	}

	kubectlApply(t, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  replicas: 1
  selector: {matchLabels: {app: %[1]s}}
  template:
    metadata: {labels: {app: %[1]s}}
    spec:
      containers:
        - name: gnb
          image: golang:1.24-alpine
          command: ["go","run","/src/main.go"]
          env:
            - {name: GOCACHE, value: /tmp/gocache}
            - {name: GOFLAGS, value: -mod=mod}
            - {name: HOME,    value: /tmp}
          ports: [{containerPort: 8001}]
          volumeMounts:
            - {name: src, mountPath: /src}
            - {name: tmp, mountPath: /tmp}
        - name: proxy
          image: nginx:1.29-alpine
          ports: [{containerPort: 8443, name: wss}]
          volumeMounts:
            - {name: conf,  mountPath: /etc/nginx/nginx.conf, subPath: nginx.conf}
            - {name: certs, mountPath: /certs, readOnly: true}
      volumes:
        - {name: src,   configMap: {name: %[1]s-src}}
        - {name: conf,  configMap: {name: %[1]s-conf}}
        - {name: certs, secret: {secretName: %[1]s-tls}}
        - {name: tmp,   emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata: {name: %[3]s, namespace: %[2]s}
spec:
  selector: {app: %[1]s}
  ports: [{name: wss, port: 8443, targetPort: 8443}]
`, wssGNB, wssNS, wssSvc))
	if _, err := kubectl(t, "-n", wssNS, "rollout", "status", "deploy/"+wssGNB, "--timeout=240s"); err != nil {
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

// ensureManagerAllowsGPHost makes the suite self-contained: the GP mock lives on a private
// ClusterIP, so without this flag the SSRF-safe fetcher blocks it and the ephemeris never
// propagates — a failure that looks like "the push is broken" but is not. Patching here
// rather than requiring a special deploy keeps the suite runnable against any already-
// deployed chart, which is what `make test-e2e-wss` promises.
func ensureManagerAllowsGPHost(t *testing.T) {
	t.Helper()
	ns := os.Getenv("WSS_MANAGER_NAMESPACE")
	if ns == "" {
		ns = "ntn-operators-system"
	}
	want := "--ephemeris-allowed-private-hosts=" + wssGPMock + "." + wssNS + ".svc.cluster.local"
	out, err := kubectl(t, "-n", ns, "get", "deploy", "-o",
		`jsonpath={.items[0].metadata.name}{"\t"}{.items[0].spec.template.spec.containers[0].args}`)
	if err != nil {
		t.Fatalf("no manager Deployment in namespace %q — deploy the chart first "+
			"(set WSS_MANAGER_NAMESPACE if it lives elsewhere): %v", ns, err)
	}
	parts := strings.SplitN(strings.TrimSpace(out), "\t", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected manager describe output: %q", out)
	}
	name, args := parts[0], parts[1]
	if strings.Contains(args, want) {
		return
	}
	t.Logf("patching manager %s/%s with %s", ns, name, want)
	if out, err := kubectl(t, "-n", ns, "patch", "deploy", name, "--type=json",
		fmt.Sprintf(`-p=[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":%q}]`, want)); err != nil {
		t.Fatalf("patch manager args: %v: %s", err, out)
	}
	if out, err := kubectl(t, "-n", ns, "rollout", "status", "deploy/"+name, "--timeout=240s"); err != nil {
		t.Fatalf("manager did not roll out after the args patch: %v: %s", err, out)
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
	ca, server, client := issueCerts(t)

	t.Cleanup(func() {
		_, _ = kubectl(t, "-n", wssNS, "delete", "ntncellconfig", wssCell, "--ignore-not-found", "--wait=false")
		_, _ = kubectl(t, "-n", wssNS, "delete", "satelliteephemeris", wssSatEph, "--ignore-not-found", "--wait=false")
		_, _ = kubectl(t, "-n", wssNS, "delete", "deploy", wssGNB, wssGPMock, "--ignore-not-found", "--wait=false")
		_, _ = kubectl(t, "-n", wssNS, "delete", "svc", wssSvc, wssGPMock, "--ignore-not-found", "--wait=false")
		_, _ = kubectl(t, "-n", wssNS, "delete", "secret", wssCredName, wssNoCA, wssGNB+"-tls", "--ignore-not-found")
		_, _ = kubectl(t, "-n", wssNS, "delete", "configmap",
			wssGNB+"-src", wssGNB+"-conf", wssGPMock+"-fixture", "--ignore-not-found")
	})

	t.Log("standing up the GP source and the gNB behind its TLS sidecar")
	deployGPMock(t)
	ensureManagerAllowsGPHost(t)
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
      endpoint: "%[3]s.%[2]s.svc:8443"
      tls: {mode: mtls, secretName: %[4]s}
  cellID: {plmn: "00101", nci: 6733824}
  ephemerisRef: %[5]s
  ephemerisNoradID: %[6]d
  ntn:
    cellSpecificKoffset: 100
    ephemerisECEF: {posX: 1000000, posY: 2000000, posZ: 3000000, velX: 0, velY: 0, velZ: 0}
`, wssCell, wssNS, wssSvc, wssCredName, wssSatEph, wssNorad))

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
		if env.Cells[0].PLMN != "00101" || env.Cells[0].NCI != 6733824 {
			t.Errorf("frame targets the wrong cell: %+v", env.Cells[0])
		}
		if env.Cells[0].Epoch <= time.Now().UnixMilli() {
			t.Errorf("epoch %d is not in the future — OCUDU rejects past epochs", env.Cells[0].Epoch)
		}
		t.Logf("frame on the wire: %s", frame)
	})

	// --- arm 2: the bearer is genuinely checked, and a 401 is classified as retry-later -
	t.Run("wrong bearer is RemoteEndpointRejected, not a permanent rejection", func(t *testing.T) {
		before := framesReceived(t)
		patchSecretToken(t, wssCredName, "definitely-not-the-token")
		bumpGeneration(t, "wrong-bearer")
		waitCondition(t, "False/RemoteEndpointRejected", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: a rejected handshake must not deliver a payload", before, now)
		}
		patchSecretToken(t, wssCredName, wssToken)
		bumpGeneration(t, "restore-bearer")
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	// --- arm 3: #313 — a bearer with no pinned CA is refused before any dial -----------
	t.Run("token without ca.crt is refused before the dial (#313)", func(t *testing.T) {
		before := framesReceived(t)
		patchSecretName(t, wssNoCA)
		waitCondition(t, "False/RemoteControlCredentialUnavailable", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: nothing may be dialled when the CA is unpinned", before, now)
		}
		patchSecretName(t, wssCredName)
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})

	// --- arm 4: mTLS is actually enforced by the proxy ---------------------------------
	t.Run("dropping the client certificate is refused by the proxy", func(t *testing.T) {
		before := framesReceived(t)
		patchTLSMode(t, "tls") // mode=tls presents no client certificate
		waitCondition(t, "False/RemoteEndpointRejected", 3*time.Minute)
		if now := framesReceived(t); now != before {
			t.Errorf("frames went %d -> %d: mTLS did not actually gate the connection", before, now)
		}
		patchTLSMode(t, "mtls")
		waitCondition(t, "True/Pushed", 3*time.Minute)
	})
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
