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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/lifecycle"
)

const (
	// groundStationLabel is the Node label used to match a GroundStationLifecycle CR.
	groundStationLabel = "ntn.operators.dev/groundstation"

	// firmwareVersionAnnotation is the Node annotation for current firmware version.
	firmwareVersionAnnotation = "ntn.operators.dev/firmware-version"

	// availableFirmwareAnnotation is the Node annotation for available firmware version.
	availableFirmwareAnnotation = "ntn.operators.dev/available-firmware-version"
)

// GroundStationLifecycleReconciler reconciles a GroundStationLifecycle object
type GroundStationLifecycleReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   events.EventRecorder
	HTTPClient *http.Client
	Now        func() time.Time // injectable clock; nil defaults to time.Now
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=groundstationlifecycles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=groundstationlifecycles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=groundstationlifecycles/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile checks ground station health, manages firmware OTA, and records phase transitions.
func (r *GroundStationLifecycleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: Fetch the CR.
	gs := &ntnv1alpha1.GroundStationLifecycle{}
	if err := r.Get(ctx, req.NamespacedName, gs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	previousPhase := gs.Status.Phase
	requeueAfter := r.healthCheckInterval(gs)

	// Step 2: Find matching Node.
	node, err := r.findMatchingNode(ctx, gs.Name)
	if err != nil {
		log.Error(err, "Failed to find matching node")
	}

	// Step 3: Evaluate health and determine phase.
	r.reconcileHealth(ctx, gs, node, err)

	// Step 4: Check firmware OTA.
	r.reconcileFirmware(ctx, gs, node)

	// Step 5: Record events for phase transitions.
	if previousPhase != gs.Status.Phase {
		eventType := "Normal"
		if gs.Status.Phase == ntnv1alpha1.PhaseOffline || gs.Status.Phase == ntnv1alpha1.PhaseDegraded {
			eventType = "Warning"
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(gs, nil, eventType, "PhaseChanged", "PhaseChanged",
				"Phase transitioned from %s to %s", previousPhase, gs.Status.Phase)
		}
	}

	// Step 6: Update status.
	if err := r.Status().Update(ctx, gs); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Reconciled ground station", "phase", gs.Status.Phase)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// healthCheckInterval returns the configured interval or 30s default.
func (r *GroundStationLifecycleReconciler) healthCheckInterval(gs *ntnv1alpha1.GroundStationLifecycle) time.Duration {
	if gs.Spec.Monitoring != nil && gs.Spec.Monitoring.HealthCheckInterval.Duration > 0 {
		return gs.Spec.Monitoring.HealthCheckInterval.Duration
	}
	return 30 * time.Second
}

// findMatchingNode finds a Node labeled ntn.operators.dev/groundstation=<name>.
func (r *GroundStationLifecycleReconciler) findMatchingNode(ctx context.Context, gsName string) (*corev1.Node, error) {
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabels{groundStationLabel: gsName}); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	switch len(nodeList.Items) {
	case 0:
		return nil, nil
	case 1:
		return &nodeList.Items[0], nil
	default:
		return nil, fmt.Errorf("ambiguous node mapping: %d nodes have label %s=%s",
			len(nodeList.Items), groundStationLabel, gsName)
	}
}

// reconcileHealth evaluates node conditions and sets phase + conditions on the GS status.
func (r *GroundStationLifecycleReconciler) reconcileHealth(
	ctx context.Context,
	gs *ntnv1alpha1.GroundStationLifecycle,
	node *corev1.Node,
	nodeErr error,
) {
	now := r.now()

	if nodeErr != nil {
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionK8sNodeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "APIError",
			Message:            nodeErr.Error(),
			ObservedGeneration: gs.Generation,
		})
		gs.Status.Phase = ntnv1alpha1.PhaseOffline
		return
	}

	if node == nil {
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionK8sNodeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "NodeNotFound",
			Message:            fmt.Sprintf("No node with label %s=%s found", groundStationLabel, gs.Name),
			ObservedGeneration: gs.Generation,
		})
		if gs.Status.Phase == "" || gs.Status.Phase == ntnv1alpha1.PhaseProvisioning {
			gs.Status.Phase = ntnv1alpha1.PhaseProvisioning
		} else {
			gs.Status.Phase = ntnv1alpha1.PhaseOffline
		}
		return
	}

	// Node exists — evaluate conditions.
	gs.Status.K8sVersion = node.Status.NodeInfo.KubeletVersion

	nodeReady := isNodeConditionTrue(node, corev1.NodeReady)
	memPressure := isNodeConditionTrue(node, corev1.NodeMemoryPressure)
	diskPressure := isNodeConditionTrue(node, corev1.NodeDiskPressure)
	pidPressure := isNodeConditionTrue(node, corev1.NodePIDPressure)

	// Set K8sNodeReady condition.
	k8sReadyStatus := metav1.ConditionTrue
	k8sReadyReason := "NodeReady"
	k8sReadyMsg := "K8s node is ready"
	if !nodeReady {
		k8sReadyStatus = metav1.ConditionFalse
		k8sReadyReason = "NodeNotReady"
		k8sReadyMsg = "K8s node is not ready"
	}
	meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionK8sNodeReady,
		Status:             k8sReadyStatus,
		Reason:             k8sReadyReason,
		Message:            k8sReadyMsg,
		ObservedGeneration: gs.Generation,
	})

	// AntennaReady: simulated as True when node exists.
	meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionAntennaReady,
		Status:             metav1.ConditionTrue,
		Reason:             "AntennaOperational",
		Message:            "Antenna system operational",
		ObservedGeneration: gs.Generation,
	})

	// Determine phase.
	if !nodeReady {
		gs.Status.Phase = ntnv1alpha1.PhaseOffline
	} else if memPressure || diskPressure || pidPressure {
		gs.Status.Phase = ntnv1alpha1.PhaseDegraded
	} else if gs.Status.Phase == ntnv1alpha1.PhaseUpdating {
		// Preserve Updating phase during firmware update.
	} else {
		gs.Status.Phase = ntnv1alpha1.PhaseRunning
	}

	// Only set lastHealthCheck when the overall health is successful.
	if gs.Status.Phase == ntnv1alpha1.PhaseRunning || gs.Status.Phase == ntnv1alpha1.PhaseUpdating {
		gs.Status.LastHealthCheck = &metav1.Time{Time: now}
	}

	// Optional HTTP health endpoint.
	if gs.Spec.Monitoring != nil && gs.Spec.Monitoring.Endpoint != "" {
		healthy := r.checkHTTPEndpoint(ctx, gs.Spec.Monitoring.Endpoint)
		rfStatus := metav1.ConditionTrue
		rfReason := "EndpointHealthy"
		rfMsg := "Monitoring endpoint returned 2xx"
		if !healthy {
			rfStatus = metav1.ConditionFalse
			rfReason = "EndpointUnhealthy"
			rfMsg = "Monitoring endpoint check failed"
			if gs.Status.Phase == ntnv1alpha1.PhaseRunning {
				gs.Status.Phase = ntnv1alpha1.PhaseDegraded
			}
		}
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionRFLinkHealthy,
			Status:             rfStatus,
			Reason:             rfReason,
			Message:            rfMsg,
			ObservedGeneration: gs.Generation,
		})
	}
}

// reconcileFirmware checks if OTA update should proceed.
func (r *GroundStationLifecycleReconciler) reconcileFirmware(
	_ context.Context,
	gs *ntnv1alpha1.GroundStationLifecycle,
	node *corev1.Node,
) {
	if gs.Spec.Firmware == nil || node == nil {
		return
	}

	// Sync firmware version from node annotation each reconcile (unless Updating).
	if gs.Status.Phase != ntnv1alpha1.PhaseUpdating {
		if v, ok := node.Annotations[firmwareVersionAnnotation]; ok {
			gs.Status.FirmwareVersion = v
		}
	}

	availableVersion := node.Annotations[availableFirmwareAnnotation]
	currentVersion := gs.Status.FirmwareVersion
	upToDate := availableVersion == "" || currentVersion == availableVersion

	fwStatus := metav1.ConditionTrue
	fwReason := "UpToDate"
	fwMsg := fmt.Sprintf("Firmware %s is current", currentVersion)
	if !upToDate {
		fwStatus = metav1.ConditionFalse
		fwReason = "UpdateAvailable"
		fwMsg = fmt.Sprintf("Update available: %s → %s", currentVersion, availableVersion)
	}
	meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionFirmwareUpToDate,
		Status:             fwStatus,
		Reason:             fwReason,
		Message:            fwMsg,
		ObservedGeneration: gs.Generation,
	})

	// Handle update completion (Updating → Running).
	if gs.Status.Phase == ntnv1alpha1.PhaseUpdating {
		gs.Status.FirmwareVersion = availableVersion
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionFirmwareUpToDate,
			Status:             metav1.ConditionTrue,
			Reason:             "UpdateCompleted",
			Message:            fmt.Sprintf("Firmware updated to %s", availableVersion),
			ObservedGeneration: gs.Generation,
		})
		gs.Status.Phase = ntnv1alpha1.PhaseRunning
		if r.Recorder != nil {
			r.Recorder.Eventf(gs, nil, "Normal", "FirmwareUpdated", "FirmwareUpdated",
				"Firmware updated to %s", availableVersion)
		}
		return
	}

	// Trigger update if conditions met.
	if !upToDate && gs.Spec.Firmware.AutoUpdate {
		if gs.Spec.Firmware.MaintenanceWindow != "" {
			inWindow, err := lifecycle.IsWithinMaintenanceWindow(gs.Spec.Firmware.MaintenanceWindow, r.now())
			if err != nil {
				meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
					Type:               ntnv1alpha1.ConditionFirmwareUpToDate,
					Status:             metav1.ConditionFalse,
					Reason:             "InvalidMaintenanceWindow",
					Message:            fmt.Sprintf("Cannot parse maintenance window %q: %v", gs.Spec.Firmware.MaintenanceWindow, err),
					ObservedGeneration: gs.Generation,
				})
				return
			}
			if !inWindow {
				return // outside maintenance window
			}
		}
		gs.Status.Phase = ntnv1alpha1.PhaseUpdating
		if r.Recorder != nil {
			r.Recorder.Eventf(gs, nil, "Normal", "FirmwareUpdateStarted", "FirmwareUpdateStarted",
				"Starting firmware update from %s to %s", currentVersion, availableVersion)
		}
	}
}

// checkHTTPEndpoint performs an HTTP GET and returns true if 2xx.
// Only http:// and https:// schemes are allowed to mitigate SSRF risk.
func (r *GroundStationLifecycleReconciler) checkHTTPEndpoint(ctx context.Context, endpoint string) bool {
	if r.HTTPClient == nil {
		return false // no client configured, cannot verify health
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return false
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// now returns the current time, using r.Now if set, else time.Now().
func (r *GroundStationLifecycleReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// isNodeConditionTrue checks if a specific Node condition is True.
func isNodeConditionTrue(node *corev1.Node, condType corev1.NodeConditionType) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == condType {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
// Watches Node changes to trigger re-reconciliation when the underlying
// edge node's status changes.
func (r *GroundStationLifecycleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ntnv1alpha1.GroundStationLifecycle{}).
		Watches(&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToGroundStation),
		).
		Named("groundstationlifecycle").
		Complete(r)
}

// nodeToGroundStation maps a Node change to the GroundStationLifecycle it belongs to.
func (r *GroundStationLifecycleReconciler) nodeToGroundStation(
	ctx context.Context, obj client.Object,
) []ctrl.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}
	gsName, found := node.Labels[groundStationLabel]
	if !found {
		return nil
	}
	// GroundStationLifecycle is namespaced; list all and match by name.
	var gsList ntnv1alpha1.GroundStationLifecycleList
	if err := r.List(ctx, &gsList); err != nil {
		log := logf.FromContext(ctx)
		log.Error(err, "Failed to list GroundStationLifecycle for node mapper")
		return nil
	}
	var requests []ctrl.Request
	for _, gs := range gsList.Items {
		if gs.Name == gsName {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(&gs),
			})
		}
	}
	return requests
}
