/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/akhenakh/sgp4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	"github.com/thc1006/ntn-operators/pkg/metrics"
)

// Durable last-good OMM cache: persists tracked OMMs to a controller-owned ConfigMap so a cold
// process (restart / leader failover) can re-propagate through a sustained upstream outage,
// instead of having nothing to propagate once the last pushed epoch expires. Store rationale
// (ConfigMap vs Secret) and alternatives: docs/adr/0007-durable-omm-cache.md.

const (
	ommCacheDataKey    = "omms.json"
	ommCacheLabelKey   = "ntn.operators.dev/omm-cache"
	ommCacheLabelValue = "true"
	// Recorded so a restore never trusts data from a different source, object, or a corrupt payload.
	ommCacheAnnFetchKey  = "ntn.operators.dev/omm-fetch-key"  // source identity (fetchInputKey)
	ommCacheAnnFetchedAt = "ntn.operators.dev/omm-fetched-at" // original fetch time (RFC3339Nano)
	ommCacheAnnDigest    = "ntn.operators.dev/omm-digest"     // sha256 of the payload
	ommCacheAnnUID       = "ntn.operators.dev/omm-owner-uid"  // delete-recreate guard
	ommCacheAnnCount     = "ntn.operators.dev/omm-count"
	// Origin cache validators, re-seeded into a cold CelesTrak fetcher on restore so the first
	// post-restart fetch is a conditional GET (304), not a full re-download.
	ommCacheAnnETag         = "ntn.operators.dev/omm-etag"
	ommCacheAnnLastModified = "ntn.operators.dev/omm-last-modified"

	ommCacheConfigMapSuffix = "-omm-cache"
	maxOMMCacheBytes        = 900 * 1024 // under the 1 MiB ConfigMap limit; larger sets skip persist
	maxConfigMapNameLen     = 253
	// 128 bits, per ADR-0007. Two SatelliteEphemeris objects colliding here is not a scenario
	// anyone reaches by naming things badly.
	ommCacheNameHashBytes = 16
)

// The per-CR cache ConfigMap is owner-ref'd to its SatelliteEphemeris, so k8s garbage-collects it
// on CR deletion — the controller never deletes it itself, hence no delete verb.
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;create;update

// readerOrClient returns the uncached APIReader when wired, else the cached client (some tests
// leave APIReader nil).
func (r *SatelliteEphemerisReconciler) readerOrClient() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// ommCacheConfigMapName derives a collision-resistant name — <readable prefix>-<128-bit
// hash>-omm-cache, keyed on namespace/name/UID (ADR-0007).
//
// The previous scheme truncated the CR name to fit 253 chars, which maps every
// SatelliteEphemeris sharing a 243-character prefix onto ONE ConfigMap. The UID annotation stops
// a wrong RESTORE, but it does not stop the contention: the losers keep overwriting an object
// whose UID then refuses their own restore, so their restart continuity is gone with nothing in
// the status or logs to say so. Availability bug, not cosmetic.
//
// Hashing the UID in also means a delete/recreate lands on a different object rather than
// inheriting the predecessor's — the annotation gate stays as defense in depth.
func ommCacheConfigMapName(eph *ntnv1alpha1.SatelliteEphemeris) string {
	sum := sha256.Sum256([]byte(eph.Namespace + "/" + eph.Name + "/" + string(eph.UID)))
	h := hex.EncodeToString(sum[:ommCacheNameHashBytes])
	prefix := eph.Name
	if room := maxConfigMapNameLen - len(ommCacheConfigMapSuffix) - len(h) - 1; len(prefix) > room {
		// Trim separators the cut may have exposed: a label may not start with "-" or ".".
		prefix = strings.TrimRight(prefix[:room], "-.")
	}
	return prefix + "-" + h + ommCacheConfigMapSuffix
}

// legacyOMMCacheConfigMapName is the pre-ADR-0007 name: read on restore so an upgrade does not
// throw away a cache that is still valid, never written. Nothing creates these any more, so the
// only ones in existence predate the upgrade.
func legacyOMMCacheConfigMapName(ephName string) string {
	if len(ephName)+len(ommCacheConfigMapSuffix) > maxConfigMapNameLen {
		ephName = ephName[:maxConfigMapNameLen-len(ommCacheConfigMapSuffix)]
	}
	return ephName + ommCacheConfigMapSuffix
}

// ommCachePayload is the tracked, capped set propagateStates would use (never the full GP
// response). sgp4.OMM's CelesTrak JSON tags let it round-trip through sgp4.ParseOMMs on restore.
func ommCachePayload(result ephemeris.GPFetchResult, eph *ntnv1alpha1.SatelliteEphemeris) []sgp4.OMM {
	var norad []int
	if eph.Spec.Satellites != nil {
		norad = eph.Spec.Satellites.NoradIDs
	}
	omms := ephemeris.FilterOMMs(result.OMMs, norad)
	if len(omms) > maxPropagatedStates {
		omms = omms[:maxPropagatedStates]
	}
	return omms
}

func ommDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// persistOMMCache best-effort writes the last-good tracked OMMs to the owner-ref'd ConfigMap.
// Failures are logged and swallowed (persistence is a restart enhancement, not the live path);
// identical payloads no-op so the ~2 h fetch cadence does not churn resourceVersion (#204-G3).
func (r *SatelliteEphemerisReconciler) persistOMMCache(
	ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris, result ephemeris.GPFetchResult, fetchKey string,
) {
	log := logf.FromContext(ctx)
	omms := ommCachePayload(result, eph)
	if len(omms) == 0 {
		return
	}
	data, err := json.Marshal(omms)
	if err != nil {
		log.V(1).Info("omm-cache: marshal failed; skipping persist", "err", err.Error())
		metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "skipped_marshal").Inc()
		return
	}
	if len(data) > maxOMMCacheBytes {
		log.Info("omm-cache: payload over bound; skipping restart-continuity persist (warm cache unaffected)",
			"bytes", len(data), "satellites", len(omms))
		metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "skipped_oversize").Inc()
		return
	}
	digest := ommDigest(data)
	key := client.ObjectKey{Namespace: eph.Namespace, Name: ommCacheConfigMapName(eph)}

	// Read uncached (APIReader) so configmaps get suffices and we do not start an informer that
	// caches every ConfigMap in the cluster; writes still go through the cached client. Mirrors
	// the Secret read. Falls back to the cached client only when APIReader is unwired (some tests).
	reader := r.readerOrClient()
	cm := &corev1.ConfigMap{}
	getErr := reader.Get(ctx, key, cm)
	switch {
	case apierrors.IsNotFound(getErr):
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
		stampOMMCache(cm, eph, fetchKey, digest, len(omms), string(data), result.FetchedAt, result.ETag, result.LastModified)
		if err := controllerutil.SetControllerReference(eph, cm, r.Scheme); err != nil {
			log.V(1).Info("omm-cache: owner ref failed; skipping persist", "err", err.Error())
			return
		}
		if err := r.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
			log.V(1).Info("omm-cache: create failed; skipping persist", "err", err.Error())
			metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "failed").Inc()
			return
		}
		metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "success").Inc()
	case getErr != nil:
		log.V(1).Info("omm-cache: get failed; skipping persist", "err", getErr.Error())
		metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "failed").Inc()
	default:
		if cm.Annotations[ommCacheAnnDigest] == digest && cm.Data[ommCacheDataKey] != "" {
			return // unchanged
		}
		stampOMMCache(cm, eph, fetchKey, digest, len(omms), string(data), result.FetchedAt, result.ETag, result.LastModified)
		_ = controllerutil.SetControllerReference(eph, cm, r.Scheme) // adopt for GC if pre-existing
		if err := r.Update(ctx, cm); err != nil {
			log.V(1).Info("omm-cache: update failed; skipping persist", "err", err.Error())
			metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "failed").Inc()
			return
		}
		metrics.OMMCachePersistTotal.WithLabelValues(eph.Namespace, eph.Name, "success").Inc()
	}
}

func stampOMMCache(cm *corev1.ConfigMap, eph *ntnv1alpha1.SatelliteEphemeris, fetchKey, digest string, count int, data string, fetchedAt time.Time, etag, lastModified string) {
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels[ommCacheLabelKey] = ommCacheLabelValue
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[ommCacheAnnFetchKey] = fetchKey
	cm.Annotations[ommCacheAnnFetchedAt] = fetchedAt.UTC().Format(time.RFC3339Nano)
	cm.Annotations[ommCacheAnnDigest] = digest
	cm.Annotations[ommCacheAnnUID] = string(eph.UID)
	cm.Annotations[ommCacheAnnCount] = strconv.Itoa(count)
	// Keep the validators in lockstep with the body: clear a stale one when the origin
	// stops sending it, so a restore never seeds a validator that no longer matches the data.
	setOrClearAnn(cm.Annotations, ommCacheAnnETag, etag)
	setOrClearAnn(cm.Annotations, ommCacheAnnLastModified, lastModified)
	cm.Data = map[string]string{ommCacheDataKey: data}
}

func setOrClearAnn(ann map[string]string, key, val string) {
	if val != "" {
		ann[key] = val
		return
	}
	delete(ann, key)
}

// restoreOMMCache hydrates the in-memory cache from the persisted ConfigMap when it is cold for
// this key, returning true on hydration. It refuses unless the digest is intact and the source
// identity (fetchKey) and owner UID match the live object, so a source edit or delete-recreate
// never restores wrong/orphaned data. The entry keeps its ORIGINAL FetchedAt, so the normal
// window/backoff/freshness gates still apply — restore removes the cold-start cliff, nothing more.
// The ADR-0007 name is tried first, then the pre-ADR one, so an upgrade keeps its cache.
func (r *SatelliteEphemerisReconciler) restoreOMMCache(
	ctx context.Context, req ctrl.Request, eph *ntnv1alpha1.SatelliteEphemeris, fetchKey string,
) bool {
	if _, ok := r.cachedOMMResult(req.NamespacedName); ok {
		return false // already warm
	}
	if r.hydrateFromCacheObject(ctx, req, eph, fetchKey, ommCacheConfigMapName(eph), false) {
		return true
	}
	// Nothing usable under the ADR-0007 name. An upgraded operator still has a perfectly good
	// cache under the old one, and discarding it would cost exactly the outage continuity this
	// feature exists for.
	//
	// This costs a second uncached GET per reconcile that runs cold, which is deliberate: the
	// window is process-start to the first successful fetch, and the only way to hold it open is
	// for fetches to keep failing — by which point one ConfigMap GET is noise next to the failing
	// HTTP fetch beside it. Caching "no legacy object here" would trade that for state to get
	// wrong.
	return r.hydrateFromCacheObject(ctx, req, eph, fetchKey, legacyOMMCacheConfigMapName(eph.Name), true)
}

// hydrateFromCacheObject validates one candidate ConfigMap and, if it passes every gate, loads it
// into the in-memory cache. Same gates for both names — a legacy object gets no easier ride.
func (r *SatelliteEphemerisReconciler) hydrateFromCacheObject(
	ctx context.Context, req ctrl.Request, eph *ntnv1alpha1.SatelliteEphemeris, fetchKey, name string, legacy bool,
) bool {
	log := logf.FromContext(ctx)
	ns, ephName := eph.Namespace, eph.Name
	cm := &corev1.ConfigMap{}
	if err := r.readerOrClient().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, cm); err != nil {
		return false // absent is the normal cold-start case, not a refusal
	}
	data := cm.Data[ommCacheDataKey]
	if cm.Labels[ommCacheLabelKey] != ommCacheLabelValue || data == "" {
		return false
	}
	if cm.Annotations[ommCacheAnnDigest] != ommDigest([]byte(data)) {
		log.Info("omm-cache: digest mismatch; ignoring (corrupt or hand-edited)", "configmap", name)
		metrics.OMMCacheRestoreTotal.WithLabelValues(ns, ephName, "refused_digest").Inc()
		return false
	}
	if cm.Annotations[ommCacheAnnUID] != string(eph.UID) || cm.Annotations[ommCacheAnnFetchKey] != fetchKey {
		// Orphaned by delete-recreate, or a different source. Under the legacy name this is also
		// how a truncation collision surfaces, so it is worth counting rather than dropping.
		metrics.OMMCacheRestoreTotal.WithLabelValues(ns, ephName, "refused_identity").Inc()
		return false
	}
	omms, err := ephemeris.ParseValidOMMs(log, []byte(data))
	if err != nil || len(omms) == 0 {
		log.V(1).Info("omm-cache: payload unparseable or fully invalid; ignoring", "err", err)
		metrics.OMMCacheRestoreTotal.WithLabelValues(ns, ephName, "refused_parse").Inc()
		return false
	}
	// Unknown fetch time → zero, i.e. very old, so the next reconcile fetches and only serves
	// this on failure; restored data is never treated as freshly fetched.
	fetchedAt, _ := time.Parse(time.RFC3339Nano, cm.Annotations[ommCacheAnnFetchedAt])
	r.ommCache.Store(req.NamespacedName, cachedFetch{
		result:   ephemeris.GPFetchResult{OMMs: omms, SatelliteCount: len(omms), FetchedAt: fetchedAt},
		fetchKey: fetchKey,
		uid:      eph.UID,
	})
	// Re-seed the cold CelesTrak fetcher's cache validators so the first fetch this process
	// makes is a conditional GET (304) rather than a full re-download. Only the validators are
	// seeded (never the url-shared body — see SeedConditionalCache); a resulting post-restart
	// 304 re-serves the entry stored just above, in obtainOMMs.
	if eph.Spec.Source.Type == "CelesTrak" {
		if seeder, ok := r.Fetcher.(conditionalCacheSeeder); ok {
			seeder.SeedConditionalCache(eph.Spec.Source.URL,
				cm.Annotations[ommCacheAnnETag], cm.Annotations[ommCacheAnnLastModified])
		}
	}
	if !fetchedAt.IsZero() {
		metrics.OMMCacheRestoredAgeSeconds.WithLabelValues(ns, ephName).Set(time.Since(fetchedAt).Seconds())
	}
	result := "hydrated"
	if legacy {
		result = "migrated"
		// Copy it forward now rather than waiting for the next fetch (~2 h), so a second restart
		// in between is not back to a cold start. The legacy object is left alone: the controller
		// holds no delete verb, and its owner reference garbage-collects it with the CR.
		r.copyCacheForward(ctx, eph, cm)
	}
	metrics.OMMCacheRestoreTotal.WithLabelValues(ns, ephName, result).Inc()
	log.Info("omm-cache: hydrated last-good OMMs after cold start",
		"satellites", len(omms), "configmap", name, "migrated", legacy)
	return true
}

// copyCacheForward writes a legacy-named cache object's contents under the ADR-0007 name.
// Best-effort: failing here costs one fetch cycle, not correctness.
func (r *SatelliteEphemerisReconciler) copyCacheForward(
	ctx context.Context, eph *ntnv1alpha1.SatelliteEphemeris, legacy *corev1.ConfigMap,
) {
	log := logf.FromContext(ctx)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   eph.Namespace,
			Name:        ommCacheConfigMapName(eph),
			Labels:      map[string]string{ommCacheLabelKey: ommCacheLabelValue},
			Annotations: maps.Clone(legacy.Annotations),
		},
		Data: map[string]string{ommCacheDataKey: legacy.Data[ommCacheDataKey]},
	}
	if err := controllerutil.SetControllerReference(eph, cm, r.Scheme); err != nil {
		log.V(1).Info("omm-cache: owner ref failed; leaving the legacy object in place", "err", err.Error())
		return
	}
	if err := r.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		log.V(1).Info("omm-cache: migrating to the hashed name failed; retrying on the next persist",
			"err", err.Error())
		return
	}
	log.Info("omm-cache: migrated to the collision-resistant name (ADR-0007)",
		"from", legacy.Name, "to", cm.Name)
}

// conditionalCacheSeeder is implemented by a GPFetcher that can restore cache validators
// (ETag / Last-Modified) after a cold start so its first fetch is a conditional GET. Only the
// CelesTrak fetcher implements it; the type assertion no-ops for any other fetcher.
type conditionalCacheSeeder interface {
	SeedConditionalCache(url, etag, lastModified string)
}
