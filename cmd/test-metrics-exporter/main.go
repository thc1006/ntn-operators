/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command test-metrics-exporter serves a synthetic Prometheus /metrics
// endpoint for exercising the NTNSlice failover engine end-to-end when
// no real gNB Prometheus exporter is available.
//
// The simulator alternates between a "pass window" (healthy RSRP /
// latency / packet loss) and a "gap" (values that deliberately cross
// the default failover triggers), repeating on a configurable cycle.
// A Prometheus instance scraping this exporter produces the same time
// series shape a real deployment would, which lets the controller
// behaviour be validated against live PromQL without an SDR in loop.
//
// Not fit for production use: this binary is a fixture.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	var (
		listen       = flag.String("listen", ":9090", "HTTP address to serve /metrics on.")
		namespace    = flag.String("namespace", "default", "Namespace label applied to every emitted series.")
		slice        = flag.String("slice", "test-slice", "Slice label applied to every emitted series.")
		passDuration = flag.Duration("pass-duration", 30*time.Second, "How long each simulated pass window lasts.")
		gapDuration  = flag.Duration("gap-duration", 30*time.Second, "How long each simulated gap between passes lasts.")
	)
	flag.Parse()

	if *passDuration <= 0 || *gapDuration <= 0 {
		log.Fatalf("pass-duration and gap-duration must both be positive; got pass=%s gap=%s", *passDuration, *gapDuration)
	}

	sim := Simulator{
		Start:        time.Now(),
		PassDuration: *passDuration,
		GapDuration:  *gapDuration,
	}
	handler := NewHandler(prometheus.NewRegistry(), sim, *namespace, *slice, time.Now)

	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("test-metrics-exporter listening on %s (slice=%s/%s, pass=%s, gap=%s)",
			*listen, *namespace, *slice, *passDuration, *gapDuration)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	_ = os.Stdout // pacify unused import guard when logging is disabled in tests
}
