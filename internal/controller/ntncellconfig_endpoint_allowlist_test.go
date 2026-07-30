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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/netutil"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestCheckRemoteControlEndpointAllowed covers the SSRF egress gate's matching logic
// (#299): empty = permit-all (backward compatible), otherwise the endpoint host must be
// in the admin allowlist. IPv6 brackets are stripped by net.SplitHostPort; matching is
// case-insensitive; a malformed endpoint fails closed once the allowlist is set.
func TestCheckRemoteControlEndpointAllowed(t *testing.T) {
	tests := []struct {
		name      string
		allowlist string
		endpoint  string
		wantErr   bool
	}{
		{"empty allowlist permits any endpoint (backward compat)", "", "127.0.0.1:8001", false},
		{"empty allowlist permits an external host too", "", "gnb.example.com:9000", false},
		{"listed IPv4 host permitted", "127.0.0.1", "127.0.0.1:8001", false},
		{"listed DNS host permitted", "gnb.ran.svc", "gnb.ran.svc:8001", false},
		{"listed host, case-insensitive", "gnb.ran.svc", "GNB.RAN.SVC:8001", false},
		{"listed IPv6 host permitted (brackets stripped)", "::1", "[::1]:8001", false},
		{"unlisted host rejected", "gnb.ran.svc", "127.0.0.1:8001", true},
		{"unlisted IPv6 rejected", "gnb.ran.svc", "[fd00::1]:8001", true},
		{"one of several listed hosts permitted", "a.svc,gnb.ran.svc,b.svc", "gnb.ran.svc:8001", false},
		{"malformed endpoint fails closed when allowlist set", "gnb.ran.svc", "no-port", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &NTNCellConfigReconciler{
				RemoteControlEndpointAllowlist: netutil.ParseEndpointAllowlist(tc.allowlist),
			}
			err := r.checkRemoteControlEndpointAllowed(tc.endpoint)
			if tc.wantErr && err == nil {
				t.Fatalf("endpoint %q with allowlist %q: want error, got nil", tc.endpoint, tc.allowlist)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("endpoint %q with allowlist %q: want nil, got %v", tc.endpoint, tc.allowlist, err)
			}
		})
	}
}

// TestPushEphemerisUpdateIfNeeded_EndpointNotAllowed proves the gate is wired into the
// runtime push path with the right classification AND fires BEFORE the Secret read.
// The cell points remoteControl.tls at a MISSING Secret and its endpoint is outside the
// allowlist: the failure must be RemoteControlEndpointNotAllowed (not a credential
// error), which can only happen if the endpoint gate runs first. The reason is permanent
// (clears on a watched spec edit), so it must NOT self-requeue.
func TestPushEphemerisUpdateIfNeeded_EndpointNotAllowed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{
		Client:                         c,
		APIReader:                      c,
		RemoteControlEndpointAllowlist: netutil.ParseEndpointAllowlist("gnb.ran.svc"),
	}
	cc := ccWithRemoteControl() // endpoint 127.0.0.1:8001, NOT in the allowlist
	// Point at a Secret that does not exist — if the gate ran AFTER the Secret read,
	// this would surface a credential error instead of an endpoint error.
	cc.Spec.Provider.RemoteControl.TLS = &ntnv1alpha1.RemoteControlTLS{Mode: "tls", SecretName: "missing"}

	_, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, &provider.MockProvider{})
	if err == nil {
		t.Fatal("an endpoint outside the allowlist must fail the push")
	}
	reason := ephemerisPushConditionReason(err)
	if reason != ephemerisReasonRemoteControlEndpointNotAllowed {
		t.Fatalf("disallowed endpoint must classify as %q (before the Secret read), got %q",
			ephemerisReasonRemoteControlEndpointNotAllowed, reason)
	}
	if ephemerisPushShouldRequeue(reason) {
		t.Fatalf("a disallowed endpoint is a permanent config error and must not self-requeue")
	}
}

// TestPushEphemerisUpdateIfNeeded_EndpointAllowed_Proceeds confirms the gate does not
// block a legitimate endpoint: with the cell's host in the allowlist (and no TLS), the
// runtime push reaches the provider and succeeds.
func TestPushEphemerisUpdateIfNeeded_EndpointAllowed_Proceeds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	eph := ephWithPropagatedState(time.Now().Add(time.Hour).UnixMilli())
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{
		Client:                         c,
		RemoteControlEndpointAllowlist: netutil.ParseEndpointAllowlist("127.0.0.1"),
	}
	cc := ccWithRemoteControl() // endpoint 127.0.0.1:8001, IS in the allowlist
	mock := &provider.MockProvider{}

	pushed, _, err := r.pushEphemerisUpdateIfNeeded(context.Background(), cc, &cc.Spec, mock)
	if err != nil {
		t.Fatalf("an allow-listed endpoint must not be blocked: %v", err)
	}
	if !pushed {
		t.Fatal("an allow-listed endpoint must complete the runtime push")
	}
	if mock.RuntimeCalls != 1 {
		t.Fatalf("provider must receive exactly one runtime push, got %d", mock.RuntimeCalls)
	}
}
