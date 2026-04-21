/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// newTestHandler builds the same *Handler configuration main() wires
// up, so tests can feed it to newMux(...) and exercise the routing +
// health surface without spawning a real process. Any future change
// to how main() constructs the Handler should update this helper in
// lockstep.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return NewHandler(
		prometheus.NewRegistry(),
		Simulator{Start: start, PassDuration: 10 * time.Second, GapDuration: 10 * time.Second},
		"default", "e2e-slice",
		func() time.Time { return start.Add(3 * time.Second) }, // inside the pass window
	)
}

func TestNewMux_MetricsRouteServesExporterOutput(t *testing.T) {
	mux := newMux(newTestHandler(t))
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "ntn_e2e_rsrp_dbm") {
		t.Errorf("/metrics body missing ntn_e2e_rsrp_dbm line; got:\n%s", body)
	}
}

func TestNewMux_HealthzReturns200(t *testing.T) {
	mux := newMux(newTestHandler(t))
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("/healthz status = %d, want 200", w.Code)
	}
}

func TestNewMux_UnknownRouteReturns404(t *testing.T) {
	mux := newMux(newTestHandler(t))
	req := httptest.NewRequest("GET", "/does-not-exist", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("unknown route status = %d, want 404", w.Code)
	}
}

func TestBuildSimulator_RejectsNonPositiveDurations(t *testing.T) {
	// Guards the flag-validation path main() does at startup: a zero or
	// negative pass/gap duration would produce a pathological simulator
	// that always returns degradedMetrics, silently breaking E2E. The
	// builder exposes the same validation surface so main() can fail
	// fast with a clear message.
	cases := []struct {
		name        string
		pass        time.Duration
		gap         time.Duration
		wantErr     bool
		mustMention []string // substrings that must all appear in the error
	}{
		{"both positive", time.Second, time.Second, false, nil},
		{"zero pass", 0, time.Second, true, []string{"pass-duration"}},
		{"zero gap", time.Second, 0, true, []string{"gap-duration"}},
		{"negative pass", -1, time.Second, true, []string{"pass-duration"}},
		{"negative gap", time.Second, -1, true, []string{"gap-duration"}},
		// When both flags are wrong the admin needs to see both mistakes
		// in one run, not fix one and re-run to discover the next.
		{"both non-positive", 0, -1, true, []string{"pass-duration", "gap-duration"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSimulator(tc.pass, tc.gap)
			if tc.wantErr && err == nil {
				t.Fatalf("want error for pass=%v gap=%v, got nil", tc.pass, tc.gap)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want no error for pass=%v gap=%v, got %v", tc.pass, tc.gap, err)
			}
			for _, want := range tc.mustMention {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
		})
	}
}
