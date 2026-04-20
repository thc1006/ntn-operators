/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler serves a Prometheus /metrics endpoint backed by a Simulator.
// Each scrape refreshes the gauge values to whatever the simulator
// produces at now(), so the scrape-time value reflects live-ish state
// without the complication of a background update goroutine.
type Handler struct {
	sim      Simulator
	now      func() time.Time
	rsrp     prometheus.Gauge
	latency  prometheus.Gauge
	loss     prometheus.Gauge
	inPass   prometheus.Gauge
	delegate http.Handler
}

// NewHandler constructs a Handler registered on reg.
// namespace and slice populate the {namespace, slice} label pair so
// the NTNSlice PromQL looks like avg(ntn_e2e_rsrp_dbm{slice="X"}).
func NewHandler(reg *prometheus.Registry, sim Simulator, namespace, slice string, nowFn func() time.Time) *Handler {
	labels := prometheus.Labels{"namespace": namespace, "slice": slice}
	h := &Handler{
		sim: sim,
		now: nowFn,
		rsrp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ntn_e2e_rsrp_dbm", Help: "Synthetic RSRP in dBm for E2E testing.",
			ConstLabels: labels,
		}),
		latency: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ntn_e2e_latency_ms", Help: "Synthetic end-to-end latency in milliseconds.",
			ConstLabels: labels,
		}),
		loss: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ntn_e2e_packet_loss_percent", Help: "Synthetic packet loss percent (0-100).",
			ConstLabels: labels,
		}),
		inPass: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ntn_e2e_in_pass", Help: "1 when the simulator reports a satellite pass window, 0 otherwise.",
			ConstLabels: labels,
		}),
	}
	reg.MustRegister(h.rsrp, h.latency, h.loss, h.inPass)
	h.delegate = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return h
}

// ServeHTTP updates the gauges to the simulator's current view and
// delegates to promhttp for the actual text-format serialisation.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v := h.sim.ValuesAt(h.now())
	h.rsrp.Set(v.RsrpDbm)
	h.latency.Set(v.LatencyMs)
	h.loss.Set(v.PacketLossPercent)
	if v.InPass {
		h.inPass.Set(1)
	} else {
		h.inPass.Set(0)
	}
	h.delegate.ServeHTTP(w, r)
}
