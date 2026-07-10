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

package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		// Private ranges.
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"127.0.0.1", true},
		{"169.254.169.254", true}, // AWS metadata
		{"0.0.0.0", true},
		{"100.64.0.1", true}, // Carrier-grade NAT
		{"192.0.0.1", true},  // IETF protocol
		{"198.18.0.1", true}, // Benchmarking

		// Public ranges.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com

		// IPv6.
		{"::1", true},                   // loopback
		{"fe80::1", true},               // link-local
		{"fc00::1", true},               // unique local
		{"2001:4860:4860::8888", false}, // Google DNS

		// I-25 additions — previously bypassed the guard (all demonstrated in
		// the audit as blocked=false).
		{"::", true},                     // IPv6 unspecified
		{"224.0.0.1", true},              // IPv4 multicast (all-hosts)
		{"239.255.255.255", true},        // IPv4 multicast (upper)
		{"240.0.0.1", true},              // IPv4 reserved / class-E
		{"255.255.255.255", true},        // IPv4 limited broadcast
		{"ff02::1", true},                // IPv6 multicast (all-nodes)
		{"64:ff9b::a00:1", true},         // NAT64 well-known embedding 10.0.0.1
		{"2002:a00:1::1", true},          // 6to4 embedding 10.0.0.1
		{"64:ff9b:1::1", true},           // NAT64 local-use (RFC 8215)
		{"::ffff:10.0.0.1", true},        // IPv4-mapped private
		{"::ffff:169.254.169.254", true}, // IPv4-mapped cloud metadata

		// Non-breaking: NAT64/6to4 transition of a PUBLIC IPv4 must stay allowed
		// (extract-and-check, not a blanket prefix block), so IPv6-only clusters
		// reaching public CelesTrak/Prometheus keep working.
		{"64:ff9b::808:808", false}, // NAT64 embedding 8.8.8.8 (public)
		{"2002:808:808::1", false},  // 6to4 embedding 8.8.8.8 (public)
		{"::ffff:8.8.8.8", false},   // IPv4-mapped public
	}

	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("invalid IP: %s", tc.ip)
		}
		got := IsPrivateIP(ip)
		if got != tc.private {
			t.Errorf("IsPrivateIP(%s) = %v, want %v", tc.ip, got, tc.private)
		}
	}
}

func TestNewSafeHTTPClient_PublicServer(t *testing.T) {
	// Create a test server (binds to 127.0.0.1 which is private).
	// We can't test public IPs in unit tests, but we CAN verify the client
	// is created with correct timeout and transport.
	client := NewSafeHTTPClient(5 * time.Second)
	if client == nil {
		t.Fatal("NewSafeHTTPClient returned nil")
	}
	if client.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %s", client.Timeout)
	}
	if client.Transport == nil {
		t.Fatal("Transport should not be nil")
	}
	if client.CheckRedirect == nil {
		t.Fatal("CheckRedirect should not be nil")
	}
}

func TestNewSafeHTTPClient_BlocksPrivateIP(t *testing.T) {
	// httptest server binds to 127.0.0.1 which is a private IP.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "should not reach here")
	}))
	defer server.Close()

	client := NewSafeHTTPClient(5 * time.Second)
	_, err := client.Get(server.URL)
	if err == nil {
		t.Fatal("expected error when connecting to private IP (127.0.0.1)")
	}
	if !errors.Is(err, ErrPrivateIP) {
		// The error is wrapped in url.Error, check string
		if !containsStr(err.Error(), "private") && !containsStr(err.Error(), "blocked") {
			t.Errorf("expected private IP error, got: %v", err)
		}
	}
}

func TestNewSafeHTTPClient_RedirectToPrivateBlocked(t *testing.T) {
	// The redirect check validates the target URL's resolved IP.
	// Since we can't easily control DNS in tests, verify the CheckRedirect
	// function exists and rejects when called with a private-resolving host.
	client := NewSafeHTTPClient(5 * time.Second)

	// Simulate a redirect to localhost (private).
	req, _ := http.NewRequest("GET", "http://127.0.0.1/redirected", nil)
	req = req.WithContext(context.Background())
	via := []*http.Request{{}} // one previous request

	err := client.CheckRedirect(req, via)
	if err == nil {
		t.Fatal("expected redirect to private IP to be blocked")
	}
}

func TestNewSafeHTTPClient_TooManyRedirects(t *testing.T) {
	client := NewSafeHTTPClient(5 * time.Second)

	req, _ := http.NewRequest("GET", "http://example.com/", nil)
	req = req.WithContext(context.Background())
	via := []*http.Request{{}, {}, {}} // 3 previous requests

	err := client.CheckRedirect(req, via)
	if err == nil {
		t.Fatal("expected too many redirects error")
	}
	if !containsStr(err.Error(), "too many redirects") {
		t.Errorf("expected 'too many redirects', got: %v", err)
	}
}

func TestNewSafeHTTPClient_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer server.Close()

	client := NewSafeHTTPClient(5 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

func TestSafeDialContext_BlocksPrivateIP(t *testing.T) {
	dial := safeDialContext(&net.Dialer{Timeout: 2 * time.Second}, EndpointAllowlist{})
	// 127.0.0.1 is private — should be blocked.
	_, err := dial(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected error for private IP")
	}
}

func TestSafeDialContext_InvalidAddress(t *testing.T) {
	dial := safeDialContext(&net.Dialer{Timeout: 2 * time.Second}, EndpointAllowlist{})
	_, err := dial(context.Background(), "tcp", "no-port")
	if err == nil {
		t.Fatal("expected error for invalid address (no port)")
	}
}

func TestSafeDialContext_AllIPsFail(t *testing.T) {
	// Connect to a public IP on a port that won't respond (fast timeout).
	dial := safeDialContext(&net.Dialer{Timeout: 100 * time.Millisecond}, EndpointAllowlist{})
	_, err := dial(context.Background(), "tcp", "8.8.8.8:1") // port 1 won't respond
	if err == nil {
		t.Fatal("expected error when all IPs fail to connect")
	}
	if !containsStr(err.Error(), "failed to connect") {
		t.Errorf("expected 'failed to connect' in error, got: %v", err)
	}
}

func TestSafeDialContext_DNSFailure(t *testing.T) {
	dial := safeDialContext(&net.Dialer{Timeout: 2 * time.Second}, EndpointAllowlist{})
	_, err := dial(context.Background(), "tcp", "this-domain-does-not-exist-xyz123.invalid:80")
	if err == nil {
		t.Fatal("expected error for DNS failure")
	}
}

func TestErrPrivateIP_IsSentinel(t *testing.T) {
	// Verify ErrPrivateIP is usable as a sentinel error.
	wrapped := fmt.Errorf("dial failed: %w", ErrPrivateIP)
	if !errors.Is(wrapped, ErrPrivateIP) {
		t.Error("errors.Is should match wrapped ErrPrivateIP")
	}
}

// TestNewSafeHTTPClient_PrivateHostAllowlist verifies that a host listed
// in the private-host allowlist bypasses the IP-range block, while hosts
// not on the list remain blocked. Mirrors the E2E use case where the
// operator fetches from an in-cluster mock Service (ClusterIP in 10.x).
func TestNewSafeHTTPClient_PrivateHostAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	// server.URL looks like http://127.0.0.1:<port>/. The host is
	// "127.0.0.1" (literal IP); we exercise the allowlist path by
	// listing it.
	parsed, _ := parseHostPort(server.URL)

	t.Run("allowed host bypasses private-IP check", func(t *testing.T) {
		allow := ParseEndpointAllowlist(parsed)
		client := NewSafeHTTPClient(5*time.Second, WithPrivateHostAllowlist(allow))
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("expected allowlist to permit %q, got: %v", parsed, err)
		}
		_ = resp.Body.Close()
	})

	t.Run("host not in allowlist still blocked", func(t *testing.T) {
		allow := ParseEndpointAllowlist("some-other-host.example")
		client := NewSafeHTTPClient(5*time.Second, WithPrivateHostAllowlist(allow))
		_, err := client.Get(server.URL)
		if err == nil {
			t.Fatalf("expected private-IP block when host not in allowlist")
		}
	})

	t.Run("empty allowlist = strict default", func(t *testing.T) {
		client := NewSafeHTTPClient(5 * time.Second)
		_, err := client.Get(server.URL)
		if err == nil {
			t.Fatal("expected private-IP block with empty allowlist")
		}
	})

	t.Run("allowlist is case-insensitive", func(t *testing.T) {
		allow := ParseEndpointAllowlist("LOCALHOST")
		client := NewSafeHTTPClient(5*time.Second, WithPrivateHostAllowlist(allow))
		// Swap 127.0.0.1 for "localhost" in the URL.
		urlWithName := "http://localhost:" + parseHostPortOrPanic(server.URL, true)
		resp, err := client.Get(urlWithName)
		if err != nil {
			t.Fatalf("case-insensitive match should permit localhost, got: %v", err)
		}
		_ = resp.Body.Close()
	})
}

// TestSafeDialContext_AllowlistBypass directly exercises the dialer's
// allowlist path without needing an HTTP round-trip.
func TestSafeDialContext_AllowlistBypass(t *testing.T) {
	// Spin up a listener on 127.0.0.1 so the dial can actually succeed
	// when permitted.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	allow := ParseEndpointAllowlist("localhost")
	dial := safeDialContext(&net.Dialer{Timeout: 2 * time.Second}, allow)
	conn, err := dial(context.Background(), "tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("expected allowed localhost dial to succeed, got: %v", err)
	}
	_ = conn.Close()
}

// TestNewSafeHTTPClient_RedirectAllowlist — a redirect to a private host
// that IS in the allowlist must be permitted; outside the list must be
// blocked. Prevents an attacker from smuggling an internal IP through
// a 302 when the initial host is also internal-allowed.
func TestNewSafeHTTPClient_RedirectAllowlist(t *testing.T) {
	allow := ParseEndpointAllowlist("localhost")
	client := NewSafeHTTPClient(5*time.Second, WithPrivateHostAllowlist(allow))

	t.Run("redirect to allowed host permitted", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://localhost/redirected", nil)
		if err := client.CheckRedirect(req, []*http.Request{{}}); err != nil {
			t.Fatalf("expected redirect to allowed host, got: %v", err)
		}
	})

	t.Run("redirect to private host NOT in allowlist blocked", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://127.0.0.1/redirected", nil)
		if err := client.CheckRedirect(req, []*http.Request{{}}); err == nil {
			t.Fatal("expected redirect to non-allowlisted private host to be blocked")
		}
	})
}

// parseHostPort extracts the host portion of a URL like
// "http://127.0.0.1:38491/" → "127.0.0.1".
func parseHostPort(raw string) (string, error) {
	// Simple parse: strip scheme + trailing path.
	s := raw
	if i := indexStr(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := indexStr(s, "/"); i >= 0 {
		s = s[:i]
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return s, err
	}
	return host, nil
}

func parseHostPortOrPanic(raw string, wantPort bool) string {
	s := raw
	if i := indexStr(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := indexStr(s, "/"); i >= 0 {
		s = s[:i]
	}
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		panic(err)
	}
	if wantPort {
		return port
	}
	return s
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
