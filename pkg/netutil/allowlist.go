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
// The parser is intentionally minimal: it does not accept schemes,
// paths, ports, or glob patterns. Admins who need multiple forms
// (short name vs FQDN) list each one explicitly; this is more typing
// but has no ambiguity.
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
