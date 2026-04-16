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
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
)

// Sentinel errors returned by GPFetcher implementations.
var (
	ErrRateLimited = errors.New("rate limited by upstream (HTTP 403)")
	ErrBadResponse = errors.New("unexpected HTTP response")
)

// maxResponseBody is the maximum size of a CelesTrak response we'll read (50 MB).
const maxResponseBody = 50 * 1024 * 1024

// GPFetchResult holds the outcome of a GP data fetch.
type GPFetchResult struct {
	// OMMs contains parsed OMM objects. On 304 Not Modified, cached OMMs
	// from the last successful fetch are returned.
	OMMs           []sgp4.OMM
	SatelliteCount int
	FetchedAt      time.Time
	NotModified    bool // true when server returned 304 (ETag matched)
}

// GPFetcher defines the interface for fetching GP (General Perturbations) data.
// Implementations must be safe for concurrent use.
type GPFetcher interface {
	Fetch(ctx context.Context, url string) (GPFetchResult, error)
}

// CelesTrakFetcher implements GPFetcher for CelesTrak OMM JSON endpoints.
// It caches ETags and OMM data per URL to avoid redundant downloads.
// On HTTP 304 Not Modified, cached OMMs are returned so callers can
// re-derive time-dependent data (e.g., pass predictions) even when
// upstream data has not changed.
type CelesTrakFetcher struct {
	httpClient *http.Client
	mu         sync.Mutex
	etagCache  map[string]string     // url -> last ETag
	ommCache   map[string][]sgp4.OMM // url -> last parsed OMMs
}

// NewCelesTrakFetcher creates a new fetcher with the given HTTP client.
func NewCelesTrakFetcher(httpClient *http.Client) *CelesTrakFetcher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CelesTrakFetcher{
		httpClient: httpClient,
		etagCache:  make(map[string]string),
		ommCache:   make(map[string][]sgp4.OMM),
	}
}

// Fetch retrieves OMM JSON from the given URL.
// It uses conditional GET (If-None-Match) when a cached ETag exists.
// Returns ErrRateLimited on HTTP 403 (CelesTrak bandwidth policy).
func (f *CelesTrakFetcher) Fetch(ctx context.Context, url string) (GPFetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "ntn-operators/0.1")

	// Set conditional GET header if we have a cached ETag.
	f.mu.Lock()
	etag := f.etagCache[url]
	f.mu.Unlock()

	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	now := time.Now()

	switch resp.StatusCode {
	case http.StatusNotModified:
		f.mu.Lock()
		cached := f.ommCache[url]
		f.mu.Unlock()
		return GPFetchResult{
			OMMs:           cached,
			SatelliteCount: len(cached),
			FetchedAt:      now,
			NotModified:    true,
		}, nil

	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
		if err != nil {
			return GPFetchResult{}, fmt.Errorf("reading response body: %w", err)
		}
		if int64(len(body)) > maxResponseBody {
			return GPFetchResult{}, fmt.Errorf("response body exceeds %d bytes limit", maxResponseBody)
		}

		omms, err := sgp4.ParseOMMs(body)
		if err != nil {
			return GPFetchResult{}, fmt.Errorf("parsing OMM JSON: %w", err)
		}

		// Cache the new ETag and OMMs.
		f.mu.Lock()
		if newETag := resp.Header.Get("ETag"); newETag != "" {
			f.etagCache[url] = newETag
		}
		f.ommCache[url] = omms
		f.mu.Unlock()

		return GPFetchResult{
			OMMs:           omms,
			SatelliteCount: len(omms),
			FetchedAt:      now,
		}, nil

	case http.StatusForbidden:
		return GPFetchResult{}, ErrRateLimited

	default:
		return GPFetchResult{}, fmt.Errorf("%w: HTTP %d from %s", ErrBadResponse, resp.StatusCode, url)
	}
}
