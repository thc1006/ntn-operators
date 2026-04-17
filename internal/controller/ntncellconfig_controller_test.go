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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

var _ = Describe("NTNCellConfig Controller", func() {
	const resourceName = "test-ntn-cell"
	const namespace = "default"

	typeNamespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

	geoSpec := func() ntnv1alpha1.NTNCellConfigSpec {
		return ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
			NTN: ntnv1alpha1.NTNParams{
				CellSpecificKoffset: 150,
				TACommon:            0,
				EphemerisECEF: ntnv1alpha1.EphemerisECEF{
					PosX: 20922195, PosY: 1967783, PosZ: 19770302,
				},
				PayloadType: "transparent",
			},
		}
	}

	createCR := func() {
		cr := &ntnv1alpha1.NTNCellConfig{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
			Spec:       geoSpec(),
		}
		Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())
	}

	deleteCR := func() {
		cr := &ntnv1alpha1.NTNCellConfig{}
		err := k8sClient.Get(context.Background(), typeNamespacedName, cr)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		// Remove finalizer if present (otherwise CR stays in Terminating).
		if controllerutil.ContainsFinalizer(cr, "ntn.operators.dev/configmap-cleanup") {
			controllerutil.RemoveFinalizer(cr, "ntn.operators.dev/configmap-cleanup")
			Expect(k8sClient.Update(context.Background(), cr)).To(Succeed())
		}
		Expect(k8sClient.Delete(context.Background(), cr)).To(Succeed())
	}

	newReconciler := func(p provider.NTNProvider) *NTNCellConfigReconciler {
		return &NTNCellConfigReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(10),
			Provider: p,
		}
	}

	// reconcileWithFinalizer runs reconcile twice: first adds the finalizer, second does actual work.
	reconcileWithFinalizer := func(reconciler *NTNCellConfigReconciler) (reconcile.Result, error) {
		req := reconcile.Request{NamespacedName: typeNamespacedName}
		// First reconcile: adds finalizer and requeues.
		result, err := reconciler.Reconcile(context.Background(), req)
		if err != nil || !result.Requeue {
			return result, err
		}
		// Second reconcile: actual logic.
		return reconciler.Reconcile(context.Background(), req)
	}

	Context("When provider succeeds", func() {
		BeforeEach(func() { createCR() })
		AfterEach(func() { deleteCR() })

		It("should apply config and set ConfigApplied=True", func() {
			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			result, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

			// Provider should have been called.
			Expect(mock.ApplyCalls).To(Equal(1))
			Expect(mock.LastSpec).NotTo(BeNil())
			Expect(mock.LastSpec.NTN.CellSpecificKoffset).To(Equal(150))

			// Status should be updated.
			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionConfigApplied)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("Applied"))
		})
	})

	Context("When provider fails", func() {
		BeforeEach(func() { createCR() })
		AfterEach(func() { deleteCR() })

		It("should set ConfigApplied=False", func() {
			mock := &provider.MockProvider{ApplyErr: errors.New("connection refused")}
			reconciler := newReconciler(mock)

			_, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred()) // graceful, not a reconciler error

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionConfigApplied)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("ApplyFailed"))
		})
	})

	Context("When resource does not exist", func() {
		It("should return without error", func() {
			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(mock.ApplyCalls).To(Equal(0))
		})
	})

	Context("When provider type is unsupported", func() {
		BeforeEach(func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "aalyria"},
					NTN: ntnv1alpha1.NTNParams{
						EphemerisECEF: ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())
		})
		AfterEach(func() { deleteCR() })

		It("should set ConfigApplied=False with UnsupportedProvider", func() {
			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			_, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionConfigApplied)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("UnsupportedProvider"))
			Expect(mock.ApplyCalls).To(Equal(0))
		})
	})

	Context("When provider is nil", func() {
		BeforeEach(func() { createCR() })
		AfterEach(func() { deleteCR() })

		It("should set ConfigApplied=False with InternalError", func() {
			reconciler := newReconciler(nil)

			_, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionConfigApplied)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InternalError"))
		})
	})
})
