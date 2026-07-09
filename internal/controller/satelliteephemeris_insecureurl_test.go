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

package controller

import (
	"context"
	"testing"
)

// TestPublicHTTPSource covers the I-21 cleartext-http guard: an http:// URL is
// rejected only when its host resolves to a PUBLIC IP; https and private/
// in-cluster http (mirrors, E2E mocks) are allowed. Literal IPs are used so the
// resolver returns them verbatim — no DNS, deterministic.
func TestPublicHTTPSource(t *testing.T) {
	r := &SatelliteEphemerisReconciler{}
	ctx := context.Background()
	cases := []struct {
		name         string
		url          string
		wantInsecure bool
	}{
		{"https public is fine", "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb", false},
		{"https literal public is fine", "https://8.8.8.8/gp.json", false},
		{"http to literal public IP is rejected", "http://8.8.8.8/gp.json", true},
		{"http to public IP with port is rejected", "http://1.1.1.1:8080/gp.json", true},
		{"http to loopback (in-cluster mock) is allowed", "http://127.0.0.1:8080/gp.json", false},
		{"http to RFC1918 mirror is allowed", "http://10.0.0.5/gp.json", false},
		{"http to link-local metadata is allowed here (blocked at dial by SSRF guard)", "http://169.254.169.254/gp.json", false},
		{"malformed URL is not our concern", "://bad", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, insecure := r.publicHTTPSource(ctx, tc.url)
			if insecure != tc.wantInsecure {
				t.Errorf("publicHTTPSource(%q) insecure=%v, want %v", tc.url, insecure, tc.wantInsecure)
			}
		})
	}
}
