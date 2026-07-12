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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// These run against the envtest API server, so they exercise the CRD's real CEL
// x-kubernetes-validations for remoteControl.endpoint (port range 1-65535 and a
// bracketed host being a valid IP), not just the Go structs.
var _ = Describe("NTNCellConfig remoteControl.endpoint admission (CEL)", func() {
	admCount := 0
	mkCC := func(endpoint string) *ntnv1alpha1.NTNCellConfig {
		admCount++
		return &ntnv1alpha1.NTNCellConfig{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("adm-endpoint-%d", admCount), Namespace: "default"},
			Spec: ntnv1alpha1.NTNCellConfigSpec{
				Provider: ntnv1alpha1.ProviderRef{
					Type:          "ocudu",
					RemoteControl: &ntnv1alpha1.RemoteControlRef{Endpoint: endpoint},
				},
				CellID: &ntnv1alpha1.CellID{PLMN: "00101", NCI: 6733824},
				NTN: ntnv1alpha1.NTNParams{
					EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 111, PosY: 222, PosZ: 333},
				},
			},
		}
	}

	DescribeTable("accepts valid host:port (incl. IPv6) and rejects bad port / non-IP",
		func(endpoint string, accepted bool) {
			cc := mkCC(endpoint)
			err := k8sClient.Create(ctx, cc)
			if accepted {
				Expect(err).NotTo(HaveOccurred(), "endpoint %q should be admitted", endpoint)
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, cc) })
			} else {
				Expect(err).To(HaveOccurred(), "endpoint %q should be rejected", endpoint)
			}
		},
		Entry("IPv4 host:port", "127.0.0.1:8001", true),
		Entry("hostname:port", "gnb.internal:8001", true),
		Entry("IPv6 literal", "[::1]:8001", true),
		Entry("full IPv6 literal", "[2001:db8::1]:443", true),
		Entry("port above 65535", "127.0.0.1:99999", false),
		Entry("port zero", "127.0.0.1:0", false),
		Entry("bracketed hex that is not a valid IP", "[fff]:443", false),
		Entry("missing port", "127.0.0.1", false),
		Entry("scheme included (must be bare host:port)", "ws://127.0.0.1:8001", false),
		Entry("out-of-range IPv4 (all-numeric host must be a real IPv4)", "999.999.999.999:8001", false),
		Entry("IPv4 octet over 255", "300.1.1.1:443", false),
		Entry("double-dot hostname", "foo..bar:8001", false),
		Entry("dashes-only host", "---:8001", false),
		Entry("underscore host (not DNS-1123)", "_host:8001", false),
	)

	// RFC 1035 DNS length limits: the whole host <= 253 and each dot-separated
	// label 1-63. The shape Pattern cannot bound these lengths, so a dedicated CEL
	// rule carries them. Each boundary below sits within MaxLength=261 and passes
	// the Pattern, so ONLY the length rule can reject the over-limit case — which is
	// what isolates the rule. (A valid host:port therefore tops out at 259 chars:
	// 253 host + ':' + 5-digit port; MaxLength=261 is a loose outer backstop that
	// this tighter DNS-host bound sits under, so a 261/262 MaxLength pair is not
	// separately testable with a well-formed endpoint.)
	mkHost := func(labels ...int) string {
		parts := make([]string, len(labels))
		for i, n := range labels {
			parts[i] = strings.Repeat("a", n)
		}
		return strings.Join(parts, ".")
	}
	DescribeTable("remoteControl.endpoint DNS host length (label<=63, host<=253)",
		func(host string, accepted bool) {
			cc := mkCC(host + ":8001")
			err := k8sClient.Create(ctx, cc)
			if accepted {
				Expect(err).NotTo(HaveOccurred(), "host len %d should be admitted", len(host))
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, cc) })
			} else {
				Expect(err).To(HaveOccurred(), "host len %d should be rejected", len(host))
			}
		},
		Entry("single label at 63", mkHost(63), true),
		Entry("single label over at 64", mkHost(64), false),
		Entry("multi-label host at 253", mkHost(63, 63, 63, 61), true),
		Entry("multi-label host over at 254", mkHost(63, 63, 63, 62), false),
	)
})

// Real admission-time boundary checks for the MaxLength ratchets. These are a
// representative sample — one accept-at-limit / reject-over-limit pair for each
// distinct limit VALUE in play (63, 2048, 253, 32), against the envtest API server —
// not exhaustive coverage of every MaxLength-annotated field; fields sharing a limit
// are validated by the same generated schema bound, so one probe per value suffices.
var _ = Describe("CRD string-length admission (MaxLength boundaries)", func() {
	rep := func(c string, n int) string { return strings.Repeat(c, n) }
	seq := 0
	nm := func(p string) string { seq++; return fmt.Sprintf("%s-%d", p, seq) }
	assertAdmit := func(err error, accepted bool, obj client.Object, what string) {
		if accepted {
			Expect(err).NotTo(HaveOccurred(), "%s should be admitted", what)
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		} else {
			Expect(err).To(HaveOccurred(), "%s should be rejected", what)
		}
	}

	DescribeTable("NTNCellConfig provider.namespace (limit 63)",
		func(nsLen int, ok bool) {
			cc := &ntnv1alpha1.NTNCellConfig{
				ObjectMeta: metav1.ObjectMeta{Name: nm("adm-ns"), Namespace: "default"},
				Spec: ntnv1alpha1.NTNCellConfigSpec{
					Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: rep("a", nsLen)},
					NTN:      ntnv1alpha1.NTNParams{EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3}},
				},
			}
			assertAdmit(k8sClient.Create(ctx, cc), ok, cc, fmt.Sprintf("namespace len %d", nsLen))
		},
		Entry("at 63", 63, true),
		Entry("over at 64", 64, false),
	)

	mkEph := func(mutate func(*ntnv1alpha1.SatelliteEphemeris)) *ntnv1alpha1.SatelliteEphemeris {
		eph := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{Name: nm("adm-eph"), Namespace: "default"},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type: "CelesTrak", URL: "https://celestrak.org/x",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
			},
		}
		mutate(eph)
		return eph
	}

	DescribeTable("SatelliteEphemeris source.url (limit 2048)",
		func(urlLen int, ok bool) {
			eph := mkEph(func(e *ntnv1alpha1.SatelliteEphemeris) {
				e.Spec.Source.URL = "https://" + rep("a", urlLen-len("https://"))
			})
			assertAdmit(k8sClient.Create(ctx, eph), ok, eph, fmt.Sprintf("url len %d", urlLen))
		},
		Entry("at 2048", 2048, true),
		Entry("over at 2049", 2049, false),
	)

	DescribeTable("SatelliteEphemeris credentials.name (limit 253)",
		func(nameLen int, ok bool) {
			eph := mkEph(func(e *ntnv1alpha1.SatelliteEphemeris) {
				e.Spec.Source.Credentials = &ntnv1alpha1.SecretReference{Name: rep("a", nameLen)}
			})
			assertAdmit(k8sClient.Create(ctx, eph), ok, eph, fmt.Sprintf("cred name len %d", nameLen))
		},
		Entry("at 253", 253, true),
		Entry("over at 254", 254, false),
	)

	DescribeTable("SatelliteEphemeris minElevation numeric string (limit 32)",
		func(mLen int, ok bool) {
			eph := mkEph(func(e *ntnv1alpha1.SatelliteEphemeris) {
				e.Spec.PassPrediction = &ntnv1alpha1.PassPredictionSpec{
					GroundStations: []string{"gs-1"}, MinElevation: "1." + rep("0", mLen-2),
				}
			})
			assertAdmit(k8sClient.Create(ctx, eph), ok, eph, fmt.Sprintf("minElevation len %d", mLen))
		},
		Entry("at 32", 32, true),
		Entry("over at 33", 33, false),
	)

	// Value bound [0,90] via pattern (negatives) + CEL (>90). Also pins that the
	// grammar-preserving "10." (trailing dot) is still admitted through both the pattern
	// AND the CEL double() conversion (#201-P3, review finding 2).
	DescribeTable("SatelliteEphemeris minElevation value bound [0,90]",
		func(val string, ok bool) {
			eph := mkEph(func(e *ntnv1alpha1.SatelliteEphemeris) {
				e.Spec.PassPrediction = &ntnv1alpha1.PassPredictionSpec{
					GroundStations: []string{"gs-1"}, MinElevation: val,
				}
			})
			assertAdmit(k8sClient.Create(ctx, eph), ok, eph, fmt.Sprintf("minElevation %q", val))
		},
		Entry("zero", "0", true),
		Entry("ten", "10", true),
		Entry("trailing dot stays valid", "10.", true),
		Entry("fractional", "72.5", true),
		Entry("zenith inclusive", "90", true),
		// float64 contract: a literal within ~half a ULP above 90 rounds to 90.0 and is
		// accepted (CEL double() converts via strconv.ParseFloat), while one that rounds
		// strictly above 90 is rejected. Pins the intended float64 semantics.
		Entry("sub-ULP above 90 accepted as float64 90", "90.000000000000001", true),
		Entry("rounds strictly above 90 rejected", "90.0000000000001", false),
		Entry("negative rejected", "-5", false),
		Entry("just over 90 rejected", "90.5", false),
		Entry("far over 90 rejected", "1000", false),
	)
})
