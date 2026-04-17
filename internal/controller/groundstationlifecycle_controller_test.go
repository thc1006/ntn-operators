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
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

var _ = Describe("GroundStationLifecycle Controller", func() {
	const gsName = "test-groundstation"
	const nodeName = "edge-node-01"
	const namespace = "default"

	typeNamespacedName := types.NamespacedName{Name: gsName, Namespace: namespace}

	fixedNow := time.Date(2026, 4, 16, 3, 0, 0, 0, time.UTC) // 03:00 UTC

	createGS := func(withMonitoring bool, withFirmware bool) {
		gs := &ntnv1alpha1.GroundStationLifecycle{
			ObjectMeta: metav1.ObjectMeta{Name: gsName, Namespace: namespace},
			Spec: ntnv1alpha1.GroundStationLifecycleSpec{
				Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "ennoconn", Model: "rugged-edge-5000"},
				Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654"}},
			},
		}
		if withMonitoring {
			gs.Spec.Monitoring = &ntnv1alpha1.MonitoringSpec{
				HealthCheckInterval: metav1.Duration{Duration: 1 * time.Minute},
			}
		}
		if withFirmware {
			gs.Spec.Firmware = &ntnv1alpha1.FirmwareSpec{
				AutoUpdate:        true,
				Channel:           "stable",
				MaintenanceWindow: "02:00-04:00 UTC",
			}
		}
		Expect(k8sClient.Create(context.Background(), gs)).To(Succeed())
	}

	createNode := func(ready bool, pressures ...corev1.NodeConditionType) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: map[string]string{groundStationLabel: namespace + "." + gsName},
				Annotations: map[string]string{
					firmwareVersionAnnotation:   "2.3.0",
					availableFirmwareAnnotation: "2.3.1",
				},
			},
		}
		Expect(k8sClient.Create(context.Background(), node)).To(Succeed())

		// Update node status (conditions).
		node.Status.NodeInfo.KubeletVersion = "v1.35.1+k3s1"
		readyStatus := corev1.ConditionTrue
		if !ready {
			readyStatus = corev1.ConditionFalse
		}
		node.Status.Conditions = []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: readyStatus},
		}
		for _, p := range pressures {
			node.Status.Conditions = append(node.Status.Conditions,
				corev1.NodeCondition{Type: p, Status: corev1.ConditionTrue})
		}
		Expect(k8sClient.Status().Update(context.Background(), node)).To(Succeed())
	}

	deleteGS := func() {
		gs := &ntnv1alpha1.GroundStationLifecycle{}
		err := k8sClient.Get(context.Background(), typeNamespacedName, gs)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(context.Background(), gs)).To(Succeed())
	}

	deleteNode := func() {
		node := &corev1.Node{}
		err := k8sClient.Get(context.Background(), types.NamespacedName{Name: nodeName}, node)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(context.Background(), node)).To(Succeed())
	}

	newReconciler := func(httpClient *http.Client) *GroundStationLifecycleReconciler {
		return &GroundStationLifecycleReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			Recorder:   events.NewFakeRecorder(10),
			HTTPClient: httpClient,
			Now:        func() time.Time { return fixedNow },
		}
	}

	reconcileGS := func(reconciler *GroundStationLifecycleReconciler) (reconcile.Result, error) {
		return reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: typeNamespacedName})
	}

	getGS := func() *ntnv1alpha1.GroundStationLifecycle {
		gs := &ntnv1alpha1.GroundStationLifecycle{}
		Expect(k8sClient.Get(context.Background(), typeNamespacedName, gs)).To(Succeed())
		return gs
	}

	// --- S7: Health Check ---

	Context("When node does not exist", func() {
		BeforeEach(func() { createGS(false, false) })
		AfterEach(func() { deleteGS() })

		It("should set phase=Provisioning for new resource", func() {
			_, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseProvisioning))
			cond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionK8sNodeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("NodeNotFound"))
		})
	})

	Context("When node exists and is healthy", func() {
		BeforeEach(func() {
			createGS(false, false)
			createNode(true)
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should set phase=Running with correct conditions", func() {
			result, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second)) // default interval

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseRunning))
			Expect(gs.Status.K8sVersion).To(Equal("v1.35.1+k3s1"))
			Expect(gs.Status.LastHealthCheck).NotTo(BeNil())

			k8sCond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionK8sNodeReady)
			Expect(k8sCond).NotTo(BeNil())
			Expect(k8sCond.Status).To(Equal(metav1.ConditionTrue))

			antCond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionAntennaReady)
			Expect(antCond).NotTo(BeNil())
			Expect(antCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("When node has MemoryPressure", func() {
		BeforeEach(func() {
			createGS(false, false)
			createNode(true, corev1.NodeMemoryPressure)
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should set phase=Degraded", func() {
			_, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseDegraded))
		})
	})

	Context("When multiple nodes match the same label", func() {
		BeforeEach(func() {
			createGS(false, false)
			createNode(true)
			// Create a second node with the same label.
			node2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "edge-node-02",
					Labels: map[string]string{groundStationLabel: namespace + "." + gsName},
				},
			}
			Expect(k8sClient.Create(context.Background(), node2)).To(Succeed())
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
			node2 := &corev1.Node{}
			if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "edge-node-02"}, node2); err == nil {
				Expect(k8sClient.Delete(context.Background(), node2)).To(Succeed())
			}
		})

		It("should set phase=Offline due to ambiguous mapping", func() {
			_, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseOffline))
			cond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionK8sNodeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("AmbiguousNodeMapping"))
		})
	})

	Context("When node is NotReady", func() {
		BeforeEach(func() {
			createGS(false, false)
			createNode(false)
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should set phase=Offline", func() {
			_, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseOffline))
		})
	})

	Context("When monitoring endpoint is healthy", func() {
		BeforeEach(func() { createNode(true) })
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should set RFLinkHealthy=True", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			DeferCleanup(server.Close)

			// Create GS with monitoring endpoint pointing to test server.
			gs := &ntnv1alpha1.GroundStationLifecycle{
				ObjectMeta: metav1.ObjectMeta{Name: gsName, Namespace: namespace},
				Spec: ntnv1alpha1.GroundStationLifecycleSpec{
					Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "ennoconn", Model: "rugged-edge-5000"},
					Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654"}},
					Monitoring: &ntnv1alpha1.MonitoringSpec{Endpoint: server.URL},
				},
			}
			Expect(k8sClient.Create(context.Background(), gs)).To(Succeed())

			_, err := reconcileGS(newReconciler(server.Client()))
			Expect(err).NotTo(HaveOccurred())

			updated := getGS()
			Expect(updated.Status.Phase).To(Equal(ntnv1alpha1.PhaseRunning))
			rfCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionRFLinkHealthy)
			Expect(rfCond).NotTo(BeNil())
			Expect(rfCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("When monitoring endpoint is unhealthy", func() {
		BeforeEach(func() { createNode(true) })
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should set phase=Degraded and RFLinkHealthy=False", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			DeferCleanup(server.Close)

			gs := &ntnv1alpha1.GroundStationLifecycle{
				ObjectMeta: metav1.ObjectMeta{Name: gsName, Namespace: namespace},
				Spec: ntnv1alpha1.GroundStationLifecycleSpec{
					Hardware:   ntnv1alpha1.HardwareSpec{Vendor: "ennoconn", Model: "rugged-edge-5000"},
					Deployment: ntnv1alpha1.DeploymentSpec{Location: ntnv1alpha1.GeoLocation{Lat: "25.0330", Lon: "121.5654"}},
					Monitoring: &ntnv1alpha1.MonitoringSpec{Endpoint: server.URL},
				},
			}
			Expect(k8sClient.Create(context.Background(), gs)).To(Succeed())

			_, err := reconcileGS(newReconciler(server.Client()))
			Expect(err).NotTo(HaveOccurred())

			updated := getGS()
			Expect(updated.Status.Phase).To(Equal(ntnv1alpha1.PhaseDegraded))
			rfCond := meta.FindStatusCondition(updated.Status.Conditions, ntnv1alpha1.ConditionRFLinkHealthy)
			Expect(rfCond).NotTo(BeNil())
			Expect(rfCond.Status).To(Equal(metav1.ConditionFalse))
		})
	})

	// --- S8: Firmware OTA ---

	Context("When firmware update available within maintenance window", func() {
		BeforeEach(func() {
			createGS(false, true) // firmware config with "02:00-04:00 UTC"
			createNode(true)
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should set phase=Updating", func() {
			reconciler := newReconciler(nil) // fixedNow=03:00 UTC → within window
			_, err := reconcileGS(reconciler)
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseUpdating))
			fwCond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
			Expect(fwCond).NotTo(BeNil())
			Expect(fwCond.Reason).To(Equal("UpdateAvailable"))
		})

		It("should complete update on next reconcile", func() {
			reconciler := newReconciler(nil)
			// First reconcile: triggers update.
			_, err := reconcileGS(reconciler)
			Expect(err).NotTo(HaveOccurred())
			Expect(getGS().Status.Phase).To(Equal(ntnv1alpha1.PhaseUpdating))

			// Second reconcile: completes update.
			_, err = reconcileGS(reconciler)
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseRunning))
			Expect(gs.Status.FirmwareVersion).To(Equal("2.3.1"))
			fwCond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
			Expect(fwCond).NotTo(BeNil())
			Expect(fwCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(fwCond.Reason).To(Equal("UpdateCompleted"))

			// Third reconcile: remains stable, does not re-trigger OTA.
			_, err = reconcileGS(reconciler)
			Expect(err).NotTo(HaveOccurred())
			gs = getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseRunning))
			Expect(gs.Status.FirmwareVersion).To(Equal("2.3.1"))
		})
	})

	Context("When firmware update available but outside maintenance window", func() {
		BeforeEach(func() {
			createGS(false, true) // firmware with "02:00-04:00 UTC"
			createNode(true)
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should NOT trigger update", func() {
			reconciler := &GroundStationLifecycleReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(10),
				Now:      func() time.Time { return time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC) }, // 12:00 UTC
			}
			_, err := reconcileGS(reconciler)
			Expect(err).NotTo(HaveOccurred())

			gs := getGS()
			Expect(gs.Status.Phase).To(Equal(ntnv1alpha1.PhaseRunning))
		})
	})

	// --- S9: Conditions + Events ---

	Context("When resource does not exist", func() {
		It("should return without error", func() {
			result, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("When custom healthCheckInterval is set", func() {
		BeforeEach(func() {
			createGS(true, false) // monitoring with 1m interval
			createNode(true)
		})
		AfterEach(func() {
			deleteGS()
			deleteNode()
		})

		It("should requeue after configured interval", func() {
			result, err := reconcileGS(newReconciler(nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(1 * time.Minute))
		})
	})
})
