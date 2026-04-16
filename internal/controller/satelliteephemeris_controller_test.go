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
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// mockGPFetcher is a test double for ephemeris.GPFetcher.
type mockGPFetcher struct {
	mu     sync.Mutex
	result ephemeris.GPFetchResult
	err    error
	calls  int
}

func (m *mockGPFetcher) Fetch(_ context.Context, _ string) (ephemeris.GPFetchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.result, m.err
}

// testISSOMM creates an ISS OMM with epoch set to now for deterministic tests.
func testISSOMM() sgp4.OMM {
	return sgp4.OMM{
		ObjectName:         "ISS (ZARYA)",
		ObjectID:           "1998-067A",
		EpochStr:           time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		MeanMotion:         15.49554387,
		Eccentricity:       0.000588,
		Inclination:        51.6381,
		RAOfAscNode:        276.7884,
		ArgOfPericenter:    282.5765,
		MeanAnomaly:        192.7824,
		EphemerisType:      0,
		ClassificationType: "U",
		NoradCatID:         25544,
		ElementSetNo:       999,
		RevAtEpoch:         47189,
		BStar:              0.00025892,
		MeanMotionDot:      0.00019394,
		MeanMotionDDot:     0,
	}
}

var _ = Describe("SatelliteEphemeris Controller", func() {
	const resourceName = "test-ephemeris"
	const gsName = "gs-taipei-01"
	const namespace = "default"

	typeNamespacedName := types.NamespacedName{
		Name:      resourceName,
		Namespace: namespace,
	}

	createResource := func() {
		resource := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceName,
				Namespace: namespace,
			},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type:            "CelesTrak",
					URL:             "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
			},
		}
		Expect(k8sClient.Create(context.Background(), resource)).To(Succeed())
	}

	createResourceWithPassPrediction := func() {
		resource := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceName,
				Namespace: namespace,
			},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type:            "CelesTrak",
					URL:             "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
				PassPrediction: &ntnv1alpha1.PassPredictionSpec{
					GroundStations: []string{gsName},
					MinElevation:   "10",
					Horizon:        metav1.Duration{Duration: 24 * time.Hour},
				},
			},
		}
		Expect(k8sClient.Create(context.Background(), resource)).To(Succeed())
	}

	createGroundStation := func() {
		gs := &ntnv1alpha1.GroundStationLifecycle{
			ObjectMeta: metav1.ObjectMeta{
				Name:      gsName,
				Namespace: namespace,
			},
			Spec: ntnv1alpha1.GroundStationLifecycleSpec{
				Hardware: ntnv1alpha1.HardwareSpec{
					Vendor: "ennoconn",
					Model:  "rugged-edge-5000",
				},
				Deployment: ntnv1alpha1.DeploymentSpec{
					Location: ntnv1alpha1.GeoLocation{
						Lat: "25.0330",
						Lon: "121.5654",
						Alt: "15",
					},
				},
			},
		}
		Expect(k8sClient.Create(context.Background(), gs)).To(Succeed())
	}

	deleteResource := func() {
		resource := &ntnv1alpha1.SatelliteEphemeris{}
		err := k8sClient.Get(context.Background(), typeNamespacedName, resource)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(context.Background(), resource)).To(Succeed())
	}

	deleteGroundStation := func() {
		gs := &ntnv1alpha1.GroundStationLifecycle{}
		err := k8sClient.Get(context.Background(), types.NamespacedName{Name: gsName, Namespace: namespace}, gs)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(context.Background(), gs)).To(Succeed())
	}

	newReconciler := func(fetcher ephemeris.GPFetcher) *SatelliteEphemerisReconciler {
		return &SatelliteEphemerisReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(10),
			Fetcher:  fetcher,
		}
	}

	// --- S2 Tests: GP Data Fetching ---

	Context("When fetching GP data successfully", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should update status with satellite count and conditions", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					SatelliteCount: 620,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(4 * time.Hour))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.SatelliteCount).To(Equal(620))
			Expect(updated.Status.LastUpdated).NotTo(BeNil())

			fetchedCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(fetchedCond).NotTo(BeNil())
			Expect(fetchedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(fetchedCond.Reason).To(Equal("FetchSucceeded"))

			parsedCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataParsed)
			Expect(parsedCond).NotTo(BeNil())
			Expect(parsedCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("When data is not modified (304)", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should not update lastUpdated", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{NotModified: true, FetchedAt: time.Now()},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(4 * time.Hour))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.LastUpdated).To(BeNil())
		})
	})

	Context("When rate limited (HTTP 403)", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should set RateLimited condition and requeue at interval", func() {
			mock := &mockGPFetcher{err: ephemeris.ErrRateLimited}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(4 * time.Hour))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("RateLimited"))
		})
	})

	Context("When fetch fails with generic error", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should set FetchFailed condition and requeue after 1 minute", func() {
			mock := &mockGPFetcher{err: errors.New("connection refused")}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Minute))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("FetchFailed"))
		})
	})

	Context("When refreshInterval is below minimum", func() {
		BeforeEach(func() {
			resource := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "CelesTrak",
						URL:             "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON",
						RefreshInterval: metav1.Duration{Duration: 30 * time.Minute},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), resource)).To(Succeed())
		})
		AfterEach(func() { deleteResource() })

		It("should clamp requeue interval to 2 hours", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{SatelliteCount: 100, FetchedAt: time.Now()},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(2 * time.Hour))
		})
	})

	Context("When resource does not exist", func() {
		It("should return without error", func() {
			reconciler := newReconciler(&mockGPFetcher{})

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("When fetcher is nil", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should set InternalError condition and requeue", func() {
			reconciler := newReconciler(nil)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Minute))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InternalError"))
		})
	})

	// --- S3 Tests: Pass Prediction ---

	Context("When pass prediction is configured with a valid ground station", func() {
		BeforeEach(func() {
			createGroundStation()
			createResourceWithPassPrediction()
		})
		AfterEach(func() {
			deleteResource()
			deleteGroundStation()
		})

		It("should compute pass windows and set PassesPredicted condition", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					OMMs:           []sgp4.OMM{testISSOMM()},
					SatelliteCount: 1,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(4 * time.Hour))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// Should have pass windows.
			Expect(updated.Status.NextPassWindows).ToNot(BeEmpty())

			// Verify pass window fields.
			pw := updated.Status.NextPassWindows[0]
			Expect(pw.Satellite).To(Equal("ISS (ZARYA)"))
			Expect(pw.GroundStation).To(Equal(gsName))
			Expect(pw.AOS.Time).NotTo(BeZero())
			Expect(pw.LOS.Time).NotTo(BeZero())
			Expect(pw.MaxElevation).NotTo(BeEmpty())

			// Verify condition.
			predCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
			Expect(predCond).NotTo(BeNil())
			Expect(predCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(predCond.Reason).To(Equal("PredictionSucceeded"))
		})
	})

	Context("When ground station resource does not exist", func() {
		BeforeEach(func() {
			createResourceWithPassPrediction() // references gs-taipei-01 but we don't create it
		})
		AfterEach(func() { deleteResource() })

		It("should set PredictionFailed condition but still update fetch status", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					OMMs:           []sgp4.OMM{testISSOMM()},
					SatelliteCount: 1,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(4 * time.Hour))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// Fetch should still succeed.
			Expect(updated.Status.SatelliteCount).To(Equal(1))

			// Prediction should fail gracefully.
			predCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
			Expect(predCond).NotTo(BeNil())
			Expect(predCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(predCond.Reason).To(Equal("PredictionFailed"))
		})
	})

	Context("When no pass prediction is configured", func() {
		BeforeEach(func() { createResource() }) // no PassPrediction in spec
		AfterEach(func() { deleteResource() })

		It("should skip pass prediction", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					OMMs:           []sgp4.OMM{testISSOMM()},
					SatelliteCount: 1,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// Should have no pass windows or prediction condition.
			Expect(updated.Status.NextPassWindows).To(BeEmpty())
			predCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
			Expect(predCond).To(BeNil())
		})
	})

	// --- S4 Tests: Watch GroundStationLifecycle changes ---

	Context("When a GroundStationLifecycle changes", func() {
		BeforeEach(func() {
			createGroundStation()
			createResourceWithPassPrediction()
		})
		AfterEach(func() {
			deleteResource()
			deleteGroundStation()
		})

		It("should map ground station to referencing ephemeris resources", func() {
			reconciler := newReconciler(&mockGPFetcher{})

			gs := &ntnv1alpha1.GroundStationLifecycle{}
			Expect(k8sClient.Get(context.Background(),
				types.NamespacedName{Name: gsName, Namespace: namespace}, gs)).To(Succeed())

			requests := reconciler.groundStationToEphemeris(context.Background(), gs)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal(resourceName))
		})

		It("should not map ground station that no ephemeris references", func() {
			reconciler := newReconciler(&mockGPFetcher{})

			unreferenced := &ntnv1alpha1.GroundStationLifecycle{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gs-unreferenced",
					Namespace: namespace,
				},
				Spec: ntnv1alpha1.GroundStationLifecycleSpec{
					Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "test", Model: "test"},
					Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "0", Lon: "0"}},
				},
			}
			Expect(k8sClient.Create(context.Background(), unreferenced)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(context.Background(), unreferenced)
			})

			requests := reconciler.groundStationToEphemeris(context.Background(), unreferenced)
			Expect(requests).To(BeEmpty())
		})
	})
})
