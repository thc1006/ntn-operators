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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// This proves the PRODUCTION field-index wiring (#204-G3b, review M1): the fake-client unit
// test registers its own index via WithIndex and therefore CANNOT catch a regression where
// SetupWithManager stops registering the index (or uses the wrong key/type) — the mapper
// silently falls back to an O(namespace) scan, so the suite would stay green while the perf
// fix is gone. Here we run the real SetupWithManager against a manager cache and prove the
// indexed lookup works through the manager client.
var _ = Describe("NTNCellConfig spec.ephemerisRef index wiring (SetupWithManager)", func() {
	const ns = "default"

	It("registers the index on the manager cache so MatchingFields resolves referencing cells", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"}, // no metrics port
		})
		Expect(err).NotTo(HaveOccurred())

		// The production wiring under test — registers the spec.ephemerisRef field index.
		r := &NTNCellConfigReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}
		Expect(r.SetupWithManager(mgr)).To(Succeed())

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Start ONLY the cache (not mgr.Start), so the informer + index come up but no
		// reconcile loop runs — this test needs no provider. A missing/wrong index makes the
		// MatchingFields List below error ("field selector not registered").
		go func() {
			defer GinkgoRecover()
			Expect(mgr.GetCache().Start(ctx)).To(Succeed())
		}()
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

		newCC := func(name, ephRef string) *ntnv1alpha1.NTNCellConfig {
			return &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 20922195, PosY: 1967783, PosZ: 19770302},
						PayloadType:         "transparent",
					},
					EphemerisRef: ephRef,
				},
			}
		}
		ccRef := newCC("cc-idx-ref", "eph-idx")
		ccOther := newCC("cc-idx-other", "eph-other")
		Expect(k8sClient.Create(ctx, ccRef)).To(Succeed())
		Expect(k8sClient.Create(ctx, ccOther)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(context.Background(), ccRef)
			_ = k8sClient.Delete(context.Background(), ccOther)
		})

		// The cache-backed indexed lookup must return ONLY the referencing cell — which is
		// only possible if SetupWithManager registered the index with the correct key + type.
		Eventually(func(g Gomega) {
			var list ntnv1alpha1.NTNCellConfigList
			g.Expect(mgr.GetCache().List(ctx, &list, client.InNamespace(ns),
				client.MatchingFields{ephemerisRefIndexKey: "eph-idx"})).To(Succeed())
			g.Expect(list.Items).To(HaveLen(1))
			g.Expect(list.Items[0].Name).To(Equal("cc-idx-ref"))
		}, "10s", "200ms").Should(Succeed())
	})
})
