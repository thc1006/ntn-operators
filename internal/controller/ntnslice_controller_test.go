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
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/netutil"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
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
		// The reconciler adds a metrics-cleanup finalizer, so a plain Delete only
		// marks the object Terminating; drop the finalizer so envtest actually
		// removes it (otherwise it lingers and poisons the next spec's create).
		if controllerutil.RemoveFinalizer(slice, sliceMetricsFinalizer) {
			Expect(client.IgnoreNotFound(k8sClient.Update(context.Background(), slice))).To(Succeed())
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), slice))).To(Succeed())
	}

	newReconciler := func() *NTNSliceReconciler {
		return &NTNSliceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(10),
			Now:      func() time.Time { return time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC) },
		}
	}

	// Safety net: after every spec, sweep any NTNSlice left in the test namespace
	// (envtest has no garbage collector, and reconciling adds a finalizer, so a
	// spec that Deletes without clearing it would leave a Terminating object that
	// poisons later specs' List counts / creates). Runs after each spec's own
	// AfterEach/DeferCleanup, so it also catches specs that clean up with a plain
	// Delete. Idempotent for specs that already cleaned up.
	AfterEach(func() {
		var list ntnv1alpha1.NTNSliceList
		Expect(k8sClient.List(context.Background(), &list, client.InNamespace(namespace))).To(Succeed())
		for i := range list.Items {
			s := &list.Items[i]
			if controllerutil.RemoveFinalizer(s, sliceMetricsFinalizer) {
				Expect(client.IgnoreNotFound(k8sClient.Update(context.Background(), s))).To(Succeed())
			}
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), s))).To(Succeed())
		}
		Eventually(func(g Gomega) {
			var l ntnv1alpha1.NTNSliceList
			g.Expect(k8sClient.List(context.Background(), &l, client.InNamespace(namespace))).To(Succeed())
			g.Expect(l.Items).To(BeEmpty())
		}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
	})

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

			// Capture FailoverTotal before reconcile and assert a +1 delta,
			// so the check is independent of any other spec that fails over
			// on the same series under -ginkgo.randomize-all.
			counterLabels := prometheus.Labels{
				"namespace": namespace, "slice": sliceName,
				"from_path": "terrestrial", "to_path": pathSatellite,
			}
			before := testutil.ToFloat64(ntnmetrics.FailoverTotal.With(counterLabels))

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
			// The deferred FailoverTotal.Inc() must have fired, now that the
			// status write persisted — guards the Inc ordering end-to-end.
			Expect(testutil.ToFloat64(ntnmetrics.FailoverTotal.With(counterLabels))).
				To(Equal(before + 1))
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

	// fullSpec returns a spec with QoS, Security, and Billing configured.
	fullSpec := func() ntnv1alpha1.NTNSliceSpec {
		spec := baseSpec()
		spec.QoSMapping = &ntnv1alpha1.QoSMapping{
			Terrestrial5QI:   9,
			SatelliteQCI:     "interactive",
			MaxLatencyBudget: metav1.Duration{Duration: 150 * time.Millisecond},
		}
		spec.Security = &ntnv1alpha1.SecurityPolicy{
			EncryptionLevel: "AES-256",
			AuthOnHandover:  "re-authenticate",
		}
		spec.Billing = &ntnv1alpha1.BillingSpec{
			TerrestrialRate: "per-volume",
			SatelliteRate:   "per-minute",
		}
		return spec
	}

	Context("When QoS, Security, and Billing are configured", func() {
		BeforeEach(func() {
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: sliceName, Namespace: namespace},
				Spec:       fullSpec(),
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
		})
		AfterEach(func() { deleteSlice() })

		It("should set QoS/Security/Billing status and conditions on terrestrial path", func() {
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// QoS status should reflect terrestrial settings.
			Expect(updated.Status.AppliedQoS).To(ContainSubstring("5QI=9"))
			qosCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionQoSApplied)
			Expect(qosCond).NotTo(BeNil())
			Expect(qosCond.Status).To(Equal(metav1.ConditionTrue))

			// Security status.
			Expect(updated.Status.AppliedEncryption).To(Equal("AES-256"))
			secCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionSecured)
			Expect(secCond).NotTo(BeNil())
			Expect(secCond.Status).To(Equal(metav1.ConditionTrue))

			// Billing status.
			Expect(updated.Status.BillingMode).To(Equal("per-volume"))
			billCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionBillingActive)
			Expect(billCond).NotTo(BeNil())
			Expect(billCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("When QoS/Security/Billing are not configured", func() {
		BeforeEach(func() { createSlice() }) // baseSpec has no QoS/Security/Billing
		AfterEach(func() { deleteSlice() })

		It("should not set QoS/Security/Billing conditions", func() {
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			Expect(updated.Status.AppliedQoS).To(BeEmpty())
			Expect(updated.Status.AppliedEncryption).To(BeEmpty())
			Expect(updated.Status.BillingMode).To(BeEmpty())

			Expect(meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionQoSApplied)).To(BeNil())
			Expect(meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionSecured)).To(BeNil())
			Expect(meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionBillingActive)).To(BeNil())
		})
	})

	Context("When on satellite path with Billing configured", func() {
		BeforeEach(func() {
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: sliceName, Namespace: namespace,
					Annotations: map[string]string{
						"ntn.operators.dev/simulated-rsrp": "-130",
					},
				},
				Spec: fullSpec(),
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
		})
		AfterEach(func() { deleteSlice() })

		It("should switch billing mode to satellite rate after failover", func() {
			eph := createEphemerisWithPass(true)
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), eph) })

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ActivePathType).To(Equal(pathSatellite))
			Expect(updated.Status.BillingMode).To(Equal("per-minute")) // satellite rate
			Expect(updated.Status.AppliedQoS).To(ContainSubstring("interactive"))
		})
	})

	// --- CEL validation tests ---

	Context("CEL: terrestrialPath.priority must be primary", func() {
		It("should reject creation when terrestrial priority is failover", func() {
			spec := baseSpec()
			spec.TerrestrialPath.Priority = "failover"
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-priority", Namespace: namespace},
				Spec:       spec,
			}
			err := k8sClient.Create(context.Background(), slice)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("primary"))
		})
	})

	Context("CEL: satellitePath.priority must be failover", func() {
		It("should reject creation when satellite priority is primary", func() {
			spec := baseSpec()
			spec.SatellitePath.Priority = "primary"
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-sat-priority", Namespace: namespace},
				Spec:       spec,
			}
			err := k8sClient.Create(context.Background(), slice)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failover"))
		})
	})

	// --- MetricsSource CEL validation tests (#67) ---

	Context("CEL: MetricsSource type=prometheus requires prometheus block", func() {
		It("should reject when type=prometheus without prometheus block", func() {
			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-prom-missing", Namespace: namespace},
				Spec:       spec,
			}
			err := k8sClient.Create(context.Background(), slice)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus block"))
		})
	})

	Context("CEL: MetricsSource prometheus requires at least one query", func() {
		It("should reject when prometheus block has all queries empty", func() {
			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: "http://prom.monitoring:9090",
					Queries:  ntnv1alpha1.PrometheusQueries{},
				},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-prom-empty-queries", Namespace: namespace},
				Spec:       spec,
			}
			err := k8sClient.Create(context.Background(), slice)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one query"))
		})
	})

	Context("CEL: MetricsSource prometheus endpoint pattern", func() {
		It("should reject endpoint without http(s):// scheme", func() {
			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: "prom.monitoring:9090",
					Queries:  ntnv1alpha1.PrometheusQueries{RsrpDbm: "sum(up)"},
				},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-prom-bad-endpoint", Namespace: namespace},
				Spec:       spec,
			}
			err := k8sClient.Create(context.Background(), slice)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("MetricsSource: explicit annotations accepted", func() {
		It("should accept type=annotations", func() {
			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourceAnnotations,
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-annotations-accept", Namespace: namespace},
				Spec:       spec,
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), slice) })
		})
	})

	Context("MetricsSource: valid prometheus config accepted", func() {
		It("should accept type=prometheus with endpoint + at least one query", func() {
			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: "http://prom.monitoring:9090",
					Queries: ntnv1alpha1.PrometheusQueries{
						RsrpDbm: "avg(ue_rsrp_dbm)",
					},
				},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-test-prom-accept", Namespace: namespace},
				Spec:       spec,
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), slice) })
		})
	})

	// --- MetricsSource integration (Prometheus mode) ---

	// promHandler builds an httptest handler that answers the Prometheus
	// instant-query API with a scalar result for any query in values.
	// Queries not in values return HTTP 500 so tests exercise the
	// "metrics unavailable" path intentionally.
	promHandler := func(values map[string]float64) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			q := req.FormValue("query")
			v, ok := values[q]
			if !ok {
				http.Error(w, "no response configured for "+q, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"%g"]}}`, v)
		}
	}

	Context("MetricsSource prometheus: happy path reaches failover engine", func() {
		It("should drive failover decision from PromQL scalars", func() {
			server := httptest.NewServer(promHandler(map[string]float64{
				"rsrp_q":    -150, // below trigger -120
				"latency_q": 10,
				"loss_q":    0,
			}))
			DeferCleanup(server.Close)

			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: server.URL,
					Queries: ntnv1alpha1.PrometheusQueries{
						RsrpDbm:           "rsrp_q",
						LatencyMs:         "latency_q",
						PacketLossPercent: "loss_q",
					},
				},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "prom-happy", Namespace: namespace},
				Spec:       spec,
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), slice) })

			_, err := newReconciler().Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "prom-happy", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: "prom-happy", Namespace: namespace}, updated)).To(Succeed())
			// RSRP=-150 is below trigger "rsrp < -120"; failover decision
			// must be reachable (not held on Unknown).
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).NotTo(Equal(metav1.ConditionUnknown),
				"Prometheus happy path must not leave FailoverReady=Unknown")
		})
	})

	Context("MetricsSource prometheus: unreachable endpoint holds current path", func() {
		It("should set FailoverReady=Unknown when Prometheus returns 500 and there is no cached value", func() {
			// Empty values map -> every query returns 500.
			server := httptest.NewServer(promHandler(map[string]float64{}))
			DeferCleanup(server.Close)

			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: server.URL,
					Queries: ntnv1alpha1.PrometheusQueries{
						RsrpDbm: "rsrp_q",
					},
				},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "prom-unknown", Namespace: namespace},
				Spec:       spec,
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), slice) })

			_, err := newReconciler().Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "prom-unknown", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: "prom-unknown", Namespace: namespace}, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
			Expect(cond.Reason).To(SatisfyAny(Equal("MetricsReaderError"), Equal("MetricsUnavailable")),
				"unreachable Prometheus must surface as one of the metrics-failure reasons")
		})
	})

	Context("MetricsSource prometheus: endpoint allow-list rejects non-listed host", func() {
		It("should set FailoverReady=Unknown with reason=EndpointNotAllowed when admin allow-list denies the host", func() {
			allow := netutil.ParseEndpointAllowlist("allowed.prom.example.com")
			provider := slicemetrics.NewProvider(slicemetrics.NewClientPool(),
				slicemetrics.WithEndpointAllowlist(allow))

			spec := baseSpec()
			spec.MetricsSource = &ntnv1alpha1.MetricsSource{
				Type: ntnv1alpha1.MetricsSourcePrometheus,
				Prometheus: &ntnv1alpha1.PrometheusMetricsSource{
					Endpoint: "http://denied.prom.example.com:9090",
					Queries:  ntnv1alpha1.PrometheusQueries{RsrpDbm: "up"},
				},
			}
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "allowlist-denied", Namespace: namespace},
				Spec:       spec,
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), slice) })

			rec := newReconciler()
			rec.ReaderProvider = provider
			_, err := rec.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "allowlist-denied", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.NTNSlice{}
			Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: "allowlist-denied", Namespace: namespace}, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionFailoverReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
			Expect(cond.Reason).To(Equal("EndpointNotAllowed"),
				"allow-list rejection must surface its own reason, not a generic one")
			Expect(cond.Message).To(ContainSubstring("denied.prom.example.com"),
				"reason message should name the offending host so kubectl describe is actionable")
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

	// Exercises the REAL finalizer-driven deletion path against the envtest
	// apiserver (the unit tests use a fake client): a deletion reconcile must
	// clear the finalizer AND release the slice's per-CR series.
	Context("When a slice with the metrics-cleanup finalizer is deleted", func() {
		It("clears the finalizer and releases its per-CR series via the deletion reconcile", func() {
			const name, ref = "del-recon", "eph-del-recon"
			key := types.NamespacedName{Name: name, Namespace: namespace}
			spec := baseSpec()
			spec.SatellitePath.EphemerisRef = ref
			slice := &ntnv1alpha1.NTNSlice{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec:       spec,
			}
			Expect(k8sClient.Create(context.Background(), slice)).To(Succeed())
			r := newReconciler()

			// First reconcile adds the finalizer (Step 1b runs before the reader
			// logic, so it lands even with no metrics endpoint configured).
			_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
			Expect(k8sClient.Get(context.Background(), key, slice)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(slice, sliceMetricsFinalizer)).To(BeTrue())

			// Seed the per-CR series as a successful reconcile would.
			ntnmetrics.SatellitePassAvailable.With(prometheus.Labels{"namespace": namespace, "ephemeris": ref}).Set(1)
			before := testutil.CollectAndCount(ntnmetrics.SatellitePassAvailable)

			// Delete → held Terminating by the finalizer on the real apiserver.
			Expect(k8sClient.Delete(context.Background(), slice)).To(Succeed())
			Expect(k8sClient.Get(context.Background(), key, slice)).To(Succeed())
			Expect(slice.DeletionTimestamp).NotTo(BeNil())

			// The deletion reconcile drives the real path: the controller clears the
			// finalizer (r.Update to the apiserver) and releases the series.
			_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(context.Background(), key, &ntnv1alpha1.NTNSlice{}))
			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(BeTrue())
			Expect(testutil.CollectAndCount(ntnmetrics.SatellitePassAvailable)).To(Equal(before - 1))
		})
	})
})
