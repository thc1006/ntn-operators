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
	"math"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/prometheus/client_golang/prometheus"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/slice"
)

const sliceRequeueInterval = 30 * time.Second

// NTNSliceReconciler reconciles a NTNSlice object
type NTNSliceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Now      func() time.Time
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntnslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntnslices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntnslices/finalizers,verbs=update
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile evaluates failover policy and manages path switching.
func (r *NTNSliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("reconciling")
	reconcileStart := time.Now()
	defer func() {
		log.V(1).Info("reconcile complete", "duration", time.Since(reconcileStart))
	}()

	// Step 1: Get the NTNSlice resource.
	ns := &ntnv1alpha1.NTNSlice{}
	if err := r.Get(ctx, req.NamespacedName, ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()

	// Step 2: Read simulated metrics from annotations.
	metrics := r.readMetrics(ns)

	// Step 3: Check satellite availability via SatelliteEphemeris.
	satelliteAvailable := r.checkSatelliteAvailability(ctx, ns, now)

	// Set FailoverReady condition based on satellite availability.
	failoverReadyStatus := metav1.ConditionTrue
	failoverReadyReason := "SatelliteAvailable"
	failoverReadyMsg := "Satellite pass window active"
	if !satelliteAvailable {
		failoverReadyStatus = metav1.ConditionFalse
		failoverReadyReason = "SatelliteUnavailable"
		failoverReadyMsg = "No satellite pass window active or SatelliteEphemeris not found"
	}
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionFailoverReady,
		Status:             failoverReadyStatus,
		Reason:             failoverReadyReason,
		Message:            failoverReadyMsg,
		ObservedGeneration: ns.Generation,
	})

	// Step 4: Evaluate failover decision.
	currentPath := slice.PathType(ns.Status.ActivePathType)
	if currentPath == "" {
		currentPath = slice.PathTerrestrial // default
	}

	var lastFailover time.Time
	if ns.Status.LastFailover != nil {
		lastFailover = ns.Status.LastFailover.Time
	}

	result := slice.EvaluateFailover(
		currentPath,
		ns.Spec.FailoverPolicy.Triggers,
		metrics,
		satelliteAvailable,
		ns.Spec.FailoverPolicy.SwitchbackDelay.Duration,
		lastFailover,
		now,
	)

	// Step 5: Apply decision.
	previousPath := string(currentPath)
	ns.Status.ActivePathType = string(result.TargetPath)

	// Update satellite availability metric.
	if satelliteAvailable {
		ntnmetrics.SatellitePassAvailable.With(prometheus.Labels{"ephemeris": ns.Spec.SatellitePath.EphemerisRef}).Set(1)
	} else {
		ntnmetrics.SatellitePassAvailable.With(prometheus.Labels{"ephemeris": ns.Spec.SatellitePath.EphemerisRef}).Set(0)
	}

	switch result.Decision {
	case slice.DecisionFailover:
		ns.Status.FailoverCount++
		ns.Status.LastFailover = &metav1.Time{Time: now}
		ntnmetrics.FailoverTotal.With(prometheus.Labels{
			"slice": ns.Name, "from_path": previousPath, "to_path": string(result.TargetPath),
		}).Inc()
		log.Info("Failover triggered", "from", previousPath, "to", result.TargetPath, "reason", result.Reason)
		if r.Recorder != nil {
			r.Recorder.Eventf(ns, nil, "Warning", "FailoverTriggered", "FailoverTriggered",
				"Failover from %s to %s: %s", previousPath, result.TargetPath, result.Reason)
		}
	case slice.DecisionSwitchback:
		log.Info("Switchback", "from", previousPath, "to", result.TargetPath, "reason", result.Reason)
		if r.Recorder != nil {
			r.Recorder.Eventf(ns, nil, "Normal", "Switchback", "Switchback",
				"Switched back from %s to %s: %s", previousPath, result.TargetPath, result.Reason)
		}
	case slice.DecisionStay:
		// No action needed.
	}

	// Step 6: Set PathActive condition.
	// Map decision to CamelCase reason for K8s API convention.
	reasonMap := map[slice.Decision]string{
		slice.DecisionStay:       "Stay",
		slice.DecisionFailover:   "Failover",
		slice.DecisionSwitchback: "Switchback",
	}
	pathReason := reasonMap[result.Decision]

	pathStatus := metav1.ConditionTrue
	if result.TargetPath == slice.PathUnavailable {
		pathStatus = metav1.ConditionFalse
		pathReason = "Unavailable"
	}

	pathMsg := fmt.Sprintf("Active on %s path: %s", ns.Status.ActivePathType, result.Reason)
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionPathActive,
		Status:             pathStatus,
		Reason:             pathReason,
		Message:            pathMsg,
		ObservedGeneration: ns.Generation,
	})

	// Step 7: Apply QoS, Security, and Billing status based on active path.
	activePath := ns.Status.ActivePathType
	r.applyQoSStatus(ns, activePath)
	r.applySecurityStatus(ns)
	r.applyBillingStatus(ns, activePath)

	// Step 8: Update status.
	if err := r.Status().Update(ctx, ns); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: sliceRequeueInterval}, nil
}

// applyQoSStatus sets the QoS-related status fields and conditions.
func (r *NTNSliceReconciler) applyQoSStatus(ns *ntnv1alpha1.NTNSlice, activePath string) {
	if ns.Spec.QoSMapping == nil {
		ns.Status.AppliedQoS = ""
		meta.RemoveStatusCondition(&ns.Status.Conditions, ntnv1alpha1.ConditionQoSApplied)
		return
	}
	qos := ns.Spec.QoSMapping
	switch activePath {
	case string(slice.PathSatellite):
		ns.Status.AppliedQoS = fmt.Sprintf("QCI=%s, maxLatency=%s", qos.SatelliteQCI, qos.MaxLatencyBudget.Duration)
	default:
		ns.Status.AppliedQoS = fmt.Sprintf("5QI=%d, maxLatency=%s", qos.Terrestrial5QI, qos.MaxLatencyBudget.Duration)
	}
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionQoSApplied,
		Status:             metav1.ConditionTrue,
		Reason:             "QoSConfigured",
		Message:            ns.Status.AppliedQoS,
		ObservedGeneration: ns.Generation,
	})
}

// applySecurityStatus sets the security-related status fields and conditions.
func (r *NTNSliceReconciler) applySecurityStatus(ns *ntnv1alpha1.NTNSlice) {
	if ns.Spec.Security == nil {
		ns.Status.AppliedEncryption = ""
		meta.RemoveStatusCondition(&ns.Status.Conditions, ntnv1alpha1.ConditionSecured)
		return
	}
	ns.Status.AppliedEncryption = ns.Spec.Security.EncryptionLevel
	msg := fmt.Sprintf("Encryption: %s, handover auth: %s",
		ns.Spec.Security.EncryptionLevel, ns.Spec.Security.AuthOnHandover)
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionSecured,
		Status:             metav1.ConditionTrue,
		Reason:             "SecurityConfigured",
		Message:            msg,
		ObservedGeneration: ns.Generation,
	})
}

// applyBillingStatus sets the billing-related status fields and conditions.
func (r *NTNSliceReconciler) applyBillingStatus(ns *ntnv1alpha1.NTNSlice, activePath string) {
	if ns.Spec.Billing == nil {
		ns.Status.BillingMode = ""
		meta.RemoveStatusCondition(&ns.Status.Conditions, ntnv1alpha1.ConditionBillingActive)
		return
	}
	switch activePath {
	case string(slice.PathSatellite):
		ns.Status.BillingMode = ns.Spec.Billing.SatelliteRate
	default:
		ns.Status.BillingMode = ns.Spec.Billing.TerrestrialRate
	}
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionBillingActive,
		Status:             metav1.ConditionTrue,
		Reason:             "BillingConfigured",
		Message:            fmt.Sprintf("Billing mode: %s (%s path)", ns.Status.BillingMode, activePath),
		ObservedGeneration: ns.Generation,
	})
}

// readMetrics reads simulated metrics from CR annotations.
// In production, these would come from UPF/Prometheus.
func (r *NTNSliceReconciler) readMetrics(ns *ntnv1alpha1.NTNSlice) slice.Metrics {
	m := slice.Metrics{
		RSRP:              -80, // default: healthy
		LatencyMs:         20,
		PacketLossPercent: 0.1,
	}
	if ns.Annotations == nil {
		return m
	}
	if v, ok := ns.Annotations["ntn.operators.dev/simulated-rsrp"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			m.RSRP = f
		}
	}
	if v, ok := ns.Annotations["ntn.operators.dev/simulated-latency"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			m.LatencyMs = f
		}
	}
	if v, ok := ns.Annotations["ntn.operators.dev/simulated-packet-loss"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			m.PacketLossPercent = f
		}
	}
	return m
}

// checkSatelliteAvailability checks if any satellite pass window is currently active.
func (r *NTNSliceReconciler) checkSatelliteAvailability(
	ctx context.Context,
	ns *ntnv1alpha1.NTNSlice,
	now time.Time,
) bool {
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	key := client.ObjectKey{Namespace: ns.Namespace, Name: ns.Spec.SatellitePath.EphemerisRef}
	if err := r.Get(ctx, key, eph); err != nil {
		if !apierrors.IsNotFound(err) {
			log := logf.FromContext(ctx)
			log.Error(err, "Failed to get SatelliteEphemeris", "ref", ns.Spec.SatellitePath.EphemerisRef)
		}
		return false
	}

	for _, pw := range eph.Status.NextPassWindows {
		if !pw.AOS.After(now) && pw.LOS.After(now) {
			return true // currently in a pass window
		}
	}
	return false
}

// now returns the current time (injectable for testing).
func (r *NTNSliceReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// SetupWithManager sets up the controller with the Manager.
// Watches SatelliteEphemeris changes to re-evaluate failover when
// pass windows change.
func (r *NTNSliceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ntnv1alpha1.NTNSlice{}).
		Watches(&ntnv1alpha1.SatelliteEphemeris{},
			handler.EnqueueRequestsFromMapFunc(r.ephemerisToSlice),
		).
		Named("ntnslice").
		Complete(r)
}

// ephemerisToSlice maps a SatelliteEphemeris change to all NTNSlices
// that reference it via spec.satellitePath.ephemerisRef.
func (r *NTNSliceReconciler) ephemerisToSlice(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	eph, ok := obj.(*ntnv1alpha1.SatelliteEphemeris)
	if !ok {
		return nil
	}

	var sliceList ntnv1alpha1.NTNSliceList
	if err := r.List(ctx, &sliceList, client.InNamespace(eph.Namespace)); err != nil {
		log := logf.FromContext(ctx)
		log.Error(err, "Failed to list NTNSlices for ephemeris mapper")
		return nil
	}

	var requests []reconcile.Request
	for _, s := range sliceList.Items {
		if s.Spec.SatellitePath.EphemerisRef == eph.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&s),
			})
		}
	}
	return requests
}
