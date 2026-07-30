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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
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
			Providers: map[string]provider.NTNProvider{
				"ocudu": p,
			},
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

	Context("When provider registry is nil", func() {
		BeforeEach(func() { createCR() })
		AfterEach(func() { deleteCR() })

		It("should set ConfigApplied=False with InternalError", func() {
			reconciler := &NTNCellConfigReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				Recorder:  events.NewFakeRecorder(10),
				Providers: nil, // nil registry
			}

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
				Providers: map[string]provider.NTNProvider{
					"ocudu": realProvider,
				},
			}
			_, err := reconcileWithFinalizer(reconciler)
			Expect(err).NotTo(HaveOccurred())

			// Verify the ConfigMap has OwnerReference pointing to the CR.
			cm := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{
				Name:      ocudu.ConfigMapNameFor(resourceName),
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

		createReferencedEphemeris := func() {
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
		}

		createCellConfig := func() {
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
		}

		AfterEach(func() {
			deleteCellConfig(cellNN)
			eph := &ntnv1alpha1.SatelliteEphemeris{}
			err := k8sClient.Get(context.Background(), ephNN, eph)
			if apierrors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Delete(context.Background(), eph)
			if apierrors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())
		})

		It("should invoke PushEphemerisUpdate on provider", func() {
			createReferencedEphemeris()
			createCellConfig()

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

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), cellNN, updated)).To(Succeed())
			ephCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			Expect(ephCond).NotTo(BeNil())
			Expect(ephCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(ephCond.Reason).To(Equal("Pushed"))

			// Metadata-only updates can bump resourceVersion without changing ephemeris data.
			// Ensure dedupe does not treat this as a new ephemeris revision.
			eph := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), ephNN, eph)).To(Succeed())
			if eph.Annotations == nil {
				eph.Annotations = map[string]string{}
			}
			eph.Annotations["review/test-rv-bump"] = "true"
			Expect(k8sClient.Update(context.Background(), eph)).To(Succeed())

			// Third reconcile should not re-push when generation + lastUpdated marker is unchanged.
			result, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
			Expect(mock.EphemerisCalls).To(Equal(1))
		})

		It("should keep ConfigApplied true and set EphemerisPushed=false when push fails", func() {
			createReferencedEphemeris()
			createCellConfig()

			mock := &provider.MockProvider{EphemerisErr: errors.New("runtime push failed")}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Second))

			result, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Minute))

			Expect(mock.ApplyCalls).To(Equal(1))
			Expect(mock.StatusCalls).To(Equal(1))
			Expect(mock.EphemerisCalls).To(Equal(1))

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), cellNN, updated)).To(Succeed())

			appliedCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionConfigApplied)
			Expect(appliedCond).NotTo(BeNil())
			Expect(appliedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(appliedCond.Reason).To(Equal("Applied"))

			ephCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			Expect(ephCond).NotTo(BeNil())
			Expect(ephCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(ephCond.Reason).To(Equal("ProviderPushFailed"))
			Expect(ephCond.Message).To(ContainSubstring("runtime push failed"))

			// The ConfigApplied event must still fire on this apply-succeeds-but-push-
			// fails path: the config WAS applied and its transition is durably persisted
			// here, so the event cannot be lost to the early return. It is emitted before
			// EphemerisPushFailed, so it is the first buffered event.
			Expect(reconciler.Recorder.(*events.FakeRecorder).Events).
				To(Receive(ContainSubstring("ConfigApplied")))
		})

		It("emits SatSwitchIgnored once per spec even while the push keeps failing", func() {
			createReferencedEphemeris()
			kmac := 200
			// ConfigMap bootstrap path (no remoteControl+cellID) with a satSwitch that
			// cannot be delivered there, and a provider whose push always fails so the
			// reconcile re-enters the not-up-to-date path every requeue.
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: cellName, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 20922195, PosY: 1967783, PosZ: 19770302},
						PayloadType:         "transparent",
						SatSwitchWithResync: &ntnv1alpha1.SatSwitchWithResync{
							NTNConfig: ntnv1alpha1.SatSwitchNTNConfig{
								EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 6000000, PosY: 1, PosZ: 1},
								KMac:          &kmac,
							},
						},
					},
					EphemerisRef: ephName,
				},
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			mock := &provider.MockProvider{EphemerisErr: errors.New("runtime push failed")}
			reconciler := newReconciler(mock)
			// finalizer add, then apply (push fails), then two more failing requeues.
			for range 4 {
				_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			}

			rec := reconciler.Recorder.(*events.FakeRecorder)
			satSwitch, pushFailed := 0, 0
			for drained := false; !drained; {
				select {
				case ev := <-rec.Events:
					if strings.Contains(ev, "SatSwitchIgnored") {
						satSwitch++
					}
					if strings.Contains(ev, "EphemerisPushFailed") {
						pushFailed++
					}
				default:
					drained = true
				}
			}
			Expect(satSwitch).To(Equal(1), "SatSwitchIgnored must fire once per spec, not on every push-failure requeue")
			Expect(pushFailed).To(Equal(1), "EphemerisPushFailed must be episode-gated across the failing requeues")
		})

		It("counts every ephemeris push failure while emitting the Warning once per episode", func() {
			createReferencedEphemeris()
			// A runtime push that always fails with a transient (requeuing) reason. The
			// COUNTER must advance on every failure so the shipped NTNEphemerisPushFailing
			// alert — increase(ephemeris_push_errors_total[15m]) > 0 for 15m — keeps firing
			// through an outage that tight-requeues each minute; an episode-gated counter
			// would increment once and let the alert resolve mid-outage. The EVENT stays
			// episode-gated. Mirrors ConfigApplyErrorsTotal's per-failure counting.
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: cellName, Namespace: namespace},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: namespace},
					NTN: ntnv1alpha1.NTNParams{
						CellSpecificKoffset: 150,
						EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 20922195, PosY: 1967783, PosZ: 19770302},
						PayloadType:         "transparent",
					},
					EphemerisRef: ephName,
				},
			}
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			// reason is ProviderPushFailed (a raw provider error), which requeues.
			labels := prometheus.Labels{"namespace": namespace, "config": cellName, "reason": "ProviderPushFailed"}
			before := testutil.ToFloat64(ntnmetrics.EphemerisPushErrorsTotal.With(labels))

			mock := &provider.MockProvider{EphemerisErr: errors.New("runtime push failed")}
			reconciler := newReconciler(mock)
			// finalizer add, then three apply-ok-but-push-fails requeues.
			for range 4 {
				_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			}

			rec := reconciler.Recorder.(*events.FakeRecorder)
			pushFailed := 0
			for drained := false; !drained; {
				select {
				case ev := <-rec.Events:
					if strings.Contains(ev, "EphemerisPushFailed") {
						pushFailed++
					}
				default:
					drained = true
				}
			}

			delta := testutil.ToFloat64(ntnmetrics.EphemerisPushErrorsTotal.With(labels)) - before
			Expect(mock.EphemerisCalls).To(BeNumerically(">=", 2),
				"the setup must drive more than one failing push, else per-failure counting is untested")
			Expect(delta).To(Equal(float64(mock.EphemerisCalls)),
				"EphemerisPushErrorsTotal must advance on EVERY push failure (alert continuity), not once per episode")
			Expect(pushFailed).To(Equal(1),
				"EphemerisPushFailed Event must stay episode-gated across the failing requeues")
		})

		It("holds ephemeris_push_ready at 0 through the outage and returns it to 1 on recovery", func() {
			createReferencedEphemeris()
			createCellConfig()
			labels := prometheus.Labels{"namespace": namespace, "config": cellName}

			// Push keeps failing → readiness 0. Unlike the per-failure counter, this
			// holds even for a PERMANENT (non-requeuing) reason, so the companion
			// `ephemeris_push_ready == 0 for 15m` alert fires where the rate alert can't.
			failing := newReconciler(&provider.MockProvider{EphemerisErr: errors.New("runtime push failed")})
			for range 2 {
				_, _ = failing.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			}
			Expect(testutil.ToFloat64(ntnmetrics.EphemerisPushReady.With(labels))).To(Equal(float64(0)),
				"push_ready must be 0 while the runtime push is failing")

			// A healthy push → readiness back to 1.
			healthy := newReconciler(&provider.MockProvider{})
			_, _ = healthy.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(testutil.ToFloat64(ntnmetrics.EphemerisPushReady.With(labels))).To(Equal(float64(1)),
				"push_ready must return to 1 once the push recovers")
		})

		It("should avoid tight requeue when ephemerisRef does not exist", func() {
			createCellConfig()

			mock := &provider.MockProvider{}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Second))

			result, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: cellNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			Expect(mock.ApplyCalls).To(Equal(1))
			Expect(mock.StatusCalls).To(Equal(1))
			Expect(mock.EphemerisCalls).To(Equal(0))

			updated := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(), cellNN, updated)).To(Succeed())
			ephCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			Expect(ephCond).NotTo(BeNil())
			Expect(ephCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(ephCond.Reason).To(Equal("EphemerisRefNotFound"))
			Expect(ephCond.Message).To(ContainSubstring(`referenced SatelliteEphemeris "test-eph-push-source"`))
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

	// --- cellSpecificKoffset range alignment with OCUDU (1-1023) ---
	//
	// OCUDU stores cell_specific_koffset as std::chrono::milliseconds with a
	// config range of 1-1023; 0 is not accepted. The CRD's Minimum=1 mirrors
	// that lower bound. Note the omitempty/default interaction: the typed Go
	// client drops a zero int (omitempty) so an "unset" koffset defaults to
	// 150 and never reaches the Minimum check — only an *explicit* 0 in the
	// wire object exercises it, which is why this case uses unstructured.
	Context("cellSpecificKoffset must be within OCUDU range 1-1023", func() {
		newCellConfigUnstructured := func(name string, koffset int64) *unstructured.Unstructured {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "ntn.operators.dev", Version: "v1alpha1", Kind: "NTNCellConfig",
			})
			u.SetName(name)
			u.SetNamespace(namespace)
			u.Object["spec"] = map[string]any{
				"provider": map[string]any{"type": "ocudu", "namespace": namespace},
				"ntn": map[string]any{
					"cellSpecificKoffset": koffset,
					"payloadType":         "transparent",
					"ephemerisECEF": map[string]any{
						"posX": int64(20922195), "posY": int64(1967783), "posZ": int64(19770302),
					},
				},
			}
			return u
		}

		It("should reject an explicit cellSpecificKoffset of 0 (below OCUDU min)", func() {
			err := k8sClient.Create(context.Background(), newCellConfigUnstructured("koffset-zero", 0))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("greater than or equal to 1"))
		})

		It("should reject a cellSpecificKoffset above OCUDU max (1024)", func() {
			err := k8sClient.Create(context.Background(), newCellConfigUnstructured("koffset-over-max", 1024))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("less than or equal to 1023"))
		})

		It("should accept the lower valid boundary cellSpecificKoffset=1", func() {
			// The OCUDU/CRD lower bound is inclusive: 1 is the smallest accepted
			// value. Guards against the Minimum being mis-set to 2 (off-by-one).
			u := newCellConfigUnstructured("koffset-min-boundary", 1)
			Expect(k8sClient.Create(context.Background(), u)).To(Succeed())
			Expect(k8sClient.Delete(context.Background(), u)).To(Succeed())
		})

		It("should default an omitted (zero) cellSpecificKoffset to 150, not reject it", func() {
			// Typed client: koffset==0 is the int zero value, omitempty drops it,
			// the apiserver applies default=150. Documents why explicit-0 rejection
			// above needs unstructured rather than the typed geoSpec().
			cr := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "koffset-defaulted", Namespace: namespace},
				Spec:       geoSpec(),
			}
			cr.Spec.NTN.CellSpecificKoffset = 0
			Expect(k8sClient.Create(context.Background(), cr)).To(Succeed())

			fetched := &ntnv1alpha1.NTNCellConfig{}
			Expect(k8sClient.Get(context.Background(),
				types.NamespacedName{Name: "koffset-defaulted", Namespace: namespace}, fetched)).To(Succeed())
			Expect(fetched.Spec.NTN.CellSpecificKoffset).To(Equal(150))

			Expect(k8sClient.Delete(context.Background(), fetched)).To(Succeed())
		})
	})
})
