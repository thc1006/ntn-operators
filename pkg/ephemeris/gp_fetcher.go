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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/go-logr/logr"
)

// Sentinel errors returned by GPFetcher implementations.
var (
	ErrRateLimited = errors.New("rate limited by upstream (HTTP 403)")
	ErrBadResponse = errors.New("unexpected HTTP response")
	// ErrAuthFailed marks a credential/authentication failure (e.g. Space-Track
	// returns HTTP 200 with a {"Login":"Failed"} body on bad credentials). It is a
	// persistent error — retrying with the same credentials cannot succeed — so the
	// caller requeues slowly rather than hammering the login endpoint (which risks
	// account suspension). findings.md I-20.
	ErrAuthFailed = errors.New("authentication failed")
)

// userAgent is a descriptive User-Agent as required by CelesTrak's usage policy
// (machine-to-machine clients must identify themselves). findings.md I-19b/N-7.
const userAgent = "ntn-operators/0.6 (+https://github.com/thc1006/ntn-operators)"

// maxRetryAfter caps a server-supplied Retry-After so a malicious or garbled value
// cannot park the reconcile arbitrarily far in the future.
const maxRetryAfter = 24 * time.Hour

// RateLimitError is returned when the GP source signals rate limiting. It carries
// the parsed Retry-After delay (0 if none) and the observed status code. Different
// sources signal differently (CelesTrak: HTTP 403; Space-Track: HTTP 500 with a
// "violated your query rate limit" body; a fronting proxy may use 429), so the
// caller keys off this type, not a raw status. It satisfies errors.Is(err,
// ErrRateLimited) for backward compatibility. findings.md I-19b.
type RateLimitError struct {
	RetryAfter time.Duration
	StatusCode int
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited by upstream (HTTP %d)", e.StatusCode)
}

// Is lets errors.Is(err, ErrRateLimited) match a *RateLimitError.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// parseRetryAfter parses a Retry-After header per RFC 9110 §10.2.3, which allows
// BOTH forms: delay-seconds (a non-negative integer, e.g. "120") OR an HTTP-date.
// Returns 0 when absent/unparseable, clamped to [0, maxRetryAfter]. CelesTrak and
// Space-Track do not currently send Retry-After; this is defensive (a CDN/proxy in
// front might). findings.md I-19b.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil { // delay-seconds
		return clampRetryAfter(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil { // HTTP-date
		return clampRetryAfter(time.Until(t))
	}
	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d > maxRetryAfter:
		return maxRetryAfter
	default:
		return d
	}
}

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
	etagCache  sync.Map // url -> string (ETag)
	ommCache   sync.Map // url -> []sgp4.OMM
}

// NewCelesTrakFetcher creates a new fetcher with the given HTTP client.
func NewCelesTrakFetcher(httpClient *http.Client) *CelesTrakFetcher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CelesTrakFetcher{
		httpClient: httpClient,
	}
}

// Fetch retrieves OMM JSON from the given URL.
// It uses conditional GET (If-None-Match) when a cached ETag exists.
// Returns ErrRateLimited on HTTP 403 (CelesTrak bandwidth policy).
func (f *CelesTrakFetcher) Fetch(ctx context.Context, url string) (GPFetchResult, error) {
	log := logr.FromContextOrDiscard(ctx)
	log.V(2).Info("fetching GP data", "url", url)
	fetchStart := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	// Set conditional GET header if we have a cached ETag.
	if etag, ok := f.etagCache.Load(url); ok {
		req.Header.Set("If-None-Match", etag.(string))
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	now := time.Now()

	switch resp.StatusCode {
	case http.StatusNotModified:
		log.V(2).Info("ETag cache hit (304)", "url", url, "duration", time.Since(fetchStart))
		var cached []sgp4.OMM
		if v, ok := f.ommCache.Load(url); ok {
			cached = v.([]sgp4.OMM)
		}
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

		// Cache OMMs before ETag so that a concurrent 304 path always
		// finds the OMM data when it observes the new ETag.
		f.ommCache.Store(url, omms)
		if newETag := resp.Header.Get("ETag"); newETag != "" {
			f.etagCache.Store(url, newETag)
		}

		log.V(1).Info("fetch complete", "satellites", len(omms), "bytes", len(body), "duration", time.Since(fetchStart))
		return GPFetchResult{
			OMMs:           omms,
			SatelliteCount: len(omms),
			FetchedAt:      now,
		}, nil

	case http.StatusForbidden, http.StatusTooManyRequests:
		// CelesTrak signals over-frequency / bandwidth abuse with a custom HTTP 403
		// (429 handled defensively for a fronting proxy). Retrying does not change
		// the response and risks a firewall block, so this is a rate-limit signal,
		// not a transient error — the caller requeues at the slow refresh cadence.
		return GPFetchResult{}, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header), StatusCode: resp.StatusCode}

	default:
		return GPFetchResult{}, fmt.Errorf("%w: HTTP %d from %s", ErrBadResponse, resp.StatusCode, url)
	}
}
