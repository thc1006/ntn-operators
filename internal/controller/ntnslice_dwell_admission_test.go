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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// These run against the envtest API server, so they exercise the CRD's REAL CEL
// x-kubernetes-validations for the failoverPolicy duration bounds — not a Go-side copy.
// A negative minTerrestrialDwell would silently disable the dwell (elapsed < negative is
// never true) and a negative switchbackDelay is equally meaningless; both are bounded to
// [0s, 24h] at admission so the footgun cannot reach the runtime.
var _ = Describe("NTNSlice failoverPolicy duration admission (CEL)", func() {
	n := 0
	// mkSlice builds an otherwise-valid slice (valid trigger, so the trigger CEL passes) with the
	// given dwell and switchback durations, so a rejection can only come from the duration rules.
	mkSlice := func(dwell, switchback metav1.Duration) *ntnv1alpha1.NTNSlice {
		n++
		return &ntnv1alpha1.NTNSlice{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("adm-dwell-%d", n), Namespace: "default"},
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
					Triggers:            []string{"rsrp < -120"},
					SwitchbackDelay:     switchback,
					MinTerrestrialDwell: dwell,
				},
			},
		}
	}

	// assertAdmission creates s and checks it is accepted, or rejected by an Invalid admission error
	// that names the offending field (so a reject cannot false-green on an unrelated validation error).
	assertAdmission := func(s *ntnv1alpha1.NTNSlice, accepted bool, field string) {
		err := k8sClient.Create(ctx, s)
		if accepted {
			Expect(err).NotTo(HaveOccurred(), "%s should be admitted", field)
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, s) })
			return
		}
		Expect(err).To(HaveOccurred(), "%s must be rejected by CEL admission", field)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "want an Invalid admission error, got %v", err)
		Expect(err.Error()).To(ContainSubstring(field), "the rejection must reference %s, got %v", field, err)
	}

	sec := func(d time.Duration) metav1.Duration { return metav1.Duration{Duration: d} }

	DescribeTable("bounds minTerrestrialDwell to [0s, 24h] at admission",
		func(dwell metav1.Duration, accepted bool) {
			assertAdmission(mkSlice(dwell, sec(60*time.Second)), accepted, "minTerrestrialDwell")
		},
		Entry("zero disables the dwell", sec(0), true),
		Entry("a normal 90s dwell", sec(90*time.Second), true),
		Entry("the 24h ceiling itself", sec(24*time.Hour), true),
		Entry("a negative dwell (would silently disable it)", sec(-90*time.Second), false),
		Entry("above the 24h fat-finger ceiling", sec(48*time.Hour), false),
	)

	DescribeTable("bounds switchbackDelay to [0s, 24h] at admission",
		func(switchback metav1.Duration, accepted bool) {
			assertAdmission(mkSlice(sec(0), switchback), accepted, "switchbackDelay")
		},
		Entry("a normal 60s delay", sec(60*time.Second), true),
		Entry("zero is allowed", sec(0), true),
		Entry("a negative delay", sec(-5*time.Second), false),
		Entry("above the 24h fat-finger ceiling", sec(48*time.Hour), false),
	)
})
