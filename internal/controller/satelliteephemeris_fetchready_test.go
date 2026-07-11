/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

// TestReconcile_GPFetchReady_TracksUsableData pins the F5 gap: gp_satellite_count == 0
// cannot alert on a cold start that has NEVER fetched (the series is absent, and
// PromQL `== 0` never matches an absent series). gp_fetch_ready is set to 0 on that
// first failed reconcile, so `gp_fetch_ready == 0 for N` catches a pipeline that never
// came up, and returns to 1 once a fetch (or cache serve) yields usable data.
func TestReconcile_GPFetchReady_TracksUsableData(t *testing.T) {
	sch := makeScheme(t)
	eph := &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: "eph-fetchready", Namespace: "default"},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	fetcher := &mockGPFetcher{err: errors.New("connection refused")}
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10), Fetcher: fetcher,
	}
	key := types.NamespacedName{Name: "eph-fetchready", Namespace: "default"}
	labels := prometheus.Labels{"namespace": "default", "ephemeris": "eph-fetchready"}

	// 1) Cold start, fetch fails, no cache to fall back on → readiness 0. This is the
	// never-fetched case gp_satellite_count == 0 cannot alert on.
	_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	if got := testutil.ToFloat64(ntnmetrics.GPFetchReady.With(labels)); got != 0 {
		t.Fatalf("failed cold-start fetch must set gp_fetch_ready=0, got %v", got)
	}

	// 2) The fetch recovers → usable data → readiness 1 (clears the prior 0).
	fetcher.err = nil
	fetcher.result = ephemeris.GPFetchResult{SatelliteCount: 1, FetchedAt: time.Now()}
	_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	if got := testutil.ToFloat64(ntnmetrics.GPFetchReady.With(labels)); got != 1 {
		t.Fatalf("recovered fetch must set gp_fetch_ready=1, got %v", got)
	}
}
