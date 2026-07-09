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

	"github.com/go-logr/logr"
)

// privateRanges are CIDR blocks that should never be accessed by the operator.
// Includes RFC 1918, loopback, link-local, cloud metadata, IPv4 multicast/
// reserved/broadcast, the unspecified addresses, and IPv6 equivalents. NAT64 and
// 6to4 are handled separately (see embeddedIPv4) because they carry an IPv4
// destination that must be checked on its own merits.
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"127.0.0.0/8",    // Loopback
		"169.254.0.0/16", // Link-local / cloud metadata (169.254.169.254)
		"0.0.0.0/8",      // Current network (incl. 0.0.0.0 unspecified)
		"100.64.0.0/10",  // Carrier-grade NAT
		"192.0.0.0/24",   // IETF protocol assignments
		"198.18.0.0/15",  // Benchmarking
		"224.0.0.0/4",    // IPv4 multicast (224.0.0.1 all-hosts, etc.)  [I-25]
		"240.0.0.0/4",    // IPv4 reserved/class-E incl. 255.255.255.255 broadcast [I-25]
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
		"::1/128",        // IPv6 loopback
		"::/128",         // IPv6 unspecified [I-25]
		"ff00::/8",       // IPv6 multicast [I-25]
		"64:ff9b:1::/48", // NAT64 local-use (RFC 8215) — conservative blanket [I-25]
	}
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("invalid CIDR %q: %v", cidr, err))
		}
		privateRanges = append(privateRanges, ipNet)
	}
}

// embeddedIPv4 extracts the IPv4 address carried inside an IPv6 transition
// address — 6to4 (2002::/16) and the well-known NAT64 prefix (64:ff9b::/96) —
// or nil. These embed an IPv4 destination inside an otherwise "global" IPv6
// address, so 64:ff9b::a00:1 and 2002:a00:1::1 both reach 10.0.0.1 and would
// bypass the range check above. Extracting the embedded IPv4 and re-checking it
// (rather than blanket-blocking the whole prefix) blocks the private-embedding
// SSRF vectors while still permitting a PUBLIC IPv4 reached via NAT64/6to4, so
// IPv6-only clusters fetching public CelesTrak/Prometheus are not broken (I-25).
func embeddedIPv4(ip net.IP) net.IP {
	ip16 := ip.To16()
	if ip16 == nil || ip.To4() != nil {
		return nil // not IPv6, or already IPv4 / IPv4-mapped (handled directly)
	}
	// 6to4: 2002:VVVV:VVVV::/48 — embedded IPv4 is bytes 2..5.
	if ip16[0] == 0x20 && ip16[1] == 0x02 {
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5]).To4()
	}
	// Well-known NAT64: 64:ff9b::/96 — embedded IPv4 is the last 32 bits.
	if ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b &&
		ip16[4] == 0 && ip16[5] == 0 && ip16[6] == 0 && ip16[7] == 0 &&
		ip16[8] == 0 && ip16[9] == 0 && ip16[10] == 0 && ip16[11] == 0 {
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
	}
	return nil
}

// IsPrivateIP checks if an IP falls within any private/reserved range, or (for
// NAT64/6to4 transition addresses) whether the embedded IPv4 is private.
func IsPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	// NAT64/6to4: re-check the embedded IPv4 (one level; the recursion bottoms
	// out because embeddedIPv4 returns nil for an IPv4/IPv4-mapped address).
	if v4 := embeddedIPv4(ip); v4 != nil {
		return IsPrivateIP(v4)
	}
	return false
}

// ErrPrivateIP is returned when a dial attempts to connect to a private IP.
var ErrPrivateIP = fmt.Errorf("connection to private/reserved IP address is blocked")

// safeClientOpts holds optional configuration for NewSafeHTTPClient.
// See the Option functions below for the supported knobs.
type safeClientOpts struct {
	// privateHostAllowlist lists hostnames whose resolved private/
	// reserved IPs should NOT be blocked. Intended for corporate mirror
	// deployments (e.g. `celestrak.internal.corp.com`) and E2E test
	// setups that route fetches through an in-cluster mock Service.
	// The default (empty) preserves the strict SSRF posture.
	privateHostAllowlist EndpointAllowlist
}

// Option configures NewSafeHTTPClient behaviour.
type Option func(*safeClientOpts)

// WithPrivateHostAllowlist exempts the listed hostnames from the
// private-IP block. Exact, case-insensitive match on the dial target
// hostname (for the initial dial) and the redirect target's URL
// hostname (for CheckRedirect) — NOT the HTTP Host header, which
// `net/http` allows to diverge from the URL host. Does NOT affect
// the base SSRF rules for hosts outside the list. Production
// deployments should leave this empty; admins opt in only for
// trusted internal mirrors.
//
// Threat model: an attacker who controls a URL in a user-facing CR
// can still only hit hosts the admin has pre-approved. The allowlist
// is consulted after hostname extraction (so `http://attacker@mirror/`
// still fails — userinfo is already rejected upstream by Check) and
// applies to BOTH the initial dial target hostname and any redirect
// follow-up request URL hostname, so an attacker cannot smuggle an
// internal IP through a 302.
func WithPrivateHostAllowlist(allow EndpointAllowlist) Option {
	return func(o *safeClientOpts) {
		o.privateHostAllowlist = allow
	}
}

// safeDialContext creates a DialContext function that validates resolved IPs
// against private/reserved ranges before connecting. Hosts in privateAllowed
// bypass the private-IP check (but every other rule — DNS resolvability,
// non-empty address set — still applies).
func safeDialContext(
	dialer *net.Dialer,
	privateAllowed EndpointAllowlist,
) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		log := logr.FromContextOrDiscard(ctx)
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		hostAllowed := privateAllowed.ContainsHost(host)

		// Resolve DNS to get actual IP addresses.
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DNS resolution failed for %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("DNS resolution for %q returned no addresses", host)
		}
		log.V(2).Info("resolved outbound host", "host", host, "ipCount", len(ips), "hostAllowed", hostAllowed)

		// Validate ALL resolved IPs before connecting. Hosts in the
		// private-host allowlist bypass this check — intended for
		// trusted internal mirrors and E2E mock servers. Note that a
		// public hostname that resolves to a private IP via DNS
		// rebinding is still blocked unless the admin explicitly
		// allow-listed it.
		if !hostAllowed {
			for _, ipAddr := range ips {
				if IsPrivateIP(ipAddr.IP) {
					log.V(1).Info("blocked outbound dial to private/reserved IP", "host", host, "ip", ipAddr.IP.String())
					return nil, fmt.Errorf("%w: %s resolves to %s", ErrPrivateIP, host, ipAddr.IP)
				}
			}
		}

		// Try each resolved IP until one connects (handles dual-stack).
		var lastErr error
		for _, ipAddr := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if dialErr == nil {
				log.V(2).Info("outbound dial succeeded", "host", host, "ip", ipAddr.IP.String())
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
//
// Optional functional knobs (e.g. WithPrivateHostAllowlist) let admins
// exempt specific trusted hostnames; the default is strict-block.
func NewSafeHTTPClient(timeout time.Duration, opts ...Option) *http.Client {
	cfg := safeClientOpts{}
	for _, opt := range opts {
		opt(&cfg)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	// Clone DefaultTransport to preserve proxy, connection pooling, and TLS settings.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialContext(dialer, cfg.privateHostAllowlist)

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Validate redirect targets against private IP ranges (defense in
		// depth). The same allowlist applies here so an internal mirror
		// that 302s to another internal host (within the allowlist) still
		// works; a public host that 302s to a private IP outside the
		// allowlist is still blocked.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			log := logr.FromContextOrDiscard(req.Context())
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			host := req.URL.Hostname()
			if cfg.privateHostAllowlist.ContainsHost(host) {
				log.V(2).Info("redirect target in private-host allowlist", "host", host)
				return nil
			}
			ips, err := net.DefaultResolver.LookupIPAddr(req.Context(), host)
			if err != nil {
				return fmt.Errorf("cannot resolve redirect target %q: %w", host, err)
			}
			for _, ipAddr := range ips {
				if IsPrivateIP(ipAddr.IP) {
					log.V(1).Info("blocked redirect to private/reserved IP", "host", host, "ip", ipAddr.IP.String())
					return fmt.Errorf("%w: redirect to %s resolves to %s", ErrPrivateIP, host, ipAddr.IP)
				}
			}
			log.V(2).Info("redirect target allowed", "host", host, "ipCount", len(ips))
			return nil
		},
	}
}
