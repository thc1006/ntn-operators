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

package slice

import (
	"regexp"
	"testing"
)

// triggerCELRegex MUST stay byte-for-byte in sync with the CEL
// x-kubernetes-validations rule on FailoverPolicy in
// api/v1alpha1/ntnslice_types.go. Kubernetes CEL matches() and Go's regexp both
// use RE2, so this const faithfully replicates the admission-time check. This
// test is the single behavioral source of truth: it proves the admission regex
// agrees with the runtime ParseTrigger, so a trigger accepted at admission always
// parses at runtime (no silent skip) and vice-versa — closing findings.md I-11.
const triggerCELRegex = `^ *(rsrp|latency|packetLoss|terrestrialRSRP|terrestrialLatency|terrestrialPacketLoss) *(<=|>=|<|>) *[-+]?([0-9]{1,10}([.][0-9]{1,10})?|[.][0-9]{1,10})([eE][-+]?[0-9]{1,2})? *$` //nolint:lll // one line to stay byte-identical to the CRD CEL rule

func TestTriggerCELRegexAgreesWithParseTrigger(t *testing.T) {
	re := regexp.MustCompile(triggerCELRegex)
	corpus := []string{
		// valid — both must accept
		"rsrp < -120", "latency > 200", "packetLoss > 5", "rsrp <= -100",
		"terrestrialRSRP >= -90", "terrestrialLatency < 50", "terrestrialPacketLoss > 0.5",
		"rsrp<-120", "  latency  >  200  ", "packetLoss > 5.25", "rsrp > +3",
		"latency >= 1e2", "packetLoss < .5", "latency > 1e99",
		// invalid — both must reject. `1e9999` is the C4 case: the prior regex accepted it
		// while ParseTrigger rejects it as +Inf (a latent disagreement); the bounded
		// exponent now rejects it at admission too, so both agree.
		"", "rsrp", "rsrp < ", "< 5", "rsrp ! 5", "rsrp == 5", "rsrp <> 5",
		"unknownMetric < 5", "RSRP < -120", "rsrp < abc", "rsrp < -120 extra",
		"rsrp < 5 < 3", "latency > 200ms", "rsrp < NaN", "rsrp < Inf", "latency > 1e9999",
	}
	for _, s := range corpus {
		reMatch := re.MatchString(s)
		_, err := ParseTrigger(s)
		parseOK := err == nil
		if reMatch != parseOK {
			t.Errorf("admission/runtime disagreement on %q: CEL-regex=%v, ParseTrigger ok=%v", s, reMatch, parseOK)
		}
	}
}

// TestTriggerCELRegexKnownDivergences documents EVERY class where the admission
// regex is intentionally stricter than the runtime ParseTrigger. All are
// "safe-direction" — admission rejects, runtime would accept, never the reverse —
// so nothing passes admission that the failover engine would then silently skip.
// These forms are never used for RSRP/latency/loss thresholds, so rejecting them
// at admission is a deliberate, fail-closed stricter-ness. Exact two-way parity is
// not achievable by regex anyway (even RE2's \s is ASCII-only, unlike the
// unicode.IsSpace that ParseTrigger's strings.TrimSpace uses).
func TestTriggerCELRegexKnownDivergences(t *testing.T) {
	re := regexp.MustCompile(triggerCELRegex)
	// Each is accepted by strconv.ParseFloat / strings.TrimSpace (so ParseTrigger
	// accepts) but rejected by the ASCII-only, plain-decimal admission regex.
	divergent := []string{
		"rsrp < 0x1p4",       // 1. hex float
		"rsrp < 5.",          // 2. trailing-dot float
		"latency > 1_000",    // 3. underscore digit grouping
		"rsrp <\u00a0-120",   // 4. non-ASCII whitespace (NBSP) where a space is expected
		"latency > 1e100",    // 5. finite but exponent > 2 digits \u2014 bounded to keep the value finite
		"rsrp < 12345678901", // 6. finite but > 10 integer digits \u2014 bounded to keep the value finite
	}
	for _, s := range divergent {
		if re.MatchString(s) {
			t.Errorf("admission regex should (deliberately) reject exotic form %q", s)
		}
		if _, err := ParseTrigger(s); err != nil {
			t.Errorf("runtime ParseTrigger is expected to accept %q (safe-direction divergence): %v", s, err)
		}
	}
}
