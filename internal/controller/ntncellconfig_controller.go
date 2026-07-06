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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlrt "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// NTNCellConfigReconciler reconciles a NTNCellConfig object
type NTNCellConfigReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                events.EventRecorder
	Providers               map[string]provider.NTNProvider
	MaxConcurrentReconciles int
}

const (
	ephemerisReasonPushFailed           = "PushFailed"
	ephemerisReasonRefNotFound          = "EphemerisRefNotFound"
	ephemerisReasonGetFailed            = "EphemerisGetFailed"
	ephemerisReasonPayloadMissing       = "EphemerisPayloadMissing"
	ephemerisReasonProviderPushFailed   = "ProviderPushFailed"
	ephemerisReasonProviderPushRejected = "ProviderPushRejected"
	ephemerisReasonEphemerisStale       = "EphemerisStale"
)

// epochSkewMargin is how far in the future a propagated epoch must be to be worth
// pushing. OCUDU's ntn_config_update rejects past epochs, and re-labeling a state
// with a different epoch would corrupt it, so a state whose epoch is within this
// margin of now is treated as stale and skipped (awaiting the SatelliteEphemeris
// producer's refresh) rather than pushed and rejected in a tight loop.
const epochSkewMargin = 10 * time.Second

type ephemerisPushError struct {
	reason string
	err    error
}

func (e *ephemerisPushError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *ephemerisPushError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newEphemerisPushError(reason string, err error) error {
	if err == nil {
		return nil
	}
	return &ephemerisPushError{reason: reason, err: err}
}

func ephemerisPushConditionReason(err error) string {
	var pushErr *ephemerisPushError
	if errors.As(err, &pushErr) && pushErr.reason != "" {
		return pushErr.reason
	}
	return ephemerisReasonPushFailed
}

func ephemerisPushConditionChanged(
	prev *metav1.Condition,
	reason, message string,
	generation int64,
) bool {
	return prev == nil ||
		prev.Status != metav1.ConditionFalse ||
		prev.Reason != reason ||
		prev.Message != message ||
		prev.ObservedGeneration != generation
}

func ephemerisPushShouldRequeue(reason string) bool {
	switch reason {
	case ephemerisReasonRefNotFound,
		ephemerisReasonPayloadMissing,
		ephemerisReasonEphemerisStale,
		ephemerisReasonProviderPushRejected:
		// These clear only on an external change — a spec edit (generation bump)
		// or a SatelliteEphemeris refresh (new marker) — both of which re-trigger
		// reconcile via generation/watch. A tight requeue would just hammer a
		// permanently-failing push (e.g. the gNB rejecting a bad config, or a
		// stale/missing propagated state) until that external change happens.
		return false
	default:
		// Transient failures (API GET error, gNB unreachable) — retry.
		return true
	}
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntncellconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntncellconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntncellconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile applies NTN cell configuration to the specified provider backend.
func (r *NTNCellConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("reconciling")
	reconcileStart := time.Now()
	defer func() {
		log.V(1).Info("reconcile complete", "duration", time.Since(reconcileStart))
	}()

	// Step 1: Get the NTNCellConfig resource.
	cc := &ntnv1alpha1.NTNCellConfig{}
	if err := r.Get(ctx, req.NamespacedName, cc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 2: Look up provider from registry (may be nil).
	prov := r.Providers[cc.Spec.Provider.Type]

	// Step 3: Handle finalizer for ConfigMap cleanup on deletion.
	// Runs before provider validation so deletions succeed even when
	// the provider registry is nil or the type is unregistered.
	if done, result, err := r.handleFinalizer(ctx, cc, prov); done {
		return result, err
	}

	// Step 4: Validate provider.
	if r.Providers == nil {
		// Clear stale koffset but preserve ConfigMapRef for best-effort finalizer cleanup.
		cc.Status.AppliedKoffset = 0
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "InternalError",
			Message:            "NTN provider registry is not configured",
			ObservedGeneration: cc.Generation,
		})
		if err := r.Status().Update(ctx, cc); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	if prov == nil {
		// Clear stale koffset but preserve ConfigMapRef for best-effort finalizer cleanup.
		cc.Status.AppliedKoffset = 0
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "UnsupportedProvider",
			Message:            fmt.Sprintf("Provider type %q is not registered", cc.Spec.Provider.Type),
			ObservedGeneration: cc.Generation,
		})
		if err := r.Status().Update(ctx, cc); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Step 4: Force provider namespace to CR namespace (prevents cross-namespace writes).
	spec := cc.Spec.DeepCopy()
	if spec.Provider.Namespace != "" && spec.Provider.Namespace != cc.Namespace {
		log.Info("Overriding provider.namespace to match CR namespace for security",
			"specified", spec.Provider.Namespace, "enforced", cc.Namespace)
	}
	spec.Provider.Namespace = cc.Namespace

	// Step 5: Apply configuration via provider.
	log.Info("Applying NTN cell configuration",
		"provider", spec.Provider.Type,
		"koffset", spec.NTN.CellSpecificKoffset)

	if err := prov.ApplyCellConfig(ctx, cc.Name, spec); err != nil {
		log.Error(err, "Failed to apply cell config")
		ntnmetrics.ConfigApplyErrorsTotal.With(prometheus.Labels{
			"config": cc.Name, "provider": spec.Provider.Type,
		}).Inc()
		cc.Status.AppliedKoffset = 0
		// Preserve existing ConfigMapRef for best-effort finalizer cleanup.
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "ApplyFailed",
			Message:            err.Error(),
			ObservedGeneration: cc.Generation,
		})
		if err := r.Status().Update(ctx, cc); err != nil {
			return ctrl.Result{}, err
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(cc, nil, "Warning", "ApplyFailed", "ApplyFailed", "%s", err.Error())
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Step 5b: Ensure ownership on provider artifact for garbage collection.
	if err := prov.EnsureOwnership(ctx, cc.Name, cc, r.Scheme); err != nil {
		log.Error(err, "failed to ensure ownership on provider artifact")
	}

	// Step 6: Get applied status from provider.
	status, err := prov.GetCellStatus(ctx, cc.Name, spec.Provider.Namespace)
	if err != nil {
		log.Error(err, "failed to get cell status after apply")
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionUnknown,
			Reason:             "StatusCheckFailed",
			Message:            fmt.Sprintf("Config applied but status verification failed: %v", err),
			ObservedGeneration: cc.Generation,
		})
		if err := r.Status().Update(ctx, cc); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	cc.Status.AppliedKoffset = status.AppliedKoffset
	cc.Status.ConfigMapRef = status.ConfigMapRef

	// Step 7: Set success condition.
	meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionConfigApplied,
		Status:             metav1.ConditionTrue,
		Reason:             "Applied",
		Message:            fmt.Sprintf("NTN config applied via %s provider", spec.Provider.Type),
		ObservedGeneration: cc.Generation,
	})

	// Step 8: Push runtime ephemeris update if ephemerisRef is configured.
	// Keep ConfigApplied semantics independent from ephemeris push status.
	if cc.Spec.EphemerisRef == "" {
		meta.RemoveStatusCondition(&cc.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
	} else {
		pushed, marker, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, spec, prov)
		if err != nil {
			pushErr := err
			log.Error(pushErr, "failed to push ephemeris update")
			reason := ephemerisPushConditionReason(pushErr)
			message := pushErr.Error()
			prevEphemerisCondition := meta.FindStatusCondition(cc.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			conditionChanged := ephemerisPushConditionChanged(prevEphemerisCondition, reason, message, cc.Generation)

			meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionEphemerisPushed,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
				ObservedGeneration: cc.Generation,
			})
			if err := r.Status().Update(ctx, cc); err != nil {
				return ctrl.Result{}, err
			}
			if conditionChanged && r.Recorder != nil {
				r.Recorder.Eventf(cc, nil, "Warning", "EphemerisPushFailed", "EphemerisPushFailed", "%s", message)
			}
			if !ephemerisPushShouldRequeue(reason) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		if pushed {
			meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionEphemerisPushed,
				Status:             metav1.ConditionTrue,
				Reason:             "Pushed",
				Message:            marker,
				ObservedGeneration: cc.Generation,
			})
		}
	}

	if err := r.Status().Update(ctx, cc); err != nil {
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(cc, nil, "Normal", "ConfigApplied", "ConfigApplied",
			"Applied NTN config (koffset=%d) via %s", spec.NTN.CellSpecificKoffset, spec.Provider.Type)
	}

	log.Info("NTN cell configuration applied successfully")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// handleFinalizer manages ConfigMap cleanup on NTNCellConfig deletion.
// Returns (done, result, error). If done is true, the caller should return.
func (r *NTNCellConfigReconciler) handleFinalizer(
	ctx context.Context, cc *ntnv1alpha1.NTNCellConfig, prov provider.NTNProvider,
) (bool, ctrl.Result, error) {
	log := logf.FromContext(ctx)
	finalizerName := "ntn.operators.dev/configmap-cleanup"

	if cc.DeletionTimestamp != nil {
		// Release the CR's per-CR metric series on deletion so /metrics does not
		// accumulate dead series across create/delete churn (idempotent).
		ntnmetrics.ConfigApplyErrorsTotal.DeletePartialMatch(prometheus.Labels{"config": cc.Name})
		if controllerutil.ContainsFinalizer(cc, finalizerName) {
			if prov == nil {
				// Best-effort cleanup using Status.ConfigMapRef when provider is missing.
				if cc.Status.ConfigMapRef != "" {
					log.Info("Provider not in registry; attempting cleanup via status.configMapRef",
						"configMapRef", cc.Status.ConfigMapRef)
					cm := &corev1.ConfigMap{}
					cmKey := client.ObjectKey{Namespace: cc.Namespace, Name: cc.Status.ConfigMapRef}
					if err := r.Get(ctx, cmKey, cm); err != nil {
						if client.IgnoreNotFound(err) != nil {
							log.Error(err, "Failed to get ConfigMap during best-effort finalization", "configmap", cmKey)
							return true, ctrl.Result{}, err // retry on transient errors
						}
					} else if metav1.IsControlledBy(cm, cc) {
						if err := r.Delete(ctx, cm); client.IgnoreNotFound(err) != nil {
							log.Error(err, "Failed to delete ConfigMap during best-effort finalization")
							return true, ctrl.Result{}, err // retry on transient errors
						}
						log.Info("Deleted orphaned ConfigMap via configMapRef", "configmap", cmKey)
					} else {
						log.Info("ConfigMap from configMapRef not owned by this CR; skipping deletion",
							"configmap", cmKey)
					}
				} else {
					log.Info("Provider not in registry and no configMapRef; removing finalizer without cleanup",
						"providerType", cc.Spec.Provider.Type)
				}
				controllerutil.RemoveFinalizer(cc, finalizerName)
				return true, ctrl.Result{}, r.Update(ctx, cc)
			}
			if err := prov.Cleanup(ctx, cc.Name, cc.Namespace); err != nil {
				log.Error(err, "Failed to cleanup provider artifact during finalization")
				return true, ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(cc, finalizerName)
			if err := r.Update(ctx, cc); err != nil {
				return true, ctrl.Result{}, err
			}
		}
		return true, ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(cc, finalizerName) {
		controllerutil.AddFinalizer(cc, finalizerName)
		if err := r.Update(ctx, cc); err != nil {
			return true, ctrl.Result{}, err
		}
		log.V(1).Info("finalizer added, requeueing")
		return true, ctrl.Result{RequeueAfter: time.Second}, nil
	}

	return false, ctrl.Result{}, nil
}

func (r *NTNCellConfigReconciler) pushEphemerisUpdateIfNeeded(
	ctx context.Context,
	cc *ntnv1alpha1.NTNCellConfig,
	spec *ntnv1alpha1.NTNCellConfigSpec,
	prov provider.NTNProvider,
) (bool, string, error) {
	if spec.EphemerisRef == "" {
		return false, "", nil
	}

	eph := &ntnv1alpha1.SatelliteEphemeris{}
	ephKey := client.ObjectKey{Namespace: cc.Namespace, Name: spec.EphemerisRef}
	if err := r.Get(ctx, ephKey, eph); err != nil {
		reason := ephemerisReasonGetFailed
		if apierrors.IsNotFound(err) {
			reason = ephemerisReasonRefNotFound
		}
		return false, "", newEphemerisPushError(
			reason,
			fmt.Errorf("getting referenced SatelliteEphemeris %q: %w", spec.EphemerisRef, err),
		)
	}
	marker := ephemerisPushMarker(eph)
	if isEphemerisPushUpToDate(cc, marker) {
		return false, marker, nil
	}

	// Runtime push path: when a remote-control endpoint + cellID are configured,
	// push the SGP4-propagated ephemeris live via ntn_config_update (#176) instead
	// of rewriting the bootstrap ConfigMap with the CR's static ephemeris.
	if spec.Provider.RemoteControl != nil && spec.CellID != nil {
		return r.pushRuntimeEphemeris(ctx, cc, spec, eph, prov, marker)
	}

	// A satSwitchWithResync is runtime-only (its k_mac has no bootstrap-YAML
	// surface, issue #52). On the ConfigMap path it cannot be delivered, so make
	// that observable instead of silently dropping it — a cell that sets a switch
	// without remoteControl+cellID is misconfigured.
	if spec.NTN.SatSwitchWithResync != nil {
		logf.FromContext(ctx).Info(
			"satSwitchWithResync is set but ignored: it requires spec.provider.remoteControl and spec.cellID (runtime push); the ConfigMap path cannot deliver it",
			"cell", cc.Name)
		if r.Recorder != nil {
			r.Recorder.Eventf(cc, nil, "Warning", "SatSwitchIgnored", "SatSwitchIgnored",
				"%s", "satSwitchWithResync (incl. k_mac) requires remoteControl+cellID; ignored on the ConfigMap path")
		}
	}

	// ConfigMap bootstrap path (backward compatible): push the CR's static
	// spec.ntn ephemeris by rewriting the ConfigMap; the gNB reloads it.
	update := provider.EphemerisUpdate{}
	switch {
	case spec.NTN.EphemerisOrbital != nil:
		update.Orbital = spec.NTN.EphemerisOrbital.DeepCopy()
	case spec.NTN.EphemerisECEF != nil:
		update.ECEF = spec.NTN.EphemerisECEF.DeepCopy()
	default:
		return false, marker, newEphemerisPushError(
			ephemerisReasonPayloadMissing,
			fmt.Errorf("ephemeris payload missing in NTNCellConfig spec"),
		)
	}

	if err := prov.PushEphemerisUpdate(ctx, cc.Name, spec.Provider.Namespace, update); err != nil {
		return false, marker, newEphemerisPushError(
			ephemerisReasonProviderPushFailed,
			fmt.Errorf("provider PushEphemerisUpdate: %w", err),
		)
	}

	return true, marker, nil
}

// pushRuntimeEphemeris pushes the SGP4-propagated ephemeris (from the referenced
// SatelliteEphemeris status) to the gNB via the runtime ntn_config_update path.
// This closes #176: the operator consumes the propagated state vector instead of
// discarding it and re-emitting the CR's static ephemeris.
func (r *NTNCellConfigReconciler) pushRuntimeEphemeris(
	ctx context.Context,
	cc *ntnv1alpha1.NTNCellConfig,
	spec *ntnv1alpha1.NTNCellConfigSpec,
	eph *ntnv1alpha1.SatelliteEphemeris,
	prov provider.NTNProvider,
	marker string,
) (bool, string, error) {
	state := selectPropagatedState(eph.Status.PropagatedStates, spec.EphemerisNoradID)
	if state == nil {
		return false, marker, newEphemerisPushError(
			ephemerisReasonPayloadMissing,
			fmt.Errorf("no propagated state in SatelliteEphemeris %q for noradID selector %v",
				eph.Name, spec.EphemerisNoradID),
		)
	}
	// Skip a stale (past / about-to-expire) epoch rather than push a value OCUDU
	// will reject. The state is only valid for its own epoch, so re-labeling it
	// would corrupt the position; instead wait for the SatelliteEphemeris producer
	// to re-propagate a fresh future epoch (which re-triggers this reconcile via
	// the watch). This reason does not tight-requeue (see ephemerisPushShouldRequeue).
	if state.EpochUnixMs <= time.Now().Add(epochSkewMargin).UnixMilli() {
		return false, marker, newEphemerisPushError(
			ephemerisReasonEphemerisStale,
			fmt.Errorf("propagated epoch for %q (%d) is not sufficiently in the future; awaiting SatelliteEphemeris refresh",
				eph.Name, state.EpochUnixMs),
		)
	}
	ulSync := 5
	if spec.NTN.NTNUlSyncValidityDur != nil {
		ulSync = *spec.NTN.NTNUlSyncValidityDur
	}
	ecef := state.ECEF // copy out of the slice before taking its address
	update := provider.RuntimeUpdate{
		Cell:              provider.CellIdentity{PLMN: spec.CellID.PLMN, NCI: uint64(spec.CellID.NCI)},
		EpochUnixMs:       state.EpochUnixMs,
		UlSyncValidityDur: ulSync,
		Ephemeris:         provider.EphemerisUpdate{ECEF: &ecef},
	}
	// Attach a pending satellite switch (issue #52 / #49 mechanism): it rides in
	// the same per-cell frame as the serving ephemeris. This is the only surface
	// that carries k_mac. The switch only reaches the gNB when a fresh serving
	// ephemeris push happens (OCUDU requires the cell's ephemeris_info anyway).
	if spec.NTN.SatSwitchWithResync != nil {
		update.SatSwitch = spec.NTN.SatSwitchWithResync
	}
	target := provider.ResolvedRemoteControl{Endpoint: spec.Provider.RemoteControl.Endpoint}
	if err := prov.PushRuntimeUpdate(ctx, target, update); err != nil {
		// A permanent rejection (bad config / malformed frame) must not tight-loop;
		// only a transient failure (gNB unreachable) is worth retrying.
		reason := ephemerisReasonProviderPushFailed
		if errors.Is(err, provider.ErrRuntimePushRejected) {
			reason = ephemerisReasonProviderPushRejected
		}
		return false, marker, newEphemerisPushError(reason, fmt.Errorf("provider PushRuntimeUpdate: %w", err))
	}
	return true, marker, nil
}

// selectPropagatedState picks the propagated state matching noradID, or the first
// available when noradID is nil. Returns nil when none match.
func selectPropagatedState(states []ntnv1alpha1.PropagatedState, noradID *int) *ntnv1alpha1.PropagatedState {
	if len(states) == 0 {
		return nil
	}
	if noradID == nil {
		return &states[0]
	}
	for i := range states {
		if states[i].NoradID == *noradID {
			return &states[i]
		}
	}
	return nil
}

func isEphemerisPushUpToDate(cc *ntnv1alpha1.NTNCellConfig, marker string) bool {
	cond := meta.FindStatusCondition(cc.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
	if cond == nil {
		return false
	}
	return cond.Status == metav1.ConditionTrue &&
		cond.ObservedGeneration == cc.Generation &&
		cond.Message == marker
}

func ephemerisPushMarker(eph *ntnv1alpha1.SatelliteEphemeris) string {
	lastUpdated := "none"
	if eph.Status.LastUpdated != nil {
		lastUpdated = eph.Status.LastUpdated.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("ephemerisRef=%s generation=%d lastUpdated=%s", eph.Name, eph.Generation, lastUpdated)
}

// SetupWithManager sets up the controller with the Manager.
// Watches SatelliteEphemeris changes to trigger re-reconciliation
// when a referenced ephemeris is updated.
func (r *NTNCellConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ntnv1alpha1.NTNCellConfig{}).
		Watches(&ntnv1alpha1.SatelliteEphemeris{},
			handler.EnqueueRequestsFromMapFunc(r.ephemerisToNTNCellConfig),
		).
		Named("ntncellconfig").
		WithOptions(ctrlrt.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
		Complete(r)
}

// ephemerisToNTNCellConfig maps a SatelliteEphemeris change to all
// NTNCellConfig resources that reference it via spec.ephemerisRef.
func (r *NTNCellConfigReconciler) ephemerisToNTNCellConfig(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	eph, ok := obj.(*ntnv1alpha1.SatelliteEphemeris)
	if !ok {
		return nil
	}

	log := logf.FromContext(ctx)

	var ccList ntnv1alpha1.NTNCellConfigList
	if err := r.List(ctx, &ccList, client.InNamespace(eph.Namespace)); err != nil {
		log.Error(err, "Failed to list NTNCellConfigs for ephemeris mapper")
		return nil
	}

	var requests []reconcile.Request
	for _, cc := range ccList.Items {
		if cc.Spec.EphemerisRef == eph.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cc.Name,
					Namespace: cc.Namespace,
				},
			})
		}
	}
	return requests
}
