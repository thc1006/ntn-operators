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

func scrape(t *testing.T, h *Handler) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("handler returned status %d", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	return string(body)
}

func TestHandler_ScrapeInPass_EmitsHealthyRsrp(t *testing.T) {
	// Clock is frozen inside the pass window.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := NewHandler(prometheus.NewRegistry(), Simulator{
		Start: start, PassDuration: 10 * time.Second, GapDuration: 10 * time.Second,
	}, "default", "test-slice", func() time.Time { return start.Add(3 * time.Second) })
	body := scrape(t, h)
	if !strings.Contains(body, `ntn_e2e_rsrp_dbm{namespace="default",slice="test-slice"} -80`) {
		t.Errorf("expected healthy RSRP line, got:\n%s", body)
	}
	if !strings.Contains(body, `ntn_e2e_in_pass{namespace="default",slice="test-slice"} 1`) {
		t.Errorf("expected in_pass=1 line, got:\n%s", body)
	}
}

func TestHandler_ScrapeOutOfPass_EmitsDegradedRsrp(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := NewHandler(prometheus.NewRegistry(), Simulator{
		Start: start, PassDuration: 10 * time.Second, GapDuration: 10 * time.Second,
	}, "default", "test-slice", func() time.Time { return start.Add(15 * time.Second) })
	body := scrape(t, h)
	if !strings.Contains(body, `ntn_e2e_rsrp_dbm{namespace="default",slice="test-slice"} -140`) {
		t.Errorf("expected degraded RSRP line, got:\n%s", body)
	}
	if !strings.Contains(body, `ntn_e2e_in_pass{namespace="default",slice="test-slice"} 0`) {
		t.Errorf("expected in_pass=0 line, got:\n%s", body)
	}
}

func TestHandler_MetricsExposePacketLossAndLatency(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := NewHandler(prometheus.NewRegistry(), Simulator{
		Start: start, PassDuration: 10 * time.Second, GapDuration: 10 * time.Second,
	}, "default", "test-slice", func() time.Time { return start.Add(3 * time.Second) })
	body := scrape(t, h)
	if !strings.Contains(body, "ntn_e2e_latency_ms") {
		t.Error("latency_ms metric missing")
	}
	if !strings.Contains(body, "ntn_e2e_packet_loss_percent") {
		t.Error("packet_loss_percent metric missing")
	}
}

func TestHandler_MultipleScrapesReuseGauges(t *testing.T) {
	// Two scrapes at different simulated times must not panic on a
	// duplicate gauge registration inside the same handler.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(3 * time.Second)
	h := NewHandler(prometheus.NewRegistry(), Simulator{
		Start: start, PassDuration: 10 * time.Second, GapDuration: 10 * time.Second,
	}, "default", "test-slice", func() time.Time { return now })
	_ = scrape(t, h)
	now = start.Add(15 * time.Second) // jump into the gap
	body2 := scrape(t, h)
	if !strings.Contains(body2, `ntn_e2e_rsrp_dbm{namespace="default",slice="test-slice"} -140`) {
		t.Errorf("second scrape must reflect updated simulator state, got:\n%s", body2)
	}
}
