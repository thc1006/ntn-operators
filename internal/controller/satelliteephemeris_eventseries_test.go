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
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// These run against the REAL client-go events.k8s.io/v1 broadcaster wired to the envtest API server
// (not a FakeRecorder), closing the integration-confidence gap #227/the WO-20 review raised: FakeRecorder
// verifies how many times WE call the recorder, but not what the broadcaster actually writes to the API,
// nor its EventSeries aggregation. This exercises both together: a single episode-gated,
// emit-after-persist transition must land exactly ONE Event object, and repeated steady reconciles must
// NOT turn it into an aggregated EventSeries (which is exactly what a broken episode gate would do).
var _ = Describe("SatelliteEphemeris events via the real events.k8s.io broadcaster", func() {
	var (
		recorder    events.EventRecorder
		stopSink    chan struct{}
		broadcaster events.EventBroadcaster
	)

	BeforeEach(func() {
		clientset, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		broadcaster = events.NewBroadcaster(&events.EventSinkImpl{Interface: clientset.EventsV1()})
		stopSink = make(chan struct{})
		broadcaster.StartRecordingToSink(stopSink)
		recorder = broadcaster.NewRecorder(scheme.Scheme, "satelliteephemeris-controller")
	})

	AfterEach(func() {
		close(stopSink)
		broadcaster.Shutdown()
	})

	// insecureURLEvents lists the InsecureURL Warning events the broadcaster wrote for eph.
	insecureURLEvents := func(ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris) []eventsv1.Event {
		var all eventsv1.EventList
		Expect(k8sClient.List(ctx, &all, client.InNamespace(eph.Namespace))).To(Succeed())
		var out []eventsv1.Event
		for _, e := range all.Items {
			if e.Reason == "InsecureURL" && e.Regarding.Name == eph.Name && e.Regarding.Kind == "SatelliteEphemeris" {
				out = append(out, e)
			}
		}
		return out
	}

	It("writes exactly one Event for an episode-gated transition and does not aggregate it on steady reconciles", func() {
		ctx := context.Background()
		eph := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "evt-insecure-", Namespace: "default"},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					// Cleartext http to a literal PUBLIC IP → publicHTTPSource → handleInsecureURL, which
					// records the condition, persists it, then (episode-gated) emits ONE Warning. 4h is a
					// valid refreshInterval so no RefreshIntervalClamped event confuses the count.
					Type: "CelesTrak", URL: "http://1.1.1.1/gp.php",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
			},
		}
		Expect(k8sClient.Create(ctx, eph)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, eph) })
		key := client.ObjectKeyFromObject(eph)

		r := &SatelliteEphemerisReconciler{
			Client:   k8sClient,
			Scheme:   scheme.Scheme,
			Recorder: recorder,
			Fetcher:  ephemeris.NewCelesTrakFetcher(&http.Client{}), // non-nil so setup passes; no real fetch happens
		}

		// First reconcile: the transition persists, then the Warning is emitted.
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		// The broadcaster sinks asynchronously — wait for exactly one Event to land, with NO series (a
		// single occurrence). A second emission of the same key would make the broadcaster set Series.
		Eventually(func(g Gomega) {
			evs := insecureURLEvents(ctx, eph)
			g.Expect(evs).To(HaveLen(1), "expected exactly one InsecureURL Event in the API")
			g.Expect(evs[0].Series).To(BeNil(), "a single episode must not be aggregated into an EventSeries")
			g.Expect(evs[0].Type).To(Equal("Warning"))
		}, 20*time.Second, 500*time.Millisecond).Should(Succeed())

		// Steady reconciles: the episode gate must suppress re-emission, so the broadcaster never sees a
		// second occurrence and the Event stays single (Series nil). A broken gate would re-emit the same
		// key and the broadcaster would aggregate it into Series.count >= 2.
		for range 4 {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}
		Consistently(func(g Gomega) {
			evs := insecureURLEvents(ctx, eph)
			g.Expect(evs).To(HaveLen(1))
			g.Expect(evs[0].Series).To(BeNil(), "steady reconciles must not aggregate the Event (episode gate held)")
		}, 4*time.Second, 500*time.Millisecond).Should(Succeed())
	})
})
