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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
	"github.com/thc1006/ntn-operators/pkg/provider/ocudu"
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
				EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
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

	// deleteCellConfig deletes an NTNCellConfig by NamespacedName, removing finalizers first.
	deleteCellConfig := func(nn types.NamespacedName) {
		cr := &ntnv1alpha1.NTNCellConfig{}
		err := k8sClient.Get(context.Background(), nn, cr)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		if controllerutil.ContainsFinalizer(cr, "ntn.operators.dev/configmap-cleanup") {
			controllerutil.RemoveFinalizer(cr, "ntn.operators.dev/configmap-cleanup")
			Expect(k8sClient.Update(context.Background(), cr)).To(Succeed())
		}
		Expect(k8sClient.Delete(context.Background(), cr)).To(Succeed())
	}

	deleteCR := func() { deleteCellConfig(typeNamespacedName) }

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
		if err != nil || result.RequeueAfter != time.Second {
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

	Context("CRD validation: provider type enum", func() {
		It("should reject creation with unsupported provider type aalyria", func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-provider", Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "aalyria"},
					NTN: ntnv1alpha1.NTNParams{
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
					},
				},
			}
			err := k8sClient.Create(context.Background(), cr)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid API error, got: %v", err)
		})

		It("should reject oai provider type", func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-oai", Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "oai"},
					NTN: ntnv1alpha1.NTNParams{
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
					},
				},
			}
			err := k8sClient.Create(context.Background(), cr)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid API error, got: %v", err)
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

	Context("When provider.namespace differs from CR namespace", func() {
		BeforeEach(func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: "other-ns"},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
						PayloadType:         "transparent",
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())
		})
		AfterEach(func() { deleteCR() })

		It("should override namespace to CR namespace and succeed", func() {
			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			result, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

			// Provider should have been called with CR namespace, not "other-ns".
			Expect(mock.ApplyCalls).To(Equal(1))
			Expect(mock.LastSpec.Provider.Namespace).To(Equal(namespace))
		})
	})

	Context("When CR is deleted with finalizer", func() {
		It("should clean up ConfigMap and remove finalizer", func() {
			// Create CR and reconcile to add finalizer.
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec:       geoSpec(),
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			// First reconcile: adds finalizer.
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile: applies config (creates ConfigMap).
			_, err = reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Delete the CR (triggers finalizer).
			Expect(k8sClient.Delete(context.Background(), cr)).To(Succeed())

			// Reconcile should handle deletion and remove finalizer.
			_, err = reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// CR should be fully deleted now.
			deleted := &ntnv1alpha1.NTNCellConfig{}
			err = k8sClient.Get(context.Background(), typeNamespacedName, deleted)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When CR is deleted but ConfigMap already gone", func() {
		It("should remove finalizer without error (NotFound path)", func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec:       geoSpec(),
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			// First reconcile: adds finalizer only.
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Don't do second reconcile (so no ConfigMap is created).
			// Delete the CR directly.
			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(k8sClient.Delete(context.Background(), updated)).To(Succeed())

			// Reconcile handles deletion — ConfigMap doesn't exist (NotFound).
			_, err = reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			deleted := &ntnv1alpha1.NTNCellConfig{}
			err = k8sClient.Get(context.Background(), typeNamespacedName, deleted)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When GetCellStatus fails", func() {
		BeforeEach(func() { createCR() })
		AfterEach(func() { deleteCR() })

		It("should set ConfigApplied=Unknown with StatusCheckFailed", func() {
			mock := &provider.MockProvider{StatusErr: errors.New("status unavailable")}
			reconciler := newReconciler(mock)

			_, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionConfigApplied)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
			Expect(cond.Reason).To(Equal("StatusCheckFailed"))
		})
	})

	Context("When reconciling a CR that already has a finalizer", func() {
		It("should skip finalizer step and proceed directly", func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       resourceName,
					Namespace:  namespace,
					Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
				},
				Spec: geoSpec(),
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())
			defer deleteCR()

			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			// Single reconcile should do the actual work (finalizer already present).
			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
			Expect(mock.ApplyCalls).To(Equal(1))
		})
	})

	Context("When provider succeeds (OwnerReference verification)", func() {
		BeforeEach(func() { createCR() })
		AfterEach(func() { deleteCR() })

		It("should set OwnerReference on generated ConfigMap", func() {
			// Use real OCUDU provider so ConfigMap actually gets created.
			realProvider := ocudu.NewProvider(k8sClient)
			reconciler := &NTNCellConfigReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(10),
				Provider: realProvider,
			}
			_, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())

			// Verify the ConfigMap has OwnerReference pointing to the CR.
			cm := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{
				Name:      "ocudu-ntn-" + resourceName,
				Namespace: namespace,
			}
			Expect(k8sClient.Get(context.Background(), cmKey, cm)).To(Succeed())

			cr := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, cr)).To(Succeed())
			Expect(metav1.IsControlledBy(cm, cr)).To(BeTrue())

			// Clean up ConfigMap.
			_ = k8sClient.Delete(context.Background(), cm)
		})
	})

	Context("When creating NTNCellConfig with ephemerisRef", func() {
		const cellWithRefName = "test-eph-ref-cell"
		cellWithRefNN := types.NamespacedName{Name: cellWithRefName, Namespace: namespace}

		AfterEach(func() { deleteCellConfig(cellWithRefNN) })

		It("should accept a CR with ephemerisRef set", func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: cellWithRefName, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
							PosX: 20922195, PosY: 1967783, PosZ: 19770302,
						},
						PayloadType: "transparent",
					},
					EphemerisRef: "my-sat-ephemeris",
				},
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			// Verify it was persisted correctly.
			fetched := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), cellWithRefNN, fetched)).To(Succeed())
			Expect(fetched.Spec.EphemerisRef).To(Equal("my-sat-ephemeris"))
		})
	})

	Context("When reconciling with ephemerisRef configured", func() {
		const (
			cellName = "test-eph-push-cell"
			ephName  = "test-eph-push-source"
		)
		cellNN := types.NamespacedName{Name: cellName, Namespace: namespace}
		ephNN := types.NamespacedName{Name: ephName, Namespace: namespace}

		AfterEach(func() {
			deleteCellConfig(cellNN)
			eph := &ntnv1alpha1.SatelliteEphemeris{}
			err := k8sClient.Get(context.Background(), ephNN, eph)
			if err == nil {
				_ = k8sClient.Delete(context.Background(), eph)
			}
		})

		It("should invoke PushEphemerisUpdate on provider", func() {
			// Referenced SatelliteEphemeris exists in the same namespace.
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: ephName, Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "CelesTrak",
						URL:             "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())

			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: cellName, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
							PosX: 20922195, PosY: 1967783, PosZ: 19770302,
						},
						PayloadType: "transparent",
					},
					EphemerisRef: ephName,
				},
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Second))

			result, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

			Expect(mock.ApplyCalls).To(Equal(1))
			Expect(mock.EphemerisCalls).To(Equal(1))
			Expect(mock.LastEphemeris).NotTo(BeNil())
			Expect(mock.LastEphemeris.ECEF).NotTo(BeNil())
			Expect(mock.LastEphemeris.ECEF.PosX).To(Equal(20922195))
		})
	})

	Context("ephemerisToNTNCellConfig mapper", func() {
		const (
			ccWithRef    = "cc-with-ref"
			ccWithoutRef = "cc-without-ref"
			ephName      = "test-eph-mapper"
		)

		BeforeEach(func() {
			// Create a NTNCellConfig that references the ephemeris.
			cr1 := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: ccWithRef, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
							PosX: 20922195, PosY: 1967783, PosZ: 19770302,
						},
						PayloadType: "transparent",
					},
					EphemerisRef: ephName,
				},
			}
			Expect(k8sClient.Create(context.Background(), cr1)).To(Succeed())

			// Create a NTNCellConfig without ephemerisRef.
			cr2 := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: ccWithoutRef, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
							PosX: 20922195, PosY: 1967783, PosZ: 19770302,
						},
						PayloadType: "transparent",
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), cr2)).To(Succeed())
		})

		AfterEach(func() {
			for _, name := range []string{ccWithRef, ccWithoutRef} {
				deleteCellConfig(types.NamespacedName{Name: name, Namespace: namespace})
			}
		})

		It("should return requests only for NTNCellConfigs that reference the ephemeris", func() {
			reconciler := newReconciler(&provider.MockProvider{})

			// Simulate a SatelliteEphemeris object.
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: ephName, Namespace: namespace},
			}

			requests := reconciler.ephemerisToNTNCellConfig(context.Background(), eph)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].NamespacedName.Name).To(Equal(ccWithRef))
			Expect(requests[0].NamespacedName.Namespace).To(Equal(namespace))
		})

		It("should return empty requests when no NTNCellConfigs reference the ephemeris", func() {
			reconciler := newReconciler(&provider.MockProvider{})

			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "nonexistent-eph", Namespace: namespace},
			}

			requests := reconciler.ephemerisToNTNCellConfig(context.Background(), eph)
			Expect(requests).To(BeEmpty())
		})
	})

	// --- CEL validation tests ---

	Context("CEL: ephemerisECEF must not be all zeros", func() {
		It("should reject creation when all ECEF positions are zero", func() {
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-ecef", Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu"},
					NTN: ntnv1alpha1.NTNParams{
						EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
							PosX: 0, PosY: 0, PosZ: 0,
						},
						PayloadType: "transparent",
					},
				},
			}
			err := k8sClient.Create(context.Background(), cr)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("zeros"))
		})
	})
})
