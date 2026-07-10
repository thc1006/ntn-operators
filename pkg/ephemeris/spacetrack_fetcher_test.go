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

package ephemeris

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

const loginPath = "/ajaxauth/login"

// sampleOMMJSON is a minimal valid OMM JSON for testing (same format as CelesTrak).
var sampleOMMJSON = `[{
	"OBJECT_NAME":"ONEWEB-0012",
	"OBJECT_ID":"2019-010A",
	"EPOCH":"2026-04-17T00:00:00.000000",
	"MEAN_MOTION":12.85,
	"ECCENTRICITY":0.001,
	"INCLINATION":87.9,
	"RA_OF_ASC_NODE":100.0,
	"ARG_OF_PERICENTER":90.0,
	"MEAN_ANOMALY":270.0,
	"EPHEMERIS_TYPE":0,
	"CLASSIFICATION_TYPE":"U",
	"NORAD_CAT_ID":44057,
	"ELEMENT_SET_NO":999,
	"REV_AT_EPOCH":1000,
	"BSTAR":0.0001,
	"MEAN_MOTION_DOT":0.0,
	"MEAN_MOTION_DDOT":0.0
}]`

func TestSpaceTrackFetcher_Success(t *testing.T) {
	var loginCalled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case loginPath:
			if r.Method != http.MethodPost {
				t.Errorf("login should be POST, got %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("identity") != "testuser" || r.FormValue("password") != "testpass" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			loginCalled.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "session123", Path: "/"})
			_, _ = fmt.Fprint(w, `""`) // SpaceTrack returns empty string on success
		case "/basicspacedata/query/class/gp/GROUP/oneweb/format/json":
			// Verify session cookie is present.
			cookie, err := r.Cookie("chocolatechip")
			if err != nil || cookie.Value != "session123" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, sampleOMMJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("testuser", "testpass")

	gpURL := server.URL + "/basicspacedata/query/class/gp/GROUP/oneweb/format/json"
	result, err := fetcher.Fetch(context.Background(), gpURL)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if result.SatelliteCount != 1 {
		t.Errorf("expected 1 satellite, got %d", result.SatelliteCount)
	}
	if result.NotModified {
		t.Error("expected NotModified=false on first fetch")
	}
	if loginCalled.Load() != 1 {
		t.Errorf("expected 1 login call, got %d", loginCalled.Load())
	}
}

func TestSpaceTrackFetcher_SessionReuse(t *testing.T) {
	var loginCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case loginPath:
			loginCount.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "session123", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
		default:
			cookie, err := r.Cookie("chocolatechip")
			if err != nil || cookie.Value != "session123" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, sampleOMMJSON)
		}
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(&http.Client{}, server.URL)
	fetcher.SetCredentials("user", "pass")

	gpURL := server.URL + "/gp"

	// Fetch twice — should only login once (cookie reused).
	if _, err := fetcher.Fetch(context.Background(), gpURL); err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.Fetch(context.Background(), gpURL); err != nil {
		t.Fatal(err)
	}
	if loginCount.Load() != 1 {
		t.Errorf("expected 1 login, got %d (session not reused)", loginCount.Load())
	}
}

func TestSpaceTrackFetcher_LoginFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Login Failed", http.StatusUnauthorized)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("bad", "creds")

	_, err := fetcher.Fetch(context.Background(), server.URL+"/gp")
	if err == nil {
		t.Fatal("expected error for failed login")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("a 401 login must be an ErrAuthFailed, got %v", err)
	}
}

// TestSpaceTrackFetcher_LoginFailure_200Body pins I-20: Space-Track returns HTTP
// 200 with a {"Login":"Failed"} body on bad credentials. Before the fix, the 200
// status alone read as success and the failure surfaced only later, cryptically.
func TestSpaceTrackFetcher_LoginFailure_200Body(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"Login":"Failed"}`) // Space-Track's bad-cred body
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("bad", "creds")

	_, err := fetcher.Fetch(context.Background(), server.URL+"/gp")
	if err == nil {
		t.Fatal("expected an auth error for a 200 + {\"Login\":\"Failed\"} body")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("a 200 + Login=Failed body must be an ErrAuthFailed, got %v", err)
	}
}

func TestLoginBodyIndicatesFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"documented failure", `{"Login":"Failed"}`, true},
		{"failure with whitespace", "  {\n \"Login\" : \"Failed\" \n} ", true},
		{"value case-insensitive", `{"Login":"failed"}`, true},
		{"key case-insensitive", `{"login":"Failed"}`, true},
		{"success empty string", `""`, false},
		{"success empty object", `{}`, false},
		{"login success value", `{"Login":"Success"}`, false},
		{"unrelated json", `{"request_id":42}`, false},
		{"non-json but not a failure marker", `Welcome`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loginBodyIndicatesFailure([]byte(tc.body)); got != tc.want {
				t.Errorf("loginBodyIndicatesFailure(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestSpaceTrackFetcher_NoCredentials(t *testing.T) {
	fetcher := NewSpaceTrackFetcher(http.DefaultClient, "https://example.com")
	// No SetCredentials called.

	_, err := fetcher.Fetch(context.Background(), "https://example.com/gp")
	if err == nil {
		t.Fatal("expected error when no credentials set")
	}
}

func TestSpaceTrackFetcher_RateLimited(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		callCount++
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("u", "p")

	_, err := fetcher.Fetch(context.Background(), server.URL+"/gp")
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected a rate-limit error, got %v", err)
	}
}

// TestSpaceTrackFetcher_RateLimited500Body pins I-19b: Space-Track signals a query
// rate-limit with HTTP 500 + a "violated your query rate limit" body (NOT 429), and
// that specific 500 must classify as rate-limited (a different 500 stays a bad response).
func TestSpaceTrackFetcher_RateLimited500Body(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "Error: You have violated your query rate limit.")
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("u", "p")

	_, err := fetcher.Fetch(context.Background(), server.URL+"/gp")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("a 500 + 'violated your query rate limit' body must be rate-limited, got %v", err)
	}

	// A different 500 must NOT be a rate limit.
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server2.Close()
	f2 := NewSpaceTrackFetcher(server2.Client(), server2.URL)
	f2.SetCredentials("u", "p")
	if _, err := f2.Fetch(context.Background(), server2.URL+"/gp"); errors.Is(err, ErrRateLimited) {
		t.Errorf("a generic 500 must NOT be classified rate-limited, got %v", err)
	}
}

func TestSpaceTrackFetcher_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		_, _ = fmt.Fprint(w, `not json at all`)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("u", "p")

	_, err := fetcher.Fetch(context.Background(), server.URL+"/gp")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSpaceTrackFetcher_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		_, _ = fmt.Fprint(w, sampleOMMJSON)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(server.Client(), server.URL)
	fetcher.SetCredentials("u", "p")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := fetcher.Fetch(ctx, server.URL+"/gp")
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

func TestSpaceTrackFetcher_SessionExpiredAutoRetry(t *testing.T) {
	var gpCallCount atomic.Int32
	var loginCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			loginCount.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "renewed", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		call := gpCallCount.Add(1)
		if call == 1 {
			// First GP call: simulate expired session.
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Second GP call (after re-login): success.
		cookie, err := r.Cookie("chocolatechip")
		if err != nil || cookie.Value != "renewed" {
			http.Error(w, "no renewed cookie", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, sampleOMMJSON)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(&http.Client{}, server.URL)
	fetcher.SetCredentials("u", "p")

	// Force loggedIn=true so it skips initial login and goes straight to GP.
	fetcher.mu.Lock()
	fetcher.loggedIn = true
	fetcher.activeUsername = "u"
	fetcher.activePassword = "p"
	fetcher.mu.Unlock()

	result, err := fetcher.Fetch(context.Background(), server.URL+"/gp")
	if err != nil {
		t.Fatalf("expected auto-retry to succeed, got: %v", err)
	}
	if result.SatelliteCount != 1 {
		t.Errorf("expected 1 satellite, got %d", result.SatelliteCount)
	}
	// Should have logged in once (re-login after 401) and made 2 GP calls.
	if loginCount.Load() != 1 {
		t.Errorf("expected 1 re-login, got %d", loginCount.Load())
	}
	if gpCallCount.Load() != 2 {
		t.Errorf("expected 2 GP calls (fail + retry), got %d", gpCallCount.Load())
	}
}

func TestSpaceTrackFetcher_FetchWithCredentials_SequentialSwitch(t *testing.T) {
	// Verify FetchWithCredentials re-authenticates when credentials change.
	var loginIdentities []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			if err := r.ParseForm(); err == nil {
				mu.Lock()
				loginIdentities = append(loginIdentities, r.FormValue("identity"))
				mu.Unlock()
			}
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, sampleOMMJSON)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(&http.Client{}, server.URL)

	// FetchWithCredentials should use alice, regardless of shared state.
	_, err := fetcher.FetchWithCredentials(context.Background(), server.URL+"/gp", "alice", "pass-a")
	if err != nil {
		t.Fatal(err)
	}

	// Then fetch as bob — should re-login as bob.
	_, err = fetcher.FetchWithCredentials(context.Background(), server.URL+"/gp", "bob", "pass-b")
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(loginIdentities) < 2 {
		t.Fatalf("expected 2 logins, got %d", len(loginIdentities))
	}
	if loginIdentities[0] != "alice" {
		t.Errorf("first login should be alice, got %s", loginIdentities[0])
	}
	if loginIdentities[1] != "bob" {
		t.Errorf("second login should be bob, got %s", loginIdentities[1])
	}
}

func TestSpaceTrackFetcher_ConcurrentCredsIsolation(t *testing.T) {
	// Regression test: exercises concurrent FetchWithCredentials calls with
	// different credentials to verify that the serialized lock prevents
	// session/cookie interleaving.
	//
	// Each login sets a per-identity cookie. The GP handler checks which
	// cookie was sent and records the identity. Both alice and bob must
	// fetch under their own identity.

	type gpReq struct {
		identity string // identity that the session cookie maps to
	}
	var gpRequests []gpReq
	var mu sync.Mutex

	// Map cookie value → identity for session tracking.
	cookieToIdentity := sync.Map{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			identity := r.FormValue("identity")
			cookieVal := identity + "-session"
			cookieToIdentity.Store(cookieVal, identity)

			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: cookieVal, Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		// GP request — record which session cookie was used.
		cookie, err := r.Cookie("chocolatechip")
		if err != nil {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		loaded, ok := cookieToIdentity.Load(cookie.Value)
		if !ok {
			http.Error(w, "unknown session", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		gpRequests = append(gpRequests, gpReq{identity: loaded.(string)})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, sampleOMMJSON)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(&http.Client{}, server.URL)
	gpURL := server.URL + "/gp"

	var wg sync.WaitGroup
	errs := make([]error, 2)

	// Launch alice and bob concurrently.
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = fetcher.FetchWithCredentials(context.Background(), gpURL, "alice", "pass-a")
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = fetcher.FetchWithCredentials(context.Background(), gpURL, "bob", "pass-b")
	}()

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
	}

	// Verify each GP request used the correct session.
	mu.Lock()
	defer mu.Unlock()
	if len(gpRequests) != 2 {
		t.Fatalf("expected exactly 2 GP requests, got %d", len(gpRequests))
	}

	// Both alice and bob should have fetched under their own identity.
	identities := map[string]bool{}
	for _, req := range gpRequests {
		identities[req.identity] = true
	}
	if !identities["alice"] || !identities["bob"] {
		t.Errorf("expected {alice, bob}, got %v", gpRequests)
	}
}

func TestSpaceTrackFetcher_CredentialsUnchangedNoRelogin(t *testing.T) {
	var loginCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			loginCount.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s", Path: "/"})
			_, _ = fmt.Fprint(w, `""`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, sampleOMMJSON)
	}))
	defer server.Close()

	fetcher := NewSpaceTrackFetcher(&http.Client{}, server.URL)
	fetcher.SetCredentials("user", "pass")

	// First fetch: login + GP.
	if _, err := fetcher.Fetch(context.Background(), server.URL+"/gp"); err != nil {
		t.Fatal(err)
	}
	// SetCredentials with SAME creds — should NOT force re-login.
	fetcher.SetCredentials("user", "pass")

	// Second fetch: should reuse session.
	if _, err := fetcher.Fetch(context.Background(), server.URL+"/gp"); err != nil {
		t.Fatal(err)
	}
	if loginCount.Load() != 1 {
		t.Errorf("expected 1 login (same creds = no re-login), got %d", loginCount.Load())
	}
}
