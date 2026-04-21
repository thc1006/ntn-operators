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

// newTestMux builds the same http.ServeMux main() assembles, so we can
// exercise its routing and health behaviour without spawning a real
// process. Any future addition of a new route should extend both this
// helper and the main() equivalent in lockstep.
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
		name     string
		pass     time.Duration
		gap      time.Duration
		wantErr  bool
		errMatch string
	}{
		{"both positive", time.Second, time.Second, false, ""},
		{"zero pass", 0, time.Second, true, "pass-duration"},
		{"zero gap", time.Second, 0, true, "gap-duration"},
		{"negative pass", -1, time.Second, true, "pass-duration"},
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
			if tc.wantErr && !strings.Contains(err.Error(), tc.errMatch) {
				t.Errorf("error %q should mention %q", err, tc.errMatch)
			}
		})
	}
}
