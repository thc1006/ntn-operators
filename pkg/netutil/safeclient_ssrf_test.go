/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netutil

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestNewSafeHTTPClient_ProxyDisabled pins #199-C2: the transport must set Proxy=nil.
// If a proxy is honored, net/http dials the PROXY (which safeDialContext validates) while
// the real, CR-controlled target host travels in the request/CONNECT and is never
// IP-validated — silently bypassing the SSRF guard. Mutation: drop `transport.Proxy = nil`
// and this fails (the clone inherits ProxyFromEnvironment != nil).
func TestNewSafeHTTPClient_ProxyDisabled(t *testing.T) {
	client := NewSafeHTTPClient(5 * time.Second)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("Transport.Proxy must be nil so an egress proxy cannot bypass the SSRF dial guard")
	}
}

// TestNewSafeHTTPClient_BlocksHTTPSDowngradeRedirect pins #199-C3(b): a redirect that
// downgrades https->http (even to a public host) must be refused, so a source reached over
// TLS (e.g. Space-Track, whose session cookie would leak in cleartext) can't be redirected
// down to http. The downgrade check runs before any DNS lookup, so no network is needed.
func TestNewSafeHTTPClient_BlocksHTTPSDowngradeRedirect(t *testing.T) {
	client := NewSafeHTTPClient(5 * time.Second)
	orig, _ := http.NewRequest(http.MethodGet, "https://celestrak.org/gp", nil)
	req, _ := http.NewRequest(http.MethodGet, "http://celestrak.org/gp", nil)
	req = req.WithContext(context.Background())

	err := client.CheckRedirect(req, []*http.Request{orig})
	if err == nil {
		t.Fatal("expected an https->http downgrade redirect to be blocked")
	}
	if !containsStr(err.Error(), "downgrade") {
		t.Errorf("expected a downgrade error, got: %v", err)
	}
}

// TestNewSafeHTTPClient_AllowsHTTPSToHTTPSRedirect is the paired control: an https->https
// redirect to an allowlisted host is NOT a downgrade and is permitted (proves the block is
// specific to the scheme downgrade, not all redirects). The allowlist path returns before
// any DNS lookup.
func TestNewSafeHTTPClient_AllowsHTTPSToHTTPSRedirect(t *testing.T) {
	client := NewSafeHTTPClient(5*time.Second, WithPrivateHostAllowlist(ParseEndpointAllowlist("mirror.internal")))
	orig, _ := http.NewRequest(http.MethodGet, "https://celestrak.org/gp", nil)
	req, _ := http.NewRequest(http.MethodGet, "https://mirror.internal/gp", nil)
	req = req.WithContext(context.Background())

	if err := client.CheckRedirect(req, []*http.Request{orig}); err != nil {
		t.Fatalf("https->https redirect to an allowlisted host must be permitted, got: %v", err)
	}
}
