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

func TestErrPrivateIP_IsSentinel(t *testing.T) {
	// Verify ErrPrivateIP is usable as a sentinel error.
	wrapped := fmt.Errorf("dial failed: %w", ErrPrivateIP)
	if !errors.Is(wrapped, ErrPrivateIP) {
		t.Error("errors.Is should match wrapped ErrPrivateIP")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
