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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestFetch_SurfacesValidators: a 200 must expose the origin's ETag and Last-Modified on the
// result so the reconciler can persist them durably (and later re-seed a cold fetcher).
func TestFetch_SurfacesValidators(t *testing.T) {
	const lm = "Wed, 30 Jul 2026 12:00:00 GMT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", lm)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	r, err := NewCelesTrakFetcher(server.Client()).Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if r.ETag != `"v1"` {
		t.Errorf("ETag = %q, want %q", r.ETag, `"v1"`)
	}
	if r.LastModified != lm {
		t.Errorf("LastModified = %q, want %q", r.LastModified, lm)
	}
}

// TestSeedConditionalCache_EnablesConditionalGET is the durable-restart payoff: a cold fetcher
// seeded with only a validator (no body) issues a conditional GET, and a resulting 304 carries
// NO OMMs — the reconciler re-serves its own per-CR cache. Seeding a body here would be unsafe
// because the url-keyed body cache is shared across every CR fetching that url.
func TestSeedConditionalCache_EnablesConditionalGET(t *testing.T) {
	var got304 atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"seeded"` {
			got304.Store(true)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"fresh"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	fetcher.SeedConditionalCache(server.URL, `"seeded"`, "") // restored validator, no body

	r, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !got304.Load() {
		t.Fatal("first post-seed fetch did not send the seeded If-None-Match (no conditional GET)")
	}
	if !r.NotModified {
		t.Fatal("expected NotModified=true from the seeded validator")
	}
	if len(r.OMMs) != 0 {
		t.Fatalf("SeedConditionalCache must not seed a body; got %d OMMs on the cold-start 304", len(r.OMMs))
	}
	if r.ETag != `"seeded"` {
		t.Errorf("304 should surface the validator used; ETag = %q", r.ETag)
	}
}

// TestFetch_IfModifiedSince_FromSeed: with only a Last-Modified seeded (no ETag), the fetch sends
// If-Modified-Since and NOT If-None-Match, so an origin that emits only Last-Modified still 304s.
func TestFetch_IfModifiedSince_FromSeed(t *testing.T) {
	const lm = "Wed, 30 Jul 2026 12:00:00 GMT"
	var sentIMS, sentINM atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Modified-Since") == lm {
			sentIMS.Store(true)
		}
		if r.Header.Get("If-None-Match") != "" {
			sentINM.Store(true)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	fetcher.SeedConditionalCache(server.URL, "", lm)

	r, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !sentIMS.Load() {
		t.Fatal("did not send If-Modified-Since from the seeded Last-Modified")
	}
	if sentINM.Load() {
		t.Fatal("sent If-None-Match with no ETag seeded")
	}
	if !r.NotModified {
		t.Fatal("expected NotModified=true")
	}
	if r.LastModified != lm {
		t.Errorf("304 should surface the Last-Modified used; got %q", r.LastModified)
	}
}

// TestSeedConditionalCache_DoesNotClobberLiveValidator: a late restore must not overwrite a
// fresher in-memory validator learned from a live fetch (LoadOrStore semantics) — otherwise a
// restart-race could downgrade a warm fetcher to a stale ETag and force a needless full download.
func TestSeedConditionalCache_DoesNotClobberLiveValidator(t *testing.T) {
	var lastINM atomic.Value // string
	lastINM.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastINM.Store(r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", `"live"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validOMMJSON)
	}))
	t.Cleanup(server.Close)

	fetcher := NewCelesTrakFetcher(server.Client())
	if _, err := fetcher.Fetch(context.Background(), server.URL); err != nil { // learns ETag "live"
		t.Fatalf("first fetch: %v", err)
	}
	fetcher.SeedConditionalCache(server.URL, `"stale"`, "") // late restore of an older validator
	if _, err := fetcher.Fetch(context.Background(), server.URL); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := lastINM.Load().(string); got != `"live"` {
		t.Fatalf("seed clobbered the live validator: If-None-Match = %q, want %q", got, `"live"`)
	}
}
