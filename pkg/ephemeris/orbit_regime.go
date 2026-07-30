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

package ephemeris

import (
	"fmt"
	"math"
	"strings"

	"github.com/akhenakh/sgp4"
)

// minutesPerDay is 1440; named so the orbital-period formula reads in units.
const minutesPerDay = 1440.0

// DeepSpacePeriodMinutes is the SGP4/SDP4 boundary from Vallado's "Revisiting
// Spacetrack Report #3": an element set whose orbital period is at least this
// long must be propagated with the deep-space (SDP4) model. The near-earth SGP4
// propagator this package uses (github.com/akhenakh/sgp4, which hardcodes
// use_deep_space_ = false) does not implement SDP4, so a deep-space element set
// run through it yields a silently-wrong position (findings.md B-5). Such sets
// are therefore rejected rather than propagated. 225 min corresponds to a mean
// motion of 1440/225 = 6.4 rev/day (roughly MEO and above); this catches MEO
// constellations such as SES O3b (~288 min) as well as GEO (~1436 min), unlike a
// narrow "GEO band" heuristic on mean motion alone.
const DeepSpacePeriodMinutes = 225.0

// OrbitalPeriodMinutes returns the orbital period in minutes derived from an
// element set's mean motion (revolutions per day). It returns 0 for a
// non-positive, NaN, or infinite mean motion (a malformed element set); callers
// treat 0 as near-earth so the downstream propagator surfaces the malformed input
// as a propagation error, rather than the orbit-regime guard masking it (and so a
// rejection summary can never print a "NaN min" period).
func OrbitalPeriodMinutes(omm sgp4.OMM) float64 {
	if omm.MeanMotion <= 0 || math.IsNaN(omm.MeanMotion) || math.IsInf(omm.MeanMotion, 0) {
		return 0
	}
	return minutesPerDay / omm.MeanMotion
}

// IsDeepSpace reports whether an element set requires the deep-space (SDP4)
// model — its orbital period is at least DeepSpacePeriodMinutes. The near-earth
// SGP4 model must not be used for these.
//
// Classification keys off the raw OMM mean motion, not the SGP4-recovered
// no_unkozai. At the 225-min boundary the two differ by <=~0.06% (a ~0.04%
// mean-motion band of near-equatorial orbits); with no 12h/24h resonance at
// 225 min the effect is immaterial, and keying off the raw value is more robust
// for a malformed OMM (the recovery depends on ecc/incl, which may be garbage).
func IsDeepSpace(omm sgp4.OMM) bool {
	return OrbitalPeriodMinutes(omm) >= DeepSpacePeriodMinutes
}

// SplitByOrbitRegime partitions element sets into those the near-earth SGP4
// propagator can handle (nearEarth) and those that require the deep-space model
// and must be rejected (deepSpace). Both returned slices are freshly allocated,
// so the input's backing array — which may be shared with the fetch cache — is
// never mutated. Order within each partition follows the input.
func SplitByOrbitRegime(omms []sgp4.OMM) (nearEarth, deepSpace []sgp4.OMM) {
	for i := range omms {
		if IsDeepSpace(omms[i]) {
			deepSpace = append(deepSpace, omms[i])
		} else {
			nearEarth = append(nearEarth, omms[i])
		}
	}
	return nearEarth, deepSpace
}

// deepSpaceSummaryMaxExamples bounds how many satellites the rejection summary
// names, so a large deep-space feed cannot bloat the status message or event.
const deepSpaceSummaryMaxExamples = 3

// DeepSpaceSummary returns a bounded, human-readable explanation for a set of
// rejected deep-space element sets, suitable for a status-condition message and
// a Warning event. It names at most deepSpaceSummaryMaxExamples satellites with
// their orbital periods and appends "..." when more were rejected. Returns "" for
// an empty slice. The text is ASCII-only (">=", "...") so log-greps and alerting
// pipelines match it; it carries no version string, so it never goes stale when a
// deep-space (SDP4) release lands.
func DeepSpaceSummary(deepSpace []sgp4.OMM) string {
	if len(deepSpace) == 0 {
		return ""
	}
	examples := make([]string, 0, deepSpaceSummaryMaxExamples)
	for i := range deepSpace {
		if len(examples) >= deepSpaceSummaryMaxExamples {
			break
		}
		name := BoundedSatelliteLabel(deepSpace[i].ObjectName) // external; bound so it can't bloat the condition/event
		if name == "" {
			name = fmt.Sprintf("NORAD %d", deepSpace[i].NoradCatID)
		}
		examples = append(examples, fmt.Sprintf("%s (%.0f min)", name, OrbitalPeriodMinutes(deepSpace[i])))
	}
	suffix := ""
	if len(deepSpace) > len(examples) {
		suffix = ", ..."
	}
	return fmt.Sprintf(
		"%d element set(s) rejected: orbital period >= %.0f min requires the deep-space (SDP4) model, "+
			"which this operator's near-earth SGP4 propagator does not implement. Examples: %s%s",
		len(deepSpace), DeepSpacePeriodMinutes, strings.Join(examples, ", "), suffix,
	)
}
