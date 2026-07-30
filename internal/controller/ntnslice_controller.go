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
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/prometheus/client_golang/prometheus"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/netutil"
	"github.com/thc1006/ntn-operators/pkg/slice"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
)

const sliceRequeueInterval = 30 * time.Second

// metricsMaxStaleness is how old a cached path-quality metric may be before the
// failover engine stops trusting it for a switch decision. Beyond this bound the
// reconcile fails static — it holds the current path (the orbital pass-window
// signal is still honored) rather than act on possibly-hours-old telemetry
// (findings.md B-4). Sized as a small multiple of a typical Prometheus scrape.
const metricsMaxStaleness = 90 * time.Second

// Reason labels for the FailoverReady=Unknown path. Matched on by
// kubectl describe and dashboard filters, so keep them stable.
const (
	reasonMetricsReaderError = "MetricsReaderError"
	reasonMetricsUnavailable = "MetricsUnavailable"
	reasonEndpointNotAllowed = "EndpointNotAllowed"
)

// metricsReaderProvider is the subset of *slicemetrics.Provider the reconciler
// needs. Extracted as an interface so tests can inject a reader that returns a
// controlled Result (e.g. a value stale beyond the freshness bound) without a
// live Prometheus or the staleCache's own clock. *slicemetrics.Provider satisfies
// it, so production wiring is unchanged.
type metricsReaderProvider interface {
	For(ns *ntnv1alpha1.NTNSlice) (slicemetrics.Reader, error)
	Evict(key client.ObjectKey)
}

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
	ReaderProvider metricsReaderProvider

	defaultProviderOnce sync.Once
	defaultProvider     metricsReaderProvider

	// flapState holds per-slice in-memory anti-flap state (the consecutive-degraded
	// counter and the recovery/switchback clocks). The consecutive-degraded counter and
	// the recovery clock are intentionally NOT persisted: losing them on a restart or
	// leader-election handoff only re-requires confirmation before the next switch — a
	// DELAY (safe), never a spurious switch. The min-dwell clock (LastSwitchback) IS the
	// exception — its loss ADVANCES a switch — so it is mirrored to .status.lastSwitchbackTime
	// and monotonically merged with it (adopt status when it is newer; repair status when it is
	// behind). min-dwell therefore survives a restart/handoff ONCE this version has durably
	// recorded at least one quality-driven switchback; a switchback recorded only by a pre-field
	// (older) version is not recoverable. Guarded by flapMu; entries are released in
	// handleSliceFinalizer on CR deletion so the map cannot leak.
	flapMu    sync.Mutex
	flapState map[types.NamespacedName]flapStateEntry
}

// flapStateEntry pairs a slice's anti-flap state with the UID of the object it belongs to. A
// same-name NTNSlice recreated with a NEW UID must not inherit the deleted object's counters or
// min-dwell clock. The NotFound and finalizer cleanups usually drop the old entry first, but a
// force-deletion (finalizer bypassed) whose DELETE the workqueue coalesces with the recreate's
// CREATE is delivered as a single reconcile of the new object — the controller never observes the
// intervening NotFound. Keying on UID as well as name closes that gap regardless of the cleanups.
type flapStateEntry struct {
	uid   types.UID
	state slice.AntiFlapState
}

// loadFlapState returns the anti-flap state for the object identified by (key, uid), or the zero
// value when no entry exists OR the stored entry belongs to a different object identity (a stale
// entry left by a same-name predecessor, which is evicted here).
func (r *NTNSliceReconciler) loadFlapState(key types.NamespacedName, uid types.UID) slice.AntiFlapState {
	r.flapMu.Lock()
	defer r.flapMu.Unlock()
	entry, ok := r.flapState[key]
	if !ok || entry.uid != uid {
		delete(r.flapState, key)
		return slice.AntiFlapState{}
	}
	return entry.state
}

// storeFlapState persists the updated anti-flap state for the object identified by (key, uid).
func (r *NTNSliceReconciler) storeFlapState(key types.NamespacedName, uid types.UID, st slice.AntiFlapState) {
	r.flapMu.Lock()
	defer r.flapMu.Unlock()
	if r.flapState == nil {
		r.flapState = make(map[types.NamespacedName]flapStateEntry)
	}
	r.flapState[key] = flapStateEntry{uid: uid, state: st}
}

// seedLastSwitchbackFromStatus does a monotonic merge of the durable min-dwell clock into the
// in-memory one: it adopts status.lastSwitchbackTime whenever that is NEWER than the in-memory
// value — a cold cache after a restart/leader handoff (memory zero), or memory that lagged a
// durable write. It never moves the clock backwards, and it ignores a status value dated in the
// FUTURE (clock skew across nodes, or a rollback) so a bad timestamp cannot lock terrestrial
// indefinitely. Only this clock is durable; the other two are safe to lose (their loss only
// DELAYS a switch).
func (r *NTNSliceReconciler) seedLastSwitchbackFromStatus(
	af *slice.AntiFlapState, ns *ntnv1alpha1.NTNSlice, now time.Time,
) {
	p := ns.Status.LastSwitchbackTime
	if p == nil {
		return
	}
	if p.After(now) {
		// A future-dated durable value is untrustworthy: the controller only ever writes values
		// <= now, so a future one means a backward clock step (NTP, VM migration) or an external
		// status edit. Refuse to enforce it AND clear it durably. Merely ignoring it (the old
		// behaviour) left it in status to become valid "history" once the wall clock passed it,
		// where the next reconcile would seed it as a min-dwell clock and block a legitimate
		// failover with a ghost dwell. Clearing is fail-open, consistent with the anti-flap design;
		// the nil write rides the reconcile's existing status update, and a real switchback simply
		// re-persists a fresh value.
		ns.Status.LastSwitchbackTime = nil
		return
	}
	if !p.After(af.LastSwitchback) {
		return
	}
	af.LastSwitchback = p.Time
}

// persistLastSwitchbackToStatus mirrors the in-memory min-dwell clock into status when the durable
// value is BEHIND memory (nil, or an older second) OR is implausibly dated in the FUTURE. It
// compares at second granularity — metav1.Time serialises whole seconds while memory keeps
// sub-second precision, so a raw After() would rewrite the same second every reconcile. The future
// case is a secondary guard: the seed already clears a future-dated value durably before this runs,
// so p is normally not in the future here; if one ever were, it is overwritten by the current
// in-memory value. The write rides the reconcile's existing (unconditional) status update, so it
// adds no extra API request.
func (r *NTNSliceReconciler) persistLastSwitchbackToStatus(ns *ntnv1alpha1.NTNSlice, latest, now time.Time) {
	if latest.IsZero() {
		return
	}
	if p := ns.Status.LastSwitchbackTime; p == nil || latest.Truncate(time.Second).After(p.Time) || p.After(now) {
		ns.Status.LastSwitchbackTime = &metav1.Time{Time: latest}
	}
}

// resetRecoveryStreak clears the confirmation and recovery clocks for a slice while
// KEEPING the min-dwell clock. Called when metrics are unreliable or satellite
// availability is unknown: a telemetry gap breaks the "continuous" recovery/degraded
// streaks (absence of evidence is not evidence of recovery — otherwise a single
// healthy sample after a long gap, plus a stale recovery timestamp, would satisfy the
// switchback delay and switch back immediately). The dwell since the last hand-back
// keeps elapsing on wall-clock time, so LastSwitchback is preserved.
func (r *NTNSliceReconciler) resetRecoveryStreak(key types.NamespacedName) {
	r.flapMu.Lock()
	defer r.flapMu.Unlock()
	entry, ok := r.flapState[key]
	if !ok || (entry.state.RecoveryObservedAt.IsZero() && entry.state.ConsecutiveDegraded == 0) {
		return // nothing tracked, or already clear — do not create a spurious entry
	}
	entry.state.RecoveryObservedAt = time.Time{}
	entry.state.ConsecutiveDegraded = 0
	r.flapState[key] = entry // preserves the entry's UID
}

// dropFlapState releases a slice's anti-flap state (on deletion).
func (r *NTNSliceReconciler) dropFlapState(key types.NamespacedName) {
	r.flapMu.Lock()
	defer r.flapMu.Unlock()
	delete(r.flapState, key)
}

// readerProvider returns the configured Provider, or a lazily-initialised
// default one so the reconciler never churns a fresh pool per Reconcile.
// Safe for concurrent callers via sync.Once.
func (r *NTNSliceReconciler) readerProvider() metricsReaderProvider {
	if r.ReaderProvider != nil {
		return r.ReaderProvider
	}
	r.defaultProviderOnce.Do(func() {
		// Lazy/test default. Wire the SSRF-safe dialer even here (strict: an
		// empty allowlist blocks every private/reserved IP) so a reconciler
		// built without an explicit ReaderProvider is not a silent SSRF hole.
		// Production wires the flag-configured allowlist via cmd/main.go (I-24).
		r.defaultProvider = slicemetrics.NewProvider(
			slicemetrics.NewClientPool(slicemetrics.WithSafeClient(netutil.EndpointAllowlist{})))
	})
	return r.defaultProvider
}

// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntnslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntnslices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=ntnslices/finalizers,verbs=update
// +kubebuilder:rbac:groups=ntn.operators.dev,resources=satelliteephemeris,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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
		if apierrors.IsNotFound(err) {
			// The CR is gone — drop any cached reader AND release the per-CR
			// ReaderStaleUsedTotal series so a deleted NTNSlice leaves no state
			// behind. Evict via readerProvider() (the same accessor reads use),
			// NOT the raw r.ReaderProvider field: with a nil ReaderProvider the
			// lazy default provider still holds the cache/series, so guarding on
			// the raw field leaked the stale series (#183). No-op if no entry.
			r.readerProvider().Evict(req.NamespacedName)
			// Also drop the in-memory anti-flap state. The finalizer normally does this,
			// but a force-deletion (finalizer bypassed) or namespace purge reaches NotFound
			// without it — otherwise a same-name NTNSlice recreated (new UID) before a
			// controller restart could inherit the deleted object's LastSwitchback.
			r.dropFlapState(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Step 1b: manage the metrics-cleanup finalizer so per-CR metric series are
	// released on deletion (see handleSliceFinalizer).
	if done, err := r.handleSliceFinalizer(ctx, ns); done {
		return ctrl.Result{}, err
	}

	now := r.now()

	// Snapshot status before the quality/availability/failover evaluation mutates it, so the
	// terminal write can be skipped when this reconcile changed nothing (see Step 8). Only durable
	// anti-flap state (LastSwitchbackTime) lives in status and so is captured here; the in-memory
	// streak (storeFlapState) is committed regardless and does not gate the write.
	oldStatus := ns.Status.DeepCopy()

	// Step 2: Read path-quality metrics and determine whether they are reliable
	// enough to drive a switch. Unreliable metrics fail static in Step 4 (the engine
	// is replaced by EvaluateSafeHold, which holds the current path but still honors
	// the orbital signal from Step 3) — findings.md B-4 / I-8. readPathQuality never
	// early-returns on a metrics failure, so Step 3's orbital check always runs.
	// Events queued by the quality/trigger helpers are emitted only after the status
	// write persists (WO-20 / N-1), so a rolled-back Status().Update never announces
	// (then re-announces on retry) a transition that did not become durable.
	var pending []deferredEvent
	metrics, qualityReliable := r.readPathQuality(ctx, ns, now, &pending)

	// Step 3: Check satellite availability via SatelliteEphemeris. satelliteKnown is
	// false on a transient ephemeris read error — availability is unknown and the
	// current path is held rather than switched off satellite (I-13).
	satelliteAvailable, satelliteKnown, satelliteDetail := r.checkSatelliteAvailability(ctx, ns, now)
	log.V(1).Info("satellite availability", "available", satelliteAvailable, "known", satelliteKnown,
		"ephemerisRef", ns.Spec.SatellitePath.EphemerisRef, "detail", satelliteDetail)

	// FailoverReady tracks whether quality-driven failover can operate. When metrics
	// are reliable it reflects satellite availability; when they are not,
	// setMetricsDegraded already set it Unknown (with the metrics-failure reason), so
	// we must not overwrite it here.
	if qualityReliable {
		var c metav1.Condition
		switch {
		case !satelliteKnown:
			// Transient ephemeris read failure: cannot determine the failover target.
			c = metav1.Condition{Status: metav1.ConditionUnknown, Reason: "SatelliteReadFailed",
				Message: "Satellite availability unknown: transient SatelliteEphemeris read error; holding current path"}
		case satelliteAvailable:
			c = metav1.Condition{Status: metav1.ConditionTrue, Reason: "SatelliteAvailable",
				Message: "Satellite pass window active"}
		default:
			msg := satelliteDetail
			if msg == "" {
				msg = "No satellite pass window active or SatelliteEphemeris not found"
			}
			c = metav1.Condition{Status: metav1.ConditionFalse, Reason: "SatelliteUnavailable", Message: msg}
		}
		c.Type = ntnv1alpha1.ConditionFailoverReady
		c.ObservedGeneration = ns.Generation
		meta.SetStatusCondition(&ns.Status.Conditions, c)

		// I-10: surface any trigger whose metric has no configured source — it is
		// armed-but-dead (never fires against the healthy placeholder). Only
		// meaningful once metrics were actually read (qualityReliable).
		r.reportInertTriggers(ns, slice.InertTriggers(ns.Spec.FailoverPolicy.Triggers, metrics), &pending)
	} else {
		// Metrics are unreliable, so whether a trigger is inert or sourced cannot be
		// evaluated. Report TriggersReady=Unknown for THIS generation rather than leave
		// a stale True/False from the last reliable reconcile (a condition must not lie
		// about the current state; absent evidence is Unknown, not the old answer).
		meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionTriggersReady,
			Status:             metav1.ConditionUnknown,
			Reason:             "MetricsUnavailable",
			Message:            "Trigger sourcing cannot be evaluated while path-quality metrics are unreliable",
			ObservedGeneration: ns.Generation,
		})
	}

	// Step 4: Evaluate the failover decision. With reliable metrics the full
	// hysteresis engine runs; when metrics are unreliable we fail static via
	// EvaluateSafeHold — it holds the current path but still switches off a satellite
	// whose pass has ended (satelliteAvailable from Step 3).
	currentPath := slice.PathType(ns.Status.ActivePathType)
	if currentPath == "" {
		currentPath = slice.PathTerrestrial // default
	}

	var result slice.FailoverResult
	// pendingFlap/pendingFlapKey defer the in-memory anti-flap commit until AFTER the Step-8
	// status write succeeds — the same "commit side-effects only once the status is durable"
	// discipline as the FailoverTotal counter and decision event below. Committing memory before
	// the durable write would leave a SPECULATIVE LastSwitchback (from a switchback whose status
	// write failed) that a later pass-ended forced switchback could launder into durable status
	// as a dwell clock for a hand-back that never actually happened.
	var pendingFlap *slice.AntiFlapState
	var pendingFlapKey types.NamespacedName
	var pendingFlapUID types.UID
	switch {
	case !satelliteKnown:
		// I-13: satellite availability is unknown (transient ephemeris read error).
		// Hold the current path — never switch OFF satellite on an unread pass
		// window, and never fail OVER to a satellite we cannot confirm is overhead.
		// This mirrors the metrics fail-static contract; the next reconcile re-reads
		// the ephemeris and resumes normal evaluation.
		result = slice.FailoverResult{
			Decision:   slice.DecisionStay,
			TargetPath: currentPath,
			Reason:     "SatelliteAvailabilityUnknown",
		}
		log.Info("satellite availability unknown; holding current path",
			"path", currentPath, "reason", result.Reason)
		// H2: a gap in the ability to evaluate transitions breaks the continuous
		// recovery/degraded streaks; do not let a stale recovery clock survive it.
		r.resetRecoveryStreak(client.ObjectKeyFromObject(ns))
	case qualityReliable:
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

		// Anti-flap config from spec (opt-in; the zero value reproduces the prior
		// immediate-failover behavior). The switchback delay is measured from the
		// in-memory recovery clock rather than status.lastFailover, so it resets on
		// re-degradation (I-9 timer-reset).
		afCfg := slice.AntiFlapConfig{
			MinTerrestrialDwell: ns.Spec.FailoverPolicy.MinTerrestrialDwell.Duration,
		}
		if ns.Spec.FailoverPolicy.ConfirmationSamples != nil {
			afCfg.ConfirmationSamples = *ns.Spec.FailoverPolicy.ConfirmationSamples
		}

		flapKey := client.ObjectKeyFromObject(ns)
		af := r.loadFlapState(flapKey, ns.UID)
		r.seedLastSwitchbackFromStatus(&af, ns, now)
		var newFlap slice.AntiFlapState
		result, newFlap = slice.EvaluateFailoverWithAntiFlap(
			ctx,
			currentPath,
			ns.Spec.FailoverPolicy.Triggers,
			metrics,
			satelliteAvailable,
			ns.Spec.FailoverPolicy.SwitchbackDelay.Duration,
			now,
			hysteresisMargin,
			afCfg,
			af,
		)
		pendingFlap, pendingFlapKey, pendingFlapUID = &newFlap, flapKey, ns.UID
		r.persistLastSwitchbackToStatus(ns, newFlap.LastSwitchback, now)
	default:
		// Metrics unreliable but satellite availability known: fail static —
		// EvaluateSafeHold holds the current path yet still honors the orbital
		// signal (a genuine end-of-pass switches off satellite; I-8).
		result = slice.EvaluateSafeHold(currentPath, satelliteAvailable)
		log.Info("metrics unreliable; failing static",
			"decision", result.Decision, "targetPath", result.TargetPath, "reason", result.Reason)
		// H2: unreliable metrics break the continuous recovery/degraded streaks — a
		// single healthy sample after the gap must not satisfy the switchback delay via
		// a stale recovery timestamp. Keep the min-dwell clock (wall-clock based).
		r.resetRecoveryStreak(client.ObjectKeyFromObject(ns))
	}

	// Step 5: Apply decision.
	previousPath := string(currentPath)
	ns.Status.ActivePathType = string(result.TargetPath)

	// Update satellite availability metric (keyed by namespace+ephemeris). Only
	// write it when availability is KNOWN — on a transient read error (unknown)
	// leave the gauge at its last value rather than reporting a misleading 0 (I-13).
	if satelliteKnown {
		passLabels := prometheus.Labels{"namespace": ns.Namespace, "ephemeris": ns.Spec.SatellitePath.EphemerisRef}
		if satelliteAvailable {
			ntnmetrics.SatellitePassAvailable.With(passLabels).Set(1)
		} else {
			ntnmetrics.SatellitePassAvailable.With(passLabels).Set(0)
		}
	}

	// The FailoverTotal counter increment is deferred until after the status write
	// (Step 8) persists, so a stale/conflicting reconcile that fails to record the
	// failover cannot double-count it. Stays nil unless this reconcile fails over.
	var failoverLabels prometheus.Labels

	switch result.Decision {
	case slice.DecisionFailover:
		ns.Status.FailoverCount++
		ns.Status.LastFailover = &metav1.Time{Time: now}
		failoverLabels = prometheus.Labels{
			"namespace": ns.Namespace, "slice": ns.Name,
			"from_path": previousPath, "to_path": string(result.TargetPath),
		}
		log.Info("Failover triggered", "from", previousPath, "to", result.TargetPath, "reason", result.Reason)
	case slice.DecisionSwitchback:
		log.Info("Switchback", "from", previousPath, "to", result.TargetPath, "reason", result.Reason)
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

	// Step 8: Persist status, but only when it changed or an event is queued — a steady-state
	// (DecisionStay, no switchback) reconcile re-derives identical status, so an unconditional
	// Update would churn every watcher each requeue (#204-G3; matches #271 / GroundStation). The
	// storeFlapState / FailoverTotal / event blocks below are unchanged: each fires only on a real
	// transition (⟹ statusChanged) or a queued event, so the durable write always precedes them.
	if err := r.persistStatusIfChanged(ctx, ns, oldStatus, pending); err != nil {
		return ctrl.Result{}, err
	}

	// Commit the anti-flap state to shared memory ONLY now that the transition is durable. A
	// failed/stale status write therefore never leaves a speculative LastSwitchback in memory,
	// so a subsequent forced switchback cannot persist a dwell clock for a hand-back that was
	// never durably recorded. Under-counting a confirmation/recovery sample on a dropped write
	// only DELAYS the next transition (fail-safe), matching the anti-flap design.
	if pendingFlap != nil {
		r.storeFlapState(pendingFlapKey, pendingFlapUID, *pendingFlap)
	}

	// Emit the failover counter only after the status write persisted, so a
	// conflicting/stale reconcile (which discards its status update) never counts
	// a failover it did not actually record — keeping FailoverTotal in step with
	// status.FailoverCount.
	if failoverLabels != nil {
		ntnmetrics.FailoverTotal.With(failoverLabels).Inc()
	}

	// Emit the decision event only after the status write persisted (same discipline
	// as the FailoverTotal counter above): a conflicting/stale reconcile that
	// discards its status update must not announce a failover/switchback it never
	// recorded, then re-announce it on retry. The Failover/Switchback decision is
	// itself one-per-transition (steady reconciles return Stay), so it needs no extra
	// condition gate.
	if r.Recorder != nil {
		switch result.Decision {
		case slice.DecisionFailover:
			r.Recorder.Eventf(ns, nil, corev1.EventTypeWarning, "FailoverTriggered", "FailoverTriggered",
				"Failover from %s to %s: %s", previousPath, result.TargetPath, result.Reason)
		case slice.DecisionSwitchback:
			r.Recorder.Eventf(ns, nil, corev1.EventTypeNormal, "Switchback", "Switchback",
				"Switched back from %s to %s: %s", previousPath, result.TargetPath, result.Reason)
		}
		// Flush the quality/trigger events queued earlier this reconcile, also only
		// after the status persisted (WO-20 / N-1).
		for _, e := range pending {
			r.Recorder.Eventf(ns, nil, e.eventType, e.reason, e.reason, "%s", e.message)
		}
	}

	return ctrl.Result{RequeueAfter: sliceRequeueInterval}, nil
}

// persistStatusIfChanged writes ns.Status only when it differs from oldStatus or an event is
// queued, so a no-op reconcile does not churn the object (#204-G3). A queued event forces the
// write so the WO-20 emit-after-persist discipline holds. Split out of Reconcile to keep that
// function under the cyclomatic-complexity budget.
func (r *NTNSliceReconciler) persistStatusIfChanged(
	ctx context.Context, ns *ntnv1alpha1.NTNSlice, oldStatus *ntnv1alpha1.NTNSliceStatus, pending []deferredEvent,
) error {
	if equality.Semantic.DeepEqual(oldStatus, &ns.Status) && len(pending) == 0 {
		return nil
	}
	return r.Status().Update(ctx, ns)
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

// readPathQuality reads path-quality metrics for ns and reports whether they are
// reliable enough to drive a switch decision. On unreliable metrics — reader-build
// failure, read failure, or a value stale beyond metricsMaxStaleness — it records
// the degraded conditions and returns qualityReliable=false so the caller fails
// static (findings.md B-4/I-8). It mutates conditions on ns in memory; the caller
// persists them once at the end of the reconcile.
func (r *NTNSliceReconciler) readPathQuality(ctx context.Context, ns *ntnv1alpha1.NTNSlice, now time.Time, pending *[]deferredEvent) (slice.Metrics, bool) {
	log := logf.FromContext(ctx)

	reader, err := r.readerProvider().For(ns)
	if err != nil {
		// Distinguish admin-configured endpoint rejection from other build errors
		// so kubectl describe surfaces a specific reason.
		reason := reasonMetricsReaderError
		if errors.Is(err, netutil.ErrEndpointNotAllowed) {
			reason = reasonEndpointNotAllowed
		}
		log.Error(err, "failed to build metrics reader; failing static", "reason", reason)
		r.setMetricsDegraded(ns, reason, err.Error())
		return slice.Metrics{}, false
	}
	readResult, err := reader.Read(ctx, ns)
	if err != nil {
		reason := reasonMetricsUnavailable
		if !errors.Is(err, slicemetrics.ErrNoMetrics) {
			reason = reasonMetricsReaderError
		}
		log.Info("metrics unavailable; failing static (holding current path)", "reason", reason, "err", err.Error())
		r.setMetricsDegraded(ns, reason, err.Error())
		return slice.Metrics{}, false
	}

	// Fresh read: record freshness. The stale Event is emitted only on the
	// transition into stale (one event per outage, not one per interval). Snapshot
	// the prior state as a VALUE before mutating — meta.FindStatusCondition returns a
	// pointer INTO the slice that SetStatusCondition then mutates in place, so a
	// pointer read after the write would always see True and miss every False->True
	// (fresh->stale) transition.
	wasStale := meta.IsStatusConditionTrue(ns.Status.Conditions, ntnv1alpha1.ConditionMetricsStale)
	if !readResult.Stale {
		meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionMetricsStale,
			Status:             metav1.ConditionFalse,
			Reason:             "FreshValue",
			Message:            "Metrics source returned a fresh observation",
			ObservedGeneration: ns.Generation,
		})
		log.V(2).Info("metrics read", "rsrp", readResult.Metrics.RSRP, "latencyMs", readResult.Metrics.LatencyMs, "packetLossPercent", readResult.Metrics.PacketLossPercent, "stale", false)
		return readResult.Metrics, true
	}

	age := now.Sub(readResult.LastFreshAt)
	log.Info("metrics source returned stale value", "lastFreshAt", readResult.LastFreshAt, "age", age)
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionMetricsStale,
		Status:             metav1.ConditionTrue,
		Reason:             "StaleValue",
		Message:            fmt.Sprintf("Using stale metrics last observed at %s (age %s)", readResult.LastFreshAt.Format(time.RFC3339), age.Round(time.Second)),
		ObservedGeneration: ns.Generation,
	})
	if !wasStale {
		*pending = append(*pending, deferredEvent{corev1.EventTypeWarning, "MetricsStale",
			fmt.Sprintf("Using stale metrics last observed at %s", readResult.LastFreshAt.Format(time.RFC3339))})
	}
	if age <= metricsMaxStaleness {
		// Stale but within the freshness bound — still usable for a decision.
		log.V(2).Info("metrics read", "rsrp", readResult.Metrics.RSRP, "latencyMs", readResult.Metrics.LatencyMs, "packetLossPercent", readResult.Metrics.PacketLossPercent, "stale", true)
		return readResult.Metrics, true
	}

	// Too stale to drive a switch → fail static. Surface it like the other
	// unreliable-metrics cases; the caller gates its FailoverReady True/False set on
	// the returned flag, so it will not overwrite this. (MetricsStale stays True —
	// the age is known here, unlike the source-unavailable case.)
	log.Info("metrics stale beyond freshness bound; failing static", "age", age, "maxStaleness", metricsMaxStaleness)
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionFailoverReady,
		Status:             metav1.ConditionUnknown,
		Reason:             "MetricsStale",
		Message:            fmt.Sprintf("Metrics stale beyond %s; quality-driven failover suspended (fail static)", metricsMaxStaleness),
		ObservedGeneration: ns.Generation,
	})
	return slice.Metrics{}, false
}

// setMetricsDegraded records that path-quality metrics are unusable for a switch
// decision: FailoverReady=Unknown (failover cannot be quality-evaluated) and
// MetricsStale=Unknown (freshness unknown — neither fresh nor stale was served, so
// leaving MetricsStale=True from a prior reconcile would be a lie). It only mutates
// conditions in memory; the reconcile fall-through persists them once at the end,
// after also applying the fail-static decision (so the orbital signal can still
// switch off a set satellite — findings.md I-8).
func (r *NTNSliceReconciler) setMetricsDegraded(ns *ntnv1alpha1.NTNSlice, reason, msg string) {
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
}

// reportInertTriggers sets the TriggersReady condition and, once per inert EPISODE,
// emits a Warning event listing the inert triggers — those whose metric has no
// configured source and can never fire (findings.md I-10). True/AllTriggersSourced
// otherwise. The episode gate keeps the event stream quiet across steady reconciles.
//
// The gate is conditionEpisodeChanged (Status/Reason/Generation), not a bare
// into-False transition, so a NEW spec generation whose inert set changed — still
// False/InertTriggers but a higher generation, e.g. one inert trigger swapped for
// another — re-warns once, matching the generation-aware failure-event policy the
// other conditions use. The message (the inert-trigger list) is deliberately outside
// the gate: it varies per spec but does not by itself begin a new episode.
func (r *NTNSliceReconciler) reportInertTriggers(ns *ntnv1alpha1.NTNSlice, inert []string, pending *[]deferredEvent) {
	if len(inert) == 0 {
		meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
			Type:               ntnv1alpha1.ConditionTriggersReady,
			Status:             metav1.ConditionTrue,
			Reason:             "AllTriggersSourced",
			Message:            "All failover triggers reference a sourced metric",
			ObservedGeneration: ns.Generation,
		})
		return
	}
	// Evaluate the episode BEFORE SetStatusCondition mutates the slice element in place.
	episode := conditionEpisodeChanged(
		meta.FindStatusCondition(ns.Status.Conditions, ntnv1alpha1.ConditionTriggersReady),
		metav1.ConditionFalse, "InertTriggers", ns.Generation)
	msg := fmt.Sprintf("failover triggers reference metrics with no configured source and will never fire: %s",
		strings.Join(inert, "; "))
	meta.SetStatusCondition(&ns.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionTriggersReady,
		Status:             metav1.ConditionFalse,
		Reason:             "InertTriggers",
		Message:            msg,
		ObservedGeneration: ns.Generation,
	})
	if episode {
		*pending = append(*pending, deferredEvent{corev1.EventTypeWarning, "InertTriggers", msg})
	}
}

// checkSatelliteAvailability reports whether a satellite pass window is currently
// active (available) and whether that answer is trustworthy (known). A transient
// ephemeris read error (informer-cache blip / apiserver hiccup) returns
// known=false so the caller HOLDS the current path instead of yanking traffic off
// satellite on an unread pass window — mirroring the metrics fail-static contract
// (findings.md I-13). A NotFound (dangling ephemerisRef) and a found-but-no-active-
// pass are genuine orbital signals (known=true): the former has no ephemeris to
// serve the satellite path, the latter is a real end-of-pass, and both may switch
// back off satellite.
func (r *NTNSliceReconciler) checkSatelliteAvailability(
	ctx context.Context,
	ns *ntnv1alpha1.NTNSlice,
	now time.Time,
) (available, known bool, detail string) {
	eph := &ntnv1alpha1.SatelliteEphemeris{}
	key := client.ObjectKey{Namespace: ns.Namespace, Name: ns.Spec.SatellitePath.EphemerisRef}
	if err := r.Get(ctx, key, eph); err != nil {
		if apierrors.IsNotFound(err) {
			// Dangling ephemerisRef: no ephemeris to drive the satellite path. This
			// is a genuine (persistent) signal, not a transient blip — allow switchback.
			return false, true, "referenced SatelliteEphemeris not found"
		}
		// Transient read error: availability is UNKNOWN. Do NOT force a switch off
		// satellite; the caller holds the current path (I-13).
		logf.FromContext(ctx).Error(err, "failed to get SatelliteEphemeris; holding current path",
			"ref", ns.Spec.SatellitePath.EphemerisRef)
		return false, false, ""
	}

	// Availability is CONSTELLATION-POOL-level: the path is available if ANY tracked member is overhead
	// and deliverable. NTNSlice is not pinned to a NORAD (satellitePath has none), because LEO members
	// hand over; which member a cell broadcasts is NTNCellConfig.ephemerisNoradID's concern. See ADR-0008.
	//
	// The pass windows are trustworthy ONLY when the producer's most recent prediction SUCCEEDED. If
	// PassesPredicted is not True — absent (never predicted), Unknown (recomputing after an input change
	// or a no-OMM failure), or False (PredictionFailed) — pass availability is UNKNOWN, so hold the
	// current path instead of reading possibly-stale or empty windows as a real end-of-pass. This
	// completes the SatelliteEphemeris(producer) -> NTNSlice(consumer) pass-status contract (ADR 0006 /
	// #234): the producer leaves PassesPredicted=True only while NextPassWindows reflects the live inputs.
	if !meta.IsStatusConditionTrue(eph.Status.Conditions, ntnv1alpha1.ConditionPassesPredicted) {
		return false, false, ""
	}

	// An active pass makes the satellite AVAILABLE unless its backing element set is present but too
	// STALE to DELIVER — the same sourceEpochFresh (maxEpochAge) gate NTNCellConfig enforces at the
	// runtime push. Without this the two consumers of PropagatedStates disagree during a >maxEpochAge
	// outage: NTNSlice fails over to a satellite whose stale ephemeris NTNCellConfig then refuses to push
	// (a control-plane split that strands traffic on an unconfigurable path). Pass prediction is itself
	// SGP4 propagation, so an element set too stale to push is equally untrustworthy for the window.
	// Correlate by NORAD — the canonical key; PropagatedState.Satellite is a truncated, non-unique name.
	//
	// Scope: only a PRESENT-but-stale state demotes the window (that IS the reported split — NTNCellConfig
	// has the state but refuses it). A window whose satellite has a fresh state, no state, or predates the
	// noradID field stays available: a window and its propagatedState are produced together from one
	// reconcile's OMMs, so "window but no state" is not the stale-outage case this closes.
	stale := make(map[int]bool)
	for i := range eph.Status.PropagatedStates {
		ps := &eph.Status.PropagatedStates[i]
		if sourceEpochFresh(now, time.UnixMilli(ps.SourceEpochUnixMs)) != nil {
			stale[ps.NoradID] = true
		}
	}
	activeDeliverable, activeStale := false, false
	for i := range eph.Status.NextPassWindows {
		pw := &eph.Status.NextPassWindows[i]
		if pw.AOS.After(now) || !pw.LOS.After(now) {
			continue // not currently in this window
		}
		if pw.NoradID != 0 && stale[pw.NoradID] {
			activeStale = true // overhead but its element set is too stale to deliver
		} else {
			activeDeliverable = true
		}
	}
	switch {
	case activeDeliverable:
		return true, true, "" // a deliverable satellite is overhead (fresh wins over any stale sibling)
	case activeStale:
		return false, true, fmt.Sprintf(
			"satellite overhead but its element set is stale beyond the %s freshness bound — not deliverable to the gNB", maxEpochAge)
	default:
		return false, true, "no active satellite pass window"
	}
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
		// reconcileTriggerPredicate (predicates.go) stops the controller's own
		// status writes (e.g. the switchback-countdown message) from re-enqueueing
		// it, while still passing spec changes and deletionTimestamp transitions
		// (the metrics-cleanup finalizer must run on delete). Annotation-only
		// changes (the simulated-metrics dev backdoor) no longer trigger an
		// immediate reconcile but are caught within the 30s RequeueAfter. The
		// SatelliteEphemeris Watches below keep no predicate.
		For(&ntnv1alpha1.NTNSlice{}, builder.WithPredicates(reconcileTriggerPredicate())).
		Watches(&ntnv1alpha1.SatelliteEphemeris{},
			handler.EnqueueRequestsFromMapFunc(r.ephemerisToSlice),
		).
		Named("ntnslice").
		WithOptions(ctrlrt.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
		Complete(r)
}

// ephemerisToSlice maps a SatelliteEphemeris change to all NTNSlices
// that reference it via spec.satellitePath.ephemerisRef.
// sliceMetricsFinalizer gates NTNSlice deletion so the reconciler can release
// the slice's per-CR Prometheus series before the object is removed.
const sliceMetricsFinalizer = "ntn.operators.dev/metrics-cleanup"

// handleSliceFinalizer releases the NTNSlice's per-CR metric series on deletion
// so /metrics does not accumulate dead series across create/delete churn.
// Returns (done, error); when done is true the caller must return.
//
// FailoverTotal is per-CR (keyed by namespace+slice) and is released directly.
// SatellitePassAvailable is keyed by ephemeris ref, which multiple slices can
// share, so it is released only once no other slice references that ref
// (refcounting) — otherwise a sibling slice's series would be wrongly dropped.
func (r *NTNSliceReconciler) handleSliceFinalizer(
	ctx context.Context, ns *ntnv1alpha1.NTNSlice,
) (bool, error) {
	log := logf.FromContext(ctx)

	if ns.DeletionTimestamp != nil {
		// Release the in-memory anti-flap state so the map does not leak entries for
		// deleted slices (idempotent; a no-op if none was ever recorded).
		r.dropFlapState(client.ObjectKeyFromObject(ns))
		if controllerutil.ContainsFinalizer(ns, sliceMetricsFinalizer) {
			// FailoverTotal is per-CR (keyed by namespace+slice); release it directly.
			ntnmetrics.FailoverTotal.DeletePartialMatch(prometheus.Labels{
				"namespace": ns.Namespace, "slice": ns.Name,
			})
			// SatellitePassAvailable is keyed by namespace+ephemeris, which several
			// slices in the SAME namespace can share; release it only once no other
			// slice in this namespace references the ref.
			ref := ns.Spec.SatellitePath.EphemerisRef
			shared, err := r.ephemerisMetricReferenced(ctx, ns.Namespace, ref, ns.UID)
			if err != nil {
				// Retry on transient list errors rather than leak the series.
				log.Error(err, "listing NTNSlices for metric refcount during finalization")
				return true, err
			}
			if !shared {
				ntnmetrics.SatellitePassAvailable.DeletePartialMatch(prometheus.Labels{
					"namespace": ns.Namespace, "ephemeris": ref,
				})
			}
			controllerutil.RemoveFinalizer(ns, sliceMetricsFinalizer)
			if err := r.Update(ctx, ns); err != nil {
				return true, err
			}
		}
		return true, nil
	}

	// Not being deleted: ensure the finalizer is present, then CONTINUE the same
	// reconcile (return done=false). r.Update writes the new resourceVersion back
	// into ns, so the later status update still applies — this must not short-circuit
	// with a requeue, or the first reconcile of a slice would compute no status.
	if !controllerutil.ContainsFinalizer(ns, sliceMetricsFinalizer) {
		controllerutil.AddFinalizer(ns, sliceMetricsFinalizer)
		if err := r.Update(ctx, ns); err != nil {
			return true, err
		}
	}
	return false, nil
}

// ephemerisMetricReferenced reports whether any NTNSlice in namespace, other than
// the one being deleted, still references ephemerisRef — matching
// SatellitePassAvailable's {namespace, ephemeris} keying (the ref is resolved
// within the slice's namespace), so a shared series is kept until its last
// referencing slice in that namespace is gone.
//
// The DeletionTimestamp skip is load-bearing for leak-safety under concurrent
// finalization: the List reads the shared, monotonically-updated informer cache,
// and this reconcile only reached the deletion branch because its own Get already
// observed self's DeletionTimestamp (Get happens-before this List). So if two
// siblings sharing a ref are finalized concurrently, at most one can observe the
// other as live — the other necessarily sees it terminating/gone and deletes the
// series. Dropping this skip (or reading un-cached) would reintroduce a
// double-KEEP leak; keep it.
func (r *NTNSliceReconciler) ephemerisMetricReferenced(
	ctx context.Context, namespace, ephemerisRef string, deleting types.UID,
) (bool, error) {
	var list ntnv1alpha1.NTNSliceList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.UID == deleting || s.DeletionTimestamp != nil {
			continue
		}
		if s.Spec.SatellitePath.EphemerisRef == ephemerisRef {
			return true, nil
		}
	}
	return false, nil
}

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
