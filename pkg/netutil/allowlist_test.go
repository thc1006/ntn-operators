/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netutil_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/thc1006/ntn-operators/pkg/netutil"
)

func TestEndpointAllowlist_Empty_AcceptsAnyHttpURL(t *testing.T) {
	// Backward-compat default: no flag set -> admin opts out -> allow
	// anything that parses to a valid http/https URL with a host.
	a := netutil.ParseEndpointAllowlist("")
	for _, url := range []string{
		"http://prometheus.monitoring.svc:9090",
		"https://prom.example.com",
		"http://127.0.0.1:9090",
	} {
		if err := a.Check(url); err != nil {
			t.Errorf("empty allowlist must accept %q, got %v", url, err)
		}
	}
}

func TestEndpointAllowlist_IsEmpty(t *testing.T) {
	if !netutil.ParseEndpointAllowlist("").IsEmpty() {
		t.Error("a blank spec must produce an empty allowlist")
	}
	if !netutil.ParseEndpointAllowlist("  , ,").IsEmpty() {
		t.Error("a spec of only blanks/whitespace must produce an empty allowlist")
	}
	if netutil.ParseEndpointAllowlist("gnb.ran.svc").IsEmpty() {
		t.Error("a spec with one host must not be empty")
	}
}

func TestEndpointAllowlist_NonEmpty_OnlyExactHostMatch(t *testing.T) {
	a := netutil.ParseEndpointAllowlist("prom.monitoring.svc,prom.example.com")
	ok := []string{
		"http://prom.monitoring.svc:9090",
		"https://prom.monitoring.svc",
		"http://prom.example.com/api/v1/query",
	}
	for _, url := range ok {
		if err := a.Check(url); err != nil {
			t.Errorf("want allow %q, got %v", url, err)
		}
	}
	rejected := []string{
		// Subdomain-confusion attack: attacker.com pretends to be a
		// subdomain of an allowed host.
		"http://prom.monitoring.svc.attacker.com:9090",
		// Prefix-bypass: admin host is only a prefix of the actual
		// registered host; rejected under exact-match semantics.
		"http://prom.monitoring.svc.evil.internal",
		// Completely different host.
		"http://attacker.com",
	}
	for _, url := range rejected {
		err := a.Check(url)
		if err == nil {
			t.Errorf("want reject %q, got accept", url)
		} else if !errors.Is(err, netutil.ErrEndpointNotAllowed) {
			t.Errorf("want ErrEndpointNotAllowed for %q, got %v", url, err)
		}
	}
}

func TestEndpointAllowlist_SchemeRestricted(t *testing.T) {
	a := netutil.ParseEndpointAllowlist("")
	for _, url := range []string{
		"javascript://prom.monitoring.svc",
		"file:///etc/passwd",
		"gopher://prom.monitoring.svc:70",
		"ftp://prom.monitoring.svc",
	} {
		if err := a.Check(url); err == nil {
			t.Errorf("URL %q must be rejected even with empty allowlist", url)
		}
	}
}

func TestEndpointAllowlist_UserinfoRejected(t *testing.T) {
	// Classic SSRF bypass: attacker stuffs a fake host into userinfo
	// so a naive prefix-on-URL check sees the allowed string, while
	// the HTTP client actually resolves the real host after @.
	a := netutil.ParseEndpointAllowlist("prom.monitoring.svc")
	attacks := []string{
		"http://attacker.com@prom.monitoring.svc/",
		"http://prom.monitoring.svc@attacker.com/", // reverse trick
		"http://user:pass@prom.monitoring.svc",
	}
	for _, url := range attacks {
		if err := a.Check(url); err == nil {
			t.Errorf("URL with userinfo %q must be rejected", url)
		}
	}
}

func TestEndpointAllowlist_EmptyHostRejected(t *testing.T) {
	a := netutil.ParseEndpointAllowlist("")
	for _, url := range []string{
		"http://",
		"http:///path",
		"https://", // host is empty after scheme
	} {
		if err := a.Check(url); err == nil {
			t.Errorf("%q must be rejected for empty host", url)
		}
	}
}

func TestEndpointAllowlist_UnparseableURLRejected(t *testing.T) {
	a := netutil.ParseEndpointAllowlist("")
	// Go's url.Parse is lenient: only a handful of truly malformed strings
	// fail it. We still want our check to refuse anything the parser
	// cannot round-trip sensibly.
	for _, url := range []string{
		"://no-scheme",
		"ht tp://space-in-scheme", // Parse does error on this form
	} {
		if err := a.Check(url); err == nil {
			t.Errorf("unparseable %q must be rejected", url)
		}
	}
}

func TestEndpointAllowlist_HostMatchIsCaseInsensitive(t *testing.T) {
	// DNS is case-insensitive; the allowlist should be too so that
	// "Prom.Monitoring.Svc" in the CR matches the admin's list.
	a := netutil.ParseEndpointAllowlist("Prom.Monitoring.Svc")
	if err := a.Check("http://prom.monitoring.svc"); err != nil {
		t.Errorf("case-insensitive match expected, got %v", err)
	}
	if err := a.Check("http://PROM.MONITORING.SVC:9090"); err != nil {
		t.Errorf("case-insensitive match expected (upper), got %v", err)
	}
}

func TestEndpointAllowlist_WhitespaceAndEmptyEntries_AreIgnored(t *testing.T) {
	// "  ,prom.example.com,   ,  " should parse to one useful entry
	// and silently drop the blank slots between the commas. The parser
	// only normalises whitespace and case; it is not smart about
	// further user mistakes (see the godoc for why).
	a := netutil.ParseEndpointAllowlist("  ,prom.example.com,   ,  ")
	if err := a.Check("http://prom.example.com"); err != nil {
		t.Errorf("valid entry rejected: %v", err)
	}
	if err := a.Check("http://other.example.com"); err == nil {
		t.Error("should still reject hosts not in allowlist")
	}
}

func TestEndpointAllowlist_ErrEndpointNotAllowed_IncludesHost(t *testing.T) {
	// The error message is surfaced to operators in kubectl describe;
	// include the host so they can act without re-running the query.
	a := netutil.ParseEndpointAllowlist("prom.allowed.com")
	err := a.Check("http://bad.example.com:9090/api/v1/query")
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "bad.example.com") {
		t.Errorf("error should identify offending host, got: %v", err)
	}
}
