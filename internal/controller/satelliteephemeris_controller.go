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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

const minRefreshInterval = 2 * time.Hour

// SatelliteEphemerisReconciler reconciles a SatelliteEphemeris object
type SatelliteEphemerisReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Fetcher  ephemeris.GPFetcher
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris/finalizers,verbs=update
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=groundstationlifecycles,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile fetches GP data, computes pass predictions, and updates status.
func (r *SatelliteEphemerisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: Get the SatelliteEphemeris resource.
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	if err := r.Get(ctx, req.NamespacedName, eph); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 2: Enforce minimum refresh interval.
	effectiveInterval := eph.Spec.Source.RefreshInterval.Duration
	if effectiveInterval < minRefreshInterval {
		log.Info("RefreshInterval below minimum, clamping to 2h", "configured", effectiveInterval, "effective", minRefreshInterval)
		effectiveInterval = minRefreshInterval
		if r.Recorder != nil {
			r.Recorder.Eventf(eph, nil, "Warning", "RefreshIntervalClamped", "RefreshIntervalClamped",
				"refreshInterval %s is below minimum 2h; using 2h", eph.Spec.Source.RefreshInterval.Duration)
		}
	}

	// Step 3: Guard against nil fetcher.
	if r.Fetcher == nil {
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionFalse,
			Reason:             "InternalError",
			Message:            "GP data fetcher is not configured",
			ObservedGeneration: eph.Generation,
		})
		if err := r.Status().Update(ctx, eph); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Step 4: Fetch GP data.
	result, fetchErr := r.Fetcher.Fetch(ctx, eph.Spec.Source.URL)

	// Step 5: Handle fetch errors.
	if fetchErr != nil {
		return r.handleFetchError(ctx, eph, fetchErr, effectiveInterval)
	}

	// Step 6: Update status with new data (skip on 304).
	if result.NotModified {
		log.Info("GP data unchanged (304 Not Modified)")
	} else {
		log.Info("Fetched GP data successfully", "satelliteCount", result.SatelliteCount)
		eph.Status.SatelliteCount = result.SatelliteCount
		eph.Status.LastUpdated = &metav1.Time{Time: result.FetchedAt}

		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionTrue,
			Reason:             "FetchSucceeded",
			Message:            fmt.Sprintf("Fetched %d satellites from %s", result.SatelliteCount, eph.Spec.Source.Type),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             "ParseSucceeded",
			Message:            "OMM JSON parsed successfully",
			ObservedGeneration: eph.Generation,
		})
	}

	// Step 7: Compute pass predictions if configured.
	if eph.Spec.PassPrediction != nil && len(eph.Spec.PassPrediction.GroundStations) > 0 {
		if err := r.predictPasses(ctx, eph, result); err != nil {
			log.Error(err, "Pass prediction failed")
			eph.Status.NextPassWindows = nil // clear stale pass data
			meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionPassesPredicted,
				Status:             metav1.ConditionFalse,
				Reason:             "PredictionFailed",
				Message:            err.Error(),
				ObservedGeneration: eph.Generation,
			})
			if r.Recorder != nil {
				r.Recorder.Eventf(eph, nil, "Warning", "PredictionFailed", "PredictionFailed", "%s", err.Error())
			}
		}
	}

	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Normal", "GPDataFetched", "GPDataFetched", "Fetched %d satellites", result.SatelliteCount)
	}

	return ctrl.Result{RequeueAfter: effectiveInterval}, nil
}

// predictPasses resolves ground stations and computes pass windows.
func (r *SatelliteEphemerisReconciler) predictPasses(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	fetchResult ephemeris.GPFetchResult,
) error {
	log := logf.FromContext(ctx)
	pp := eph.Spec.PassPrediction

	// Resolve ground station coordinates.
	var stations []ephemeris.GroundStation
	for _, gsName := range pp.GroundStations {
		gs := &ntnv1alpha1.GroundStationLifecycle{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: eph.Namespace, Name: gsName}, gs); err != nil {
			return fmt.Errorf("ground station %q not found: %w", gsName, err)
		}
		lat, err := ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Lat)
		if err != nil {
			return fmt.Errorf("invalid latitude for %q: %w", gsName, err)
		}
		lon, err := ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Lon)
		if err != nil {
			return fmt.Errorf("invalid longitude for %q: %w", gsName, err)
		}
		alt := 0.0
		if gs.Spec.Deployment.Location.Alt != "" {
			alt, err = ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Alt)
			if err != nil {
				return fmt.Errorf("invalid altitude for %q: %w", gsName, err)
			}
		}
		stations = append(stations, ephemeris.GroundStation{
			Name:      gsName,
			Latitude:  lat,
			Longitude: lon,
			Altitude:  alt,
		})
	}

	// Parse minElevation.
	minEl, err := ephemeris.ParseElevation(pp.MinElevation)
	if err != nil {
		return fmt.Errorf("invalid minElevation: %w", err)
	}

	// Parse horizon.
	horizon := pp.Horizon.Duration
	if horizon == 0 {
		horizon = 24 * time.Hour
	}

	// Build NORAD filter from SatelliteSelector.
	var noradFilter []int
	if eph.Spec.Satellites != nil {
		noradFilter = eph.Spec.Satellites.NoradIDs
	}

	// Run pass prediction.
	passes, err := ephemeris.PredictPasses(fetchResult.OMMs, stations, minEl, horizon, noradFilter)
	if err != nil {
		return fmt.Errorf("computing passes: %w", err)
	}

	// Convert to CRD PassWindow format.
	windows := make([]ntnv1alpha1.PassWindow, 0, len(passes))
	for _, p := range passes {
		windows = append(windows, ntnv1alpha1.PassWindow{
			Satellite:     p.Satellite,
			GroundStation: p.GroundStation,
			AOS:           metav1.Time{Time: p.AOS},
			LOS:           metav1.Time{Time: p.LOS},
			MaxElevation:  fmt.Sprintf("%.1f", p.MaxElevation),
		})
	}
	eph.Status.NextPassWindows = windows

	log.Info("Pass prediction completed", "passCount", len(windows), "stationCount", len(stations))

	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionPassesPredicted,
		Status:             metav1.ConditionTrue,
		Reason:             "PredictionSucceeded",
		Message:            fmt.Sprintf("Computed %d passes over %d ground stations", len(windows), len(stations)),
		ObservedGeneration: eph.Generation,
	})

	if r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Normal", "PassesPredicted", "PassesPredicted", "Computed %d passes over %d ground stations", len(windows), len(stations))
	}

	return nil
}

// handleFetchError records the error as a Condition and Event, then requeues.
func (r *SatelliteEphemerisReconciler) handleFetchError(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	fetchErr error,
	effectiveInterval time.Duration,
) (ctrl.Result, error) {
	reason := "FetchFailed"
	requeueAfter := time.Minute

	if errors.Is(fetchErr, ephemeris.ErrRateLimited) {
		reason = "RateLimited"
		requeueAfter = effectiveInterval
	}

	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataFetched,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            fetchErr.Error(),
		ObservedGeneration: eph.Generation,
	})

	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, "Warning", reason, reason, "%s", fetchErr.Error())
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SatelliteEphemerisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ntnv1alpha1.SatelliteEphemeris{}).
		Named("satelliteephemeris").
		Complete(r)
}
