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
// session/cookie interleaving when concurrent reconciles use different
// credentials. This is safe because controller-runtime defaults to
// MaxConcurrentReconciles=1, and SpaceTrack fetches are infrequent (every 2h+).
func (f *SpaceTrackFetcher) FetchWithCredentials(
	ctx context.Context, gpURL, username, password string,
) (GPFetchResult, error) {
	if username == "" || password == "" {
		return GPFetchResult{}, fmt.Errorf("SpaceTrack credentials not configured; set credentials via Secret reference")
	}

	// Serialize entire login+fetch to prevent cookie/session interleaving.
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	log := logr.FromContextOrDiscard(ctx)

	// Check if we need to (re-)login for this credential pair.
	needLogin := !f.loggedIn || f.activeUsername != username || f.activePassword != password

	if needLogin {
		log.V(1).Info("authenticating with SpaceTrack")
		if err := f.doLogin(ctx, username, password); err != nil {
			return GPFetchResult{}, fmt.Errorf("SpaceTrack login failed: %w", err)
		}
		f.activeUsername = username
		f.activePassword = password
		f.loggedIn = true
	}

	// Fetch GP data.
	result, err := f.doFetchGP(ctx, gpURL, now)
	if err == nil {
		return result, nil
	}

	// On 401, retry login once and re-fetch.
	if isSessionExpired(err) {
		log.V(1).Info("session expired, re-authenticating")
		if loginErr := f.doLogin(ctx, username, password); loginErr != nil {
			return GPFetchResult{}, fmt.Errorf("SpaceTrack re-login failed: %w", loginErr)
		}
		f.activeUsername = username
		f.activePassword = password
		f.loggedIn = true
		return f.doFetchGP(ctx, gpURL, now)
	}

	return GPFetchResult{}, err
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

// doFetchGP performs the authenticated GP data request.
// Caller must hold f.mu.
func (f *SpaceTrackFetcher) doFetchGP(ctx context.Context, gpURL string, now time.Time) (GPFetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gpURL, nil)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("creating GP request: %w", err)
	}
	req.Header.Set("User-Agent", "ntn-operators/0.1")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return GPFetchResult{}, fmt.Errorf("GP request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
		if err != nil {
			return GPFetchResult{}, fmt.Errorf("reading GP response: %w", err)
		}
		if len(body) > maxResponseBody {
			return GPFetchResult{}, fmt.Errorf("GP response exceeds %d bytes", maxResponseBody)
		}

		omms, err := sgp4.ParseOMMs(body)
		if err != nil {
			return GPFetchResult{}, fmt.Errorf("parsing OMM JSON: %w", err)
		}

		return GPFetchResult{
			OMMs:           omms,
			SatelliteCount: len(omms),
			FetchedAt:      now,
		}, nil

	case http.StatusForbidden, http.StatusTooManyRequests:
		return GPFetchResult{}, ErrRateLimited

	case http.StatusUnauthorized:
		f.loggedIn = false
		return GPFetchResult{}, fmt.Errorf("%w (HTTP 401)", errSessionExpired)

	default:
		return GPFetchResult{}, fmt.Errorf("%w: HTTP %d from %s", ErrBadResponse, resp.StatusCode, gpURL)
	}
}
