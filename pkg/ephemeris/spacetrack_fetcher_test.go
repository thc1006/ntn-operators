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
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if err != ErrRateLimited {
		t.Errorf("expected ErrRateLimited, got %v", err)
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
