/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"fmt"
	"time"
)

// SHARED LAYER for source-element-set (OMM EPOCH) validity.
//
// The SatelliteEphemeris producer and the NTNCellConfig runtime-push consumer each used to
// carry their own copy of these bounds and their own comparison — and they had silently
// drifted: the producer compared the future bound against the PROPAGATION TARGET epoch
// (now + propagationEpochLead) while the consumer compared against time.Now(). A source epoch
// inside that 5-minute band was therefore propagated and hash-stamped by the producer yet
// permanently refused by the consumer: a state that could never be delivered. Both sides now
// call the SAME function with their own "now", so the rule is single-sourced.
//
// There are deliberately TWO rules, because the two sides need different ones:
//
//   - PLAUSIBILITY (sourceEpochPlausible) — is this element set CORRUPT? Enforced by BOTH.
//     The producer must refuse to propagate from it (SGP4 would be driven backward from a
//     bogus epoch and write a wildly wrong ECEF into status — refusing only at the push
//     stops delivery, not the propagation). The consumer re-checks as defense in depth.
//
//   - FRESHNESS (sourceEpochFresh) — is this element set too OLD to deliver? Enforced by the
//     CONSUMER only. A stale-but-valid element set still propagates meaningfully (SGP4 just
//     accumulates in-track error), so the producer deliberately still emits the state and
//     merely COUNTS it (the EphemerisEpochStale condition, I-17) — that keeps a drifting feed
//     observable. The delivery gate is the consumer's job (#200-C4).
//
// Applying freshness at the producer instead would collapse a precise "EphemerisStale"
// diagnosis into a bare "EphemerisPayloadMissing" and silently drop satellites the operator
// is still tracking.

// maxEpochAge bounds how old a fetched element set's OWN epoch may be before its SGP4
// propagation is unreliable (findings.md I-17). Independent of the refresh cadence — a
// source can serve elements whose epoch is already stale. ~7 days keeps LEO in-track error
// to at most a few km.
const maxEpochAge = 7 * 24 * time.Hour

// maxSourceEpochFutureSkew bounds how far into the future a source element-set epoch may be
// before it is implausible (a corrupt or spoofed feed). Real OMM/TLE epochs are at or before
// "now"; a far-future epoch would otherwise sail past the "older than maxEpochAge" check,
// which only catches the PAST direction. Generous, so legitimate near-present epochs are
// never rejected.
const maxSourceEpochFutureSkew = 24 * time.Hour

// sourceEpochPlausible reports whether a source element-set epoch could plausibly have come
// from a real feed, evaluated at time now. Enforced on BOTH the producer (before SGP4) and
// the consumer (before push) — see the file comment.
func sourceEpochPlausible(now, src time.Time) error {
	if src.After(now.Add(maxSourceEpochFutureSkew)) {
		return fmt.Errorf("source element epoch %s is implausibly future-dated (more than %s ahead of %s)",
			src.UTC().Format(time.RFC3339), maxSourceEpochFutureSkew, now.UTC().Format(time.RFC3339))
	}
	return nil
}

// sourceEpochFresh reports whether a source element-set epoch is fresh enough to DELIVER to
// the gNB, evaluated at time now. Enforced by the consumer only — see the file comment.
//
// NOTE ON THE ZERO VALUE: PropagatedState.SourceEpochUnixMs is a plain int64, so 0 would be
// an ambiguous sentinel — it means BOTH "unparseable" and the perfectly legal instant
// 1970-01-01T00:00:00Z. The consumer used to fail OPEN on 0 ("unknown, allow it"), which let
// a 1970 epoch bypass the entire freshness gate. There is no escape hatch any more: the
// producer never emits a state whose epoch it could not parse (and SGP4's own OMM.ToTLE
// parses the same epoch, so such an element set fails propagation anyway), so a 0 can only be
// a genuine 1970 epoch — which this rule then correctly reports as stale.
func sourceEpochFresh(now, src time.Time) error {
	if age := now.Sub(src); age > maxEpochAge {
		return fmt.Errorf("source element epoch %s is %s old, beyond the %s freshness bound (drifting)",
			src.UTC().Format(time.RFC3339), age.Round(time.Second), maxEpochAge)
	}
	return nil
}
