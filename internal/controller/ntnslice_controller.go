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
	"math"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlrt "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/prometheus/client_golang/prometheus"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

const sliceRequeueInterval = 30 * time.Second

// NTNSliceReconciler reconciles a NTNSlice object
type NTNSliceReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                events.EventRecorder
	MaxConcurrentReconciles int
	Now                     func() time.Time

	// ReaderProvider chooses the metrics Reader for each NTNSlice based on
	// spec.metricsSource. If nil, the reconciler lazily builds a default
	// Provider on first use so existing development tests that construct
	// the reconciler directly continue to work without rebuilding pool +
	// provider on every Reconcile call.
	ReaderProvider *slicemetrics.Provider

	defaultProviderOnce sync.Once
	defaultProvider     *slicemetrics.Provider
}

// readerProvider returns the configured Provider, or a lazily-initialised
// default one so the reconciler never churns a fresh pool per Reconcile.
// Safe for concurrent callers via sync.Once.
func (r *NTNSliceReconciler) readerProvider() *slicemetrics.Provider {
	if r.ReaderProvider != nil {
		return r.ReaderProvider
	}
	r.defaultProviderOnce.Do(func() {
		r.defaultProvider = slicemetrics.NewProvider(slicemetrics.NewClientPool())
	})
	return r.defaultProvider
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
		if apierrors.IsNotFound(err) && r.ReaderProvider != nil {
			// The CR is gone — drop any cached reader so the Provider
			// does not leak staleCache state for slices that no longer
			// exist. Safe to call even if no entry was present.
			r.ReaderProvider.Evict(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()

	// Step 2: Read path quality metrics via the configured source.
	reader, err := r.readerProvider().For(ns)
	if err != nil {
		log.Error(err, "failed to build metrics reader")
		return r.setMetricsUnknown(ctx, ns, "MetricsReaderError", err.Error())
	}
	readResult, err := reader.Read(ctx, ns)
	if err != nil {
		reason := "MetricsUnavailable"
		if !errors.Is(err, slicemetrics.ErrNoMetrics) {
			reason = "MetricsReaderError"
		}
		log.Info("metrics unavailable; holding current path", "reason", reason, "err", err.Error())
		return r.setMetricsUnknown(ctx, ns, reason, err.Error())
	}
	metrics := readResult.Metrics
	// Track stale-ness as a Condition and emit an Event only on the
	// transition into stale. A prolonged outage therefore produces one
	// event, not one per reconcile interval, while dashboards and
	// admission tooling can still observe the current state via the
	// MetricsStale condition directly.
	prevStale := meta.FindStatusCondition(ns.Status.Conditions, ntnv1alpha1.ConditionMetricsStale)
	if readResult.Stale {
		log.Info("metrics source returned stale value", "lastFreshAt", readResult.LastFreshAt)
		meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionMetricsStale,
			Status:             metav1.ConditionTrue,
			Reason:             "StaleValue",
			Message:            fmt.Sprintf("Using stale metrics last observed at %s", readResult.LastFreshAt.Format(time.RFC3339)),
			ObservedGeneration: ns.Generation,
		})
		transitioned := prevStale == nil || prevStale.Status != metav1.ConditionTrue
		if r.Recorder != nil && transitioned {
			r.Recorder.Eventf(ns, nil, "Warning", "MetricsStale", "MetricsStale",
				"Using stale metrics last observed at %s", readResult.LastFreshAt.Format(time.RFC3339))
		}
	} else {
		meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionMetricsStale,
			Status:             metav1.ConditionFalse,
			Reason:             "FreshValue",
			Message:            "Metrics source returned a fresh observation",
			ObservedGeneration: ns.Generation,
		})
	}
	log.V(2).Info("metrics read", "rsrp", metrics.RSRP, "latencyMs", metrics.LatencyMs, "packetLossPercent", metrics.PacketLossPercent, "stale", readResult.Stale)

	// Step 3: Check satellite availability via SatelliteEphemeris.
	satelliteAvailable := r.checkSatelliteAvailability(ctx, ns, now)
	log.V(1).Info("satellite availability", "available", satelliteAvailable, "ephemerisRef", ns.Spec.SatellitePath.EphemerisRef)

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

	// Parse hysteresis margin from spec (string → float64, default 0).
	var hysteresisMargin float64
	if ns.Spec.FailoverPolicy.HysteresisMargin != "" {
		if v, err := strconv.ParseFloat(ns.Spec.FailoverPolicy.HysteresisMargin, 64); err != nil {
			log.Error(err, "invalid failoverPolicy.hysteresisMargin; defaulting to 0",
				"hysteresisMargin", ns.Spec.FailoverPolicy.HysteresisMargin)
		} else if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			log.Info("non-finite or negative failoverPolicy.hysteresisMargin; defaulting to 0",
				"hysteresisMargin", ns.Spec.FailoverPolicy.HysteresisMargin)
		} else {
			hysteresisMargin = v
		}
	}

	result := slice.EvaluateFailoverWithHysteresis(
		ctx,
		currentPath,
		ns.Spec.FailoverPolicy.Triggers,
		metrics,
		satelliteAvailable,
		ns.Spec.FailoverPolicy.SwitchbackDelay.Duration,
		lastFailover,
		now,
		hysteresisMargin,
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

// setMetricsUnknown marks FailoverReady=Unknown with the supplied reason,
// also resets MetricsStale=Unknown because "we could not read metrics"
// means neither fresh nor stale was served this reconcile and leaving
// MetricsStale=True from the previous reconcile would be a lie, then
// persists the status update and requeues. Used whenever the metrics
// source cannot deliver a value: a broken spec or an unreachable
// Prometheus must hold the slice in its current path rather than let
// the failover engine decide on invented data.
func (r *NTNSliceReconciler) setMetricsUnknown(ctx context.Context, ns *ntnv1alpha1.NTNSlice, reason, msg string) (ctrl.Result, error) {
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionFailoverReady,
		Status:             metav1.ConditionUnknown,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: ns.Generation,
	})
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionMetricsStale,
		Status:             metav1.ConditionUnknown,
		Reason:             reason,
		Message:            "Metric freshness unknown: " + msg,
		ObservedGeneration: ns.Generation,
	})
	if err := r.Status().Update(ctx, ns); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: sliceRequeueInterval}, nil
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
			log.Error(err, "failed to get SatelliteEphemeris", "ref", ns.Spec.SatellitePath.EphemerisRef)
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
		WithOptions(ctrlrt.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
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
		log.Error(err, "failed to list NTNSlices for ephemeris mapper")
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
