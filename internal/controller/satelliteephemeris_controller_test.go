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
	"net/http"
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

// callCount returns how many times Fetch was invoked (race-safe).
func (m *mockGPFetcher) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
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

// testMEOOMM creates a deep-space MEO (period ~288 min, O3b-class) inclined to
// transit Taipei, with epoch now. Unlike a GEO — which is never visible from
// Taipei, so a leak leaves no observable trace — a wrongly near-earth-propagated
// MEO DOES produce pass windows over the test ground station (probe-verified:
// ≥3 windows for any 24h start). It therefore gives the pass-prediction exclusion
// test real teeth: if the guard stopped filtering it, it would surface in
// NextPassWindows and fail the assertion.
func testMEOOMM() sgp4.OMM {
	return sgp4.OMM{
		ObjectName:      "O3B-TEST (MEO)",
		ObjectID:        "2020-001A",
		EpochStr:        time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		MeanMotion:      5.0, // ~288 min period → deep space
		Eccentricity:    0.001,
		Inclination:     55.0, // transits Taipei (25°N)
		RAOfAscNode:     60.0,
		ArgOfPericenter: 0.0,
		MeanAnomaly:     0.0,
		NoradCatID:      49999,
	}
}

// testGEOOMM creates a deep-space (GEO, ~1436 min period) OMM with epoch now.
// It drives the orbit-regime guard (findings.md B-5): the near-earth SGP4
// propagator must reject it rather than propagate it into a wrong position.
func testGEOOMM() sgp4.OMM {
	return sgp4.OMM{
		ObjectName:      "INTELSAT-10 (TEST)",
		ObjectID:        "2004-022A",
		EpochStr:        time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		MeanMotion:      1.00272, // ~1436 min period → deep space
		Eccentricity:    0.0003,
		Inclination:     0.05,
		RAOfAscNode:     75.0,
		ArgOfPericenter: 0.0,
		MeanAnomaly:     0.0,
		NoradCatID:      28358,
		BStar:           0.0,
		MeanMotionDot:   0,
		MeanMotionDDot:  0,
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

	createResourceTrackingNoradIDs := func(ids ...int) {
		resource := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type:            "CelesTrak",
					URL:             "https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
				Satellites: &ntnv1alpha1.SatelliteSelector{NoradIDs: ids},
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
			Client: k8sClient,
			// Exercise the uncached-read path for the SpaceTrack credentials Secret.
			APIReader: k8sClient,
			Scheme:    k8sClient.Scheme(),
			Recorder:  events.NewFakeRecorder(10),
			Fetcher:   fetcher,
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
			Expect(result.RequeueAfter).To(Equal(propagationRefreshInterval))

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

	// --- Orbit-regime guard (findings.md B-5): reject deep-space element sets ---

	Context("When the source contains deep-space element sets", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("rejects them, propagates only near-earth sats, and sets UnsupportedOrbitRegime", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					// One LEO (ISS, propagatable) + one GEO (deep space, must be rejected).
					OMMs:           []sgp4.OMM{testISSOMM(), testGEOOMM()},
					SatelliteCount: 2,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// The guard condition is True with the deep-space reason.
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("DeepSpaceElementsRejected"))

			// SatelliteCount stays the honest fetched count (2), but only the LEO
			// satellite is propagated — the GEO one never reaches the propagator.
			Expect(updated.Status.SatelliteCount).To(Equal(2))
			Expect(updated.Status.PropagatedStates).To(HaveLen(1))
			Expect(updated.Status.PropagatedStates[0].NoradID).To(Equal(25544)) // ISS, not the GEO 28358
		})
	})

	Context("When the source is entirely near-earth", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("sets UnsupportedOrbitRegime to False (AllNearEarth)", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					OMMs:           []sgp4.OMM{testISSOMM()},
					SatelliteCount: 1,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("AllNearEarth"))
			Expect(updated.Status.PropagatedStates).To(HaveLen(1))
		})
	})

	Context("When the source is a cleartext http:// URL resolving to a public IP (I-21)", func() {
		// A public http source is a forged-OMM-into-SIB19 vector; it must be
		// refused BEFORE any fetch. In-cluster http mirrors (private-resolving)
		// stay allowed and are covered by the TestPublicHTTPSource unit table.
		insecureName := types.NamespacedName{Name: "eph-insecure-http", Namespace: namespace}
		AfterEach(func() {
			r := &ntnv1alpha1.SatelliteEphemeris{}
			if err := k8sClient.Get(context.Background(), insecureName, r); err == nil {
				Expect(k8sClient.Delete(context.Background(), r)).To(Succeed())
			}
		})

		It("rejects with InsecureURL and never calls the fetcher", func() {
			resource := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: insecureName.Name, Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "CelesTrak",
						URL:             "http://8.8.8.8/gp.json", // literal public IP over cleartext
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), resource)).To(Succeed())

			mock := &mockGPFetcher{result: ephemeris.GPFetchResult{SatelliteCount: 1, FetchedAt: time.Now()}}
			_, err := newReconciler(mock).Reconcile(context.Background(), reconcile.Request{NamespacedName: insecureName})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), insecureName, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InsecureURL"))
			// The fetch must NOT have happened — the guard runs before it.
			Expect(mock.callCount()).To(Equal(0))
		})
	})

	Context("When a deep-space set is in the feed but excluded via spec.satellites.noradIDs", func() {
		// The operator narrows tracking to LEO sats; the upstream feed still carries
		// a GEO. The guard must NOT raise UnsupportedOrbitRegime for a bird the
		// operator explicitly excluded (adversarial-review IMPORTANT: tracked-scoped
		// reporting, not source-scoped).
		BeforeEach(func() { createResourceTrackingNoradIDs(25544) }) // ISS only
		AfterEach(func() { deleteResource() })

		It("does not false-alarm; condition is False and only the tracked LEO is propagated", func() {
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					OMMs:           []sgp4.OMM{testISSOMM(), testGEOOMM()}, // GEO 28358 is NOT tracked
					SatelliteCount: 2,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse), "excluded GEO must not raise the condition")
			Expect(cond.Reason).To(Equal("AllNearEarth"))

			Expect(updated.Status.PropagatedStates).To(HaveLen(1))
			Expect(updated.Status.PropagatedStates[0].NoradID).To(Equal(25544))
		})
	})

	Context("When pass prediction is configured and the feed contains a deep-space set", func() {
		// Locks in that deep-space sets are excluded from pass prediction, not just
		// from propagated states (adversarial-review NIT: the pass-prediction path
		// shares result.OMMs but was previously unasserted).
		BeforeEach(func() {
			createGroundStation()
			createResourceWithPassPrediction()
		})
		AfterEach(func() {
			deleteResource()
			deleteGroundStation()
		})

		It("rejects the deep-space set and never lists it in NextPassWindows", func() {
			// Use a MEO (not a GEO): a wrongly near-earth-propagated MEO WOULD
			// produce Taipei pass windows, so removing the guard makes this test
			// fail (real teeth). A GEO never rises over Taipei, so it could not.
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					OMMs:           []sgp4.OMM{testISSOMM(), testMEOOMM()},
					SatelliteCount: 2,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// The MEO was in scope (no noradIDs) and rejected...
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			// ...and never reaches pass prediction: no window may reference it,
			// even though it would produce windows if it had leaked through.
			for _, w := range updated.Status.NextPassWindows {
				Expect(w.Satellite).NotTo(Equal("O3B-TEST (MEO)"), "deep-space sat leaked into pass prediction")
			}
		})
	})

	Context("When data is not modified (304)", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should still populate satelliteCount and lastUpdated from cached OMMs", func() {
			// A 304 response is still a successful validation that the
			// current data is fresh — semantically we should mark the
			// CR's status with the cached counts + "verified at" time,
			// not leave status empty. Especially important when a new
			// CR reuses a URL whose fetcher cache was primed by a
			// sibling CR (reproducible in E2E via #105's mock setup).
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{
					SatelliteCount: 620,
					NotModified:    true,
					FetchedAt:      time.Now(),
				},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(propagationRefreshInterval))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.SatelliteCount).To(Equal(620))
			Expect(updated.Status.LastUpdated).NotTo(BeNil())

			fetchedCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(fetchedCond).NotTo(BeNil())
			Expect(fetchedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(fetchedCond.Reason).To(Equal("NotModified"))
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
			// Rate-limit backoff is unchanged by #179: error paths still requeue at
			// the (clamped) GP-refresh interval, not the short propagation cadence.
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

		It("should set FetchFailed and return the error so the workqueue backs off (I-19b)", func() {
			mock := &mockGPFetcher{err: errors.New("connection refused")}
			reconciler := newReconciler(mock)

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			// I-19b: a generic transient fetch error is RETURNED so controller-runtime's
			// workqueue applies exponential backoff, instead of a flat requeue.
			Expect(err).To(HaveOccurred())

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("FetchFailed"))
		})
	})

	Context("When fetch is rate-limited (I-19b)", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("requeues at the slow cadence with NO error (not a controller error), honoring Retry-After", func() {
			// A rate-limit is an expected polite-backoff state, not a controller error:
			// Reconcile must return nil + a RequeueAfter (>= a minute), never the error
			// (which would trigger the workqueue's fast exponential retry → firewall risk).
			mock := &mockGPFetcher{err: &ephemeris.RateLimitError{StatusCode: 403, RetryAfter: 90 * time.Minute}}
			result, err := newReconciler(mock).Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// Retry-After (90m) exceeds the 2h floor? No — max(effectiveInterval, 90m).
			Expect(result.RequeueAfter).To(BeNumerically(">=", time.Minute))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("RateLimited"))
		})
	})

	Context("When fetch fails with stale pass windows (regression #13)", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("should clear NextPassWindows and remove PassesPredicted on fetch failure", func() {
			// First: seed status with pass windows and PassesPredicted=True.
			eph := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, eph)).To(Succeed())
			eph.Status.SatelliteCount = 100
			eph.Status.NextPassWindows = []ntnv1alpha1.PassWindow{
				{
					Satellite: "SAT-1", GroundStation: "gs-01",
					AOS: metav1.Time{Time: time.Now()}, LOS: metav1.Time{Time: time.Now().Add(time.Hour)},
					MaxElevation: "45.0",
				},
			}
			meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
				Type: ntnv1alpha1.ConditionPassesPredicted, Status: metav1.ConditionTrue,
				Reason: "PredictionSucceeded", Message: "1 passes",
			})
			Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())

			// Verify seeded state.
			Expect(eph.Status.NextPassWindows).To(HaveLen(1))

			// Now reconcile with a failing fetcher.
			mock := &mockGPFetcher{err: errors.New("network timeout")}
			reconciler := newReconciler(mock)
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			// I-19b: generic error is returned (workqueue backoff). With a COLD cache
			// (no prior successful fetch) the pass windows are still cleared.
			Expect(err).To(HaveOccurred())

			// Verify stale data is cleared.
			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.NextPassWindows).To(BeEmpty())

			passCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
			Expect(passCond).To(BeNil(), "PassesPredicted condition should be removed on fetch failure")
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

		It("requeues on the short propagation cadence even when refreshInterval is below minimum", func() {
			// The 2h clamp still gates the GP fetch (effectiveInterval), but since
			// #179 the requeue is the short propagation cadence, not the fetch
			// interval. The clamp's effect on fetch cadence is covered by the
			// "re-propagating between GP refreshes" context below.
			mock := &mockGPFetcher{
				result: ephemeris.GPFetchResult{SatelliteCount: 100, FetchedAt: time.Now()},
			}
			reconciler := newReconciler(mock)

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(propagationRefreshInterval))
		})
	})

	Context("When re-propagating between GP refreshes (#179)", func() {
		BeforeEach(func() { createResource() })
		AfterEach(func() { deleteResource() })

		It("reuses cached OMMs without a second GP fetch within the refresh window", func() {
			mock := &mockGPFetcher{result: ephemeris.GPFetchResult{
				SatelliteCount: 1,
				OMMs:           []sgp4.OMM{testISSOMM()},
				FetchedAt:      time.Now(),
			}}
			// One reconciler instance so its ommCache persists across reconciles.
			reconciler := newReconciler(mock)
			req := reconcile.Request{NamespacedName: typeNamespacedName}

			// 1st reconcile: cold cache → exactly one real GP fetch, short requeue.
			r1, err := reconciler.Reconcile(context.Background(), req)
			Expect(err).NotTo(HaveOccurred())
			Expect(mock.callCount()).To(Equal(1))
			Expect(r1.RequeueAfter).To(Equal(propagationRefreshInterval))

			// 2nd reconcile immediately (well within the 4h refresh window): it MUST
			// re-propagate from cache with NO second GP-source request (#179 accept).
			r2, err := reconciler.Reconcile(context.Background(), req)
			Expect(err).NotTo(HaveOccurred())
			Expect(mock.callCount()).To(Equal(1)) // still 1 — the rate-limit guarantee
			Expect(r2.RequeueAfter).To(Equal(propagationRefreshInterval))

			// The runtime-push epoch is refreshed on the cached re-propagation and
			// stays in the future, so an off-cycle consumer finds a fresh epoch.
			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.PropagatedStates).NotTo(BeEmpty())
			Expect(updated.Status.PropagatedStates[0].EpochUnixMs).To(BeNumerically(">", time.Now().UnixMilli()))

			// The cache-reuse path is reflected honestly in the condition reason.
			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("CachedRepropagation"))
		})

		It("re-fetches after a spec change instead of serving stale cached OMMs", func() {
			mock := &mockGPFetcher{result: ephemeris.GPFetchResult{
				SatelliteCount: 1,
				OMMs:           []sgp4.OMM{testISSOMM()},
				FetchedAt:      time.Now(),
			}}
			reconciler := newReconciler(mock)
			req := reconcile.Request{NamespacedName: typeNamespacedName}

			_, err := reconciler.Reconcile(context.Background(), req)
			Expect(err).NotTo(HaveOccurred())
			Expect(mock.callCount()).To(Equal(1))

			// Edit the spec (bumps metadata.generation): change the source URL.
			cur := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, cur)).To(Succeed())
			cur.Spec.Source.URL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=starlink&FORMAT=JSON"
			Expect(k8sClient.Update(context.Background(), cur)).To(Succeed())

			// The generation bump must invalidate the cache → a real fetch of the
			// new source, not a stale reuse of the old one (#179 regression guard).
			_, err = reconciler.Reconcile(context.Background(), req)
			Expect(err).NotTo(HaveOccurred())
			Expect(mock.callCount()).To(Equal(2))
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

		It("should set FetcherSetupFailed condition and requeue", func() {
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
			Expect(cond.Reason).To(Equal("FetcherSetupFailed"))
		})
	})

	// --- SpaceTrack fetcherForSource Tests ---

	Context("When source type is SpaceTrack but no SpaceTrackFetcher configured", func() {
		BeforeEach(func() {
			resource := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "SpaceTrack",
						URL:             "https://www.space-track.org/basicspacedata/query/class/gp/format/json",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
						Credentials:     &ntnv1alpha1.SecretReference{Name: "st-creds"},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), resource)).To(Succeed())
		})
		AfterEach(func() { deleteResource() })

		It("should set FetcherSetupFailed when SpaceTrackFetcher is nil", func() {
			reconciler := newReconciler(&mockGPFetcher{})
			// SpaceTrackFetcher is nil by default.

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
			Expect(cond.Reason).To(Equal("FetcherSetupFailed"))
			Expect(cond.Message).To(ContainSubstring("SpaceTrack fetcher is not configured"))
		})
	})

	Context("When source type is SpaceTrack but credentials Secret missing", func() {
		BeforeEach(func() {
			resource := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "SpaceTrack",
						URL:             "https://www.space-track.org/basicspacedata/query/class/gp/format/json",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
						Credentials:     &ntnv1alpha1.SecretReference{Name: "nonexistent-secret"},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), resource)).To(Succeed())
		})
		AfterEach(func() { deleteResource() })

		It("should set FetcherSetupFailed when Secret doesn't exist", func() {
			stFetcher := ephemeris.NewSpaceTrackFetcher(&http.Client{}, "https://fake")
			reconciler := newReconciler(&mockGPFetcher{})
			reconciler.SpaceTrackFetcher = stFetcher

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Minute))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("FetcherSetupFailed"))
			// Uniform CR-facing message (no Secret existence/key oracle); the specific cause
			// (NotFound) is logged, not surfaced on the CR. #222 review blocker 3.
			Expect(cond.Message).To(ContainSubstring("credentials unavailable or not authorized"))
		})
	})

	Context("When source type is SpaceTrack with no credentials ref (CEL validation)", func() {
		It("should reject CR creation at API level", func() {
			resource := &ntnv1alpha1.SatelliteEphemeris{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
				Spec: ntnv1alpha1.SatelliteEphemerisSpec{
					Source: ntnv1alpha1.EphemerisSource{
						Type:            "SpaceTrack",
						URL:             "https://www.space-track.org/basicspacedata/query/class/gp/format/json",
						RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
						// No Credentials set — CEL rule should reject this.
					},
				},
			}
			err := k8sClient.Create(context.Background(), resource)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("credentials"))
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
			Expect(result.RequeueAfter).To(Equal(propagationRefreshInterval))

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

		It("should set GroundStationNotFound condition but still update fetch status", func() {
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
			Expect(result.RequeueAfter).To(Equal(propagationRefreshInterval))

			updated := &ntnv1alpha1.SatelliteEphemeris{}
			Expect(k8sClient.Get(context.Background(), typeNamespacedName, updated)).To(Succeed())

			// Fetch should still succeed.
			Expect(updated.Status.SatelliteCount).To(Equal(1))

			// Prediction should fail gracefully.
			predCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
			Expect(predCond).NotTo(BeNil())
			Expect(predCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(predCond.Reason).To(Equal("GroundStationNotFound"))
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
