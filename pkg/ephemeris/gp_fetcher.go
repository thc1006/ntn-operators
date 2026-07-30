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
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/akhenakh/sgp4"
	"github.com/go-logr/logr"

	"github.com/thc1006/ntn-operators/pkg/netutil"
)

// Sentinel errors returned by GPFetcher implementations.
var (
	ErrRateLimited = errors.New("rate limited by upstream (HTTP 403)")
	ErrBadResponse = errors.New("unexpected HTTP response")
	// ErrInvalidSourceURL marks a spec.source.url that a fetcher rejects up front (e.g. a
	// Space-Track query URL off the trusted origin). It is a PERMANENT configuration error —
	// retrying cannot fix it — so the caller must classify it non-requeuing/slow, not loop it
	// through the transient workqueue backoff. #222 review.
	ErrInvalidSourceURL = errors.New("invalid source URL")
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
	// Snippet is a short, sanitized one-line prefix of the response body. CelesTrak
	// returns an explanatory reason in the 403 body (e.g. "GP data has not updated since
	// your last successful download"); surfacing it turns an opaque "HTTP 403" into an
	// actionable diagnosis. Empty when the source sent no body. findings.md I-19b/#200-C6.
	Snippet string
}

func (e *RateLimitError) Error() string {
	if e.Snippet != "" {
		return fmt.Sprintf("rate limited by upstream (HTTP %d): %s", e.StatusCode, e.Snippet)
	}
	return fmt.Sprintf("rate limited by upstream (HTTP %d)", e.StatusCode)
}

// maxSnippetBytes bounds how much of an (untrusted) error response body we read for
// diagnostics.
const maxSnippetBytes = 512

// readBodySnippet reads a short, sanitized one-line prefix of an untrusted response body
// for inclusion in an error/condition/log message: bounded to maxSnippetBytes, control
// characters (incl. newlines) collapsed to spaces and whitespace runs squeezed, so the
// snippet stays single-line and log-safe.
func readBodySnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, maxSnippetBytes))
	return sanitizeSnippet(b)
}

// sanitizeSnippet turns an already-read untrusted body into a single-line, log-safe string
// whose ENCODED length never exceeds maxSnippetBytes. It keeps only printable runes
// (unicode.IsPrint — which excludes control AND format/bidi/zero-width characters that
// unicode.IsControl misses) and collapses everything else, including invalid UTF-8 bytes, to
// a space. The bound is on the OUTPUT bytes, not the input: an invalid byte re-encodes to a
// 3-byte U+FFFD and a multibyte rune to up to 4 bytes, so truncating the input alone could
// still overflow maxSnippetBytes (#222 review, sanitizer hardening).
func sanitizeSnippet(b []byte) string {
	out := make([]byte, 0, maxSnippetBytes)
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		i += size
		ch := ' '
		// Keep only printable runes; an invalid UTF-8 byte decodes to (RuneError, size 1).
		validRune := r != utf8.RuneError || size != 1
		if validRune && unicode.IsPrint(r) {
			ch = r
		}
		if len(out)+utf8.RuneLen(ch) > maxSnippetBytes {
			break
		}
		out = utf8.AppendRune(out, ch)
	}
	return strings.Join(strings.Fields(string(out)), " ")
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
	// delay-seconds: RFC 9110 allows an arbitrary-length non-negative integer, which may
	// exceed int64. Treat any all-ASCII-digit value as delay-seconds and SATURATE to the cap
	// on overflow — do NOT let strconv's range error fall through to the HTTP-date branch and
	// return 0 (which would wrongly drop a huge but valid Retry-After) (#222 review).
	if isAllASCIIDigits(v) {
		secs, err := strconv.ParseUint(v, 10, 64)
		if errors.Is(err, strconv.ErrRange) || (err == nil && secs >= uint64(maxRetryAfter/time.Second)) {
			return maxRetryAfter
		}
		if err != nil {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil { // HTTP-date
		return clampRetryAfter(time.Until(t))
	}
	return 0
}

// isAllASCIIDigits reports whether s is a non-empty run of ASCII digits 0-9.
func isAllASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
	// ETag / LastModified are the origin's cache validators for this body, surfaced so a
	// caller can persist them and re-seed a cold fetcher (SeedConditionalCache) after a
	// restart — making the first post-restart fetch a conditional GET, not a full download.
	ETag         string
	LastModified string
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
	httpClient   *http.Client
	etagCache    sync.Map // url -> string (ETag)
	lastModCache sync.Map // url -> string (Last-Modified)
	ommCache     sync.Map // url -> []sgp4.OMM
}

// NewCelesTrakFetcher creates a new fetcher with the given HTTP client.
func NewCelesTrakFetcher(httpClient *http.Client) *CelesTrakFetcher {
	if httpClient == nil {
		// Default to the SSRF-safe client, NOT a bare http.Client: the source URL is
		// CR-controlled, so a nil client must never silently yield an unguarded fetcher
		// (default ProxyFromEnvironment, no dial-time IP validation). #199-C2.
		httpClient = netutil.NewSafeHTTPClient(30 * time.Second)
	}
	return &CelesTrakFetcher{
		httpClient: httpClient,
	}
}

// SeedConditionalCache restores a prior fetch's cache validators (ETag / Last-Modified) for url
// so the FIRST fetch after a process restart or leader failover issues a conditional GET
// (304-capable) instead of re-downloading the full body. It deliberately seeds NO OMM body: the
// url-keyed body cache is shared across every CR fetching that url, and a durable restore only
// holds THIS CR's filtered/capped subset — seeding it would hand a co-url CR with a different
// NORAD selector the wrong 304 body. On a post-restart 304 the fetcher therefore returns an empty
// OMM set (NotModified=true) and the reconciler re-serves its own per-CR cache. LoadOrStore never
// clobbers a live in-memory validator with older restored data.
func (f *CelesTrakFetcher) SeedConditionalCache(url, etag, lastModified string) {
	if url == "" {
		return
	}
	if etag != "" {
		f.etagCache.LoadOrStore(url, etag)
	}
	if lastModified != "" {
		f.lastModCache.LoadOrStore(url, lastModified)
	}
}

// stringOrEmpty unwraps a sync.Map value that is a string, or "" when absent/other-typed.
func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
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

	// Conditional GET: prefer the strong ETag validator (If-None-Match); also send the
	// Last-Modified validator (If-Modified-Since) so an origin that emits only Last-Modified
	// still answers 304. Per RFC 9110 the origin evaluates If-None-Match first, so sending
	// both is safe. Either header may come from a durable restore (SeedConditionalCache) so
	// the FIRST fetch after a restart is conditional rather than a full re-download.
	if etag, ok := f.etagCache.Load(url); ok {
		req.Header.Set("If-None-Match", etag.(string))
	}
	if lastMod, ok := f.lastModCache.Load(url); ok {
		req.Header.Set("If-Modified-Since", lastMod.(string))
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	now := time.Now()

	switch resp.StatusCode {
	case http.StatusNotModified:
		log.V(2).Info("conditional GET hit (304)", "url", url, "duration", time.Since(fetchStart))
		var cached []sgp4.OMM
		if v, ok := f.ommCache.Load(url); ok {
			cached = v.([]sgp4.OMM)
		}
		// cached is empty when only the validators were restored into a cold fetcher
		// (SeedConditionalCache seeds no body); the reconciler then re-serves its own
		// per-CR cache for THIS object, which is why the body is not seeded here.
		etag, _ := f.etagCache.Load(url)
		lastMod, _ := f.lastModCache.Load(url)
		return GPFetchResult{
			OMMs:           cached,
			SatelliteCount: len(cached),
			FetchedAt:      now,
			NotModified:    true,
			ETag:           stringOrEmpty(etag),
			LastModified:   stringOrEmpty(lastMod),
		}, nil

	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
		if err != nil {
			return GPFetchResult{}, fmt.Errorf("reading response body: %w", err)
		}
		if int64(len(body)) > maxResponseBody {
			return GPFetchResult{}, fmt.Errorf("response body exceeds %d bytes limit", maxResponseBody)
		}

		omms, err := ParseValidOMMs(log, body)
		if err != nil {
			return GPFetchResult{}, fmt.Errorf("parsing OMM JSON: %w", err)
		}

		// Cache OMMs before the validators so that a concurrent 304 path always
		// finds the OMM data when it observes the new ETag / Last-Modified.
		f.ommCache.Store(url, omms)
		newETag := resp.Header.Get("ETag")
		newLastMod := resp.Header.Get("Last-Modified")
		if newETag != "" {
			f.etagCache.Store(url, newETag)
		}
		if newLastMod != "" {
			f.lastModCache.Store(url, newLastMod)
		}

		log.V(1).Info("fetch complete", "satellites", len(omms), "bytes", len(body), "duration", time.Since(fetchStart))
		return GPFetchResult{
			OMMs:           omms,
			SatelliteCount: len(omms),
			FetchedAt:      now,
			ETag:           newETag,
			LastModified:   newLastMod,
		}, nil

	case http.StatusForbidden, http.StatusTooManyRequests:
		// CelesTrak signals over-frequency / bandwidth abuse with a custom HTTP 403
		// (429 handled defensively for a fronting proxy). Retrying does not change
		// the response and risks a firewall block, so this is a rate-limit signal,
		// not a transient error — the caller requeues at the slow refresh cadence.
		return GPFetchResult{}, &RateLimitError{
			RetryAfter: parseRetryAfter(resp.Header),
			StatusCode: resp.StatusCode,
			Snippet:    readBodySnippet(resp.Body),
		}

	default:
		return GPFetchResult{}, fmt.Errorf("%w: HTTP %d from %s", ErrBadResponse, resp.StatusCode, url)
	}
}

// ParseValidOMMs parses OMM JSON and drops any element set whose orbital elements are non-finite or
// physically out of range (see validOMM), so a malformed-but-valid-JSON member never reaches the OMM
// cache or SGP4 — where it would propagate to NaN/garbage positions that flow into SIB19. It is the
// single validated entry point for turning OMM bytes into element sets, shared by the CelesTrak fetch,
// the Space-Track fetch, and the durable-cache restore so none of them can bypass validation (#227).
func ParseValidOMMs(log logr.Logger, body []byte) ([]sgp4.OMM, error) {
	omms, err := sgp4.ParseOMMs(body)
	if err != nil {
		return nil, err
	}
	return filterValidOMMs(log, omms), nil
}

// filterValidOMMs returns the element sets whose orbital elements are finite and physically in range,
// dropping the rest. It logs a single line when anything was dropped (fetches are hours apart, so this
// is not spammy) so a source data-quality regression is visible rather than silently propagated.
func filterValidOMMs(log logr.Logger, omms []sgp4.OMM) []sgp4.OMM {
	out := omms[:0:0] // fresh backing array; do not alias the caller's slice
	dropped := 0
	for _, o := range omms {
		if err := validOMM(o); err != nil {
			dropped++
			log.V(1).Info("dropping malformed element set", "norad", o.NoradCatID, "object", o.ObjectName, "reason", err.Error())
			continue
		}
		out = append(out, o)
	}
	if dropped > 0 {
		log.Info("dropped malformed element sets", "dropped", dropped, "kept", len(out))
	}
	return out
}

// validOMM reports why an OMM's orbital elements are unusable, or nil when they are sound. SGP4 would
// otherwise turn a non-finite or out-of-range set into NaN/garbage positions (eccentricity >= 1 is a
// non-elliptical orbit SGP4 does not model; a non-positive mean motion is a malformed set). Angles are
// only checked for finiteness — SGP4 normalises them modulo 360°. Epoch validity is NOT re-checked here:
// the controller's source-epoch freshness/future-skew gate already rejects an absurd or stale epoch.
func validOMM(o sgp4.OMM) error {
	for _, e := range []struct {
		name string
		v    float64
	}{
		{"MEAN_MOTION", o.MeanMotion}, {"ECCENTRICITY", o.Eccentricity}, {"INCLINATION", o.Inclination},
		{"RA_OF_ASC_NODE", o.RAOfAscNode}, {"ARG_OF_PERICENTER", o.ArgOfPericenter}, {"MEAN_ANOMALY", o.MeanAnomaly},
		{"BSTAR", o.BStar}, {"MEAN_MOTION_DOT", o.MeanMotionDot}, {"MEAN_MOTION_DDOT", o.MeanMotionDDot},
	} {
		if math.IsNaN(e.v) || math.IsInf(e.v, 0) {
			return fmt.Errorf("%s is not finite (%v)", e.name, e.v)
		}
	}
	if o.MeanMotion <= 0 {
		return fmt.Errorf("MEAN_MOTION %v must be positive", o.MeanMotion)
	}
	if o.Eccentricity < 0 || o.Eccentricity >= 1 {
		return fmt.Errorf("ECCENTRICITY %v outside [0,1)", o.Eccentricity)
	}
	if o.Inclination < 0 || o.Inclination > 180 {
		return fmt.Errorf("INCLINATION %v outside [0,180]", o.Inclination)
	}
	return nil
}
