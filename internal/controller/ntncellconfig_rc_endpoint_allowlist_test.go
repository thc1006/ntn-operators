/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/netutil"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestPushEphemerisUpdateIfNeeded_RemoteControlEndpointAllowlist pins the #251 / ADR-0009 endpoint
// egress boundary: EVERY push — credentialed remoteControl.tls or plaintext — is refused BEFORE the
// Secret is read when its endpoint host is outside --remote-control-allowed-endpoint-hosts (a
// credentialed one would exfiltrate a labelled credential; a plaintext one would relay through the
// operator to an internal host). An empty allow-list permits any endpoint (opt-in). Mutation: drop the
// allow-list Check and the "attacker endpoint" cases push (the mock is called), failing the assertions.
func TestPushEphemerisUpdateIfNeeded_RemoteControlEndpointAllowlist(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := selfSignedPEM(t)

	// run performs one push with the given allow-list CSV and endpoint. withTLS sets remoteControl.tls
	// (makes it a credentialed push); withSecret provisions the opted-in Secret. Returns the push error
	// and whether the provider was actually invoked (i.e. the credential left the operator).
	run := func(t *testing.T, allow, endpoint string, withTLS, withSecret bool) (error, bool) {
		t.Helper()
		eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
		objs := []client.Object{eph}
		cc := ccWithRemoteControl()
		cc.Spec.Provider.RemoteControl.Endpoint = endpoint
		if withTLS {
			cc.Spec.Provider.RemoteControl.TLS = &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "cred"}
		}
		if withSecret {
			objs = append(objs, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cred", Namespace: eph.Namespace,
					Labels: map[string]string{remoteControlCredentialLabel: "true"},
				},
				Data: map[string][]byte{"ca.crt": certPEM},
			})
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
		r := &NTNCellConfigReconciler{
			Client: c, APIReader: c,
			RemoteControlAllowedHosts: netutil.ParseEndpointAllowlist(allow),
		}
		mock := &provider.MockProvider{}
		_, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
		return err, mock.LastTarget != nil
	}

	t.Run("credentialed push to a non-allowlisted endpoint is refused before the Secret is read", func(t *testing.T) {
		// No Secret is provisioned on purpose: the endpoint check must run FIRST, so the refusal is
		// errRemoteControlEndpointNotAllowed — NOT the credential-unavailable error a missing Secret
		// would give if resolution ran first. This proves there is no "Secret is valid" oracle.
		err, pushed := run(t, "gnb.allowed.svc", "attacker.evil.example:8001", true, false)
		if err == nil || !errors.Is(err, errRemoteControlEndpointNotAllowed) {
			t.Fatalf("want errRemoteControlEndpointNotAllowed (endpoint checked before the Secret read), got %v", err)
		}
		if reason := ephemerisPushConditionReason(err); reason != ephemerisReasonRemoteControlEndpointNotAllowed {
			t.Fatalf("must classify as %q, got %q", ephemerisReasonRemoteControlEndpointNotAllowed, reason)
		}
		if pushed {
			t.Fatal("the credential must NOT be sent to a non-allowlisted endpoint (the provider was called)")
		}
	})

	t.Run("credentialed push to an allowlisted endpoint proceeds", func(t *testing.T) {
		err, pushed := run(t, "gnb.allowed.svc", "gnb.allowed.svc:8001", true, true)
		if err != nil {
			t.Fatalf("an allowlisted credentialed push must proceed, got %v", err)
		}
		if !pushed {
			t.Fatal("an allowlisted credentialed push must reach the provider")
		}
	})

	t.Run("empty allow-list permits any endpoint (opt-in)", func(t *testing.T) {
		err, pushed := run(t, "", "attacker.evil.example:8001", true, true)
		if err != nil {
			t.Fatalf("an empty allow-list must permit any endpoint, got %v", err)
		}
		if !pushed {
			t.Fatal("an empty allow-list must let the credentialed push proceed")
		}
	})

	t.Run("plaintext push to a non-allowlisted endpoint is ALSO refused (SSRF relay)", func(t *testing.T) {
		err, pushed := run(t, "gnb.allowed.svc", "attacker.evil.example:8001", false, false)
		if err == nil || !errors.Is(err, errRemoteControlEndpointNotAllowed) {
			t.Fatalf("a plaintext push to a non-allowlisted host must be refused, got %v", err)
		}
		if pushed {
			t.Fatal("the operator must NOT relay a plaintext push to a non-allowlisted endpoint")
		}
	})

	t.Run("plaintext push to an allowlisted endpoint proceeds", func(t *testing.T) {
		err, pushed := run(t, "gnb.allowed.svc", "gnb.allowed.svc:8001", false, false)
		if err != nil {
			t.Fatalf("an allowlisted plaintext push must proceed, got %v", err)
		}
		if !pushed {
			t.Fatal("an allowlisted plaintext push must reach the provider")
		}
	})
}
