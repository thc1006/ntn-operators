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
	"errors"
	"testing"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/netutil"
)

func ephWithSource(srcType, url string) *ntnv1alpha1.SatelliteEphemeris {
	return &ntnv1alpha1.SatelliteEphemeris{
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{Type: srcType, URL: url},
		},
	}
}

func TestFetcherForSource_SourceHostAllowlist(t *testing.T) {
	const celestrak = "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=json"

	t.Run("CelesTrak host not in allow-list is refused before any dial", func(t *testing.T) {
		r := &SatelliteEphemerisReconciler{
			Fetcher:             &mockGPFetcher{}, // present, but must not be returned
			SourceHostAllowlist: netutil.ParseEndpointAllowlist("space-track.org"),
		}
		f, err := r.fetcherForSource(context.Background(), ephWithSource("CelesTrak", celestrak))
		if !errors.Is(err, errSourceHostNotAllowed) {
			t.Fatalf("expected errSourceHostNotAllowed, got %v", err)
		}
		if f != nil {
			t.Errorf("no fetcher must be returned on a policy refusal, got %T", f)
		}
	})

	t.Run("CelesTrak host in allow-list is permitted", func(t *testing.T) {
		r := &SatelliteEphemerisReconciler{
			Fetcher:             &mockGPFetcher{},
			SourceHostAllowlist: netutil.ParseEndpointAllowlist("celestrak.org"),
		}
		f, err := r.fetcherForSource(context.Background(), ephWithSource("CelesTrak", celestrak))
		if err != nil {
			t.Fatalf("allow-listed host must be permitted, got %v", err)
		}
		if f == nil {
			t.Error("expected the configured fetcher, got nil")
		}
	})

	t.Run("empty allow-list permits any host (backward compatible)", func(t *testing.T) {
		r := &SatelliteEphemerisReconciler{Fetcher: &mockGPFetcher{}} // zero-value allow-list
		if _, err := r.fetcherForSource(context.Background(), ephWithSource("CelesTrak", celestrak)); err != nil {
			t.Fatalf("empty allow-list must permit any host, got %v", err)
		}
	})

	t.Run("SpaceTrack is not gated by the source-host allow-list", func(t *testing.T) {
		// SpaceTrack's contact host is the admin-hardcoded API base, so the CelesTrak-scoped
		// gate must not fire even when the source URL host is absent from the allow-list.
		r := &SatelliteEphemerisReconciler{
			SpaceTrackFetcher:   nil, // disabled → a DIFFERENT error, never the host refusal
			SourceHostAllowlist: netutil.ParseEndpointAllowlist("celestrak.org"),
		}
		_, err := r.fetcherForSource(context.Background(),
			ephWithSource("SpaceTrack", "https://www.space-track.org/basicspacedata/query/..."))
		if errors.Is(err, errSourceHostNotAllowed) {
			t.Fatalf("SpaceTrack must not be gated by the source-host allow-list, got %v", err)
		}
	})
}
