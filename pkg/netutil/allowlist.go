/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netutil

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ErrEndpointNotAllowed is returned by EndpointAllowlist.Check when a URL
// fails validation — either because it does not parse, carries userinfo,
// uses a non-HTTP scheme, has an empty host, or the host is absent from
// a non-empty allowlist. Callers should map this sentinel to a
// deterministic Status condition rather than bubbling it up as a
// generic reconcile error.
var ErrEndpointNotAllowed = errors.New("netutil: endpoint not allowed")

// EndpointAllowlist validates that a user-supplied URL is a well-formed
// HTTP(S) endpoint whose hostname, when the list is non-empty, exactly
// matches one of the configured hosts. It deliberately does NOT perform
// DNS resolution or IP-range checks — that duty belongs to the operator
// Pod's NetworkPolicy, which is the Kubernetes-native way to enforce
// "the operator may only egress to these targets". See the
// docs/design/metrics-source.md "Non-goals" section for the full
// rationale.
//
// Why host-exact match rather than URL-prefix:
// a prefix check on raw URL text is trivially bypassed by subdomain
// confusion (http://allowed.svc.attacker.com) and userinfo tricks
// (http://attacker@allowed.svc). Parsing the URL and comparing only
// the Hostname() against the list closes both classes without needing
// a regex engine.
//
// Empty allowlist — backward-compatible permissive default. Every
// deployment that was running before the flag existed continues to
// reconcile without change; admins opt in to restriction by setting
// the operator flag.
type EndpointAllowlist struct {
	hosts []string // already lowercased; empty slice = permit-all
}

// ParseEndpointAllowlist accepts a comma-separated list of hosts. Blank
// entries and surrounding whitespace are ignored. Hostnames are
// normalised to lower case for DNS-equivalent matching.
//
// The parser is intentionally minimal: it does not validate token
// structure beyond trimming whitespace and lowercasing. Entries are
// treated literally, so an admin who accidentally writes
// "https://prom.example.com" or "prom.example.com:9090" will store
// that whole string as the key and Check will never match a real
// Hostname against it — the NTNSlice will be rejected with
// EndpointNotAllowed until the flag is fixed. Admins should provide
// bare hostnames only; if multiple forms are needed (short name vs
// FQDN), list each one explicitly.
func ParseEndpointAllowlist(csv string) EndpointAllowlist {
	if csv == "" {
		return EndpointAllowlist{}
	}
	parts := strings.Split(csv, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		hosts = append(hosts, strings.ToLower(p))
	}
	return EndpointAllowlist{hosts: hosts}
}

// IsEmpty reports whether the allowlist has no entries. Empty means "permit
// all" to Check (the user-supplied-URL gate) and "whitelist nothing" to
// ContainsHost (the dialer bypass) — the two callers deliberately read the
// empty list with opposite senses. A caller that needs permit-all-when-empty
// on a bare host:port (no URL to hand to Check, e.g. the remote-control WS
// endpoint) composes IsEmpty with ContainsHost to make that choice explicit.
func (a EndpointAllowlist) IsEmpty() bool {
	return len(a.hosts) == 0
}

// ContainsHost reports whether a bare hostname is in the allowlist.
// It lowercases the input for DNS-equivalent matching (allowlist hosts
// are stored lowercased by ParseEndpointAllowlist). Unlike Check, this
// method does no URL parsing and is intended for the transport-level
// dialer which has already split host:port and needs to decide
// whether to bypass an IP-range check for a specific hostname.
//
// Returns false for the empty allowlist — callers must decide whether
// empty means "permit all" (the Check semantic for user-supplied URLs)
// or "permit nothing" (the dialer semantic: empty list = no private
// hosts are whitelisted, default SSRF rule applies).
func (a EndpointAllowlist) ContainsHost(host string) bool {
	if len(a.hosts) == 0 {
		return false
	}
	return slices.Contains(a.hosts, strings.ToLower(host))
}

// Check validates raw and returns nil if the URL is acceptable, or an
// error wrapping ErrEndpointNotAllowed with a reason suitable for
// logging and kubectl describe.
func (a EndpointAllowlist) Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: parse: %v", ErrEndpointNotAllowed, err)
	}
	if sch := strings.ToLower(u.Scheme); sch != "http" && sch != "https" {
		return fmt.Errorf("%w: scheme %q not in {http,https}", ErrEndpointNotAllowed, u.Scheme)
	}
	// Reject userinfo outright. `http://attacker@allowed/` has
	// Hostname()="allowed" but the HTTP client actually treats
	// attacker as the host for authorization semantics; even if it
	// did not, operators do not embed credentials in the CR URL.
	if u.User != nil {
		return fmt.Errorf("%w: URL must not carry userinfo", ErrEndpointNotAllowed)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: URL has empty host", ErrEndpointNotAllowed)
	}
	if len(a.hosts) == 0 {
		return nil
	}
	if slices.Contains(a.hosts, host) {
		return nil
	}
	return fmt.Errorf("%w: host %q is not in allow-list", ErrEndpointNotAllowed, host)
}
