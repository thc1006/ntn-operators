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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestEphemerisToNTNCellConfig_IssuesIndexedList_M1 pins that the mapper actually CONSUMES the
// spec.ephemerisRef index (#224 review-2 M1): it must issue a List carrying the field selector,
// not a full namespace scan. The earlier index-registration + result tests would both stay
// green if the mapper reverted to a full List + in-Go filter (same result, index unused) — the
// silent perf regression this PR exists to prevent. This intercepts List and asserts the field
// selector is present and the indexed path makes exactly one call (no fallback scan).
func TestEphemerisToNTNCellConfig_IssuesIndexedList_M1(t *testing.T) {
	const ns = "default"
	ccRef := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-ref", Namespace: ns},
		Spec:       ntnv1alpha1.NTNCellConfigSpec{EphemerisRef: "eph-x"},
	}
	var listCalls int
	var sawFieldSelector bool
	c := fake.NewClientBuilder().
		WithScheme(makeScheme(t)).
		WithObjects(ccRef).
		WithIndex(&ntnv1alpha1.NTNCellConfig{}, ephemerisRefIndexKey, indexNTNCellConfigByEphemerisRef).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listCalls++
				var lo client.ListOptions
				for _, o := range opts {
					o.ApplyToList(&lo)
				}
				if lo.FieldSelector != nil && strings.Contains(lo.FieldSelector.String(), ephemerisRefIndexKey) {
					sawFieldSelector = true
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()
	r := &NTNCellConfigReconciler{Client: c}

	eph := &ntnv1alpha1.SatelliteEphemeris{ObjectMeta: metav1.ObjectMeta{Name: "eph-x", Namespace: ns}}
	reqs := r.ephemerisToNTNCellConfig(context.Background(), eph)

	if len(reqs) != 1 || reqs[0].Name != "cc-ref" {
		t.Fatalf("mapper must return exactly cc-ref, got %v", reqs)
	}
	if !sawFieldSelector {
		t.Fatal("the mapper must issue an INDEXED List carrying the spec.ephemerisRef field selector; a " +
			"full namespace scan + Go filter returns the same result but silently reverts the perf fix")
	}
	if listCalls != 1 {
		t.Errorf("the indexed path must make exactly one List call (no fallback scan), got %d", listCalls)
	}
}
