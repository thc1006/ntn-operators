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
	"strconv"
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

// ommCacheConfigMapName derives the per-CR cache ConfigMap name, bounded to the k8s name limit.
// Names over the limit are truncated; the restore path UID-gates, so a (pathological) truncation
// collision between two >243-char names never restores wrong data — see ADR-0007.
func ommCacheConfigMapName(ephName string) string {
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
		return
	}
	if len(data) > maxOMMCacheBytes {
		log.Info("omm-cache: payload over bound; skipping restart-continuity persist (warm cache unaffected)",
			"bytes", len(data), "satellites", len(omms))
		return
	}
	digest := ommDigest(data)
	key := client.ObjectKey{Namespace: eph.Namespace, Name: ommCacheConfigMapName(eph.Name)}

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
		}
	case getErr != nil:
		log.V(1).Info("omm-cache: get failed; skipping persist", "err", getErr.Error())
	default:
		if cm.Annotations[ommCacheAnnDigest] == digest && cm.Data[ommCacheDataKey] != "" {
			return // unchanged
		}
		stampOMMCache(cm, eph, fetchKey, digest, len(omms), string(data), result.FetchedAt, result.ETag, result.LastModified)
		_ = controllerutil.SetControllerReference(eph, cm, r.Scheme) // adopt for GC if pre-existing
		if err := r.Update(ctx, cm); err != nil {
			log.V(1).Info("omm-cache: update failed; skipping persist", "err", err.Error())
		}
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
func (r *SatelliteEphemerisReconciler) restoreOMMCache(
	ctx context.Context, req ctrl.Request, eph *ntnv1alpha1.SatelliteEphemeris, fetchKey string,
) bool {
	log := logf.FromContext(ctx)
	if _, ok := r.cachedOMMResult(req.NamespacedName); ok {
		return false // already warm
	}
	cm := &corev1.ConfigMap{}
	if err := r.readerOrClient().Get(ctx, client.ObjectKey{Namespace: eph.Namespace, Name: ommCacheConfigMapName(eph.Name)}, cm); err != nil {
		return false
	}
	data := cm.Data[ommCacheDataKey]
	if cm.Labels[ommCacheLabelKey] != ommCacheLabelValue || data == "" {
		return false
	}
	if cm.Annotations[ommCacheAnnDigest] != ommDigest([]byte(data)) {
		log.Info("omm-cache: digest mismatch; ignoring (corrupt or hand-edited)")
		return false
	}
	if cm.Annotations[ommCacheAnnUID] != string(eph.UID) || cm.Annotations[ommCacheAnnFetchKey] != fetchKey {
		return false // orphaned by delete-recreate, or a different source — do not restore
	}
	omms, err := ephemeris.ParseValidOMMs(log, []byte(data))
	if err != nil || len(omms) == 0 {
		log.V(1).Info("omm-cache: payload unparseable or fully invalid; ignoring", "err", err)
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
	log.Info("omm-cache: hydrated last-good OMMs after cold start", "satellites", len(omms))
	return true
}

// conditionalCacheSeeder is implemented by a GPFetcher that can restore cache validators
// (ETag / Last-Modified) after a cold start so its first fetch is a conditional GET. Only the
// CelesTrak fetcher implements it; the type assertion no-ops for any other fetcher.
type conditionalCacheSeeder interface {
	SeedConditionalCache(url, etag, lastModified string)
}
