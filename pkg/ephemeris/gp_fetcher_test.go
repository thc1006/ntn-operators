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
	"time"
)

// validOMMJSON is a minimal valid CelesTrak OMM JSON array with 3 satellites.
const validOMMJSON = `[
  {
    "OBJECT_NAME": "ONEWEB-0012",
    "OBJECT_ID": "2019-010A",
    "EPOCH": "2026-04-15T22:27:22.252608",
    "MEAN_MOTION": 13.1659366,
    "ECCENTRICITY": 0.00021061,
    "INCLINATION": 87.9064,
    "RA_OF_ASC_NODE": 241.1509,
    "ARG_OF_PERICENTER": 110.8263,
    "MEAN_ANOMALY": 249.3093,
    "EPHEMERIS_TYPE": 0,
    "CLASSIFICATION_TYPE": "U",
    "NORAD_CAT_ID": 44057,
    "ELEMENT_SET_NO": 999,
    "REV_AT_EPOCH": 34337,
    "BSTAR": -0.00034023158,
    "MEAN_MOTION_DOT": -1.17e-6,
    "MEAN_MOTION_DDOT": 0
  },
  {
    "OBJECT_NAME": "ONEWEB-0008",
    "OBJECT_ID": "2019-010B",
    "EPOCH": "2026-04-15T20:10:00.000000",
    "MEAN_MOTION": 13.1660000,
    "ECCENTRICITY": 0.00020000,
    "INCLINATION": 87.9100,
    "RA_OF_ASC_NODE": 241.2000,
    "ARG_OF_PERICENTER": 111.0000,
    "MEAN_ANOMALY": 250.0000,
    "EPHEMERIS_TYPE": 0,
    "CLASSIFICATION_TYPE": "U",
    "NORAD_CAT_ID": 44058,
    "ELEMENT_SET_NO": 999,
    "REV_AT_EPOCH": 34330,
    "BSTAR": -0.00030000,
    "MEAN_MOTION_DOT": -1.0e-6,
    "MEAN_MOTION_DDOT": 0
  },
  {
    "OBJECT_NAME": "ONEWEB-0010",
    "OBJECT_ID": "2019-010C",
    "EPOCH": "2026-04-15T21:00:00.000000",
    "MEAN_MOTION": 13.1655000,
    "ECCENTRICITY": 0.00022000,
    "INCLINATION": 87.9000,
    "RA_OF_ASC_NODE": 241.1000,
    "ARG_OF_PERICENTER": 110.5000,
    "MEAN_ANOMALY": 248.0000,
    "EPHEMERIS_TYPE": 0,
    "CLASSIFICATION_TYPE": "U",
    "NORAD_CAT_ID": 44059,
    "ELEMENT_SET_NO": 999,
    "REV_AT_EPOCH": 34335,
    "BSTAR": -0.00032000,
    "MEAN_MOTION_DOT": -1.1e-6,
    "MEAN_MOTION_DDOT": 0
  }
]`

func TestFetch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validOMMJSON))
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
		_, _ = w.Write([]byte(validOMMJSON))
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
		_, _ = w.Write([]byte(validOMMJSON))
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
		_, _ = w.Write([]byte(validOMMJSON))
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
		_, _ = w.Write([]byte(validOMMJSON))
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
