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
	"net"
	neturl "net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
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

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/netutil"
)

const minRefreshInterval = 2 * time.Hour

// maxRefreshInterval bounds how stale a fetched element set may become: GP data
// is re-fetched at least this often, so SGP4 propagation never runs on an element
// set older than roughly this age (LEO in-track error grows ~1–3 km/day from the
// element epoch). CelesTrak/SpaceTrack publish updates well within a day, so a
// daily floor on freshness costs nothing (findings.md I-17).
const maxRefreshInterval = 24 * time.Hour

// SatelliteEphemerisReconciler reconciles a SatelliteEphemeris object
type SatelliteEphemerisReconciler struct {
	client.Client
	// APIReader is an UNCACHED reader (mgr.GetAPIReader) used only to read the
	// SpaceTrack credentials Secret. The cached client would need secrets list;watch
	// to start an informer (and would then cache every Secret in the cluster); an
	// uncached Get needs only secrets get. Falls back to the cached client if unset.
	APIReader               client.Reader
	Scheme                  *runtime.Scheme
	Recorder                events.EventRecorder
	MaxConcurrentReconciles int
	Fetcher                 ephemeris.GPFetcher          // CelesTrak fetcher
	SpaceTrackFetcher       *ephemeris.SpaceTrackFetcher // SpaceTrack fetcher (nil = disabled)

	// ommCache holds the last real GP-fetch result per SatelliteEphemeris so the
	// orbit can be re-propagated on a short cadence WITHOUT re-contacting the GP
	// source (rate-limit etiquette; #179). Keyed by client.ObjectKey, evicted on
	// CR deletion. In-memory only — a cold cache (e.g. after a restart) just forces
	// one fetch on the next reconcile.
	ommCache sync.Map
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris/finalizers,verbs=update
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=groundstationlifecycles,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// clampRefreshInterval clamps spec.source.refreshInterval into [minRefreshInterval,
// maxRefreshInterval] and records the RefreshIntervalClamped condition — True with
// reason BelowMinimum/AboveMaximum when clamped, and False/WithinBounds (NOT absent)
// when the configured interval is used as-is, so False is distinguishable from the
// not-yet-reconciled Unknown per the Kubernetes condition contract. The clamp Warning
// is emitted inline (pre-persist) and episode-gated, so it fires once per spec across
// NORMAL reconciles instead of on every ~3-minute reconcile. Inline emission is
// deliberate: the clamp is a deterministic function of the spec, so the Warning is
// never fabricated and is not lost on an early-return fetch-error path. The one edge
// it does NOT cover is a status-update CONFLICT that discards the reconcile — the prior
// condition then does not persist, so the next reconcile re-detects the episode and
// re-emits; that is rare and benign for a spec-validation Warning, and is the accepted
// trade-off against threading a deferred note through every status-write path.
func (r *SatelliteEphemerisReconciler) clampRefreshInterval(ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris) time.Duration {
	effectiveInterval := eph.Spec.Source.RefreshInterval.Duration
	var clampReason, clampMsg string
	switch {
	case effectiveInterval < minRefreshInterval:
		effectiveInterval = minRefreshInterval
		clampReason = "BelowMinimum"
		clampMsg = fmt.Sprintf("refreshInterval %s is below minimum 2h; using 2h", eph.Spec.Source.RefreshInterval.Duration)
	case effectiveInterval > maxRefreshInterval:
		effectiveInterval = maxRefreshInterval
		clampReason = "AboveMaximum"
		clampMsg = fmt.Sprintf("refreshInterval %s exceeds maximum 24h; using 24h to keep the element set fresh", eph.Spec.Source.RefreshInterval.Duration)
	}
	if clampReason == "" {
		// Within bounds: record an explicit False (not absent) so a consumer can tell
		// "evaluated, no clamp needed" from "not yet reconciled" (absent = Unknown).
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionRefreshIntervalClamped,
			Status:             metav1.ConditionFalse,
			Reason:             "WithinBounds",
			Message:            "Configured refreshInterval is within the supported range [2h, 24h]",
			ObservedGeneration: eph.Generation,
		})
		return effectiveInterval
	}
	clampEpisode := conditionEpisodeChanged(
		meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionRefreshIntervalClamped),
		metav1.ConditionTrue, clampReason, eph.Generation)
	// Log at Info once per clamp episode; drop to V(1) across steady-state reconciles
	// so a persistently out-of-bounds spec does not print the same line every ~3
	// minutes, mirroring the event hygiene this WO enforces.
	clampLog := logf.FromContext(ctx)
	if !clampEpisode {
		clampLog = clampLog.V(1)
	}
	clampLog.Info("RefreshInterval clamped", "reason", clampReason,
		"configured", eph.Spec.Source.RefreshInterval.Duration, "effective", effectiveInterval)
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionRefreshIntervalClamped,
		Status:             metav1.ConditionTrue,
		Reason:             clampReason,
		Message:            clampMsg,
		ObservedGeneration: eph.Generation,
	})
	if clampEpisode && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, "RefreshIntervalClamped", clampReason, "%s", clampMsg)
	}
	return effectiveInterval
}

// Reconcile fetches GP data, computes pass predictions, and updates status.
func (r *SatelliteEphemerisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("reconciling")
	reconcileStart := time.Now()
	defer func() {
		log.V(1).Info("reconcile complete", "duration", time.Since(reconcileStart))
	}()

	// Step 1: Get the SatelliteEphemeris resource.
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	if err := r.Get(ctx, req.NamespacedName, eph); err != nil {
		if apierrors.IsNotFound(err) {
			// CR deleted — release its per-CR metric series so /metrics does not
			// accumulate dead series across create/delete churn, and drop its
			// cached OMMs so the in-memory cache does not leak (#179).
			ntnmetrics.GPSatelliteCount.DeletePartialMatch(prometheus.Labels{"namespace": req.Namespace, "ephemeris": req.Name})
			ntnmetrics.GPDeepSpaceRejectedCount.DeletePartialMatch(prometheus.Labels{"namespace": req.Namespace, "ephemeris": req.Name})
			ntnmetrics.EphemerisEpochStaleCount.DeletePartialMatch(prometheus.Labels{"namespace": req.Namespace, "ephemeris": req.Name})
			ntnmetrics.GPFetchReady.DeletePartialMatch(prometheus.Labels{"namespace": req.Namespace, "ephemeris": req.Name})
			r.ommCache.Delete(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 2: Clamp the refresh interval into [minRefreshInterval, maxRefreshInterval].
	// The floor honours the GP source's rate-limit etiquette; the ceiling bounds how
	// stale the element set may get — an unbounded refreshInterval (e.g. 720h) would
	// re-propagate a 30-day-old element set as if fresh, and LEO SGP4 error grows
	// ~1–3 km/day from the element epoch (findings.md I-17). metav1.Duration
	// serialises as a string, so both bounds are enforced here rather than via CEL.
	effectiveInterval := r.clampRefreshInterval(ctx, eph)
	now := time.Now()

	// Step 3: Can this reconcile be answered from cache WITHOUT contacting the source?
	//   (a) inside the GP-refresh window — normal (#179), or
	//   (b) inside a failed fetch's retry backoff — degraded, but it is exactly what keeps
	//       the runtime-push epoch alive through a long outage.
	// Consulted BEFORE fetcher/credential setup: a reconcile the cache can answer must not
	// read the source Secret, so a transient Secret failure (or a credential rotation) cannot
	// break SIB19 continuity.
	var outcome ommFetchOutcome
	var earlyResult *ctrl.Result
	var earlyErr error
	if c, why := r.cacheServe(req.NamespacedName, eph, now, effectiveInterval); why != cacheServeNone {
		switch why {
		case cacheServeWindow:
			log.V(1).Info("re-propagating from cached OMMs; GP source not contacted",
				"cacheAge", now.Sub(c.result.FetchedAt).String(), "refreshInterval", effectiveInterval.String())
			outcome = ommFetchOutcome{result: c.result, reusedCache: true}
		case cacheServeBackoff:
			log.V(1).Info("re-propagating from cached OMMs; upstream fetch suppressed by retry backoff",
				"cacheAge", now.Sub(c.result.FetchedAt).String(),
				"nextFetchAttempt", c.nextFetchAttempt.UTC().String(), "lastErr", c.lastFetchErr.Error())
			outcome = ommFetchOutcome{result: c.result, servedCacheOnError: true, cacheServeErr: c.lastFetchErr}
		case cacheServeNone:
		}
	} else {
		// Step 3b: a real fetch is due → select the fetcher (loads credentials) and fetch.
		fetcher, fetcherErr := r.fetcherForSource(ctx, eph)
		if fetcherErr != nil {
			meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
				Type:               ntnv1alpha1.ConditionGPDataFetched,
				Status:             metav1.ConditionFalse,
				Reason:             "FetcherSetupFailed",
				Message:            fetcherErr.Error(),
				ObservedGeneration: eph.Generation,
			})
			// No usable GP data this cycle → readiness 0. Holds across a persistent
			// setup failure so `gp_fetch_ready == 0` alerts even on a never-fetched cold
			// start, where `gp_satellite_count == 0` cannot (absent series).
			ntnmetrics.GPFetchReady.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(0)
			if err := r.Status().Update(ctx, eph); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		outcome, earlyResult, earlyErr = r.obtainOMMs(ctx, req, eph, fetcher, effectiveInterval, now)
	}
	if earlyResult != nil {
		// obtainOMMs only early-returns on no-usable-data outcomes: a refused insecure
		// URL, or a fetch error with no cache to fall back on. Readiness 0 either way
		// (served-cache and fresh/304 do NOT early-return — they reach the 1 below).
		ntnmetrics.GPFetchReady.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(0)
		return *earlyResult, earlyErr
	}
	result := outcome.result
	reusedCache := outcome.reusedCache
	servedCacheOnError := outcome.servedCacheOnError
	cacheServeErr := outcome.cacheServeErr

	// Step 6: Update status with new data.
	//
	// Note: both paths (200 fresh fetch, 304 NotModified) populate
	// SatelliteCount and LastUpdated. The 304 path previously only
	// updated conditions, which silently corrupted status on the
	// edge case where a second CR reuses an already-cached URL
	// while the first CR's status populated the fetcher's in-memory
	// OMM cache (the GPFetchResult on 304 carries the cached OMMs
	// via fetcher.ommCache, so len(result.OMMs) == count is correct).
	// Fixed along with #105's E2E mock which reliably reproduces the
	// case: two sequential CRs with the same URL hit 304 on the
	// second reconcile.
	eph.Status.SatelliteCount = result.SatelliteCount
	eph.Status.LastUpdated = &metav1.Time{Time: result.FetchedAt}
	ntnmetrics.GPSatelliteCount.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(float64(result.SatelliteCount))
	// Reaching here means usable GP data was obtained (fresh fetch, 304, or served
	// cache) — readiness 1. This also clears a prior 0 once the pipeline recovers.
	ntnmetrics.GPFetchReady.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(1)

	// Snapshot (before the switch mutates it) whether serve-from-cache is a NEW
	// episode, so its Warning fires once per outage — not on every short-cadence
	// re-propagation that keeps serving the same stale cache.
	serveCacheEpisode := servedCacheOnError && conditionEpisodeChanged(
		meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched),
		metav1.ConditionFalse, "FetchFailedServingCache", eph.Generation)

	switch {
	case servedCacheOnError:
		log.Info("serving cached OMMs after fetch failure", "satelliteCount", result.SatelliteCount)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:   ntnv1alpha1.ConditionGPDataFetched,
			Status: metav1.ConditionFalse,
			Reason: "FetchFailedServingCache",
			Message: fmt.Sprintf("GP fetch failed (%s); serving %d satellites from cached OMMs — pass windows and runtime push preserved",
				cacheServeErr.Error(), result.SatelliteCount),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             reasonCachedOMMs,
			Message:            "Using cached OMM data from a previous fetch",
			ObservedGeneration: eph.Generation,
		})
	case reusedCache:
		log.V(1).Info("GP data re-propagated from cache (no fetch)", "satelliteCount", result.SatelliteCount)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionTrue,
			Reason:             "CachedRepropagation",
			Message:            fmt.Sprintf("Re-propagated %d satellites from cached OMMs; GP source not contacted within the refresh window", result.SatelliteCount),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             reasonCachedOMMs,
			Message:            "Using cached OMM data from previous fetch",
			ObservedGeneration: eph.Generation,
		})
	case result.NotModified:
		log.Info("GP data unchanged (304 Not Modified)", "satelliteCount", result.SatelliteCount)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataFetched,
			Status:             metav1.ConditionTrue,
			Reason:             "NotModified",
			Message:            fmt.Sprintf("GP data unchanged from %s (304)", eph.Spec.Source.Type),
			ObservedGeneration: eph.Generation,
		})
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionGPDataParsed,
			Status:             metav1.ConditionTrue,
			Reason:             reasonCachedOMMs,
			Message:            "Using cached OMM data from previous fetch",
			ObservedGeneration: eph.Generation,
		})
	default:
		log.Info("Fetched GP data successfully", "satelliteCount", result.SatelliteCount)
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

	// Step 6b: Orbit-regime guard (findings.md B-5). The bundled SGP4 propagator
	// (github.com/akhenakh/sgp4) is near-earth only — it hardcodes
	// use_deep_space_ = false — so an element set whose orbital period is >= 225
	// min (roughly MEO and up) would be propagated into a silently-wrong ECEF.
	// Reject those explicitly here so only the near-earth set reaches pass
	// prediction and the runtime-push propagation below. v1.0 is LEO-only; MEO/GEO
	// support is a v1.1 roadmap item. SplitByOrbitRegime allocates fresh slices, so
	// the cached OMMs stored above are not mutated by reassigning result.OMMs.
	nearEarth, deepSpace := ephemeris.SplitByOrbitRegime(result.OMMs)
	result.OMMs = nearEarth
	// Report only on the tracked (spec.satellites.noradIDs) deep-space sets. A
	// broad/mixed upstream feed (e.g. a CelesTrak group with LEO + MEO + GEO) must
	// not raise UnsupportedOrbitRegime for MEO/GEO birds the operator explicitly
	// excluded via noradIDs — that would be a false alarm on satellites they never
	// intended to propagate. An unset selector tracks the whole feed, so every
	// deep-space set is reported. Propagation is already deep-space-free (result.OMMs
	// above); propagateStates / predictPasses then apply this same noradIDs selector,
	// so only tracked near-earth sats are ever propagated.
	var trackedNorad []int
	if eph.Spec.Satellites != nil {
		trackedNorad = eph.Spec.Satellites.NoradIDs
	}
	trackedDeepSpace := ephemeris.FilterOMMs(deepSpace, trackedNorad)
	orbitRegimeEvent := r.reportOrbitRegime(ctx, eph, len(trackedDeepSpace), ephemeris.DeepSpaceSummary(trackedDeepSpace))
	ntnmetrics.GPDeepSpaceRejectedCount.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(float64(len(trackedDeepSpace)))

	// Step 7: Compute pass predictions if configured; clear stale data if disabled.
	passNote, passFailNote := r.handlePassPrediction(ctx, eph, result)

	// Step 7b: Propagate tracked satellites to a NEAR-now epoch for the runtime
	// ephemeris push (#176). The epoch is only a small lead ahead of now — enough
	// to satisfy OCUDU's "epoch must be in the future" check and the consumer's
	// stale-epoch guard plus reconcile/delivery latency. OCUDU internally
	// re-propagates this state vector from the epoch to each SIB19 Tx time
	// (ntn_assistance_info_generator: propagate(SIB19_Tx - epoch)), so the epoch
	// must be the true reference time of the ECEF. Using now+refreshInterval (as
	// an earlier version did) made OCUDU back-propagate hours from a position that
	// was itself SGP4-propagated hours forward, compounding LEO error; a near-now
	// epoch keeps OCUDU's internal propagation short and forward.
	truncEvent := r.propagateStates(ctx, eph, result, now.Add(propagationEpochLead))

	// Stamp the propagation-input digest these propagatedStates were computed under.
	// Reaching here means the current spec's inputs were (re)propagated — from a fresh
	// fetch OR a valid cache entry for the SAME UPSTREAM FETCH IDENTITY (fetchInputKey;
	// the cache is deliberately NOT generation-keyed). A fetch failure with NO matching cache
	// early-returns via handleFetchError, which deliberately does NOT restamp, so the hash
	// keeps pointing at the LAST inputs that actually produced states. The consumer blocks
	// only when the live spec's propagation inputs differ from this — a source/selector
	// edit whose re-propagate has not yet succeeded — not on a pass-prediction-only edit
	// (#204-G1; #221 review finding 2).
	eph.Status.PropagatedStatesInputHash = propagationInputHash(eph.Spec)

	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}

	// Emit the orbit-regime Warning ONLY after the status persisted, so a failed
	// update does not fire an event for a transition that rolled back (it would
	// re-fire on the next reconcile, defeating the transition gate). This also
	// suppresses a spurious event on a terminating object, whose Status().Update
	// fails NotFound above. (findings.md B-5; deep-review C-#4)
	if orbitRegimeEvent != "" && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, "UnsupportedOrbitRegime", "OrbitRegimeGuard", "%s", orbitRegimeEvent)
	}

	// Same post-persist, transition-gated discipline: emit the StatesTruncated
	// Warning only when propagateStates reported a fresh entry into the truncated
	// state, so the ~3-minute re-propagation cadence never re-fires it.
	if truncEvent != "" && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, "StatesTruncated", "StatesTruncated", "%s", truncEvent)
	}

	// Pass-prediction events, same post-persist + transition-gated discipline (#188):
	// the notes are non-empty only when the PassesPredicted condition changed, so the
	// ~3-minute cache re-propagation never re-emits an identical pass event, and a
	// rolled-back status update fires neither.
	if passNote != "" && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeNormal, "PassesPredicted", "PassesPredicted", "%s", passNote)
	}
	if passFailNote != "" && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, "PredictionFailed", "PredictionFailed", "%s", passFailNote)
	}

	// Emit the "fetched" event only on a real successful fetch — not on the
	// short-cadence cache re-propagations (would flood the stream, #179) and not on
	// a serve-cache-on-error cycle (the fetch failed). The latter emits its own
	// Warning so an operator sees SIB19 is running on stale cache (I-18).
	switch {
	case servedCacheOnError:
		if serveCacheEpisode && r.Recorder != nil {
			r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, "FetchFailedServingCache", "FetchFailedServingCache",
				"GP fetch failed (%s); serving %d satellites from cached OMMs", cacheServeErr.Error(), result.SatelliteCount)
		}
	case !reusedCache:
		if r.Recorder != nil {
			r.Recorder.Eventf(eph, nil, corev1.EventTypeNormal, "GPDataFetched", "GPDataFetched", "Fetched %d satellites", result.SatelliteCount)
		}
	}

	// Requeue on the short PROPAGATION cadence — on EVERY path that produced usable states,
	// including a serve-cache-on-error cycle. The FETCH is held back separately by
	// cachedFetch.nextFetchAttempt (armed in obtainOMMs), so running the reconcile every
	// 3 minutes does NOT re-hit a rate-limited source: cacheServe answers from cache without
	// contacting it.
	//
	// This is the fix for the outage-continuity bug. Previously a serve-cache cycle requeued
	// at the FETCH backoff (classifyFetchError → 2–24h for RateLimited/AuthFailed, or an
	// error for workqueue backoff that ramps to ~1000s). The cycle re-propagated the cached
	// OMMs once to now+propagationEpochLead (5 min) and then nothing ran for hours — so the
	// pushed epoch expired ~5 minutes in and the consumer refused it for the rest of the
	// window. "Serving cache preserves SIB19 continuity" was therefore only true for ONE
	// reconcile. The two cadences are now independent: fetch politeness is enforced on the
	// fetch, and the epoch is kept alive on the propagation heartbeat.
	return ctrl.Result{RequeueAfter: min(effectiveInterval, propagationRefreshInterval)}, nil
}

// maxPropagatedStates caps how many per-satellite propagated states are stored
// in status, to respect etcd object-size limits (~1.5 MB).
const maxPropagatedStates = 128

// propagationEpochLead is how far ahead of now the runtime-push epoch is stamped.
// It must exceed the consumer's epochSkewMargin (10s) with room for reconcile +
// WebSocket delivery latency, yet stay small so OCUDU's internal propagation from
// this epoch is short (a few minutes of SGP4/RK4 divergence is negligible for
// LEO). The push happens on the watch-triggered reconcile right after this
// propagation, so the epoch is fresh at push time.
const propagationEpochLead = 5 * time.Minute

// propagationRefreshInterval is how often the orbit is re-propagated (from cached
// OMMs, WITHOUT a GP fetch) to keep the runtime-push epoch fresh between GP
// refreshes (#179). It must be shorter than propagationEpochLead minus the
// consumer's epochSkewMargin (10s) + delivery latency, so an off-cycle consumer
// reconcile always finds a future epoch. The producer requeues at this cadence;
// each re-propagation updates status.propagatedStates, which fans out (via the
// NTNCellConfig watch) a fresh push to every referencing cell.
const propagationRefreshInterval = 3 * time.Minute

// cachedFetch is a per-CR OMM cache entry. fetchKey+uid pin it to the RAW UPSTREAM
// PAYLOAD identity of the exact object: an edited source.type/URL (fetchKey change) or a
// delete-recreate reusing the same name (uid change) invalidates the entry and forces a
// fresh fetch, so a changed source takes effect immediately rather than after the refresh
// window (#179 regression guard).
//
// It is deliberately NOT keyed on .metadata.generation. Generation bumps on ANY spec edit
// — including passPrediction / refreshInterval, which do not change the upstream payload —
// and dropping the cache on those edits meant a fetch failing right afterwards had NO
// cache to fall back on, so the producer could not re-propagate and SIB19 continuity broke.
// Keying on the fetch inputs keeps last-good OMMs usable across non-fetch edits
// (#221 review finding 2).
type cachedFetch struct {
	result   ephemeris.GPFetchResult
	fetchKey string
	uid      types.UID

	// nextFetchAttempt suppresses the UPSTREAM FETCH until this instant after a failure —
	// INDEPENDENTLY of the reconcile cadence. Previously the fetch backoff WAS the reconcile
	// cadence (RequeueAfter = the 2–24h fetch backoff on a serve-cache cycle), which starved
	// the propagation heartbeat: the served cache was re-propagated once to now+5m and then
	// nothing ran for hours, so the pushed epoch expired ~5 minutes in and SIB19 went stale
	// for the rest of the window. The reconcile now keeps running on the 3-minute propagation
	// cadence (which re-propagates from cache and keeps the epoch fresh) while ONLY the fetch
	// is held back to here — polite to the source AND continuous for the gNB.
	nextFetchAttempt time.Time
	// lastFetchErr is the failure that armed nextFetchAttempt, replayed on each backoff-serve
	// so the degraded status/condition/event stays truthful about WHY we are on cache.
	lastFetchErr error
	// fetchFailures counts consecutive failures, for the transient-error backoff ramp.
	fetchFailures int
}

// fetchRetryDelay is how long to suppress the next UPSTREAM fetch after a failure. It is the
// FETCH cadence only — the reconcile keeps running on the propagation heartbeat regardless,
// so this can be long without starving SIB19.
func fetchRetryDelay(fetchErr error, effectiveInterval time.Duration, failures int) time.Duration {
	switch {
	case errors.Is(fetchErr, ephemeris.ErrRateLimited):
		// Honor the source's policy (and any Retry-After); never faster than the refresh floor.
		return max(effectiveInterval, retryAfterFrom(fetchErr))
	case errors.Is(fetchErr, ephemeris.ErrAuthFailed):
		// Bad credentials will not fix themselves; hammering the login endpoint risks a lockout.
		return effectiveInterval
	default:
		// Transient (network/5xx): ramp 1m, 2m, 4m … capped at the refresh interval. Bounded
		// exponent so the shift cannot overflow.
		shift := min(max(failures-1, 0), 10)
		return min(time.Minute<<shift, effectiveInterval)
	}
}

// cacheServeReason says WHY a reconcile is being served from cache without contacting the source.
type cacheServeReason int

const (
	cacheServeNone    cacheServeReason = iota
	cacheServeWindow                   // inside the GP-refresh window — normal, healthy
	cacheServeBackoff                  // a fetch failed and we are inside its retry backoff — degraded
)

// cacheServe decides whether this reconcile can be answered from the cached OMMs WITHOUT
// contacting the upstream source. It is consulted BEFORE fetcher/credential setup, so a
// reconcile the cache can answer never reads the source Secret — a transient Secret
// unavailability (or a credential rotation) must not break SIB19 continuity, and the cache
// key deliberately excludes credentials (the same URL returns the same GP data whichever
// account fetched it).
func (r *SatelliteEphemerisReconciler) cacheServe(
	key client.ObjectKey, eph *ntnv1alpha1.SatelliteEphemeris, now time.Time, effectiveInterval time.Duration,
) (cachedFetch, cacheServeReason) {
	c, ok := r.cachedOMMResult(key)
	if !ok || c.fetchKey != fetchInputKey(eph.Spec) || c.uid != eph.UID {
		return cachedFetch{}, cacheServeNone
	}
	if now.Sub(c.result.FetchedAt) < effectiveInterval {
		return c, cacheServeWindow
	}
	if now.Before(c.nextFetchAttempt) {
		return c, cacheServeBackoff
	}
	return cachedFetch{}, cacheServeNone
}

// fetchInputKey identifies WHAT RAW upstream payload a cached entry holds: the source type
// and URL. It deliberately EXCLUDES the NORAD selector — FilterOMMs applies that
// client-side to the raw payload, so a selector edit must NOT discard the raw OMMs — and
// every non-fetch field (passPrediction, refreshInterval, credentials: the same URL returns
// the same GP data regardless of which account fetched it). %q-quoted so a crafted URL
// cannot forge another spec's key.
func fetchInputKey(spec ntnv1alpha1.SatelliteEphemerisSpec) string {
	return fmt.Sprintf("type=%q url=%q", spec.Source.Type, spec.Source.URL)
}

// cachedOMMResult returns the cached fetch for key, if any.
func (r *SatelliteEphemerisReconciler) cachedOMMResult(key client.ObjectKey) (cachedFetch, bool) {
	v, ok := r.ommCache.Load(key)
	if !ok {
		return cachedFetch{}, false
	}
	c, ok := v.(cachedFetch)
	return c, ok
}

// maxSatelliteNameLen bounds the externally-sourced OMM ObjectName stored per
// propagated state, so a malformed GP feed cannot bloat the status object.
const maxSatelliteNameLen = 64

// maxEpochAge and maxSourceEpochFutureSkew, and the sourceEpochUsable rule that applies
// them, live in the shared layer (ephemeris_freshness.go) so the producer and the consumer
// cannot drift apart.

// parseOMMEpoch parses an OMM element-set epoch (ISO-8601, with or without a
// fractional second or trailing Z), returning ok=false when unparseable.
func parseOMMEpoch(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05.000000", "2006-01-02T15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// reportEphemerisEpochStale sets the EphemerisEpochStale condition: True when one
// or more propagated satellites' element-set epoch is older than maxEpochAge, so
// the pushed ECEF is derived from stale elements and drifting (I-17). Condition-
// only (no event) — the durable signal for alerting; the 3-minute re-propagation
// cadence would make an event noisy.
func (r *SatelliteEphemerisReconciler) reportEphemerisEpochStale(eph *ntnv1alpha1.SatelliteEphemeris, stale, total int) {
	c := metav1.Condition{
		Type:               ntnv1alpha1.ConditionEphemerisEpochStale,
		Status:             metav1.ConditionFalse,
		Reason:             "EpochsFresh",
		Message:            "All propagated element sets are within the freshness bound",
		ObservedGeneration: eph.Generation,
	}
	if stale > 0 {
		c.Status = metav1.ConditionTrue
		c.Reason = "EpochStale"
		c.Message = fmt.Sprintf("%d of %d tracked element set(s) have an epoch older than %s; the pushed ECEF is derived from stale elements and is drifting",
			stale, total, maxEpochAge)
	}
	meta.SetStatusCondition(&eph.Status.Conditions, c)
	ntnmetrics.EphemerisEpochStaleCount.With(prometheus.Labels{"namespace": eph.Namespace, "ephemeris": eph.Name}).Set(float64(stale))
}

// propagationInputHash is a stable digest of the SatelliteEphemeris spec fields that
// determine WHICH orbital data is fetched and WHICH satellites are propagated — the
// source (type + url) and the NORAD selector. It deliberately EXCLUDES every field that
// does not change the propagated ECEF (pass-prediction params, refreshInterval,
// credentials, the deprecated constellation), so a pass-prediction-only edit does NOT
// falsely invalidate valid propagated states and break last-good continuity during an
// upstream outage (#221 review, finding 2). The producer stamps it on every successful
// (re)propagation; the runtime-push consumer recomputes it from the live spec and blocks
// only when the propagation-relevant inputs have actually changed.
func propagationInputHash(spec ntnv1alpha1.SatelliteEphemerisSpec) string {
	var norad []int
	if spec.Satellites != nil {
		norad = append(norad, spec.Satellites.NoradIDs...)
	}
	// Canonicalize: sort AND de-duplicate. FilterOMMs applies the selector through a map, so
	// [25544] and [25544, 25544] select exactly the same satellites and yield byte-identical
	// propagated output — they must therefore hash the SAME. Without Compact, adding a
	// duplicate NORAD ID changed the hash, which the consumer read as EphemerisInputsStale
	// and answered with a needless re-propagation + runtime re-push.
	sort.Ints(norad)
	norad = slices.Compact(norad)
	h := sha256.New()
	// Quote the user-controlled strings (%q) so a crafted source.url can't embed the field
	// delimiters and collide with a different spec's serialization. hash.Hash.Write never
	// returns an error (documented); ignore it explicitly.
	_, _ = fmt.Fprintf(h, "type=%q\nurl=%q\nnorad=%v\n", spec.Source.Type, spec.Source.URL, norad)
	return hex.EncodeToString(h.Sum(nil))
}

// propagateStates propagates the tracked satellites' orbits (SGP4) to the given
// future epoch and records the ECEF state vectors in status.propagatedStates for
// downstream runtime ephemeris push (#176). The set is filtered by
// spec.satellites.noradIDs and capped; un-propagatable satellites are skipped.
func (r *SatelliteEphemerisReconciler) propagateStates(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	result ephemeris.GPFetchResult,
	epoch time.Time,
) string {
	var norad []int
	if eph.Spec.Satellites != nil {
		norad = eph.Spec.Satellites.NoradIDs
	}
	omms := ephemeris.FilterOMMs(result.OMMs, norad)
	epochMs := epoch.UnixMilli()

	// I-17: count tracked element sets whose OWN epoch is stale — a data property
	// independent of whether SGP4 propagation then succeeds.
	now := epoch.Add(-propagationEpochLead) // epoch is now+lead; recover ~now
	staleEpochs := 0
	for i := range omms {
		if t, ok := parseOMMEpoch(omms[i].EpochStr); ok && now.Sub(t) > maxEpochAge {
			staleEpochs++
		}
	}

	states := make([]ntnv1alpha1.PropagatedState, 0, min(len(omms), maxPropagatedStates))
	skippedByCap := 0
	rejectedEpochs := 0 // element sets refused BEFORE SGP4: unparseable or implausibly future
	for i := range omms {
		if len(states) >= maxPropagatedStates {
			// The cap is reached; the remaining OMMs are NOT attempted. This is the
			// truthful count dropped by the cap — propagation failures above already
			// reduced the successful set, so len(omms)-cap would over-report a
			// truncation that never actually happened.
			skippedByCap = len(omms) - i
			logf.FromContext(ctx).Info("propagated-state list capped; some satellites omitted from runtime-push status",
				"selected", len(omms), "cap", maxPropagatedStates, "omitted", skippedByCap,
				"hint", "set spec.satellites.noradIDs to the satellites pushed at runtime")
			break
		}
		// Validate the SOURCE element-set epoch BEFORE propagating, using the SHARED rule
		// against the SAME `now` the consumer will use (ephemeris_freshness.go). Previously
		// this compared against the propagation TARGET epoch (now + 5m) while the consumer
		// compared against now, so a source epoch inside that 5-minute band was propagated
		// and hash-stamped here yet permanently refused at the push — a state that could
		// never be delivered.
		//
		// An UNPARSEABLE epoch now SKIPS the satellite rather than emitting a state with
		// SourceEpochUnixMs = 0. That 0 was an ambiguous sentinel (it also means the legal
		// instant 1970-01-01T00:00:00Z), and the consumer's fail-open on it let a 1970 epoch
		// bypass the whole freshness gate. Nothing is lost: SGP4's own OMM.ToTLE parses the
		// same epoch, so an element set we cannot parse fails PropagateToECEF below anyway.
		t, ok := parseOMMEpoch(omms[i].EpochStr)
		if !ok {
			rejectedEpochs++
			logf.FromContext(ctx).Info("skipping satellite: source element epoch is unparseable",
				"norad", omms[i].NoradCatID, "epochStr", omms[i].EpochStr)
			continue
		}
		// PLAUSIBILITY only. A STALE (merely old) element set is deliberately still
		// propagated and merely counted (EphemerisEpochStale, I-17) so a drifting feed stays
		// observable; the consumer owns the delivery gate (#200-C4). Rejecting staleness here
		// would collapse a precise "EphemerisStale" diagnosis into a bare "PayloadMissing".
		if err := sourceEpochPlausible(now, t); err != nil {
			rejectedEpochs++
			logf.FromContext(ctx).Info("skipping satellite: implausible source element epoch",
				"norad", omms[i].NoradCatID, "sourceEpoch", t.UTC(), "err", err.Error())
			continue
		}
		srcEpochMs := t.UnixMilli()

		ecef, err := ephemeris.PropagateToECEF(omms[i], epoch)
		if err != nil {
			continue // skip satellites whose SGP4 propagation fails / goes out of range
		}
		// ObjectName comes from external GP data; bound it so a malformed/huge
		// name can't bloat the status object past the etcd size limit.
		name := omms[i].ObjectName
		if len(name) > maxSatelliteNameLen {
			name = name[:maxSatelliteNameLen]
		}
		states = append(states, ntnv1alpha1.PropagatedState{
			Satellite:         name,
			NoradID:           omms[i].NoradCatID,
			EpochUnixMs:       epochMs,
			SourceEpochUnixMs: srcEpochMs,
			ECEF:              *ecef,
		})
	}
	eph.Status.PropagatedStates = states
	truncEvent := r.reportStatesTruncated(eph, len(omms), skippedByCap)
	r.reportEphemerisEpochStale(eph, staleEpochs, len(omms))
	r.reportSourceEpochRejected(eph, rejectedEpochs, len(omms))
	return truncEvent
}

// reportSourceEpochRejected surfaces element sets the producer refused BEFORE SGP4 — an
// unparseable or implausibly future-dated OMM EPOCH. Without a durable condition the guard is
// invisible: the satellite simply has no propagated state, and a cell selecting it reports a
// bare "EphemerisPayloadMissing" with no way to tell a corrupt feed from a deep-space
// rejection or a NORAD typo (the deep-space case already gets a cross-CR hint; this one had
// none — it only wrote a log line).
func (r *SatelliteEphemerisReconciler) reportSourceEpochRejected(
	eph *ntnv1alpha1.SatelliteEphemeris, rejected, total int,
) {
	c := metav1.Condition{
		Type:               ntnv1alpha1.ConditionSourceEpochRejected,
		Status:             metav1.ConditionFalse,
		Reason:             "SourceEpochsPlausible",
		Message:            "Every tracked element set has a parseable, plausibly-dated epoch",
		ObservedGeneration: eph.Generation,
	}
	if rejected > 0 {
		c.Status = metav1.ConditionTrue
		c.Reason = "ImplausibleSourceEpoch"
		c.Message = fmt.Sprintf(
			"%d of %d tracked element set(s) were REFUSED before propagation: the OMM EPOCH is unparseable "+
				"or implausibly future-dated (more than %s ahead). They produce no propagated state, so a cell "+
				"selecting one reports EphemerisPayloadMissing. Check the GP source for corrupt data.",
			rejected, total, maxSourceEpochFutureSkew)
	}
	meta.SetStatusCondition(&eph.Status.Conditions, c)
}

// reportStatesTruncated records status.truncatedSatelliteCount and the
// StatesTruncated condition, and RETURNS the Warning-event note the caller must
// emit only AFTER a successful Status().Update ("" = nothing). Like
// reportOrbitRegime, the note is non-empty only on the FIRST transition into the
// truncated state, so the short re-propagation cadence (#179) does not re-emit the
// event on every reconcile; deferring it to post-persist keeps a rolled-back
// update from firing (and re-firing) an event for a transition that never landed.
func (r *SatelliteEphemerisReconciler) reportStatesTruncated(eph *ntnv1alpha1.SatelliteEphemeris, selected, truncated int) string {
	eph.Status.TruncatedSatelliteCount = truncated
	if truncated == 0 {
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionStatesTruncated,
			Status:             metav1.ConditionFalse,
			Reason:             "WithinCap",
			Message:            "All selected satellites fit within the propagated-state cap",
			ObservedGeneration: eph.Generation,
		})
		return ""
	}
	// Read the persisted condition before mutating it, so the note is non-empty
	// once per entry into the truncated state rather than every reconcile.
	firstTransition := !meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionStatesTruncated)
	msg := fmt.Sprintf("selected %d satellites but the propagated-state cap is %d; %d omitted from "+
		"runtime-push status — narrow spec.satellites.noradIDs or the source URL GROUP=",
		selected, maxPropagatedStates, truncated)
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionStatesTruncated,
		Status:             metav1.ConditionTrue,
		Reason:             "PropagatedStateCapExceeded",
		Message:            msg,
		ObservedGeneration: eph.Generation,
	})
	if firstTransition {
		return msg
	}
	return ""
}

// reportOrbitRegime records the UnsupportedOrbitRegime condition and RETURNS the
// Warning-event note the caller must emit only AFTER a successful Status().Update
// ("" = emit nothing). Element sets whose orbital period is >=
// ephemeris.DeepSpacePeriodMinutes need the deep-space (SDP4) model this operator
// does not implement, so they are rejected upstream rather than propagated into a
// wrong ECEF (findings.md B-5). rejectedCount and summary come from the caller's
// ephemeris.SplitByOrbitRegime / DeepSpaceSummary, keeping this method free of the
// sgp4 element type.
//
// The event is DEFERRED to post-persist (returned, not emitted here) so a failed
// Status().Update cannot fire an event for a transition that never persisted —
// otherwise the next reconcile, re-reading the old status, would re-fire it and
// defeat the transition gate. This mirrors handleFetchError's update-then-event
// ordering. The note is non-empty only on the first transition into the rejected
// state, so the short re-propagation cadence (#179) does not flood the stream.
func (r *SatelliteEphemerisReconciler) reportOrbitRegime(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	rejectedCount int,
	summary string,
) string {
	if rejectedCount == 0 {
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionUnsupportedOrbitRegime,
			Status:             metav1.ConditionFalse,
			Reason:             "AllNearEarth",
			Message:            "No deep-space element sets among the tracked satellites",
			ObservedGeneration: eph.Generation,
		})
		return ""
	}

	// Read the persisted state before mutating it, so the returned note is
	// non-empty once per entry into the rejected state rather than every reconcile.
	firstTransition := !meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionUnsupportedOrbitRegime)
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionUnsupportedOrbitRegime,
		Status:             metav1.ConditionTrue,
		Reason:             "DeepSpaceElementsRejected",
		Message:            summary,
		ObservedGeneration: eph.Generation,
	})
	logf.FromContext(ctx).Info("rejected deep-space element sets; near-earth SGP4 cannot propagate them",
		"rejected", rejectedCount, "detail", summary)
	if firstTransition {
		return summary
	}
	return ""
}

// handlePassPrediction runs pass prediction (when configured) and returns the
// post-persist event notes: passNote (Normal, success) and passFailNote (Warning,
// failure), each non-empty only on a real PassesPredicted-condition transition, so
// the caller emits them at most once per change after the status write persists.
func (r *SatelliteEphemerisReconciler) handlePassPrediction(
	ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris, result ephemeris.GPFetchResult,
) (passNote, passFailNote string) {
	if eph.Spec.PassPrediction == nil || len(eph.Spec.PassPrediction.GroundStations) == 0 {
		// Pass prediction not configured; clear stale pass windows and condition.
		eph.Status.NextPassWindows = nil
		meta.RemoveStatusCondition(&eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)
		return "", ""
	}
	note, err := r.predictPasses(ctx, eph, result)
	if err != nil {
		logf.FromContext(ctx).Error(err, "pass prediction failed")
		eph.Status.NextPassWindows = nil // clear stale pass data
		// Gate on the failure EPISODE (Status/Reason/Generation, ignoring the varying
		// error Message) and defer the event to after the status persists (like
		// reportOrbitRegime), so a rolled-back status update does not fire — and a
		// message-only change does not re-fire — a PredictionFailed event.
		failEpisode := conditionEpisodeChanged(
			meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted),
			metav1.ConditionFalse, "PredictionFailed", eph.Generation)
		meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionPassesPredicted,
			Status:             metav1.ConditionFalse,
			Reason:             "PredictionFailed",
			Message:            err.Error(),
			ObservedGeneration: eph.Generation,
		})
		if failEpisode {
			return "", err.Error()
		}
		return "", ""
	}
	return note, ""
}

// predictPasses resolves ground stations and computes pass windows. It sets the
// PassesPredicted condition and RETURNS the Normal-event note the caller must emit
// only AFTER a successful Status().Update; the note is non-empty ONLY when the
// condition actually transitioned, so the ~3-minute cache re-propagation cadence
// does not re-fire an identical PassesPredicted event (#179/#188).
func (r *SatelliteEphemerisReconciler) predictPasses(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	fetchResult ephemeris.GPFetchResult,
) (string, error) {
	log := logf.FromContext(ctx)
	pp := eph.Spec.PassPrediction

	// Resolve ground station coordinates.
	var stations []ephemeris.GroundStation
	for _, gsName := range pp.GroundStations {
		gs := &ntnv1alpha1.GroundStationLifecycle{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: eph.Namespace, Name: gsName}, gs); err != nil {
			return "", fmt.Errorf("ground station %q not found: %w", gsName, err)
		}
		lat, err := ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Lat)
		if err != nil {
			return "", fmt.Errorf("invalid latitude for %q: %w", gsName, err)
		}
		lon, err := ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Lon)
		if err != nil {
			return "", fmt.Errorf("invalid longitude for %q: %w", gsName, err)
		}
		alt := 0.0
		if gs.Spec.Deployment.Location.Alt != "" {
			alt, err = ephemeris.ParseGeoCoord(gs.Spec.Deployment.Location.Alt)
			if err != nil {
				return "", fmt.Errorf("invalid altitude for %q: %w", gsName, err)
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
		return "", fmt.Errorf("invalid minElevation: %w", err)
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
	passes, err := ephemeris.PredictPasses(fetchResult.OMMs, stations, minEl, horizon, noradFilter, fetchResult.FetchedAt)
	if err != nil {
		return "", fmt.Errorf("computing passes: %w", err)
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

	msg := fmt.Sprintf("Computed %d passes over %d ground stations", len(windows), len(stations))
	changed := meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionPassesPredicted,
		Status:             metav1.ConditionTrue,
		Reason:             "PredictionSucceeded",
		Message:            msg,
		ObservedGeneration: eph.Generation,
	})
	if changed {
		return msg, nil
	}
	return "", nil
}

// publicHTTPSource reports whether raw is a cleartext http:// URL whose host
// resolves to a public IP, and if so returns that IP. Cleartext http to a
// private/in-cluster host (a NetworkPolicy-protected mirror or an E2E mock) is
// permitted; https is always fine. It fails OPEN when the host cannot be
// resolved (returns false): the fetch's own SSRF-safe client still blocks
// private-IP dials, and a public host that momentarily fails DNS is re-checked
// on the next reconcile — so a transient resolver blip never wedges a valid
// https source, and never silently downgrades a public one for long. A literal
// public IP (e.g. http://203.0.113.5) resolves to itself and is rejected;
// a literal private IP is allowed (findings.md I-21).
func (r *SatelliteEphemerisReconciler) publicHTTPSource(ctx context.Context, raw string) (string, bool) {
	u, err := neturl.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "http" {
		return "", false
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return "", false // cannot classify — fail open
	}
	for _, ip := range ips {
		if !netutil.IsPrivateIP(ip.IP) {
			return ip.IP.String(), true
		}
	}
	return "", false
}

// handleInsecureURL records the InsecureURL condition + a Warning event for a
// rejected cleartext-http public source and requeues without fetching (I-21).
func (r *SatelliteEphemerisReconciler) handleInsecureURL(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	ip string,
) (ctrl.Result, error) {
	msg := fmt.Sprintf(
		"cleartext http source resolves to public IP %s; use https to prevent forged OMM injection into SIB19",
		ip)
	insecureEpisode := conditionEpisodeChanged(
		meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched),
		metav1.ConditionFalse, "InsecureURL", eph.Generation)
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataFetched,
		Status:             metav1.ConditionFalse,
		Reason:             "InsecureURL",
		Message:            msg,
		ObservedGeneration: eph.Generation,
	})
	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}
	// Gate on the episode so a permanently-insecure source emits one Warning per
	// episode, not one per (minutely) requeue.
	if insecureEpisode && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, "InsecureURL", "InsecureURL", "%s", msg)
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// handleFetchError records the error as a Condition and Event, then requeues.
func (r *SatelliteEphemerisReconciler) handleFetchError(
	ctx context.Context,
	eph *ntnv1alpha1.SatelliteEphemeris,
	fetchErr error,
	effectiveInterval time.Duration,
) (ctrl.Result, error) {
	reason, requeueAfter, returnAsError := classifyFetchError(fetchErr, effectiveInterval)

	// Gate the Warning on the fetch-failure EPISODE (Status/Reason/Generation,
	// ignoring the varying error Message) so a persistent outage — whose message
	// flaps between e.g. "timeout after 30.001s" and "…30.013s" — emits one Warning
	// per episode, not one per requeue.
	fetchEpisode := conditionEpisodeChanged(
		meta.FindStatusCondition(eph.Status.Conditions, ntnv1alpha1.ConditionGPDataFetched),
		metav1.ConditionFalse, reason, eph.Generation)

	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataFetched,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            fetchErr.Error(),
		ObservedGeneration: eph.Generation,
	})
	meta.SetStatusCondition(&eph.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionGPDataParsed,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            "No data to parse due to fetch failure",
		ObservedGeneration: eph.Generation,
	})

	// Clear stale pass windows to prevent NTNSlice from using outdated data
	// for failover readiness decisions.
	eph.Status.NextPassWindows = nil
	meta.RemoveStatusCondition(&eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted)

	if err := r.Status().Update(ctx, eph); err != nil {
		return ctrl.Result{}, err
	}

	if fetchEpisode && r.Recorder != nil {
		r.Recorder.Eventf(eph, nil, corev1.EventTypeWarning, reason, reason, "%s", fetchErr.Error())
	}

	if returnAsError {
		return ctrl.Result{}, fetchErr
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// retryAfterFrom extracts a server-supplied Retry-After delay from a rate-limit
// error, or 0 if none. Lets a fronting proxy's Retry-After override the default
// refresh-cadence requeue.
func retryAfterFrom(err error) time.Duration {
	var rle *ephemeris.RateLimitError
	if errors.As(err, &rle) {
		return rle.RetryAfter
	}
	return 0
}

// reasonCachedOMMs is the GPDataParsed condition reason when serving OMMs from the
// in-memory cache rather than a fresh parse.
const reasonCachedOMMs = "CachedOMMs"

// ommFetchOutcome is how a reconcile obtained its OMM data: a cache re-propagation
// within the refresh window (reusedCache), a fresh fetch, or — on fetch failure with
// a still-valid cache — a degraded serve-from-cache (servedCacheOnError). See obtainOMMs.
type ommFetchOutcome struct {
	result             ephemeris.GPFetchResult
	reusedCache        bool
	servedCacheOnError bool
	cacheServeErr      error
}

// obtainOMMs resolves the OMM data for this reconcile (Step 4b/5). It returns the
// outcome plus an optional early Reconcile result: non-nil when the caller must
// return immediately — an insecure-URL rejection (I-21) or a cold-cache fetch
// failure (the wipe path). Within the refresh window it re-propagates from cache
// without contacting the source (#179); otherwise it fetches, falling back to a
// still-valid cache on error rather than blackholing SIB19 (I-18). The refresh-
// window age check is intentionally NOT applied to the error fallback — we fetched
// because the window elapsed, and the served cache's staleness is surfaced by the
// I-17 EphemerisEpochStale condition.
func (r *SatelliteEphemerisReconciler) obtainOMMs(
	ctx context.Context, req ctrl.Request, eph *ntnv1alpha1.SatelliteEphemeris,
	fetcher ephemeris.GPFetcher, effectiveInterval time.Duration, now time.Time,
) (ommFetchOutcome, *ctrl.Result, error) {
	log := logf.FromContext(ctx)
	fetchKey := fetchInputKey(eph.Spec)
	// (The cache-serve decision — refresh window OR fetch-retry backoff — is made by
	// cacheServe BEFORE fetcher/credential setup, so obtainOMMs is only reached when a real
	// upstream fetch is actually due.)

	// Refuse a cleartext http:// source that resolves to a PUBLIC IP (I-21).
	if ip, insecure := r.publicHTTPSource(ctx, eph.Spec.Source.URL); insecure {
		res, err := r.handleInsecureURL(ctx, eph, ip)
		return ommFetchOutcome{}, &res, err
	}

	fetchStart := time.Now()
	result, fetchErr := fetcher.Fetch(ctx, eph.Spec.Source.URL)
	fetchStatus := "success"
	if fetchErr != nil {
		fetchStatus = "error"
	}
	ntnmetrics.GPFetchDuration.With(prometheus.Labels{"source_type": eph.Spec.Source.Type, "status": fetchStatus}).Observe(time.Since(fetchStart).Seconds())

	if fetchErr != nil {
		// Fall back to the last-good OMMs for THIS source (fetchKey), not this spec
		// revision: a passPrediction/refreshInterval edit must not strand the fallback
		// and break re-propagation during an upstream outage (#221 review finding 2).
		if c, ok := r.cachedOMMResult(req.NamespacedName); ok &&
			c.fetchKey == fetchKey && c.uid == eph.UID {
			// ARM THE FETCH BACKOFF on the cache entry. This — not RequeueAfter — is what
			// keeps us polite to the source, which frees the reconcile to keep running on the
			// 3-minute propagation cadence and keep the pushed epoch alive for the whole
			// outage. (Before this, the serve-cache cycle requeued at the 2–24h fetch backoff,
			// so the epoch expired ~5 minutes in and SIB19 went stale for the rest of it.)
			c.fetchFailures++
			c.nextFetchAttempt = now.Add(fetchRetryDelay(fetchErr, effectiveInterval, c.fetchFailures))
			c.lastFetchErr = fetchErr
			r.ommCache.Store(req.NamespacedName, c)
			log.Info("GP fetch failed; serving last cached OMMs for SIB19 continuity",
				"err", fetchErr.Error(), "cacheAge", now.Sub(c.result.FetchedAt).String(),
				"nextFetchAttempt", c.nextFetchAttempt.UTC().String(), "consecutiveFailures", c.fetchFailures)
			return ommFetchOutcome{result: c.result, servedCacheOnError: true, cacheServeErr: fetchErr}, nil, nil
		}
		// No cache to serve: nothing to keep fresh, so the fetch backoff IS the reconcile
		// cadence here (the wipe path) — unchanged.
		res, err := r.handleFetchError(ctx, eph, fetchErr, effectiveInterval)
		return ommFetchOutcome{}, &res, err
	}

	// A successful fetch clears the backoff.
	r.ommCache.Store(req.NamespacedName, cachedFetch{result: result, fetchKey: fetchKey, uid: eph.UID})
	return ommFetchOutcome{result: result}, nil, nil
}

// classifyFetchError chooses the retry strategy for a failed GP fetch (findings.md
// I-19b), shared by the cache-wipe and the serve-cache (I-18) paths so both back off
// identically:
//   - rate-limited: an EXPECTED polite-backoff state (not a controller error).
//     Requeue at the slow refresh cadence (honoring any Retry-After) — never hammer
//     the source into a firewall block / account suspension.
//   - auth failure: a persistent config error; slow requeue, surfaced by condition.
//   - anything else (timeout, DNS, 5xx, parse): a genuine transient error — return it
//     (returnAsError) so the controller-runtime workqueue applies exponential backoff
//     (5ms→1000s) and a persistent failure surfaces on the reconcile-error alert.
func classifyFetchError(fetchErr error, effectiveInterval time.Duration) (reason string, requeueAfter time.Duration, returnAsError bool) {
	switch {
	case errors.Is(fetchErr, ephemeris.ErrRateLimited):
		return "RateLimited", max(effectiveInterval, retryAfterFrom(fetchErr)), false
	case errors.Is(fetchErr, ephemeris.ErrAuthFailed):
		return "AuthFailed", effectiveInterval, false
	default:
		return "FetchFailed", 0, true
	}
}

// fetcherForSource returns the appropriate GPFetcher for the source type.
// For SpaceTrack, it also loads credentials from the referenced Secret.
func (r *SatelliteEphemerisReconciler) fetcherForSource(
	ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris,
) (ephemeris.GPFetcher, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("selecting fetcher", "sourceType", eph.Spec.Source.Type)
	switch eph.Spec.Source.Type {
	case "CelesTrak":
		if r.Fetcher == nil {
			return nil, fmt.Errorf("CelesTrak fetcher is not configured")
		}
		return r.Fetcher, nil

	case "SpaceTrack":
		if r.SpaceTrackFetcher == nil {
			return nil, fmt.Errorf("SpaceTrack fetcher is not configured")
		}
		// Load credentials from Secret.
		creds := eph.Spec.Source.Credentials
		if creds == nil {
			return nil, fmt.Errorf("SpaceTrack requires credentials; set spec.source.credentials")
		}
		secret := &corev1.Secret{}
		secretKey := types.NamespacedName{Namespace: eph.Namespace, Name: creds.Name}
		// Read the Secret UNCACHED (APIReader) so a secrets-get RBAC suffices and the
		// operator does not cache every Secret in the cluster. Fall back to the cached
		// client only if no APIReader was wired (e.g. some tests).
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		if err := reader.Get(ctx, secretKey, secret); err != nil {
			return nil, fmt.Errorf("reading credentials Secret %q: %w", creds.Name, err)
		}
		key := creds.Key
		if key == "" {
			key = "password"
		}
		password, ok := secret.Data[key]
		if !ok {
			return nil, fmt.Errorf("secret %q does not contain key %q", creds.Name, key)
		}
		username, ok := secret.Data["username"]
		if !ok {
			return nil, fmt.Errorf("secret %q does not contain key %q", creds.Name, "username")
		}
		// Return a credentials-bound fetcher to prevent interleaving
		// when multiple CRs share the SpaceTrackFetcher instance.
		return &boundSpaceTrackFetcher{
			fetcher:  r.SpaceTrackFetcher,
			username: string(username),
			password: string(password),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported source type %q", eph.Spec.Source.Type)
	}
}

// boundSpaceTrackFetcher wraps SpaceTrackFetcher with request-scoped credentials.
// FetchWithCredentials serializes login+fetch under a mutex, preventing
// cookie/session interleaving across concurrent reconciles.
type boundSpaceTrackFetcher struct {
	fetcher  *ephemeris.SpaceTrackFetcher
	username string
	password string
}

func (b *boundSpaceTrackFetcher) Fetch(ctx context.Context, url string) (ephemeris.GPFetchResult, error) {
	return b.fetcher.FetchWithCredentials(ctx, url, b.username, b.password)
}

// SetupWithManager sets up the controller with the Manager.
// Watches GroundStationLifecycle changes to trigger pass re-prediction
// when ground stations are added, modified, or deleted.
func (r *SatelliteEphemerisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// reconcileTriggerPredicate (predicates.go) filters this controller's own
		// status writes (a fresh push epoch every reconcile) so they don't
		// re-enqueue it, while still delivering spec changes and deletions. The
		// GroundStationLifecycle Watches below deliberately have NO predicate,
		// since fan-in must react to status changes.
		For(&ntnv1alpha1.SatelliteEphemeris{}, builder.WithPredicates(reconcileTriggerPredicate())).
		Watches(&ntnv1alpha1.GroundStationLifecycle{},
			handler.EnqueueRequestsFromMapFunc(r.groundStationToEphemeris),
		).
		Named("satelliteephemeris").
		WithOptions(ctrlrt.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
		Complete(r)
}

// groundStationToEphemeris maps a GroundStationLifecycle change to all
// SatelliteEphemeris resources that reference it in passPrediction.groundStations.
func (r *SatelliteEphemerisReconciler) groundStationToEphemeris(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	gs, ok := obj.(*ntnv1alpha1.GroundStationLifecycle)
	if !ok {
		return nil
	}

	log := logf.FromContext(ctx)

	var ephList ntnv1alpha1.SatelliteEphemerisList
	if err := r.List(ctx, &ephList, client.InNamespace(gs.Namespace)); err != nil {
		log.Error(err, "failed to list SatelliteEphemeris for ground station mapper")
		return nil
	}

	var requests []reconcile.Request
	for _, eph := range ephList.Items {
		if eph.Spec.PassPrediction == nil {
			continue
		}
		if slices.Contains(eph.Spec.PassPrediction.GroundStations, gs.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      eph.Name,
					Namespace: eph.Namespace,
				},
			})
		}
	}
	return requests
}
