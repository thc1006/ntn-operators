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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
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
	// APIReader is an UNCACHED reader (mgr.GetAPIReader) used only to read the
	// remoteControl.tls Secret. The cached client would need secrets list;watch to
	// start an informer (and would then cache every Secret in the cluster); an
	// uncached Get needs only secrets get. Falls back to the cached client if unset.
	APIReader               client.Reader
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
	ephemerisReasonRemoteControlConfig  = "RemoteControlConfigInvalid"
	// ephemerisReasonInputsStale marks a push HELD because the referenced
	// SatelliteEphemeris's propagatedStates were computed under DIFFERENT propagation
	// inputs than the live spec (status.propagatedStatesInputHash != hash(spec)) — a
	// source/selector edit whose re-propagate has not yet succeeded (#204-G1). It does NOT
	// fire on a pass-prediction-only edit. It clears when the producer re-propagates (its
	// Status().Update re-triggers this reconcile via the ephemeris watch), so it is
	// non-requeuing like the other await-external-change reasons.
	ephemerisReasonInputsStale = "EphemerisInputsStale"
)

// epochSkewMargin is how far in the future a propagated epoch must be to be worth
// pushing. OCUDU's ntn_config_update rejects past epochs, and re-labeling a state
// with a different epoch would corrupt it, so a state whose epoch is within this
// margin of now is treated as stale and skipped (awaiting the SatelliteEphemeris
// producer's refresh) rather than pushed and rejected in a tight loop.
const epochSkewMargin = 10 * time.Second

// maxSourceEpochFutureSkew bounds how far into the future a SOURCE element-set epoch may
// be before it is treated as implausible (a corrupt or spoofed feed). Real OMM/TLE epochs
// are at or before "now"; a far-future epoch would otherwise sail past the "older than
// maxEpochAge" check, which only catches the PAST direction.
//
// It is enforced in TWO places, deliberately: the producer refuses to PROPAGATE from such
// an element set (propagateStates — SGP4 would be driven backward from the bogus epoch,
// writing a wildly wrong ECEF into status), and the consumer refuses to PUSH one if it
// ever reaches status anyway (defense in depth). The consumer check alone cannot prevent
// the backward propagation, only its delivery. Generous, so legitimate near-present epochs
// are never rejected.
const maxSourceEpochFutureSkew = 24 * time.Hour

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

func ephemerisPushShouldRequeue(reason string) bool {
	switch reason {
	case ephemerisReasonRefNotFound,
		ephemerisReasonPayloadMissing,
		ephemerisReasonEphemerisStale,
		ephemerisReasonInputsStale,
		ephemerisReasonRemoteControlConfig,
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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// emitConfigApplied records the ConfigApplied Normal event. It is invoked on every
// reconcile path that persists a ConfigApplied=True transition (the
// ephemeris-push-failure early return as well as the full-success path), so the
// event is never lost when a status write other than the last one carries the
// transition. Callers gate it on the transition (configApplyChanged).
func (r *NTNCellConfigReconciler) emitConfigApplied(cc *ntnv1alpha1.NTNCellConfig, spec *ntnv1alpha1.NTNCellConfigSpec) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(cc, nil, corev1.EventTypeNormal, "ConfigApplied", "ConfigApplied",
		"Applied NTN config (koffset=%d) via %s", spec.NTN.CellSpecificKoffset, spec.Provider.Type)
	// A satSwitchWithResync on the ConfigMap path (no remoteControl+cellID) cannot be
	// delivered (its k_mac has no bootstrap-YAML surface, issue #52). Surface it here,
	// gated on the same ConfigApplied episode as its caller (configApplyChanged), so it
	// warns once per spec rather than on every (minutely) push-failure requeue.
	if spec.NTN.SatSwitchWithResync != nil && (spec.Provider.RemoteControl == nil || spec.CellID == nil) {
		r.Recorder.Eventf(cc, nil, corev1.EventTypeWarning, "SatSwitchIgnored", "SatSwitchIgnored",
			"%s", "satSwitchWithResync (incl. k_mac) requires remoteControl+cellID; ignored on the ConfigMap path")
	}
}

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

	if err := prov.ApplyCellConfig(ctx, cc, spec, r.Scheme); err != nil {
		log.Error(err, "Failed to apply cell config")
		ntnmetrics.ConfigApplyErrorsTotal.With(prometheus.Labels{
			"namespace": cc.Namespace, "config": cc.Name, "provider": spec.Provider.Type,
		}).Inc()
		cc.Status.AppliedKoffset = 0
		// A ConfigMap-ownership collision is distinct from a generic apply failure:
		// the operator refuses to overwrite a ConfigMap it does not own.
		reason := "ApplyFailed"
		if errors.Is(err, provider.ErrConfigMapNotOwned) {
			reason = "OwnershipConflict"
		}
		// Preserve existing ConfigMapRef for best-effort finalizer cleanup. Gate the
		// Warning on a real episode (Status/Reason/Generation change, IGNORING the
		// error Message) so a persistent failure whose message merely varies does not
		// re-emit on every (minutely) requeue.
		applyFailEpisode := conditionEpisodeChanged(
			meta.FindStatusCondition(cc.Status.Conditions, ntnv1alpha1.ConditionConfigApplied),
			metav1.ConditionFalse, reason, cc.Generation)
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            err.Error(),
			ObservedGeneration: cc.Generation,
		})
		if err := r.Status().Update(ctx, cc); err != nil {
			return ctrl.Result{}, err
		}
		if applyFailEpisode && r.Recorder != nil {
			r.Recorder.Eventf(cc, nil, corev1.EventTypeWarning, reason, reason, "%s", err.Error())
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	// The controller reference is set ATOMICALLY at ConfigMap creation inside
	// ApplyCellConfig, so no separate best-effort ownership step is needed.

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

	// Step 7: Set success condition. Capture whether it actually changed (status/
	// reason/generation, or a new object) so the ConfigApplied event fires once per
	// change, not on every reconcile. The events.k8s.io/v1 recorder this operator uses
	// correlates events by type, reason, action, reporting identity, AND the full
	// regarding ObjectReference — which carries resourceVersion (client-go
	// reference.GetReference). Because this event is emitted after a Status().Update
	// that bumps the object's resourceVersion, a per-reconcile Normal event would NOT
	// fold into one EventSeries; each reconcile would mint a distinct Event object.
	// Gating on a real change therefore avoids both redundant Event objects and
	// needless broadcaster/series churn, and keeps the human-relevant history legible.
	// (The message here is constant, so meta.SetStatusCondition's return is a fine
	// gate; failure events whose message varies use conditionEpisodeChanged instead.)
	configApplyChanged := meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
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
		// No runtime push configured: clear any readiness series from a prior spec
		// that did configure one, so a stale 0/1 does not linger and misfire the alert.
		ntnmetrics.EphemerisPushReady.DeletePartialMatch(prometheus.Labels{"namespace": cc.Namespace, "config": cc.Name})
	} else {
		pushed, marker, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, spec, prov)
		if err != nil {
			pushErr := err
			log.Error(pushErr, "failed to push ephemeris update")
			reason := ephemerisPushConditionReason(pushErr)
			message := pushErr.Error()
			prevEphemerisCondition := meta.FindStatusCondition(cc.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			conditionChanged := conditionEpisodeChanged(prevEphemerisCondition, metav1.ConditionFalse, reason, cc.Generation)

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
			// This persist path durably records the ConfigApplied=True transition, so
			// emit its event here too — the config WAS applied; the push is a separate
			// concern reported below. Without this, a first apply whose ephemeris push
			// fails would swallow ConfigApplied forever: the success-path emit below is
			// skipped by this branch's early return, and later reconciles see no
			// transition (findings: WO-20 adversarial review).
			if configApplyChanged {
				r.emitConfigApplied(cc, spec)
			}
			// Count EVERY real push failure by reason (I-19), NOT once per episode:
			// _errors_total is a failure counter, and the shipped NTNEphemerisPushFailing
			// alert is increase(...[15m]) > 0 for 15m — which only stays firing while the
			// series keeps advancing. A transient outage tight-requeues every minute, so
			// per-failure counting keeps increase() > 0 across the outage; an episode gate
			// would increment once and let the alert resolve mid-outage. This mirrors
			// ConfigApplyErrorsTotal (counted per failure above). (Permanent reasons that
			// do not requeue still increment only once — a durable EphemerisPushed=False
			// condition covers those; a readiness gauge is a recommended follow-up.)
			ntnmetrics.EphemerisPushErrorsTotal.With(prometheus.Labels{
				"namespace": cc.Namespace, "config": cc.Name, "reason": reason,
			}).Inc()
			// Readiness gauge: 0 while the push is failing. Unlike the counter, this
			// holds across a PERMANENT (non-requeuing) failure, so `push_ready == 0 for
			// 15m` alerts even when the counter stops advancing (issue #216).
			ntnmetrics.EphemerisPushReady.With(prometheus.Labels{
				"namespace": cc.Namespace, "config": cc.Name,
			}).Set(0)
			// The Event stays episode-gated (once per outage) to keep the Event stream
			// legible; the counter above carries the per-failure signal.
			if conditionChanged && r.Recorder != nil {
				r.Recorder.Eventf(cc, nil, corev1.EventTypeWarning, "EphemerisPushFailed", "EphemerisPushFailed", "%s", message)
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
		// Reaching here means the push did not error (the failure branch returns
		// early), so the runtime push is healthy — freshly pushed or already up to
		// date. Mark ready=1 (this also clears a prior 0 once the outage recovers).
		ntnmetrics.EphemerisPushReady.With(prometheus.Labels{
			"namespace": cc.Namespace, "config": cc.Name,
		}).Set(1)
	}

	if err := r.Status().Update(ctx, cc); err != nil {
		return ctrl.Result{}, err
	}

	if configApplyChanged {
		r.emitConfigApplied(cc, spec)
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
		ntnmetrics.ConfigApplyErrorsTotal.DeletePartialMatch(prometheus.Labels{"namespace": cc.Namespace, "config": cc.Name})
		ntnmetrics.EphemerisPushErrorsTotal.DeletePartialMatch(prometheus.Labels{"namespace": cc.Namespace, "config": cc.Name})
		ntnmetrics.EphemerisPushReady.DeletePartialMatch(prometheus.Labels{"namespace": cc.Namespace, "config": cc.Name})
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
			if err := prov.Cleanup(ctx, cc); err != nil {
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
	// Runtime push path: when a remote-control endpoint + cellID are configured,
	// push the SGP4-propagated ephemeris live via ntn_config_update (#176) instead
	// of rewriting the bootstrap ConfigMap with the CR's static ephemeris. Its
	// freshness key is the propagated EPOCH, not the 2h-stable GP fetch time: the
	// SatelliteEphemeris re-propagates a fresh epoch every few minutes and fans out
	// to this reconcile, so keying the dedup marker on the epoch is what re-pushes
	// the live state between fetches and after a gNB restart (I-12). Refreshing
	// epoch/ephemeris/TA is SI-change-free per TS 38.331, so the periodic re-push is
	// a level re-assert that does not disturb UEs. pushRuntimeEphemeris computes its
	// own epoch-aware marker and does its own up-to-date check.
	if spec.Provider.RemoteControl != nil && spec.CellID != nil {
		return r.pushRuntimeEphemeris(ctx, cc, spec, eph, prov)
	}

	// ConfigMap bootstrap path: the CR's static spec.ntn ephemeris; the last GP
	// fetch time (LastUpdated) is the correct freshness key here.
	marker := ephemerisPushMarker(eph)
	if isEphemerisPushUpToDate(cc, marker) {
		return false, marker, nil
	}

	// A satSwitchWithResync is runtime-only (its k_mac has no bootstrap-YAML
	// surface, issue #52). On the ConfigMap path it cannot be delivered, so make
	// that observable instead of silently dropping it — a cell that sets a switch
	// without remoteControl+cellID is misconfigured.
	if spec.NTN.SatSwitchWithResync != nil {
		// The SatSwitchIgnored Warning is emitted post-persist, once per ConfigApplied
		// episode, in emitConfigApplied — NOT here. Emitting inline re-fired it on every
		// (minutely) push-failure requeue, since this path is re-entered whenever the
		// push is not up to date, which it never is while the push keeps failing.
		logf.FromContext(ctx).V(1).Info(
			"satSwitchWithResync is set but ignored: it requires spec.provider.remoteControl and spec.cellID (runtime push); the ConfigMap path cannot deliver it",
			"cell", cc.Name)
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

	if err := prov.PushEphemerisUpdate(ctx, cc, update); err != nil {
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
) (bool, string, error) {
	// Gate — stale propagation inputs (#204-G1): the propagatedStates must reflect the
	// CURRENT propagation-relevant spec (source type/url + NORAD selector). The producer
	// stamps status.propagatedStatesInputHash whenever it (re)propagates; if the live
	// spec hashes differently, a source/selector edit's re-propagate has not yet
	// succeeded and the persisted states are stale for the current inputs — pushing them
	// would send orbital data computed under superseded inputs. This deliberately does
	// NOT fire on a pass-prediction-only / refreshInterval edit (those do not change the
	// propagated ECEF), preserving last-good continuity during an upstream outage
	// (#221 review finding 2). An empty stored hash — states predating this field on
	// upgrade, or no successful reconcile yet — also blocks until the first
	// re-propagation stamps it (fail-closed; see CHANGELOG upgrade note). Wait for the
	// producer to re-propagate; its Status().Update re-triggers this reconcile via the
	// ephemeris watch.
	if eph.Status.PropagatedStatesInputHash != propagationInputHash(eph.Spec) {
		return false, ephemerisPushMarker(eph), newEphemerisPushError(
			ephemerisReasonInputsStale,
			fmt.Errorf("SatelliteEphemeris %q propagatedStates were computed under different propagation inputs than the current spec; awaiting re-propagation", eph.Name),
		)
	}
	state := selectPropagatedState(eph.Status.PropagatedStates, spec.EphemerisNoradID)
	if state == nil {
		// A referenced satellite that the SatelliteEphemeris rejected as deep-space
		// (near-earth SGP4 can't propagate it) has no propagated state — surface that
		// cross-CR cause so the operator isn't left correlating a bare "payload
		// missing" against the other object's condition (deep-review A-NIT-1).
		hint := ""
		if meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime) {
			hint = " (the referenced SatelliteEphemeris reports UnsupportedOrbitRegime: deep-space element sets are rejected because the near-earth SGP4 propagator cannot handle them)"
		}
		return false, ephemerisPushMarker(eph), newEphemerisPushError(
			ephemerisReasonPayloadMissing,
			fmt.Errorf("no propagated state in SatelliteEphemeris %q for noradID selector %v%s",
				eph.Name, spec.EphemerisNoradID, hint),
		)
	}

	marker := runtimeEphemerisPushMarker(eph, state)

	// ---- Currency gates: these MUST run BEFORE the dedup early-return below. ----
	// A stalled producer (controller down, fetcher-setup failure, upstream outage) stops
	// re-propagating, so the marker STOPS CHANGING while the already-delivered epoch
	// expires on the gNB. If dedup returned first, these gates would never run again and
	// the caller would keep reporting EphemerisPushReady=1 — falsely healthy in exactly
	// the producer-stall case the hard gate exists for. Re-validating currency on EVERY
	// reconcile (not only when a new push is imminent) makes the push fail CLOSED
	// (EphemerisPushed=False + push_ready=0) once the delivered data goes stale, so
	// `push_ready == 0 for 15m` alerts (issue #216). #221 review finding 1.

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
	// Hard-stale, per-satellite (#200-C4; #221 review finding 1): refuse to push the
	// SELECTED satellite when its OWN source element-set epoch is beyond the freshness
	// bound (drifting) or implausibly future-dated. Per-satellite, so a stale, cap-omitted,
	// or unselected sibling in the same SatelliteEphemeris does NOT block this cell. A 0
	// source epoch means the producer could not parse it (unknown, not stale) — allowed,
	// matching the producer's stale-count which also skips unparseable epochs.
	if state.SourceEpochUnixMs != 0 {
		src := time.UnixMilli(state.SourceEpochUnixMs)
		nowT := time.Now()
		if nowT.Sub(src) > maxEpochAge {
			return false, marker, newEphemerisPushError(
				ephemerisReasonEphemerisStale,
				fmt.Errorf("selected satellite %d source element epoch is older than %s (drifting); awaiting SatelliteEphemeris refresh",
					state.NoradID, maxEpochAge),
			)
		}
		if src.After(nowT.Add(maxSourceEpochFutureSkew)) {
			return false, marker, newEphemerisPushError(
				ephemerisReasonEphemerisStale,
				fmt.Errorf("selected satellite %d source element epoch %s is implausibly future-dated; refusing",
					state.NoradID, src.UTC()),
			)
		}
	}

	// Dedup on the propagated epoch (I-12): re-push whenever the SatelliteEphemeris
	// re-propagated to a fresh epoch — the watch fans that out to this reconcile, so
	// keying on the epoch (not the 2h-stable LastUpdated) keeps the gNB fresh between
	// GP fetches and recovers it after a gNB restart. Same epoch ⇒ same marker ⇒ no
	// redundant push; spec changes (koffset/TA/satSwitch) still re-push via the
	// ObservedGeneration check in isEphemerisPushUpToDate. Deliberately placed AFTER the
	// currency gates above, so a same-marker reconcile still re-validates freshness
	// instead of short-circuiting past it (#221 review finding 1).
	if isEphemerisPushUpToDate(cc, marker) {
		return false, marker, nil
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
	// Resolve opt-in transport security (wss:// + shared secret / mTLS) from the
	// referenced Secret; nil TLSConfig keeps the plaintext ws:// behavior (N-12).
	tlsConfig, authToken, tlsErr := r.resolveRemoteControlTLS(ctx, eph.Namespace, spec.Provider.RemoteControl)
	if tlsErr != nil {
		// A deterministic credential/config error (wrong Secret type, missing opt-in
		// label, malformed cert) is PERMANENT — it clears only on a Secret/spec edit,
		// which re-triggers reconcile, so classify it non-requeuing instead of hammering
		// the apiserver every minute. A transient Secret read error stays requeuing.
		reason := ephemerisReasonProviderPushFailed
		if errors.Is(tlsErr, errRemoteControlCredentialInvalid) {
			reason = ephemerisReasonRemoteControlConfig
		}
		// Log the specific cause for the operator; surface only a uniform message on the
		// CR (see errRemoteControlCredentialUnavailable — avoids a Secret existence/type
		// oracle for a principal that can write the CR but not read Secrets).
		logf.FromContext(ctx).Error(tlsErr, "remoteControl.tls credential could not be resolved", "cell", cc.Name)
		return false, marker, newEphemerisPushError(reason, errRemoteControlCredentialUnavailable)
	}
	target := provider.ResolvedRemoteControl{
		Endpoint:  spec.Provider.RemoteControl.Endpoint,
		TLSConfig: tlsConfig,
		AuthToken: authToken,
	}
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

const (
	// remoteControlCredentialLabel must be set to "true" by the Secret's owner for a
	// Secret to be usable as a remoteControl.tls credential — an opt-in that prevents
	// a CR author from redirecting the operator's Secret read at an unrelated Secret.
	remoteControlCredentialLabel = "ntn.operators.dev/remote-control-credential"
	// secretTypeBootstrapToken is the k8s bootstrap-token Secret type (no corev1
	// constant); like a service-account token it is a cluster credential, not a gNB one.
	secretTypeBootstrapToken = "bootstrap.kubernetes.io/token"
)

// errRemoteControlCredentialInvalid tags a DETERMINISTIC remoteControl.tls config
// failure (wrong Secret type, missing opt-in label, malformed cert material). The
// caller classifies it as permanent — it clears only on a Secret/spec edit (which
// re-triggers reconcile via generation or the ephemeris fan-out), so it must not
// tight-requeue like a transient failure would.
var errRemoteControlCredentialInvalid = errors.New("not a usable remote-control credential")

// errRemoteControlCredentialUnavailable is the UNIFORM, CR-facing message for any
// remoteControl.tls resolution failure. The specific cause (Secret missing / wrong
// type / unlabelled / malformed cert) is logged for the operator but never surfaced
// on the CR condition/event: it would otherwise let a principal who can write the CR
// but not read Secrets probe Secret existence and type (an oracle).
var errRemoteControlCredentialUnavailable = errors.New("referenced remote-control credential is unavailable or not authorized")

// resolveRemoteControlTLS builds the TLS config and bearer token for the runtime
// push from the Secret referenced by remoteControl.tls. It reads the Secret from
// the NTNCellConfig's OWN namespace (never cross-namespace) and never logs its
// contents. The Secret must be owner-opted-in (label remoteControlCredentialLabel=
// true) and must not be a Kubernetes API credential (service-account / bootstrap
// token) — see the confused-deputy note below. Returns (nil, "", nil) when TLS is
// not configured — the caller then dials plaintext ws:// (N-12). Recognized Secret
// keys: ca.crt (PEM CA to verify the server; omit ⇒ system roots), token (Bearer
// shared secret; optional), and for mode=mtls tls.crt + tls.key (client certificate).
func (r *NTNCellConfigReconciler) resolveRemoteControlTLS(
	ctx context.Context, namespace string, rc *ntnv1alpha1.RemoteControlRef,
) (*tls.Config, string, error) {
	if rc == nil || rc.TLS == nil {
		return nil, "", nil
	}
	t := rc.TLS
	// Read the Secret UNCACHED (APIReader) so a secrets-get RBAC suffices and the
	// operator does not cache every Secret in the cluster. Fall back to the cached
	// client only if no APIReader was wired (e.g. some tests).
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: t.SecretName}, secret); err != nil {
		return nil, "", fmt.Errorf("reading remoteControl.tls secret %q: %w", t.SecretName, err)
	}
	// SECURITY (partial confused-deputy MITIGATION, not an authorization boundary): the
	// operator reads this Secret with its OWN cluster-wide secrets-get, on behalf of
	// whoever authored the NTNCellConfig — who may not be able to read Secrets. Two gates
	// raise the bar against a CR author pointing the operator at an arbitrary Secret and
	// shipping its contents to a CR-controlled endpoint:
	//   (1) refuse a Kubernetes API credential — a service-account or bootstrap token
	//       Secret stores its apiserver token under the same "token" key we read;
	//   (2) require the Secret's owner to opt it in via a label.
	// Known LIMITS (a real per-CR/per-endpoint SubjectAccessReview or grant resource is a
	// tracked follow-up): the opt-in is namespace-scoped — once labelled, ANY NTNCellConfig
	// in the namespace may use the Secret; a principal with secrets `patch` but not `get`
	// could add the label to a Secret it cannot read; and the type check does not stop an
	// Opaque Secret from holding some other bearer token.
	switch secret.Type {
	case corev1.SecretTypeServiceAccountToken, secretTypeBootstrapToken:
		return nil, "", fmt.Errorf("remoteControl.tls secret %q has type %q — a Kubernetes API credential must not be used as a gNB remote-control secret: %w", t.SecretName, secret.Type, errRemoteControlCredentialInvalid)
	}
	if secret.Labels[remoteControlCredentialLabel] != "true" {
		return nil, "", fmt.Errorf("remoteControl.tls secret %q must be labelled %s=true by its owner to be usable as a remote-control credential: %w", t.SecretName, remoteControlCredentialLabel, errRemoteControlCredentialInvalid)
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// ServerName verified against the certificate SANs: explicit override, else the
	// endpoint host (Go validates SANs only, so an IP/host mismatch would fail).
	if t.ServerName != "" {
		cfg.ServerName = t.ServerName
	} else if host, _, err := net.SplitHostPort(rc.Endpoint); err == nil {
		cfg.ServerName = host
	}
	// Trust a private CA when provided; otherwise fall back to the system roots.
	if ca := secret.Data["ca.crt"]; len(ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, "", fmt.Errorf("remoteControl.tls secret %q: ca.crt is not valid PEM: %w", t.SecretName, errRemoteControlCredentialInvalid)
		}
		cfg.RootCAs = pool
	}
	// mTLS: present a client certificate.
	if t.Mode == "mtls" {
		crt, key := secret.Data["tls.crt"], secret.Data["tls.key"]
		if len(crt) == 0 || len(key) == 0 {
			return nil, "", fmt.Errorf("remoteControl.tls mode=mtls requires tls.crt and tls.key in secret %q: %w", t.SecretName, errRemoteControlCredentialInvalid)
		}
		pair, err := tls.X509KeyPair(crt, key)
		if err != nil {
			return nil, "", fmt.Errorf("remoteControl.tls secret %q: invalid client certificate/key (%v): %w", t.SecretName, err, errRemoteControlCredentialInvalid)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	// Optional shared secret, sent as Authorization: Bearer (only over the TLS conn).
	return cfg, string(secret.Data["token"]), nil
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

// runtimeEphemerisPushMarker is the dedup key for the runtime (ntn_config_update)
// push path. The ConfigMap path keys on the GP fetch time (ephemerisPushMarker),
// but the runtime push delivers the SGP4-propagated state, which the
// SatelliteEphemeris re-propagates to a fresh EPOCH every few minutes; keying on
// that epoch (not the 2h-stable LastUpdated) is what makes the watch fan-out
// re-push the live state between GP fetches and after a gNB restart (I-12). The
// epoch is a monotonic 1:1 proxy for the propagated ECEF — each re-propagation
// yields a new epoch and a new state — so a same-epoch reconcile dedups without
// hashing the payload. Refreshing epoch/ephemeris/TA is SI-change-free per TS
// 38.331, and the re-push cadence is bounded by the SatelliteEphemeris
// re-propagation interval, well under ntn-UlSyncValidityDuration (NR max 240 s).
func runtimeEphemerisPushMarker(eph *ntnv1alpha1.SatelliteEphemeris, state *ntnv1alpha1.PropagatedState) string {
	return fmt.Sprintf("ephemerisRef=%s ephGeneration=%d norad=%d epoch=%d",
		eph.Name, eph.Generation, state.NoradID, state.EpochUnixMs)
}

// SetupWithManager sets up the controller with the Manager.
// Watches SatelliteEphemeris changes to trigger re-reconciliation
// when a referenced ephemeris is updated.
func (r *NTNCellConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// reconcileTriggerPredicate (predicates.go) filters status-only self-writes
		// but passes spec changes AND deletionTimestamp transitions — the latter is
		// load-bearing here: the configmap-cleanup finalizer must run on delete even
		// for a cell sitting on a no-requeue terminal path (e.g. a dangling
		// ephemerisRef → EphemerisRefNotFound). The SatelliteEphemeris Watches below
		// keep no predicate.
		For(&ntnv1alpha1.NTNCellConfig{}, builder.WithPredicates(reconcileTriggerPredicate())).
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
