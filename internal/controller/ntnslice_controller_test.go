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
	const pathSatellite = "satellite"

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
			Expect(updated.Status.ActivePathType).To(Equal(pathSatellite))
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

	Context("When ephemeris changes and slice references it", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should map ephemeris to referencing slices", func() {
			reconciler := newReconciler()

			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type: "CelesTrak", URL: "https://test",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			requests := reconciler.ephemerisToSlice(context.Background(), eph)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal(sliceName))
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

	// Helper: create SatelliteEphemeris with active pass window around the test fixed time (2026-04-17 12:00 UTC).
	createEphemerisWithPass := func(active bool) *ntnv1alpha1.SatelliteEphemeris {
		eph := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: namespace},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type: "CelesTrak", URL: "https://celestrak.org/test",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
			},
		}
		Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())
		if active {
			eph.Status.SatelliteCount = 651
			eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{
				{
					Satellite: "ONEWEB-0012", GroundStation: "gs-taipei-01",
					AOS:          metav1.Time{Time: time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC)},
					LOS:          metav1.Time{Time: time.Date(2026, 4, 17, 13, 0, 0, 0, time.UTC)},
					MaxElevation: "45.0",
				},
			}
		} else {
			eph.Status.SatelliteCount = 651
			eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{
				{
					Satellite: "ONEWEB-0012", GroundStation: "gs-taipei-01",
					AOS:          metav1.Time{Time: time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)},
					LOS:          metav1.Time{Time: time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC)},
					MaxElevation: "45.0",
				},
			}
		}
		Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())
		return eph
	}

	Context("Switchback: satellite pass ends while on satellite", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should switchback to terrestrial when pass ends", func() {
			// Create ephemeris with expired pass (not active now).
			eph := createEphemerisWithPass(false)
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			// Put slice into satellite state first.
			slice := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, slice)).To(Succeed())
			slice.Status.ActivePathType = pathSatellite
			slice.Status.FailoverCount = 1
			slice.Status.LastFailover = &metav1.Time{Time: time.Date(2026, 4, 17, 11, 30, 0, 0, time.UTC)}
			Expect(k8sClient.Status().Update(context.Background(), slice)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("terrestrial"))

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPathActive)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("Switchback"))
		})
	})

	Context("Switchback delay not elapsed", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should stay on satellite when terrestrial recovers but delay hasn't passed", func() {
			// Create ephemeris with active pass.
			eph := createEphemerisWithPass(true)
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			// Put slice on satellite with recent failover (30s ago, delay is 60s).
			slice := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, slice)).To(Succeed())
			slice.Status.ActivePathType = pathSatellite
			slice.Status.FailoverCount = 1
			slice.Status.LastFailover = &metav1.Time{Time: time.Date(2026, 4, 17, 11, 59, 30, 0, time.UTC)} // 30s ago
			Expect(k8sClient.Status().Update(context.Background(), slice)).To(Succeed())

			// No degraded annotations → terrestrial is healthy, but delay not elapsed.
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal(pathSatellite)) // stays

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPathActive)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("Stay"))
		})
	})

	Context("Switchback delay elapsed", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should switchback when terrestrial recovers and delay has passed", func() {
			eph := createEphemerisWithPass(true)
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			// Put slice on satellite with old failover (5 min ago, delay is 60s).
			slice := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, slice)).To(Succeed())
			slice.Status.ActivePathType = pathSatellite
			slice.Status.FailoverCount = 1
			slice.Status.LastFailover = &metav1.Time{Time: time.Date(2026, 4, 17, 11, 55, 0, 0, time.UTC)} // 5min ago
			Expect(k8sClient.Status().Update(context.Background(), slice)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("terrestrial"))

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPathActive)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("Switchback"))
		})
	})

	Context("Terrestrial degraded, satellite still degraded → stay on satellite", func() {
		BeforeEach(func() {
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: sliceName, Namespace: namespace,
					Annotations: map[string]string{
						"ntn.operators.dev/simulated-rsrp": "-130", // degraded
					},
				},
				Spec: baseSpec(),
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
		})
		AfterEach(func() { deleteSlice() })

		It("should stay on satellite when terrestrial is still degraded", func() {
			eph := createEphemerisWithPass(true)
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			// Pre-set to satellite.
			slice := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, slice)).To(Succeed())
			slice.Status.ActivePathType = pathSatellite
			slice.Status.FailoverCount = 1
			slice.Status.LastFailover = &metav1.Time{Time: time.Date(2026, 4, 17, 11, 55, 0, 0, time.UTC)}
			Expect(k8sClient.Status().Update(context.Background(), slice)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal(pathSatellite))
		})
	})

	Context("Both paths degraded (no satellite available)", func() {
		BeforeEach(func() {
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: sliceName, Namespace: namespace,
					Annotations: map[string]string{
						"ntn.operators.dev/simulated-rsrp": "-130",
					},
				},
				Spec: baseSpec(),
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
		})
		AfterEach(func() { deleteSlice() })

		It("should report unavailable when on unknown path and both degraded", func() {
			// Pre-set to unavailable.
			slice := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, slice)).To(Succeed())
			slice.Status.ActivePathType = "unavailable"
			Expect(k8sClient.Status().Update(context.Background(), slice)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("unavailable"))

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPathActive)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("Unavailable"))
		})
	})

	Context("ephemerisToSlice mapper edge cases", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should return empty for non-matching ephemeris name", func() {
			reconciler := newReconciler()

			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "starlink-constellation", Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type: "CelesTrak", URL: "https://test",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			requests := reconciler.ephemerisToSlice(context.Background(), eph)
			Expect(requests).To(BeEmpty())
		})

		It("should return nil for non-SatelliteEphemeris object", func() {
			reconciler := newReconciler()
			// Pass a wrong type (NTNSlice instead of SatelliteEphemeris).
			wrongObj := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "wrong-type", Namespace: namespace},
			}
			requests := reconciler.ephemerisToSlice(context.Background(), wrongObj)
			Expect(requests).To(BeNil())
		})
	})

	Context("Reconciler with nil Now function", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should use real time.Now() as fallback", func() {
			reconciler := &NTNSliceReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(10),
				Now:      nil, // triggers fallback to time.Now()
			}
			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(sliceRequeueInterval))

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal("terrestrial"))
		})
	})

	Context("ephemerisToSlice with multiple slices", func() {
		It("should only return slices that reference the specific ephemeris", func() {
			// Create two slices: one referencing "oneweb", one referencing "starlink".
			slice1 := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "slice-oneweb", Namespace: namespace},
				Spec:       baseSpec(), // references oneweb-constellation
			}
			Expect(k8sClient.Create(context.Background(), slice1)).To(Succeed())

			spec2 := baseSpec()
			spec2.SatellitePath.EphemerisRef = "starlink-constellation"
			slice2 := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "slice-starlink", Namespace: namespace},
				Spec:       spec2,
			}
			Expect(k8sClient.Create(context.Background(), slice2)).To(Succeed())

			DeferCleanup(func() {
				_ = k8sClient.Delete(context.Background(), slice1)
				_ = k8sClient.Delete(context.Background(), slice2)
			})

			reconciler := newReconciler()

			// Trigger with oneweb ephemeris — should only match slice1.
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type: "CelesTrak", URL: "https://test",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			requests := reconciler.ephemerisToSlice(context.Background(), eph)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("slice-oneweb"))
		})
	})

	Context("SatelliteEphemeris with empty pass windows", func() {
		BeforeEach(func() { createSlice() })
		AfterEach(func() { deleteSlice() })

		It("should report satellite unavailable when pass list is empty", func() {
			eph := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: "oneweb-constellation", Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type: "CelesTrak", URL: "https://test",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())
			// No pass windows in status.
			eph.Status.SatelliteCount = 651
			Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("SatelliteUnavailable"))
		})
	})
})
