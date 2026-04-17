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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

var _ = Describe("NTNSlice Controller", func() {
	const sliceName = "test-slice"
	const namespace = "default"

	typeNamespacedName := types.NamespacedName{Name: sliceName, Namespace: namespace}

	baseSpec := func() ntnv1alpha1.NTNSliceSpec {
		return ntnv1alpha1.NTNSliceSpec{
			Tenant: "acme-corp",
			TerrestrialPath: ntnv1alpha1.PathSpec{
				Provider: "chunghwa-telecom",
				APN:      "internet",
				Priority: "primary",
			},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec: ntnv1alpha1.PathSpec{
					Provider: "oneweb",
					Priority: "failover",
				},
				EphemerisRef: "oneweb-constellation",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers:          []string{"rsrp < -120", "latency > 200", "packetLoss > 5"},
				SwitchbackDelay:   metav1.Duration{Duration: 60 * time.Second},
				SessionContinuity: true,
			},
		}
	}

	createSlice := func() {
		slice := &ntnv1alpha1.NTNSlice{
			ObjectMeta: metav1.ObjectMeta{Name: sliceName, Namespace: namespace},
			Spec:       baseSpec(),
		}
		Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
	}

	deleteSlice := func() {
		slice := &ntnv1alpha1.NTNSlice{}
		err := k8sClient.Get(context.Background(), typeNamespacedName, slice)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(context.Background(), slice)).To(Succeed())
	}

	newReconciler := func() *NTNSliceReconciler {
		return &NTNSliceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(10),
			Now:      func() time.Time { return time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC) },
		}
	}

	Context("When terrestrial is healthy (initial state)", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should set activePathType=terrestrial and PathActive=True", func() {
			reconciler := newReconciler()

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("terrestrial"))

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPathActive)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("When failover is triggered via annotation", func() {
		BeforeEach(func() {
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sliceName,
					Namespace: namespace,
					Annotations: map[string]string{
						"ntn.operators.dev/simulated-rsrp":        "-130",
						"ntn.operators.dev/simulated-latency":     "20",
						"ntn.operators.dev/simulated-packet-loss": "0.1",
					},
				},
				Spec: baseSpec(),
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
		})
		AfterEach(func() { deleteSlice() })

		It("should failover to satellite when SatelliteEphemeris has passes", func() {
			// Create a SatelliteEphemeris with pass windows so satellite is "available".
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "CelesTrak",
						URL:             "https://celestrak.org/test",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())
			// Set status with pass windows.
			eph.Status.SatelliteCount = 651
			eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{
				{
					Satellite:     "ONEWEB-0012",
					GroundStation: "gs-taipei-01",
					AOS:           metav1.Time{Time: time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC)},
					LOS:           metav1.Time{Time: time.Date(2026, 4, 17, 13, 0, 0, 0, time.UTC)},
					MaxElevation:  "45.0",
				},
			}
			Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(context.Background(), eph)
			})

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("satellite"))
			Expect(updated.Status.FailoverCount).To(Equal(1))
			Expect(updated.Status.LastFailover).NotTo(BeNil())
		})
	})

	Context("When no SatelliteEphemeris exists", func() {
		BeforeEach(func() {
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sliceName,
					Namespace: namespace,
					Annotations: map[string]string{
						"ntn.operators.dev/simulated-rsrp": "-130",
					},
				},
				Spec: baseSpec(),
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
		})
		AfterEach(func() { deleteSlice() })

		It("should stay on terrestrial even if degraded", func() {
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("terrestrial"))

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		})
	})

	Context("When resource does not exist", func() {
		It("should return without error", func() {
			reconciler := newReconciler()
			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})
})
