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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
)

const (
	// groundStationLabel is the Node label used to match a GroundStationLifecycle CR.
	// Value format: "<namespace>.<name>" to prevent cross-namespace collision.
	// Uses "." as separator since "/" is not valid in K8s label values.
	groundStationLabel = "ntn.operators.dev/groundstation"

	// firmwareVersionAnnotation is the Node annotation for current firmware version.
	firmwareVersionAnnotation = "ntn.operators.dev/firmware-version"

	// firmwareUpdateTimeout is the maximum duration an update can be in Updating phase
	// before being marked as timed out and transitioned to Degraded.
	firmwareUpdateTimeout = 30 * time.Minute

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
	log.V(1).Info("reconciling")
	reconcileStart := time.Now()
	defer func() {
		log.V(1).Info("reconcile complete", "duration", time.Since(reconcileStart))
	}()

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

	// Step 6: Update health metrics.
	stationKey := gs.Namespace + "." + gs.Name
	for _, condType := range []string{
		ntnv1alpha1.ConditionK8sNodeReady,
		ntnv1alpha1.ConditionAntennaReady,
		ntnv1alpha1.ConditionRFLinkHealthy,
	} {
		cond := meta.FindStatusCondition(gs.Status.Conditions, condType)
		val := float64(-1) // Unknown
		if cond != nil {
			switch cond.Status {
			case metav1.ConditionTrue:
				val = 1
			case metav1.ConditionFalse:
				val = 0
			}
		}
		ntnmetrics.GroundStationHealth.With(prometheus.Labels{
			"station": stationKey, "condition": condType,
		}).Set(val)
	}

	// Step 7: Update status.
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

// maxLabelValueLen is the Kubernetes label value length limit.
const maxLabelValueLen = 63

// groundStationLabelValue returns the label value for a ground station.
// If namespace.name exceeds the 63-char K8s label limit, the gsName part
// is truncated with a hash suffix while preserving "namespace." prefix.
// This ensures nodeToGroundStation can always parse the namespace back.
func groundStationLabelValue(namespace, gsName string) string {
	labelValue := namespace + "." + gsName
	if len(labelValue) <= maxLabelValueLen {
		return labelValue
	}
	// Always preserve a dot separator so nodeToGroundStation can parse
	// the namespace. If namespace is too long, truncate it too.
	h := sha256.Sum256([]byte(labelValue))
	suffix := hex.EncodeToString(h[:4]) // 8 hex chars

	prefix := namespace + "."
	remaining := maxLabelValueLen - len(prefix) - 9 // room for "-" + 8-char hash
	if remaining < 1 {
		// Namespace too long — truncate namespace to fit while keeping ".".
		nsMax := min(maxLabelValueLen-1-8, len(namespace)) // 63 - "." - suffix = 54, clamped
		truncNs := strings.TrimRight(namespace[:nsMax], "-.")
		return truncNs + "." + suffix
	}
	truncName := strings.TrimRight(gsName[:remaining], "-.")
	if truncName == "" {
		// gsName was entirely "-." chars — use hash only.
		return prefix + suffix
	}
	return prefix + truncName + "-" + suffix
}

// findMatchingNode finds a Node labeled ntn.operators.dev/groundstation=<namespace>.<name>.
// For long names, the label value is truncated+hashed via groundStationLabelValue.
func (r *GroundStationLifecycleReconciler) findMatchingNode(ctx context.Context, namespace, gsName string) (*corev1.Node, error) {
	log := logf.FromContext(ctx)
	labelValue := groundStationLabelValue(namespace, gsName)
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabels{groundStationLabel: labelValue}); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	log.V(2).Info("node lookup", "label", labelValue, "matchCount", len(nodeList.Items))
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
	log := logf.FromContext(ctx)
	now := r.now()
	log.V(1).Info("evaluating health", "hasNode", node != nil, "hasNodeErr", nodeErr != nil)

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
			Message:            fmt.Sprintf("No node with label %s=%s found", groundStationLabel, groundStationLabelValue(gs.Namespace, gs.Name)),
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

	// Determine phase. Updating and firmware-uncertain Degraded are preserved
	// so firmware lifecycle can proceed even under transient resource pressure.
	fwCond := meta.FindStatusCondition(gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
	firmwareUncertain := fwCond != nil && (fwCond.Reason == "UpdateInterrupted" || fwCond.Reason == "UpdateTimedOut")
	if !nodeReady {
		gs.Status.Phase = ntnv1alpha1.PhaseOffline
	} else if gs.Status.Phase == ntnv1alpha1.PhaseUpdating {
		// Preserve Updating phase during firmware update.
	} else if gs.Status.Phase == ntnv1alpha1.PhaseDegraded && firmwareUncertain {
		// Preserve Degraded when firmware state is uncertain.
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
	ctx context.Context,
	gs *ntnv1alpha1.GroundStationLifecycle,
	node *corev1.Node,
) {
	log := logf.FromContext(ctx)
	if gs.Spec.Firmware == nil {
		meta.RemoveStatusCondition(&gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
		return
	}
	if node == nil {
		meta.RemoveStatusCondition(&gs.Status.Conditions, ntnv1alpha1.ConditionFirmwareUpToDate)
		gs.Status.FirmwareVersion = ""
		return
	}

	// Always sync firmware version from node annotation so that external
	// agent changes (outside of our OTA cycle) are reflected in status.
	if v, ok := node.Annotations[firmwareVersionAnnotation]; ok {
		gs.Status.FirmwareVersion = v
	}

	availableVersion := node.Annotations[availableFirmwareAnnotation]
	currentVersion := gs.Status.FirmwareVersion
	upToDate := availableVersion == "" || currentVersion == availableVersion
	log.V(1).Info("firmware check", "current", currentVersion, "available", availableVersion, "upToDate", upToDate)

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

	// Handle update timeout (Updating → Degraded if stuck > 30 min).
	if gs.Status.Phase == ntnv1alpha1.PhaseUpdating && gs.Status.FirmwareUpdateStarted != nil {
		startedAt := gs.Status.FirmwareUpdateStarted.Time
		if r.now().Sub(startedAt) > firmwareUpdateTimeout {
			meta.SetStatusCondition(&gs.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionFirmwareUpToDate,
				Status:             metav1.ConditionFalse,
				Reason:             "UpdateTimedOut",
				Message:            "Firmware update exceeded 30-minute timeout",
				ObservedGeneration: gs.Generation,
			})
			gs.Status.Phase = ntnv1alpha1.PhaseDegraded
			gs.Status.FirmwareUpdateStarted = nil
			if r.Recorder != nil {
				r.Recorder.Eventf(gs, nil, "Warning", "FirmwareUpdateTimedOut", "FirmwareUpdateTimedOut",
					"Firmware update started at %s timed out after 30m", startedAt)
			}
			return
		}
	}

	// Handle update completion (Updating → Running).
	// Completion is determined by the node agent updating the firmware-version
	// annotation to match the available version.
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
			gs.Status.Phase = ntnv1alpha1.PhaseDegraded
			gs.Status.FirmwareUpdateStarted = nil
			return
		}
		// Check if the node agent reports the firmware has been updated.
		nodeReportedVersion := node.Annotations[firmwareVersionAnnotation]
		if nodeReportedVersion == availableVersion {
			// Node agent confirms update is complete.
			gs.Status.FirmwareVersion = availableVersion
			gs.Status.FirmwareUpdateStarted = nil
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
		// Node agent hasn't reported completion yet — stay in Updating.
		return
	}

	// Trigger update if conditions met. Only start OTA when node is healthy.
	if !upToDate && gs.Spec.Firmware.AutoUpdate &&
		(gs.Status.Phase == ntnv1alpha1.PhaseRunning || gs.Status.Phase == ntnv1alpha1.PhaseDegraded) {
		if gs.Spec.Firmware.MaintenanceWindow != "" {
			inWindow, err := lifecycle.IsWithinMaintenanceWindowWithContext(ctx, gs.Spec.Firmware.MaintenanceWindow, r.now())
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
		now := r.now()
		gs.Status.Phase = ntnv1alpha1.PhaseUpdating
		gs.Status.FirmwareUpdateStarted = &metav1.Time{Time: now}
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
	log := logf.FromContext(ctx)
	if r.HTTPClient == nil {
		return true // no client configured, skip check
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		// Log without err details — the error may contain the full URL with credentials.
		log.V(1).Info("invalid endpoint URL")
		return false
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		log.V(1).Info("unsupported URL scheme", "scheme", req.URL.Scheme)
		return false
	}
	// Log only scheme+host to avoid leaking credentials/tokens in URL path or query.
	safeEndpoint := req.URL.Scheme + "://" + req.URL.Host
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		// Don't log err directly — net/http errors embed the full URL which may contain credentials.
		log.V(2).Info("health check failed", "endpoint", safeEndpoint)
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	healthy := resp.StatusCode >= 200 && resp.StatusCode < 300
	log.V(2).Info("health check result", "endpoint", safeEndpoint, "status", resp.StatusCode, "healthy", healthy)
	return healthy
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
// For short names, the label is "<namespace>.<name>" and can be parsed directly.
// For long names (hashed labels), the mapper lists GS CRs in the parsed namespace
// and matches by recomputing groundStationLabelValue.
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
	parts := strings.SplitN(labelValue, ".", 2)
	if len(parts) != 2 {
		return nil // invalid label format (or hash-only fallback)
	}
	gsNamespace, gsName := parts[0], parts[1]

	// Fast path: try direct Get with the parsed name.
	key := client.ObjectKey{Namespace: gsNamespace, Name: gsName}
	var gs ntnv1alpha1.GroundStationLifecycle
	if err := r.Get(ctx, key, &gs); err == nil {
		// Verify the label matches to avoid false hits (e.g., a real GS
		// name that coincides with a truncated+hashed value).
		if groundStationLabelValue(gs.Namespace, gs.Name) == labelValue {
			return []ctrl.Request{{NamespacedName: key}}
		}
	} else if !apierrors.IsNotFound(err) {
		// Transient error — still enqueue parsed key so the event isn't
		// lost (mapper funcs aren't retried; reconcile will handle it).
		log := logf.FromContext(ctx)
		log.Error(err, "transient error in node mapper Get, enqueuing best-guess", "key", key)
		return []ctrl.Request{{NamespacedName: key}}
	}

	// Slow path: NotFound — label is likely hashed.
	// List GS CRs in parsed namespace and match by recomputing label value.
	log := logf.FromContext(ctx)
	var gsList ntnv1alpha1.GroundStationLifecycleList
	if err := r.List(ctx, &gsList, client.InNamespace(gsNamespace)); err != nil {
		log.Error(err, "failed to list GroundStations for node mapper, enqueuing best-guess")
		return []ctrl.Request{{NamespacedName: key}}
	}
	var requests []ctrl.Request
	for _, gs := range gsList.Items {
		if groundStationLabelValue(gs.Namespace, gs.Name) == labelValue {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gs.Namespace, Name: gs.Name,
				},
			})
		}
	}
	// If namespace was truncated (label near max length with hash suffix),
	// the parsed namespace won't match any real namespace.
	// Only do expensive cluster-wide search for labels that look truncated.
	looksLikeTruncatedNs := len(requests) == 0 && len(labelValue) >= maxLabelValueLen-2
	if looksLikeTruncatedNs {
		var allGS ntnv1alpha1.GroundStationLifecycleList
		if err := r.List(ctx, &allGS); err != nil {
			log.Error(err, "cluster-wide GS list failed, enqueuing best-guess")
			return []ctrl.Request{{NamespacedName: key}}
		}
		for _, gs := range allGS.Items {
			if groundStationLabelValue(gs.Namespace, gs.Name) == labelValue {
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Namespace: gs.Namespace, Name: gs.Name,
					},
				})
			}
		}
	}
	return requests
}
