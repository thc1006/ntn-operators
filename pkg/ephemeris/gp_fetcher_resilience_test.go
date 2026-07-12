/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ephemeris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thc1006/ntn-operators/pkg/netutil"
)

// TestParseRetryAfter_HugeValueSaturates pins #222 review blocker 2: a delay-seconds value
// that exceeds int/int64 range is still a valid RFC 9110 non-negative integer and must
// SATURATE to the 24h cap — not fall through the (failed) int parse to the HTTP-date branch
// and return 0. Mutation: revert to strconv.Atoi → these all return 0.
func TestParseRetryAfter_HugeValueSaturates(t *testing.T) {
	for _, v := range []string{
		"9223372036854775808",    // int64 max + 1 (Atoi range error)
		"18446744073709551616",   // uint64 max + 1 (ParseUint range error)
		strings.Repeat("9", 100), // 100-digit
	} {
		h := http.Header{}
		h.Set("Retry-After", v)
		if got := parseRetryAfter(h); got != maxRetryAfter {
			t.Errorf("Retry-After=%q must saturate to %s, got %s", v, maxRetryAfter, got)
		}
	}
}

// TestSanitizeSnippet_BoundsOutputAndStripsFormatChars pins #222 review blocker 3: the
// output is bounded by ENCODED bytes (not truncated input), invalid UTF-8 becomes a space
// (not a 3-byte U+FFFD), and format/bidi/zero-width chars unicode.IsControl misses are stripped.
func TestSanitizeSnippet_BoundsOutputAndStripsFormatChars(t *testing.T) {
	// 900 bytes of a 3-byte rune → without an output bound this would be 900 bytes.
	if got := sanitizeSnippet(bytes.Repeat([]byte("中"), 300)); len(got) > maxSnippetBytes {
		t.Errorf("output must be bounded to %d bytes, got %d", maxSnippetBytes, len(got))
	}
	// Invalid UTF-8 bytes must collapse to spaces, NOT re-encode to U+FFFD.
	if got := sanitizeSnippet([]byte{'a', 0xff, 0xfe, 'b'}); got != "a b" {
		t.Errorf("invalid UTF-8 must become spaces (collapsed), got %q", got)
	}
	// Format/bidi/zero-width runes (missed by unicode.IsControl) must be stripped:
	// U+202E RIGHT-TO-LEFT OVERRIDE, U+2066 LEFT-TO-RIGHT ISOLATE, U+200B ZERO WIDTH SPACE.
	for _, r := range []rune{'‮', '⁦', '​'} {
		if got := sanitizeSnippet([]byte("a" + string(r) + "b")); strings.ContainsRune(got, r) {
			t.Errorf("sanitizeSnippet must strip format char %U, got %q", r, got)
		}
	}
	if got := sanitizeSnippet([]byte("hello   world")); got != "hello world" {
		t.Errorf("normal body must be preserved with collapsed whitespace, got %q", got)
	}
}

// TestParseRetryAfter_OverflowClampedToMax pins #200-C7: a delay-seconds value whose
// *time.Second product overflows int64 nanoseconds must clamp to maxRetryAfter, not wrap
// into a small/negative duration. secs=1e10 overflows (1e10*1e9 = 1e19 > int64 max),
// wrapping negative → clampRetryAfter would return 0 WITHOUT the pre-multiply bound.
// Mutation: remove the `secs >= maxRetryAfter/second` guard and this returns 0, not 24h.
func TestParseRetryAfter_OverflowClampedToMax(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "10000000000") // 1e10 s; *1e9 ns overflows int64 and wraps negative
	if got := parseRetryAfter(h); got != maxRetryAfter {
		t.Fatalf("an overflowing Retry-After must clamp to %s, got %s", maxRetryAfter, got)
	}
}

// TestParseRetryAfter_NegativeIsZero guards the delay-seconds negative case explicitly.
func TestParseRetryAfter_NegativeIsZero(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "-30")
	if got := parseRetryAfter(h); got != 0 {
		t.Fatalf("a negative Retry-After must be 0, got %s", got)
	}
}

// TestFetch_403_CapturesBodySnippet pins #200-C6: CelesTrak returns an explanatory reason
// in the 403 body; it must be surfaced in the error, not discarded. Mutation: drop the
// Snippet field from the 403 handler and the reason no longer appears in the error.
func TestFetch_403_CapturesBodySnippet(t *testing.T) {
	const reason = "GP data has not updated since your last successful download"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, reason)
	}))
	defer srv.Close()

	_, err := NewCelesTrakFetcher(srv.Client()).Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected a rate-limit error on HTTP 403")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("the 403 body reason must be surfaced in the error, got: %v", err)
	}
}

// TestReadBodySnippet_SanitizesAndBounds pins the sanitizer: control chars/newlines are
// collapsed to single spaces and the output is bounded, so an untrusted body cannot inject
// newlines into a log/condition or blow up its size.
func TestReadBodySnippet_SanitizesAndBounds(t *testing.T) {
	got := readBodySnippet(strings.NewReader("line1\n\tline2\x00\x07   line3\r\n"))
	if got != "line1 line2 line3" {
		t.Fatalf("snippet sanitization: got %q want %q", got, "line1 line2 line3")
	}
	big := strings.Repeat("A", 4096)
	if snip := readBodySnippet(strings.NewReader(big)); len(snip) > maxSnippetBytes {
		t.Fatalf("snippet must be bounded to %d bytes, got %d", maxSnippetBytes, len(snip))
	}
}

// TestNewCelesTrakFetcher_NilClientIsSSRFSafe pins the nil-client hardening (#199-C2): a
// nil client must default to the SSRF-safe client, so a fetch to a private IP is blocked
// at dial. httptest binds to 127.0.0.1 (private). Mutation: revert the nil fallback to a
// bare &http.Client{} and the dial succeeds → the error becomes a JSON parse error, not
// ErrPrivateIP.
func TestNewCelesTrakFetcher_NilClientIsSSRFSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "should not be reached")
	}))
	defer srv.Close()

	_, err := NewCelesTrakFetcher(nil).Fetch(context.Background(), srv.URL) // srv is 127.0.0.1
	if err == nil {
		t.Fatal("a nil-client fetcher must block a private-IP target (SSRF-safe default)")
	}
	if !errors.Is(err, netutil.ErrPrivateIP) {
		t.Fatalf("expected a private-IP dial block (ErrPrivateIP), got: %v", err)
	}
}
