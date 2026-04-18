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
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/go-logr/logr"
)

// SpaceTrackFetcher implements GPFetcher for Space-Track.org OMM JSON endpoints.
// It manages cookie-based authentication with automatic session reuse and
// retry on 401. Credentials are passed per-Fetch call so multiple CRs with
// different Secrets don't interfere.
type SpaceTrackFetcher struct {
	httpClient *http.Client
	baseURL    string // e.g., "https://www.space-track.org"

	mu             sync.Mutex
	activeUsername string // credentials of the current session
	activePassword string
	loggedIn       bool
}

// NewSpaceTrackFetcher creates a SpaceTrack fetcher.
// baseURL is the SpaceTrack API root (e.g., "https://www.space-track.org").
// The httpClient should be created via netutil.NewSafeHTTPClient for SSRF protection.
func NewSpaceTrackFetcher(httpClient *http.Client, baseURL string) *SpaceTrackFetcher {
	// Ensure the client has a cookie jar for session management.
	if httpClient.Jar == nil {
		jar, _ := cookiejar.New(nil)
		httpClient.Jar = jar
	}
	return &SpaceTrackFetcher{
		httpClient: httpClient,
		baseURL:    baseURL,
	}
}

// SetCredentials sets the SpaceTrack login credentials.
// Only triggers re-login if the credentials actually changed.
func (f *SpaceTrackFetcher) SetCredentials(username, password string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activeUsername == username && f.activePassword == password {
		return // no change
	}
	f.activeUsername = username
	f.activePassword = password
	f.loggedIn = false // force re-login on next fetch
}

// Fetch retrieves GP data using previously set credentials (via SetCredentials).
// Deprecated: Use FetchWithCredentials for request-scoped credential isolation.
func (f *SpaceTrackFetcher) Fetch(ctx context.Context, gpURL string) (GPFetchResult, error) {
	f.mu.Lock()
	username := f.activeUsername
	password := f.activePassword
	f.mu.Unlock()
	// FetchWithCredentials re-acquires the lock internally.
	return f.FetchWithCredentials(ctx, gpURL, username, password)
}

// FetchWithCredentials retrieves GP data with request-scoped credentials.
// It re-authenticates the shared session when the requested credentials
// differ from the current active session and auto-retries on 401.
//
// The entire login+fetch sequence is serialized under a mutex to prevent
// cookie/session interleaving when concurrent callers use different
// credentials. This preserves correctness for the shared authenticated
// session at the cost of reduced throughput while a fetch is in progress.
func (f *SpaceTrackFetcher) FetchWithCredentials(
	ctx context.Context, gpURL, username, password string,
) (GPFetchResult, error) {
	if username == "" || password == "" {
		return GPFetchResult{}, fmt.Errorf("SpaceTrack credentials not set; call SetCredentials or pass credentials to FetchWithCredentials")
	}

	now := time.Now()
	log := logr.FromContextOrDiscard(ctx)

	// Serialize login + HTTP request to prevent cookie/session interleaving.
	// JSON parsing is done outside the lock to reduce contention.
	f.mu.Lock()

	needLogin := !f.loggedIn || f.activeUsername != username || f.activePassword != password
	if needLogin {
		log.V(1).Info("authenticating with SpaceTrack")
		if err := f.doLogin(ctx, username, password); err != nil {
			f.mu.Unlock()
			return GPFetchResult{}, fmt.Errorf("SpaceTrack login failed: %w", err)
		}
		f.activeUsername = username
		f.activePassword = password
		f.loggedIn = true
	}

	body, err := f.doFetchGPRaw(ctx, gpURL)
	if err != nil && isSessionExpired(err) {
		// On 401, retry login once and re-fetch.
		log.V(1).Info("session expired, re-authenticating")
		if loginErr := f.doLogin(ctx, username, password); loginErr != nil {
			f.mu.Unlock()
			return GPFetchResult{}, fmt.Errorf("SpaceTrack re-login failed: %w", loginErr)
		}
		f.activeUsername = username
		f.activePassword = password
		f.loggedIn = true
		body, err = f.doFetchGPRaw(ctx, gpURL)
	}

	f.mu.Unlock()

	if err != nil {
		return GPFetchResult{}, err
	}

	// Parse OMM JSON outside the lock — no shared state needed.
	return parseGPResult(body, now)
}

// doLogin authenticates with SpaceTrack via POST to /ajaxauth/login.
// Caller must hold f.mu.
func (f *SpaceTrackFetcher) doLogin(ctx context.Context, username, password string) error {
	loginURL := f.baseURL + "/ajaxauth/login"

	form := url.Values{
		"identity": {username},
		"password": {password},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("creating login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain body to reuse connection.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login returned HTTP %d", resp.StatusCode)
	}

	return nil
}

// errSessionExpired is a sentinel for 401 detection in retry logic.
var errSessionExpired = fmt.Errorf("SpaceTrack session expired")

// isSessionExpired checks if an error wraps the session expired sentinel.
func isSessionExpired(err error) bool {
	return errors.Is(err, errSessionExpired)
}

// doFetchGPRaw performs the authenticated HTTP request and returns the raw
// response body. Caller must hold f.mu (the cookie jar is used during .Do).
// On non-OK status, returns an appropriate error (draining the body first).
func (f *SpaceTrackFetcher) doFetchGPRaw(ctx context.Context, gpURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gpURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GP request: %w", err)
	}
	req.Header.Set("User-Agent", "ntn-operators/0.1")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GP request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
		if err != nil {
			return nil, fmt.Errorf("reading GP response: %w", err)
		}
		if len(body) > maxResponseBody {
			return nil, fmt.Errorf("GP response exceeds %d bytes", maxResponseBody)
		}
		return body, nil

	case http.StatusForbidden, http.StatusTooManyRequests:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, ErrRateLimited

	case http.StatusUnauthorized:
		_, _ = io.Copy(io.Discard, resp.Body)
		f.loggedIn = false
		return nil, fmt.Errorf("%w (HTTP 401)", errSessionExpired)

	default:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%w: HTTP %d from %s", ErrBadResponse, resp.StatusCode, gpURL)
	}
}

// parseGPResult parses raw OMM JSON into a GPFetchResult. Does not require the mutex.
func parseGPResult(body []byte, now time.Time) (GPFetchResult, error) {
	omms, err := sgp4.ParseOMMs(body)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("parsing OMM JSON: %w", err)
	}
	return GPFetchResult{
		OMMs:           omms,
		SatelliteCount: len(omms),
		FetchedAt:      now,
	}, nil
}
