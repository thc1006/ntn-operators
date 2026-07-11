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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	)
})
