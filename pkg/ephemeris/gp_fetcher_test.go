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
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// validOMMJSON is a minimal valid CelesTrak OMM JSON array with 3
// satellites, embedded from pkg/ephemeris/testdata/oneweb-gp.json.
// The same file is also mounted into the E2E mock server (see
// test/e2e/e2e_suite_test.go setupCelestrakMock) so this fetcher's
// unit tests and the E2E reconciler test share a single source of
// truth — update the JSON once and both layers stay consistent.
//
//go:embed testdata/oneweb-gp.json
var validOMMJSON []byte

func TestFetch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	result, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SatelliteCount != 3 {
		t.Errorf("expected 3 satellites, got %d", result.SatelliteCount)
	}
	if result.NotModified {
		t.Error("expected NotModified=false")
	}
	if result.FetchedAt.IsZero() {
		t.Error("expected FetchedAt to be set")
	}
	if len(result.OMMs) != 3 {
		t.Fatalf("expected 3 OMMs, got %d", len(result.OMMs))
	}
	if result.OMMs[0].ObjectName != "ONEWEB-0012" {
		t.Errorf("expected first OMM to be ONEWEB-0012, got %s", result.OMMs[0].ObjectName)
	}
	if result.OMMs[0].NoradCatID != 44057 {
		t.Errorf("expected NORAD ID 44057, got %d", result.OMMs[0].NoradCatID)
	}
}

func TestFetch_ETagCaching_304(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())

	// First fetch: should get 200 with data.
	r1, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("first fetch error: %v", err)
	}
	if r1.SatelliteCount != 3 {
		t.Errorf("first fetch: expected 3 satellites, got %d", r1.SatelliteCount)
	}

	// Second fetch: should get 304.
	r2, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("second fetch error: %v", err)
	}
	if !r2.NotModified {
		t.Error("second fetch: expected NotModified=true")
	}

	if requestCount.Load() != 2 {
		t.Errorf("expected 2 requests, got %d", requestCount.Load())
	}
}

func TestFetch_ETagCaching_NewData(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentV := version.Load()
		currentETag := fmt.Sprintf(`"v%d"`, currentV)
		if r.Header.Get("If-None-Match") == currentETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", currentETag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())

	// First fetch with v1.
	_, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Change version so next fetch gets new data.
	version.Store(2)

	// Second fetch: ETag doesn't match v2, gets 200 with new data.
	r2, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if r2.NotModified {
		t.Error("expected new data, not 304")
	}
	if r2.SatelliteCount != 3 {
		t.Errorf("expected 3 satellites, got %d", r2.SatelliteCount)
	}
}

func TestFetch_HTTP403_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("GP data has not updated since your last successful download"))
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	_, err := fetcher.Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited, got: %v", err)
	}
}

// TestFetch_HTTP429_RateLimited pins I-19b: a 429 (defensive; e.g. a fronting CDN)
// is rate-limited and its Retry-After is parsed into the typed error.
func TestFetch_HTTP429_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	_, err := NewCelesTrakFetcher(server.Client()).Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429 must be rate-limited, got %v", err)
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected a *RateLimitError, got %v", err)
	}
	if rle.RetryAfter != 120*time.Second {
		t.Errorf("Retry-After: 120 must parse to 120s, got %v", rle.RetryAfter)
	}
	if rle.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", rle.StatusCode)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name, val string
		want      time.Duration
		approx    bool
	}{
		{"absent", "", 0, false},
		{"delay-seconds", "120", 120 * time.Second, false},
		{"zero", "0", 0, false},
		{"garbage", "soon", 0, false},
		{"negative clamped to 0", "-5", 0, false},
		{"huge clamped to max", "999999999", maxRetryAfter, false},
		{"http-date future", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{"http-date past", now.Add(-1 * time.Hour).Format(http.TimeFormat), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.val != "" {
				h.Set("Retry-After", tc.val)
			}
			got := parseRetryAfter(h)
			if tc.approx {
				if got < tc.want-5*time.Second || got > tc.want+2*time.Second {
					t.Errorf("parseRetryAfter(%q) = %v, want ~%v", tc.val, got, tc.want)
				}
			} else if got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestFetch_HTTP500_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	_, err := fetcher.Fetch(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrBadResponse) {
		t.Errorf("expected ErrBadResponse, got: %v", err)
	}
}

func TestFetch_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"not": "an array"}`))
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	_, err := fetcher.Fetch(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected parse error for invalid JSON")
	}
}

func TestFetch_EmptyArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	result, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SatelliteCount != 0 {
		t.Errorf("expected 0 satellites, got %d", result.SatelliteCount)
	}
}

func TestFetch_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // slow response
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	fetcher := NewCelesTrakFetcher(server.Client())
	_, err := fetcher.Fetch(ctx, server.URL)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestFetch_ConcurrentAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"concurrent"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())

	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	for range 10 {
		wg.Go(func() {
			result, err := fetcher.Fetch(context.Background(), server.URL)
			if err != nil {
				errCh <- err
				return
			}
			if result.SatelliteCount != 3 {
				errCh <- errors.New("unexpected satellite count")
			}
		})
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent fetch error: %v", err)
	}
}
