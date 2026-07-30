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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider/ocudu"
)

// This runs against the envtest API server because a fake client does not enforce delete
// preconditions. It pins the atomic UID-scoped delete: the ownership check and the delete must hit the
// SAME object, so a same-name ConfigMap recreated (new UID) between the Get and the delete is NOT
// removed. The replacement is injected during the delete via an interceptor — exactly the "swap
// between GET and DELETE" race. Mutation: drop the client.Preconditions{UID} in ocudu.Cleanup and the
// replacement is deleted (this fails).
var _ = Describe("NTNCellConfig ocudu.Cleanup UID-scoped delete", func() {
	It("does not delete a same-name ConfigMap recreated between the ownership check and the delete", func() {
		ctx := context.Background()
		const ns = "default"

		// ccWithRemoteControl() builds an admission-valid spec (spec.ntn.ephemerisECEF, cellID, ocudu);
		// override the identity so envtest assigns a fresh name+UID in the default namespace.
		cc := ccWithRemoteControl()
		cc.ObjectMeta = metav1.ObjectMeta{GenerateName: "uid-del-", Namespace: ns}
		Expect(k8sClient.Create(ctx, cc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, cc) })

		cmName := ocudu.ConfigMapNameFor(cc.Name)

		sch := runtime.NewScheme()
		Expect(ntnv1alpha1.AddToScheme(sch)).To(Succeed())
		Expect(corev1.AddToScheme(sch)).To(Succeed())

		// cmA: the CR's own ConfigMap (controller-owned by cc).
		cmA := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns},
			Data:       map[string]string{"who": "cmA-owned-by-cc"},
		}
		Expect(controllerutil.SetControllerReference(cc, cmA, sch)).To(Succeed())
		Expect(k8sClient.Create(ctx, cmA)).To(Succeed())

		// A WithWatch envtest client wrapped so the FIRST delete first swaps the ConfigMap for a
		// same-name replacement with a different UID (and no owner) — the TOCTOU window — then delegates
		// the real (UID-preconditioned) delete, which the API server must reject with Conflict.
		base, err := client.NewWithWatch(cfg, client.Options{Scheme: sch})
		Expect(err).NotTo(HaveOccurred())
		swapped := false
		racing := interceptor.NewClient(base, interceptor.Funcs{
			Delete: func(dctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if !swapped {
					swapped = true
					cur := &corev1.ConfigMap{}
					Expect(k8sClient.Get(dctx, client.ObjectKey{Name: cmName, Namespace: ns}, cur)).To(Succeed())
					Expect(k8sClient.Delete(dctx, cur)).To(Succeed())
					repl := &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns},
						Data:       map[string]string{"who": "replacement-not-owned-by-cc"},
					}
					Expect(k8sClient.Create(dctx, repl)).To(Succeed())
				}
				return c.Delete(dctx, obj, opts...)
			},
		})

		err = ocudu.NewProvider(racing).Cleanup(ctx, cc)
		Expect(err).To(HaveOccurred(), "the UID precondition must reject the delete of a replaced object")
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "want a UID-precondition Conflict, got %v", err)

		// The replacement (a different object the CR does not own) must survive.
		got := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cmName, Namespace: ns}, got)).To(Succeed())
		Expect(got.Data["who"]).To(Equal("replacement-not-owned-by-cc"),
			"the same-name replacement must NOT be deleted by the CR's cleanup")
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, got) })
	})
})
