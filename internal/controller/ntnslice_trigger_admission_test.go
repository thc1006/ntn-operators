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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// These run against the envtest API server, so they exercise the CRD's REAL CEL
// x-kubernetes-validations for failoverPolicy.triggers — the bounded-magnitude value
// regex (#220 C4) as evaluated by the apiserver on CREATE, not just a Go-side copy of
// the regex compared against ParseTrigger. This closes the "the test never sends an
// object to admission" gap: it proves an object with an overflowing trigger value is
// actually rejected by the deployed CRD.
var _ = Describe("NTNSlice failoverPolicy.triggers admission (CEL)", func() {
	n := 0
	mkSlice := func(trigger string) *ntnv1alpha1.NTNSlice {
		n++
		return &ntnv1alpha1.NTNSlice{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("adm-trigger-%d", n), Namespace: "default"},
			Spec: ntnv1alpha1.NTNSliceSpec{
				Tenant: "acme-corp",
				TerrestrialPath: ntnv1alpha1.PathSpec{
					Provider: "chunghwa-telecom", APN: "internet", Priority: "primary",
				},
				SatellitePath: ntnv1alpha1.SatellitePathSpec{
					PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
					EphemerisRef: "oneweb-constellation",
				},
				FailoverPolicy: ntnv1alpha1.FailoverPolicy{
					Triggers:        []string{trigger},
					SwitchbackDelay: metav1.Duration{Duration: 60 * time.Second},
				},
			},
		}
	}

	DescribeTable("bounds the trigger value to a finite magnitude at admission",
		func(trigger string, accepted bool) {
			s := mkSlice(trigger)
			err := k8sClient.Create(ctx, s)
			if accepted {
				Expect(err).NotTo(HaveOccurred(), "trigger %q should be admitted", trigger)
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, s) })
			} else {
				Expect(err).To(HaveOccurred(), "trigger %q must be rejected by CEL admission", trigger)
			}
		},
		Entry("normal negative rsrp", "rsrp < -120", true),
		Entry("decimal packetLoss", "packetLoss > 5.25", true),
		Entry("2-digit exponent stays finite", "latency > 1e99", true),
		Entry("3-digit exponent is bounded out", "latency > 1e100", false),
		Entry("overflowing exponent 1e9999 (=+Inf)", "latency > 1e9999", false),
		Entry("11 integer digits bounded out", "rsrp < 12345678901", false),
		Entry("non-finite NaN literal", "rsrp < NaN", false),
	)
})
