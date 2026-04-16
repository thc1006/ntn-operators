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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// NTNCellConfigReconciler reconciles a NTNCellConfig object
type NTNCellConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Provider provider.NTNProvider
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntncellconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntncellconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntncellconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile applies NTN cell configuration to the specified provider backend.
func (r *NTNCellConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: Get the NTNCellConfig resource.
	cc := &ntnv1alpha1.NTNCellConfig{}
	if err := r.Get(ctx, req.NamespacedName, cc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 2: Guard against nil provider.
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

	// Step 4: Default namespace to CR namespace if not set.
	spec := cc.Spec.DeepCopy()
	if spec.Provider.Namespace == "" {
		spec.Provider.Namespace = cc.Namespace
	}

	// Step 5: Apply configuration via provider.
	log.Info("Applying NTN cell configuration",
		"provider", spec.Provider.Type,
		"koffset", spec.NTN.CellSpecificKoffset)

	if err := r.Provider.ApplyCellConfig(ctx, spec); err != nil {
		log.Error(err, "Failed to apply cell config")
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

	// Step 6: Get applied status from provider.
	status, err := r.Provider.GetCellStatus(ctx, spec.Provider.Namespace)
	if err != nil {
		log.Error(err, "Failed to get cell status after apply")
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

// SetupWithManager sets up the controller with the Manager.
func (r *NTNCellConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ntnv1alpha1.NTNCellConfig{}).
		Named("ntncellconfig").
		Complete(r)
}
