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
	"github.com/thc1006/ntn-operators/pkg/provider/ocudu"
)

// NTNCellConfigReconciler reconciles a NTNCellConfig object
type NTNCellConfigReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                events.EventRecorder
	MaxConcurrentReconciles int
	Provider                provider.NTNProvider
}

const (
	ephemerisReasonPushFailed         = "PushFailed"
	ephemerisReasonRefNotFound        = "EphemerisRefNotFound"
	ephemerisReasonGetFailed          = "EphemerisGetFailed"
	ephemerisReasonPayloadMissing     = "EphemerisPayloadMissing"
	ephemerisReasonProviderPushFailed = "ProviderPushFailed"
)

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
	return reason != ephemerisReasonRefNotFound
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

	// Step 2: Handle finalizer for ConfigMap cleanup on deletion.
	if done, result, err := r.handleFinalizer(ctx, cc); done {
		return result, err
	}

	// Step 3: Guard against nil provider.
	if r.Provider == nil {
		cc.Status.AppliedKoffset = 0
		cc.Status.ConfigMapRef = ""
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "InternalError",
			Message:            "NTN provider is not configured",
			ObservedGeneration: cc.Generation,
		})
		if err := r.Status().Update(ctx, cc); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Step 3: Validate provider type.
	if cc.Spec.Provider.Type != "ocudu" {
		cc.Status.AppliedKoffset = 0
		cc.Status.ConfigMapRef = ""
		meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionConfigApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "UnsupportedProvider",
			Message:            fmt.Sprintf("Provider type %q is not yet supported; only 'ocudu' is available", cc.Spec.Provider.Type),
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

	if err := r.Provider.ApplyCellConfig(ctx, cc.Name, spec); err != nil {
		log.Error(err, "Failed to apply cell config")
		ntnmetrics.ConfigApplyErrorsTotal.With(prometheus.Labels{
			"config": cc.Name, "provider": spec.Provider.Type,
		}).Inc()
		cc.Status.AppliedKoffset = 0
		cc.Status.ConfigMapRef = ""
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

	// Step 5b: Ensure OwnerReference on ConfigMap for garbage collection.
	cm := &corev1.ConfigMap{}
	cmKey := client.ObjectKey{
		Namespace: cc.Namespace,
		Name:      ocudu.ConfigMapNameFor(cc.Name),
	}
	if err := r.Get(ctx, cmKey, cm); err == nil {
		if !metav1.IsControlledBy(cm, cc) {
			if err := controllerutil.SetControllerReference(cc, cm, r.Scheme); err != nil {
				log.Error(err, "failed to set OwnerReference on ConfigMap", "namespace", cm.Namespace, "name", cm.Name)
			} else if err := r.Update(ctx, cm); err != nil {
				log.Error(err, "failed to update ConfigMap with OwnerReference", "namespace", cm.Namespace, "name", cm.Name)
			}
		}
	}

	// Step 6: Get applied status from provider.
	status, err := r.Provider.GetCellStatus(ctx, cc.Name, spec.Provider.Namespace)
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
		pushed, marker, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, spec)
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
	ctx context.Context, cc *ntnv1alpha1.NTNCellConfig,
) (bool, ctrl.Result, error) {
	log := logf.FromContext(ctx)
	finalizerName := "ntn.operators.dev/configmap-cleanup"

	if cc.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(cc, finalizerName) {
			cm := &corev1.ConfigMap{}
			cmKey := client.ObjectKey{
				Namespace: cc.Namespace,
				Name:      ocudu.ConfigMapNameFor(cc.Name),
			}
			if err := r.Get(ctx, cmKey, cm); err != nil {
				if client.IgnoreNotFound(err) != nil {
					log.Error(err, "Failed to get ConfigMap during finalization")
					return true, ctrl.Result{}, err
				}
			} else {
				if err := r.Delete(ctx, cm); client.IgnoreNotFound(err) != nil {
					log.Error(err, "Failed to delete ConfigMap during finalization")
					return true, ctrl.Result{}, err
				}
				log.Info("Deleted ConfigMap during finalization", "configmap", cmKey)
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

	if err := r.Provider.PushEphemerisUpdate(ctx, cc.Name, spec.Provider.Namespace, update); err != nil {
		return false, marker, newEphemerisPushError(
			ephemerisReasonProviderPushFailed,
			fmt.Errorf("provider PushEphemerisUpdate: %w", err),
		)
	}

	return true, marker, nil
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
