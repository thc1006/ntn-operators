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

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// selfSignedPEM returns a fresh self-signed cert + key PEM, usable as both a CA
// (ca.crt) and a client cert/key (tls.crt/tls.key) for the resolver test.
func selfSignedPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gnb-test"},
		NotBefore:             time.Unix(1_700_000_000, 0),
		NotAfter:              time.Unix(1_900_000_000, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestResolveRemoteControlTLS(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := selfSignedPEM(t)
	const ns = "ns1"

	newR := func(objs ...runtime.Object) *NTNCellConfigReconciler {
		b := fake.NewClientBuilder().WithScheme(scheme)
		for _, o := range objs {
			b = b.WithRuntimeObjects(o)
		}
		c := b.Build()
		// Exercise the uncached-read path: APIReader is what production wires.
		return &NTNCellConfigReconciler{Client: c, APIReader: c}
	}
	// Correctly owner-opted-in credential Secret (the label is now mandatory).
	secret := func(name string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns,
				Labels: map[string]string{remoteControlCredentialLabel: "true"},
			},
			Data: data,
		}
	}
	ctx := context.Background()

	t.Run("no tls block → plaintext (nil config, no token)", func(t *testing.T) {
		cfg, tok, err := newR().resolveRemoteControlTLS(ctx, ns, &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1"})
		if err != nil || cfg != nil || tok != "" {
			t.Fatalf("want (nil,\"\",nil), got (%v,%q,%v)", cfg, tok, err)
		}
	})

	t.Run("mode=tls → RootCAs, token, ServerName from host, TLS1.2, no client cert", func(t *testing.T) {
		r := newR(secret("s", map[string][]byte{"ca.crt": certPEM, "token": []byte("shhh")}))
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "gnb.internal:8001",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "s"}}
		cfg, tok, err := r.resolveRemoteControlTLS(ctx, ns, rc)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cfg.RootCAs == nil {
			t.Error("RootCAs should be set from ca.crt")
		}
		if cfg.ServerName != "gnb.internal" {
			t.Errorf("ServerName=%q, want gnb.internal", cfg.ServerName)
		}
		if cfg.MinVersion != tls.VersionTLS12 {
			t.Errorf("MinVersion=%d, want TLS1.2", cfg.MinVersion)
		}
		if len(cfg.Certificates) != 0 {
			t.Error("mode=tls must not load a client cert")
		}
		if tok != "shhh" {
			t.Errorf("token=%q, want shhh", tok)
		}
	})

	t.Run("serverName override wins over the endpoint host", func(t *testing.T) {
		r := newR(secret("s", map[string][]byte{"ca.crt": certPEM}))
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "10.0.0.5:8001",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "s", ServerName: "gnb.svc"}}
		cfg, _, err := r.resolveRemoteControlTLS(ctx, ns, rc)
		if err != nil || cfg.ServerName != "gnb.svc" {
			t.Fatalf("ServerName=%q err=%v, want gnb.svc", cfg.ServerName, err)
		}
	})

	t.Run("mode=mtls → client certificate loaded", func(t *testing.T) {
		r := newR(secret("s", map[string][]byte{"ca.crt": certPEM, "tls.crt": certPEM, "tls.key": keyPEM}))
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "mtls", SecretName: "s"}}
		cfg, _, err := r.resolveRemoteControlTLS(ctx, ns, rc)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(cfg.Certificates) != 1 {
			t.Fatalf("mode=mtls must load exactly one client cert, got %d", len(cfg.Certificates))
		}
	})

	t.Run("missing secret → error", func(t *testing.T) {
		r := newR()
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "absent"}}
		if _, _, err := r.resolveRemoteControlTLS(ctx, ns, rc); err == nil {
			t.Fatal("expected an error for a missing secret")
		}
	})

	t.Run("mode=mtls without tls.crt/tls.key → error", func(t *testing.T) {
		r := newR(secret("s", map[string][]byte{"ca.crt": certPEM}))
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "mtls", SecretName: "s"}}
		if _, _, err := r.resolveRemoteControlTLS(ctx, ns, rc); err == nil {
			t.Fatal("expected an error for mtls without a client cert")
		}
	})

	t.Run("invalid ca.crt PEM → error", func(t *testing.T) {
		r := newR(secret("s", map[string][]byte{"ca.crt": []byte("not a pem")}))
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "s"}}
		if _, _, err := r.resolveRemoteControlTLS(ctx, ns, rc); err == nil {
			t.Fatal("expected an error for an invalid ca.crt PEM")
		}
	})

	// SECURITY — confused-deputy gates.
	t.Run("secret without the opt-in label → rejected", func(t *testing.T) {
		unlabelled := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: ns}, // no remoteControlCredentialLabel
			Data:       map[string][]byte{"ca.crt": certPEM, "token": []byte("shhh")},
		}
		r := newR(unlabelled)
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "s"}}
		_, tok, err := r.resolveRemoteControlTLS(ctx, ns, rc)
		if err == nil {
			t.Fatal("an un-opted-in Secret (no owner label) must be rejected — a CR author must not point the operator at an arbitrary Secret")
		}
		if tok != "" {
			t.Fatalf("no token may be returned on rejection, got %q", tok)
		}
	})

	t.Run("service-account-token Secret → rejected even if labelled", func(t *testing.T) {
		saTok := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "s", Namespace: ns,
				Labels: map[string]string{remoteControlCredentialLabel: "true"}, // even opted-in
			},
			Type: corev1.SecretTypeServiceAccountToken,
			Data: map[string][]byte{"token": []byte("apiserver-cred"), "ca.crt": certPEM},
		}
		r := newR(saTok)
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "s"}}
		_, tok, err := r.resolveRemoteControlTLS(ctx, ns, rc)
		if err == nil {
			t.Fatal("a service-account-token Secret must never be usable as a remote-control credential — its token is an apiserver credential")
		}
		if tok != "" {
			t.Fatalf("the apiserver token must never be returned, got %q", tok)
		}
	})

	t.Run("bootstrap-token Secret → rejected even if labelled", func(t *testing.T) {
		bootstrap := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "s", Namespace: ns,
				Labels: map[string]string{remoteControlCredentialLabel: "true"},
			},
			Type: secretTypeBootstrapToken,
			Data: map[string][]byte{"token": []byte("bootstrap-cred"), "ca.crt": certPEM},
		}
		r := newR(bootstrap)
		rc := &ntnv1alpha1.RemoteControlRef{Endpoint: "h:1",
			TLS: &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "s"}}
		_, tok, err := r.resolveRemoteControlTLS(ctx, ns, rc)
		if err == nil || tok != "" {
			t.Fatal("a bootstrap-token Secret must never be usable as a remote-control credential")
		}
	})
}

// TestEphemerisPushRequeue_RemoteControlCredentialSelfHeals pins that a
// remoteControl.tls credential error requeues on a low-frequency SELF-HEAL poll — not a tight
// per-minute retry, and not never. Its fix is a Secret edit, which the controller does NOT watch
// (only NTNCellConfig + SatelliteEphemeris are watched; secrets are get-only), so without a
// self-requeue the cell recovers only on the next SatelliteEphemeris heartbeat fan-out, and never
// if the producer stalls. A transient ProviderPushFailed still tight-requeues (control).
func TestEphemerisPushRequeue_RemoteControlCredentialSelfHeals(t *testing.T) {
	if !ephemerisPushShouldRequeue(ephemerisReasonRemoteControlCredential) {
		t.Error("RemoteControlCredentialUnavailable must self-requeue: its Secret fix is not watched, so no requeue would strand the cell")
	}
	if got := ephemerisPushRequeueInterval(ephemerisReasonRemoteControlCredential); got != remoteControlConfigRequeue {
		t.Errorf("credential requeue = %v, want %v (a slow self-heal poll, not a tight retry)", got, remoteControlConfigRequeue)
	}
	// A generic ProviderPushFailed (an actual gNB push failure, NOT a credential failure) still
	// tight-requeues — control that the 5m interval is credential-specific, not a blanket slowdown.
	if !ephemerisPushShouldRequeue(ephemerisReasonProviderPushFailed) {
		t.Error("a transient ProviderPushFailed must still requeue (control)")
	}
	if got := ephemerisPushRequeueInterval(ephemerisReasonProviderPushFailed); got != time.Minute {
		t.Errorf("transient requeue = %v, want 1m (control)", got)
	}
}

// TestPushEphemerisUpdateIfNeeded_BadCredentialClassifiesRemoteControlCredential proves the input
// to the self-heal requeue: an un-opted-in (unlabelled) remoteControl.tls Secret makes the runtime
// push fail with the uniform RemoteControlCredentialUnavailable reason. Combined with the
// requeue-classification above, this is the end-to-end guarantee — a bad Secret schedules a
// bounded self-heal retry rather than stranding the cell, since a Secret edit does not re-trigger
// reconcile.
func TestPushEphemerisUpdateIfNeeded_BadCredentialClassifiesRemoteControlCredential(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	// An un-opted-in Secret (no owner label) is a deterministic credential-invalid error; it lives
	// in the ephemeris namespace, where resolveRemoteControlTLS looks it up.
	badSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cred", Namespace: eph.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte("x")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph, badSecret).Build()
	r := &NTNCellConfigReconciler{Client: c, APIReader: c}

	cc := ccWithRemoteControl()
	cc.Spec.Provider.RemoteControl.TLS = &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "cred"}

	_, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, &provider.MockProvider{})
	if err == nil {
		t.Fatal("an un-opted-in remoteControl.tls Secret must fail the push")
	}
	reason := ephemerisPushConditionReason(err)
	if reason != ephemerisReasonRemoteControlCredential {
		t.Fatalf("a bad credential must classify as %q, got %q", ephemerisReasonRemoteControlCredential, reason)
	}
	// And that reason self-heal-requeues (the Secret fix is unwatched), so the cell is not stranded.
	if !ephemerisPushShouldRequeue(reason) || ephemerisPushRequeueInterval(reason) != remoteControlConfigRequeue {
		t.Fatalf("a bad credential must schedule a %v self-heal requeue, got requeue=%v interval=%v",
			remoteControlConfigRequeue, ephemerisPushShouldRequeue(reason), ephemerisPushRequeueInterval(reason))
	}
}

// TestPushEphemerisUpdateIfNeeded_SecretFixRecovers is the end-to-end coverage the review asked for
// on the RemoteControlConfigInvalid self-heal (#282): patch ONLY the Secret and observe recovery. A
// remoteControl.tls Secret missing the opt-in label fails the push as a credential-config error;
// adding the label (no spec edit, no ephemeris change) makes the very next push — which is exactly
// what the 5m self-heal requeue re-runs — succeed. That is what lets the cell recover even when the
// SatelliteEphemeris producer is stalled (no heartbeat fan-out to enqueue it).
func TestPushEphemerisUpdateIfNeeded_SecretFixRecovers(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := selfSignedPEM(t)
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	// Valid cert material, but NOT opted in (no owner label) → RemoteControlConfigInvalid.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cred", Namespace: eph.Namespace},
		Data:       map[string][]byte{"ca.crt": certPEM},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph, secret).Build()
	r := &NTNCellConfigReconciler{Client: c, APIReader: c}
	cc := ccWithRemoteControl()
	cc.Spec.Provider.RemoteControl.TLS = &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "cred"}

	// Push 1: the un-opted-in Secret fails the push as a credential-config error.
	if _, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, &provider.MockProvider{}); err == nil ||
		ephemerisPushConditionReason(err) != ephemerisReasonRemoteControlCredential {
		t.Fatalf("push 1: want a RemoteControlCredentialUnavailable failure, got %v", err)
	}

	// Patch ONLY the Secret — add the opt-in label. No spec edit, no ephemeris change.
	secret.Labels = map[string]string{remoteControlCredentialLabel: "true"}
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatalf("patch secret: %v", err)
	}

	// Push 2 (what the 5m self-heal requeue re-runs): the now-opted-in Secret resolves → recovered.
	mock := &provider.MockProvider{}
	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil || !pushed || mock.RuntimeCalls != 1 {
		t.Fatalf("push 2 (post-Secret-fix) must recover with a successful push; got pushed=%v err=%v calls=%d",
			pushed, err, mock.RuntimeCalls)
	}
}

// TestPushEphemerisUpdateIfNeeded_CredentialFailuresUniform is the security regression the review
// asked for: EVERY remoteControl.tls resolution failure — a missing/unreadable Secret and a
// present-but-invalid one alike — must surface the IDENTICAL public reason, message, AND requeue
// cadence on the CR, so a principal who can write the NTNCellConfig but not read Secrets cannot
// distinguish a missing Secret from a present-but-bad one (a Secret existence/type oracle; #219).
// The uniform cadence matters because EphemerisPushErrorsTotal counts per-failure, so a split
// interval would leak the same distinction via the metric's increment rate.
func TestPushEphemerisUpdateIfNeeded_CredentialFailuresUniform(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := selfSignedPEM(t)

	const secretName = "cred"
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	ns := eph.Namespace // resolveRemoteControlTLS looks the Secret up in the ephemeris namespace
	labelled := map[string]string{remoteControlCredentialLabel: "true"}
	mkSecret := func(labels map[string]string, typ corev1.SecretType, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Labels: labels},
			Type:       typ,
			Data:       data,
		}
	}

	cases := []struct {
		name    string
		mode    string
		secret  *corev1.Secret // nil ⇒ Secret ABSENT (missing)
		readErr bool           // inject a Secret GET failure (unreadable / forbidden)
	}{
		{name: "missing", mode: "tls", secret: nil},
		{name: "read-failure", mode: "tls", readErr: true,
			secret: mkSecret(labelled, "", map[string][]byte{"ca.crt": certPEM})},
		{name: "unlabelled", mode: "tls",
			secret: mkSecret(nil, "", map[string][]byte{"ca.crt": certPEM})},
		{name: "service-account-token", mode: "tls",
			secret: mkSecret(labelled, corev1.SecretTypeServiceAccountToken, map[string][]byte{"token": []byte("apiserver"), "ca.crt": certPEM})},
		{name: "bootstrap-token", mode: "tls",
			secret: mkSecret(labelled, secretTypeBootstrapToken, map[string][]byte{"token": []byte("bootstrap"), "ca.crt": certPEM})},
		{name: "malformed-ca", mode: "tls",
			secret: mkSecret(labelled, "", map[string][]byte{"ca.crt": []byte("not-pem")})},
		{name: "malformed-client-cert", mode: "mtls",
			secret: mkSecret(labelled, "", map[string][]byte{"ca.crt": certPEM, "tls.crt": []byte("bad"), "tls.key": []byte("bad")})},
	}

	wantMsg := errRemoteControlCredentialUnavailable.Error()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{eph.DeepCopy()}
			if tc.secret != nil {
				objs = append(objs, tc.secret)
			}
			b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
			if tc.readErr {
				b = b.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return context.DeadlineExceeded // a transient/unreadable Secret GET
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})
			}
			c := b.Build()
			r := &NTNCellConfigReconciler{Client: c, APIReader: c}
			cc := ccWithRemoteControl()
			cc.Spec.Provider.RemoteControl.TLS = &ntnv1alpha1.RemoteControlTLS{Mode: tc.mode, SecretName: secretName}

			_, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, &provider.MockProvider{})
			if err == nil {
				t.Fatalf("%s: expected a credential failure", tc.name)
			}
			// UNIFORM public reason — no Secret existence/type oracle.
			if got := ephemerisPushConditionReason(err); got != ephemerisReasonRemoteControlCredential {
				t.Errorf("%s: public reason = %q, want the uniform %q", tc.name, got, ephemerisReasonRemoteControlCredential)
			}
			// UNIFORM public message.
			if got := err.Error(); got != wantMsg {
				t.Errorf("%s: public message = %q, want the uniform %q", tc.name, got, wantMsg)
			}
			// UNIFORM self-heal cadence — so the per-failure error metric's increment rate cannot
			// distinguish the cases either.
			if iv := ephemerisPushRequeueInterval(ephemerisPushConditionReason(err)); iv != remoteControlConfigRequeue {
				t.Errorf("%s: requeue interval = %v, want the uniform %v", tc.name, iv, remoteControlConfigRequeue)
			}
		})
	}
}
