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
	"fmt"
	"io"
	"net/http"
	"strings"
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
	// Value format: "<namespace>.<name>" to prevent cross-namespace collision.
	// Uses "." as separator since "/" is not valid in K8s label values.
	groundStationLabel = "ntn.operators.dev/groundstation"

	// firmwareVersionAnnotation is the Node annotation for current firmware version.
	firmwareVersionAnnotation = "ntn.operators.dev/firmware-version"

	// availableFirmwareAnnotation is the Node annotation for available firmware version.
	availableFirmwareAnnotation = "ntn.operators.dev/available-firmware-version"
)

// ambiguousNodeError is returned when multiple Nodes match the same ground station label.
type ambiguousNodeError struct {
	count  int
	gsName string
}

func (e *ambiguousNodeError) Error() string {
	return fmt.Sprintf("ambiguous node mapping: %d nodes have label %s=%s",
		e.count, groundStationLabel, e.gsName)
}

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
	node, err := r.findMatchingNode(ctx, gs.Namespace, gs.Name)
	if err != nil {
		log.Error(err, "Failed to find matching node")
	}

	// Step 3: Evaluate health and determine phase.
	r.reconcileHealth(ctx, gs, node, err)

	// Step 4: Check firmware OTA (skip when node lookup failed to avoid
	// clearing firmware status on transient API errors).
	if err == nil {
		r.reconcileFirmware(ctx, gs, node)
	}

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

// findMatchingNode finds a Node labeled ntn.operators.dev/groundstation=<namespace>.<name>.
func (r *GroundStationLifecycleReconciler) findMatchingNode(ctx context.Context, namespace, gsName string) (*corev1.Node, error) {
	labelValue := namespace + "." + gsName
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabels{groundStationLabel: labelValue}); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	switch len(nodeList.Items) {
	case 0:
		return nil, nil
	case 1:
		return &nodeList.Items[0], nil
	default:
		return nil, &ambiguousNodeError{count: len(nodeList.Items), gsName: namespace + "." + gsName}
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
		reason := "APIError"
		var ambErr *ambiguousNodeError
		if errors.As(nodeErr, &ambErr) {
			reason = "AmbiguousNodeMapping"
		}
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionK8sNodeReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            nodeErr.Error(),
			ObservedGeneration: gs.Generation,
		})
		// Set node-dependent conditions to Unknown (preserve diagnostic history).
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type: ntnv1alpha1.ConditionAntennaReady, Status: metav1.ConditionUnknown,
			Reason: "NodeAPIError", Message: "Cannot determine antenna status: " + nodeErr.Error(),
			ObservedGeneration: gs.Generation,
		})
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type: ntnv1alpha1.ConditionRFLinkHealthy, Status: metav1.ConditionUnknown,
			Reason: "NodeAPIError", Message: "Cannot determine RF link status: " + nodeErr.Error(),
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
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type: ntnv1alpha1.ConditionAntennaReady, Status: metav1.ConditionUnknown,
			Reason: "NodeNotFound", Message: "Node not found, antenna status unknown",
			ObservedGeneration: gs.Generation,
		})
		meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
			Type: ntnv1alpha1.ConditionRFLinkHealthy, Status: metav1.ConditionUnknown,
			Reason: "NodeNotFound", Message: "Node not found, RF link status unknown",
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

	// Determine phase. Updating is preserved over Degraded so firmware
	// completion can proceed even under transient resource pressure.
	if !nodeReady {
		gs.Status.Phase = ntnv1alpha1.PhaseOffline
	} else if gs.Status.Phase == ntnv1alpha1.PhaseUpdating {
		// Preserve Updating phase during firmware update.
	} else if memPressure || diskPressure || pidPressure {
		gs.Status.Phase = ntnv1alpha1.PhaseDegraded
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
	} else {
		// Endpoint not configured; remove stale RFLinkHealthy condition.
		meta.RemoveStatusCondition(&gs.Status.Conditions, ntnv1alpha1.ConditionRFLinkHealthy)
	}
}

// reconcileFirmware checks if OTA update should proceed.
func (r *GroundStationLifecycleReconciler) reconcileFirmware(
	_ context.Context,
	gs *ntnv1alpha1.GroundStationLifecycle,
	node *corev1.Node,
) {
	if gs.Spec.Firmware == nil {
		meta.RemoveStatusCondition(&gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
		return
	}
	if node == nil {
		meta.RemoveStatusCondition(&gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
		gs.Status.FirmwareVersion = ""
		return
	}

	// Sync firmware version from node annotation on first reconcile or
	// when current status version doesn't match either known version
	// (indicating an external agent updated the firmware outside our control).
	if v, ok := node.Annotations[firmwareVersionAnnotation]; ok && gs.Status.FirmwareVersion == "" {
		gs.Status.FirmwareVersion = v
	}

	availableVersion := node.Annotations[availableFirmwareAnnotation]
	currentVersion := gs.Status.FirmwareVersion
	upToDate := availableVersion == "" || currentVersion == availableVersion

	var fwStatus metav1.ConditionStatus
	var fwReason, fwMsg string
	switch {
	case currentVersion == "":
		fwStatus = metav1.ConditionUnknown
		fwReason = "VersionUnknown"
		fwMsg = "Current firmware version not reported by node"
	case !upToDate:
		fwStatus = metav1.ConditionFalse
		fwReason = "UpdateAvailable"
		fwMsg = fmt.Sprintf("Update available: %s → %s", currentVersion, availableVersion)
	default:
		fwStatus = metav1.ConditionTrue
		fwReason = "UpToDate"
		fwMsg = fmt.Sprintf("Firmware %s is current", currentVersion)
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
		if availableVersion == "" {
			// Available version disappeared during update; keep current version.
			meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionFirmwareUpToDate,
				Status:             metav1.ConditionUnknown,
				Reason:             "UpdateInterrupted",
				Message:            "Available firmware version annotation removed during update",
				ObservedGeneration: gs.Generation,
			})
			gs.Status.Phase = ntnv1alpha1.PhaseRunning
			return
		}
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

	// Trigger update if conditions met. Only start OTA when node is healthy.
	if !upToDate && gs.Spec.Firmware.AutoUpdate &&
		(gs.Status.Phase == ntnv1alpha1.PhaseRunning || gs.Status.Phase == ntnv1alpha1.PhaseDegraded) {
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
// Returns true when HTTPClient is nil (skip check, assume healthy).
// SSRF protection: HTTPClient should be created via netutil.NewSafeHTTPClient
// which validates resolved IPs at TCP dial level. Scheme check is defense in depth.
func (r *GroundStationLifecycleReconciler) checkHTTPEndpoint(ctx context.Context, endpoint string) bool {
	if r.HTTPClient == nil {
		return true // no client configured, skip check
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
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
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
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
	labelValue, found := node.Labels[groundStationLabel]
	if !found {
		return nil
	}
	// Label format: "<namespace>.<name>". Parse directly.
	parts := strings.SplitN(labelValue, ".", 2)
	if len(parts) != 2 {
		return nil // invalid label format
	}
	gsNamespace, gsName := parts[0], parts[1]

	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{Namespace: gsNamespace, Name: gsName},
	}}
}
