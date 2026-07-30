/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ocudu

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestPushEphemerisUpdate_NoOpOnIdenticalEphemeris_G3 pins the byte-equality guard on the
// ConfigMap bootstrap push. PushEphemerisUpdate must NOT rewrite the ConfigMap when the
// re-rendered ephemeris block is byte-identical to what is already stored: on the ConfigMap
// path ApplyCellConfig has already written this same static spec ephemeris, yet the push
// gate marker keys on the SatelliteEphemeris GP fetch time (changes every ~2h fetch), so an
// unconditional Update would rewrite identical bytes and churn the gNB's config source — the
// exact resourceVersion churn #204-G3 removed from ApplyCellConfig. A genuine ephemeris
// change must still write.
//
// Mutation: drop the `updated == yamlContent` skip in PushEphemerisUpdate → the identical
// push bumps resourceVersion and the first assertion fails.
func TestPushEphemerisUpdate_NoOpOnIdenticalEphemeris_G3(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	owner := ownerFor("test-cr")
	spec := geoSpec() // inline EphemerisECEF
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, owner, spec, ownerScheme(t)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var cm1 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm1); err != nil {
		t.Fatalf("get after apply: %v", err)
	}

	// Push the SAME ephemeris the spec already carries (the steady-state fan-out: the marker
	// advanced on a GP fetch, but the static spec ephemeris is unchanged). The re-render is
	// byte-identical to what ApplyCellConfig wrote, so the guard must skip the Update.
	same := provider.EphemerisUpdate{ECEF: spec.NTN.EphemerisECEF}
	if err := p.PushEphemerisUpdate(ctx, owner, same); err != nil {
		t.Fatalf("push (identical): %v", err)
	}
	var cm2 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm2); err != nil {
		t.Fatalf("get after identical push: %v", err)
	}
	if cm1.ResourceVersion != cm2.ResourceVersion {
		t.Fatalf("PushEphemerisUpdate on a byte-identical ephemeris must NOT rewrite the ConfigMap "+
			"(#204-G3): resourceVersion %q -> %q", cm1.ResourceVersion, cm2.ResourceVersion)
	}

	// A genuine ephemeris change must still write → resourceVersion advances.
	changed := provider.EphemerisUpdate{ECEF: &ntnv1alpha1.EphemerisECEF{PosX: 99999, PosY: 88888, PosZ: 77777}}
	if err := p.PushEphemerisUpdate(ctx, owner, changed); err != nil {
		t.Fatalf("push (changed): %v", err)
	}
	var cm3 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm3); err != nil {
		t.Fatalf("get after changed push: %v", err)
	}
	if cm2.ResourceVersion == cm3.ResourceVersion {
		t.Fatal("a changed ephemeris must rewrite the ConfigMap: resourceVersion did not advance")
	}
}
