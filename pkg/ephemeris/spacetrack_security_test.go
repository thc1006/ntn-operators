/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ephemeris

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSpaceTrackFetcher_403BodyNotReflected pins #222 review blocker 1: the AUTHENTICATED
// Space-Track response body must NEVER be reflected into the error (and thus the public CR
// Condition/Event). The 403 body here carries a sentinel; the surfaced error must be a fixed
// classified message, not the body. Mutation: restore Snippet: readBodySnippet(resp.Body) →
// the sentinel appears in the error.
func TestSpaceTrackFetcher_403BodyNotReflected(t *testing.T) {
	const sentinel = "TOP-SECRET-ACCOUNT-DATA"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ajaxauth/login" {
			w.WriteHeader(http.StatusOK) // login succeeds
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, sentinel)
	}))
	defer server.Close()

	f := NewSpaceTrackFetcher(server.Client(), server.URL)
	_, err := f.FetchWithCredentials(context.Background(), server.URL+"/gp", "user", "pass")
	if err == nil {
		t.Fatal("expected a rate-limit error on HTTP 403")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("the authenticated Space-Track response body must NOT appear in the error: %v", err)
	}
}

// TestSpaceTrackFetcher_RejectsForeignOrigin pins #222 review blocker 1 (origin lock): the
// authenticated session must not be sent to a scheme/host other than the configured trusted
// base — the query URL is CR-controlled. Rejection happens BEFORE login. Mutation: remove the
// validateGPURL call → these are no longer classified as ErrInvalidSourceURL at validation.
func TestSpaceTrackFetcher_RejectsForeignOrigin(t *testing.T) {
	f := NewSpaceTrackFetcher(http.DefaultClient, "https://www.space-track.org")
	for _, bad := range []string{
		"http://www.space-track.org/basicspacedata/query", // scheme downgrade
		"https://evil.example.com/basicspacedata/query",   // wrong host
		"https://www.space-track.org.evil.com/x",          // lookalike host suffix
	} {
		_, err := f.FetchWithCredentials(context.Background(), bad, "user", "pass")
		if err == nil {
			t.Errorf("gpURL %q on a foreign origin must be rejected", bad)
			continue
		}
		if !errors.Is(err, ErrInvalidSourceURL) {
			t.Errorf("gpURL %q must be rejected as ErrInvalidSourceURL (permanent config error), got %v", bad, err)
		}
	}
}

// TestSpaceTrackFetcher_RedirectPolicyRefusesCrossOrigin pins #222 review-2 blocker 1: the
// redirect policy enforces exact-origin on EVERY hop, not just the initial URL. A 307/308
// re-POSTs the login credential body and the session cookie rides redirects, so a
// cross-origin redirect must be refused — otherwise credentials/session leave the trusted
// Space-Track origin. Mutation: drop the CheckRedirect override → the policy is nil (or the
// permissive SSRF one) and cross-origin is no longer refused.
func TestSpaceTrackFetcher_RedirectPolicyRefusesCrossOrigin(t *testing.T) {
	f := NewSpaceTrackFetcher(&http.Client{}, "https://www.space-track.org")
	if f.httpClient.Jar == nil {
		t.Fatal("expected a cookie jar (public-suffix list)")
	}
	cr := f.httpClient.CheckRedirect
	if cr == nil {
		t.Fatal("SpaceTrack must install an exact-origin redirect policy")
	}
	orig, _ := http.NewRequest(http.MethodPost, "https://www.space-track.org/ajaxauth/login", nil)

	same, _ := http.NewRequest(http.MethodGet, "https://www.space-track.org/basicspacedata/query", nil)
	if err := cr(same, []*http.Request{orig}); err != nil {
		t.Fatalf("a same-origin redirect must be allowed, got: %v", err)
	}
	for _, bad := range []string{
		"https://evil.example.com/steal",         // foreign host
		"http://www.space-track.org/x",           // scheme downgrade
		"https://www.space-track.org.evil.com/x", // lookalike host suffix
	} {
		req, _ := http.NewRequest(http.MethodGet, bad, nil)
		if err := cr(req, []*http.Request{orig}); err == nil {
			t.Errorf("a cross-origin redirect to %q must be refused (credential/cookie exfil)", bad)
		}
	}
}
