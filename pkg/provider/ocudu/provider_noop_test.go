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
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestApplyCellConfig_NoOpOnIdenticalContent_G3 pins #204-G3a: re-applying an identical
// (static-spec-derived) config must NOT rewrite the ConfigMap — an unconditional Update
// bumps resourceVersion and churns every watcher on the ~3-minute re-propagation fan-out.
// A genuine change must still write. Mutation: drop the content-equality skip → the
// identical re-apply bumps resourceVersion and the first assertion fails.
func TestApplyCellConfig_NoOpOnIdenticalContent_G3(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	spec := geoSpec()
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var cm1 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm1); err != nil {
		t.Fatalf("get after first apply: %v", err)
	}

	// Re-apply the SAME spec → no-op, resourceVersion must not advance.
	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("second (identical) apply: %v", err)
	}
	var cm2 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm2); err != nil {
		t.Fatalf("get after second apply: %v", err)
	}
	if cm1.ResourceVersion != cm2.ResourceVersion {
		t.Fatalf("an identical re-apply must NOT rewrite the ConfigMap (#204-G3): resourceVersion %q -> %q",
			cm1.ResourceVersion, cm2.ResourceVersion)
	}

	// A real change (koffset) must still write → resourceVersion advances.
	spec.NTN.CellSpecificKoffset = 500
	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("third (changed) apply: %v", err)
	}
	var cm3 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm3); err != nil {
		t.Fatalf("get after third apply: %v", err)
	}
	if cm2.ResourceVersion == cm3.ResourceVersion {
		t.Fatalf("a changed spec must rewrite the ConfigMap: resourceVersion stayed %q", cm3.ResourceVersion)
	}
	if !contains(cm3.Data["geo_ntn.yml"], "cell_specific_koffset: 500") {
		t.Error("the changed apply must persist koffset=500")
	}
}

// TestApplyCellConfig_AdoptsUnownedIdenticalConfigMap_G3 pins the load-bearing adoption
// invariant behind the `alreadyOwned` snapshot (#204-G3, review M2): a pre-existing,
// operator-managed but UNOWNED ConfigMap whose content ALREADY equals the desired output
// must still be Updated so the freshly-set controller reference is persisted — the no-op
// content-equality skip must not short-circuit before the adoption write. The other no-op
// test starts from differing content, so it would NOT catch a dropped `alreadyOwned` guard.
// Mutation: drop `alreadyOwned &&` from the skip → this ConfigMap is never adopted.
func TestApplyCellConfig_AdoptsUnownedIdenticalConfigMap_G3(t *testing.T) {
	ctx := context.Background()
	spec := geoSpec()
	// The exact content ApplyCellConfig would generate for this spec.
	yamlData, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	seed := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapNameFor("test-cr"),
			Namespace: "ntn-system",
			// operator-managed labels, but NO controller owner reference (unowned).
			Labels: map[string]string{managedByLabel: managedByValue, componentLabel: componentValue},
			Annotations: map[string]string{
				"ntn.operators.dev/koffset": strconv.Itoa(spec.NTN.CellSpecificKoffset),
			},
		},
		Data: map[string]string{configDataKey: string(yamlData)}, // identical to desired
	}
	p := newTestProviderWith(t, seed)
	owner := ownerFor("test-cr")

	if err := p.ApplyCellConfig(ctx, owner, spec, ownerScheme(t)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got corev1.ConfigMap
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}
	if err := p.client.Get(ctx, key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !metav1.IsControlledBy(&got, owner) {
		t.Fatal("an unowned operator-managed ConfigMap with identical content must still be ADOPTED " +
			"(controller ref persisted); the alreadyOwned guard must not let the no-op skip bypass the adoption write")
	}
}

// TestApplyCellConfig_NoOp_NonKoffsetYAMLChangeWrites_M2 pins the independence of the YAML
// equality clause (#224 review-2 M2): a change to a YAML-producing field that is NOT the
// koffset annotation (here ta_common) must still rewrite the ConfigMap. The existing
// changed-spec test moves koffset, which changes BOTH the YAML and the annotation, so it
// could not catch a dropped YAML comparison. Mutation: drop `cm.Data[...] == desiredData`
// from the skip → this no-ops and resourceVersion does not advance.
func TestApplyCellConfig_NoOp_NonKoffsetYAMLChangeWrites_M2(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	spec := geoSpec()
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var cm1 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm1); err != nil {
		t.Fatalf("get1: %v", err)
	}

	spec.NTN.TACommon = 42 // changes the YAML (ta_common) but NOT the koffset annotation
	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var cm2 corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm2); err != nil {
		t.Fatalf("get2: %v", err)
	}
	if cm1.ResourceVersion == cm2.ResourceVersion {
		t.Fatal("a non-koffset YAML change must rewrite the ConfigMap " +
			"(the YAML equality clause must be required independently)")
	}
}

// TestApplyCellConfig_NoOp_AnnotationRepaired_M2 pins the independence of the koffset-annotation
// equality clause (#224 review-2 M2): if the annotation is corrupted while the YAML stays
// correct, ApplyCellConfig must repair it. Mutation: drop the annotation comparison from the
// skip → the corrupted annotation is never repaired.
func TestApplyCellConfig_NoOp_AnnotationRepaired_M2(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	spec := geoSpec()
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var cm corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm); err != nil {
		t.Fatalf("get: %v", err)
	}
	cm.Annotations["ntn.operators.dev/koffset"] = "99999" // corrupt ONLY the annotation
	if err := p.client.Update(ctx, &cm); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	var before corev1.ConfigMap
	_ = p.client.Get(ctx, key, &before)

	if err := p.ApplyCellConfig(ctx, ownerFor("test-cr"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	var after corev1.ConfigMap
	if err := p.client.Get(ctx, key, &after); err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.Annotations["ntn.operators.dev/koffset"] != strconv.Itoa(spec.NTN.CellSpecificKoffset) {
		t.Fatalf("the corrupted koffset annotation must be repaired, got %q", after.Annotations["ntn.operators.dev/koffset"])
	}
	if after.ResourceVersion == before.ResourceVersion {
		t.Fatal("repairing the annotation must Update (the annotation equality clause must be required independently)")
	}
}
