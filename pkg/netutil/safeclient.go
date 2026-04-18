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

// Package netutil provides SSRF-safe HTTP client utilities.
// It validates resolved IP addresses at the TCP dial level to prevent
// DNS rebinding attacks and access to internal/cloud metadata endpoints.
package netutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// privateRanges are CIDR blocks that should never be accessed by the operator.
// Includes RFC 1918, loopback, link-local, cloud metadata, and IPv6 equivalents.
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"127.0.0.0/8",    // Loopback
		"169.254.0.0/16", // Link-local / cloud metadata
		"0.0.0.0/8",      // Current network
		"100.64.0.0/10",  // Carrier-grade NAT
		"192.0.0.0/24",   // IETF protocol assignments
		"198.18.0.0/15",  // Benchmarking
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
		"::1/128",        // IPv6 loopback
	}
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("invalid CIDR %q: %v", cidr, err))
		}
		privateRanges = append(privateRanges, ipNet)
	}
}

// IsPrivateIP checks if an IP falls within any private/reserved range.
func IsPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// ErrPrivateIP is returned when a dial attempts to connect to a private IP.
var ErrPrivateIP = fmt.Errorf("connection to private/reserved IP address is blocked")

// safeDialContext creates a DialContext function that validates resolved IPs
// against private/reserved ranges before connecting.
func safeDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}

		// Resolve DNS to get actual IP addresses.
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DNS resolution failed for %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("DNS resolution for %q returned no addresses", host)
		}

		// Validate ALL resolved IPs before connecting.
		for _, ipAddr := range ips {
			if IsPrivateIP(ipAddr.IP) {
				return nil, fmt.Errorf("%w: %s resolves to %s", ErrPrivateIP, host, ipAddr.IP)
			}
		}

		// Try each resolved IP until one connects (handles dual-stack).
		var lastErr error
		for _, ipAddr := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("all resolved IPs for %q failed to connect: %w", host, lastErr)
	}
}

// NewSafeHTTPClient creates an HTTP client that rejects connections to
// private/reserved IP addresses at the TCP dial level, preventing SSRF
// even against DNS rebinding attacks.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	// Clone DefaultTransport to preserve proxy, connection pooling, and TLS settings.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialContext(dialer)

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Validate redirect targets against private IP ranges (defense in depth).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			// Resolve the redirect target and reject private IPs.
			host := req.URL.Hostname()
			ips, err := net.DefaultResolver.LookupIPAddr(req.Context(), host)
			if err != nil {
				return fmt.Errorf("cannot resolve redirect target %q: %w", host, err)
			}
			for _, ipAddr := range ips {
				if IsPrivateIP(ipAddr.IP) {
					return fmt.Errorf("%w: redirect to %s resolves to %s", ErrPrivateIP, host, ipAddr.IP)
				}
			}
			return nil
		},
	}
}
