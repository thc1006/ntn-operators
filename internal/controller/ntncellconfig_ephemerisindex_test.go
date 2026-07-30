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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// TestEphemerisToNTNCellConfig_IndexedPath_G3 pins the INDEXED mapper path (#204-G3b):
// with the spec.ephemerisRef field index registered (as SetupWithManager does on the
// manager cache), the mapper resolves referencing cells via client.MatchingFields — only
// the cell that references the ephemeris is returned, with no in-Go filter. The mapper has
// no namespace-scan fallback: on an indexed-lookup error it drops the fan-out (the cells
// re-resolve on their own requeue), so every test drives it through an indexed client.
// Mutation: point the index at the wrong field, or the mapper at the wrong key, and this
// returns the wrong set.
func TestEphemerisToNTNCellConfig_IndexedPath_G3(t *testing.T) {
	const ns = "default"
	ccRef := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-ref", Namespace: ns},
		Spec:       ntnv1alpha1.NTNCellConfigSpec{EphemerisRef: "eph-x"},
	}
	ccOther := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-other", Namespace: ns},
		Spec:       ntnv1alpha1.NTNCellConfigSpec{EphemerisRef: "eph-y"},
	}
	ccNone := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-none", Namespace: ns},
		Spec:       ntnv1alpha1.NTNCellConfigSpec{},
	}

	c := fake.NewClientBuilder().
		WithScheme(makeScheme(t)).
		WithObjects(ccRef, ccOther, ccNone).
		WithIndex(&ntnv1alpha1.NTNCellConfig{}, ephemerisRefIndexKey, indexNTNCellConfigByEphemerisRef).
		Build()
	r := &NTNCellConfigReconciler{Client: c}

	eph := &ntnv1alpha1.SatelliteEphemeris{ObjectMeta: metav1.ObjectMeta{Name: "eph-x", Namespace: ns}}
	reqs := r.ephemerisToNTNCellConfig(context.Background(), eph)

	if len(reqs) != 1 || reqs[0].Name != "cc-ref" || reqs[0].Namespace != ns {
		t.Fatalf("indexed mapper must return exactly cc-ref for eph-x, got %v", reqs)
	}

	// An ephemeris nobody references returns no requests.
	ephNone := &ntnv1alpha1.SatelliteEphemeris{ObjectMeta: metav1.ObjectMeta{Name: "eph-z", Namespace: ns}}
	if reqs := r.ephemerisToNTNCellConfig(context.Background(), ephNone); len(reqs) != 0 {
		t.Fatalf("an unreferenced ephemeris must map to no requests, got %v", reqs)
	}
}
