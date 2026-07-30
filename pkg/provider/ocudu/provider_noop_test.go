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

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
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

// orbitalSpec returns a spec whose ephemeris is the Keplerian orbital variant, so the
// tests exercise the renderEphemerisBlock / GenerateConfig orbital branch (semi_major_axis,
// eccentricity, inclination, longitude, periapsis, mean_anomaly) alongside the ECEF branch.
// Values are in-range per the CRD kubebuilder bounds and chosen so the codepoint→physical-SI
// conversions produce non-integer floats: a float-format divergence between the two renderers
// would then surface in the byte comparison, not hide behind round numbers.
func orbitalSpec() *ntnv1alpha1.NTNCellConfigSpec {
	return &ntnv1alpha1.NTNCellConfigSpec{
		Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: "ntn-system"},
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisOrbital: &ntnv1alpha1.EphemerisOrbital{
				SemiMajorAxis:  6900000, // metres (LEO ~520 km) — passthrough int
				Eccentricity:   1000,    // ×1e-6 → 0.001
				Inclination:    530000,  // 1e-4 deg (53°) → radians
				RightAscension: 1200000, // 1e-4 deg (120°) → radians
				ArgOfPeriapsis: 900000,  // 1e-4 deg (90°) → radians
				MeanAnomaly:    450000,  // 1e-4 deg (45°) → radians
			},
			PayloadType: "transparent",
		},
	}
}

// assertPushRoundTripNoChurn drives the REAL bootstrap workflow for one ephemeris variant
// and pins the two-renderer byte-equality contract end to end. PushEphemerisUpdate re-renders
// the ephemeris block with a SEPARATE renderer (renderEphemerisBlock + replaceEphemeris's line
// surgery) from the full-config GenerateConfig that ApplyCellConfig writes; renderEphemerisBlock
// only promises in a comment to apply "the SAME codepoint→physical-SI conversions as
// GenerateConfig". Nothing pinned that the two agree byte-for-byte. If they ever diverge
// (whitespace, float format, key name/order, a scale, or replaceEphemeris eating a surrounding
// line), the bytes Push writes stop equalling GenerateConfig(spec), so the NEXT Apply on every
// SatelliteEphemeris fan-out rewrites the byte-different ConfigMap and churns every watcher on
// the ~3-minute cadence (#204-G3) — silently, since no error is raised.
//
// It asserts BOTH halves the churn depends on:
//
//	(contract) the post-Push ConfigMap byte-equals GenerateConfig(spec); AND
//	(no churn) a second Apply on the same spec is a no-op (resourceVersion unchanged).
//
// The resourceVersion check is meaningful here (unlike a redundant identical write, which the
// store would short-circuit) because a renderer divergence is a REAL byte change: the post-Push
// Apply would genuinely rewrite and bump resourceVersion. Mutation: perturb renderEphemerisBlock
// (rename a key, drop a scale factor, or change the indent) → the contract assertion fails first,
// and the round-trip assertion independently catches the same divergence (the post-Push Apply
// then rewrites and bumps resourceVersion).
func assertPushRoundTripNoChurn(t *testing.T, spec *ntnv1alpha1.NTNCellConfigSpec, update provider.EphemerisUpdate) {
	t.Helper()
	p := newTestProvider(t)
	ctx := context.Background()
	owner := ownerFor("test-cr")
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, owner, spec, ownerScheme(t)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Push the SAME ephemeris the spec already carries (the steady-state fan-out: a
	// re-resolved SatelliteEphemeris with unchanged elements). The re-render must
	// reproduce exactly what GenerateConfig emitted for that ephemeris.
	if err := p.PushEphemerisUpdate(ctx, owner, update); err != nil {
		t.Fatalf("push: %v", err)
	}

	want, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	var afterPush corev1.ConfigMap
	if err := p.client.Get(ctx, key, &afterPush); err != nil {
		t.Fatalf("get after push: %v", err)
	}
	if got := afterPush.Data[configDataKey]; got != string(want) {
		t.Fatalf("PushEphemerisUpdate must reproduce GenerateConfig(spec) byte-for-byte "+
			"(the two ephemeris renderers must agree, else every fan-out re-Apply churns the ConfigMap)"+
			":\n--- push wrote ---\n%s\n--- GenerateConfig ---\n%s", got, want)
	}

	// Round-trip: the next Apply on the same spec finds byte-identical stored content, so the
	// #204-G3 no-op guard skips the write and resourceVersion does not advance.
	if err := p.ApplyCellConfig(ctx, owner, spec, ownerScheme(t)); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	var afterApply corev1.ConfigMap
	if err := p.client.Get(ctx, key, &afterApply); err != nil {
		t.Fatalf("get after re-apply: %v", err)
	}
	if afterPush.ResourceVersion != afterApply.ResourceVersion {
		t.Fatalf("a post-Push Apply on the same spec must be a round-trip no-op: "+
			"resourceVersion %q -> %q", afterPush.ResourceVersion, afterApply.ResourceVersion)
	}
}

// TestRoundTrip_PushMatchesGenerateConfig_ECEF pins the two-renderer contract for the ECEF
// state-vector path (pos_x…vel_z, ×1.3 m / ×0.06 m·s⁻¹). See assertPushRoundTripNoChurn.
func TestRoundTrip_PushMatchesGenerateConfig_ECEF(t *testing.T) {
	spec := geoSpec()
	assertPushRoundTripNoChurn(t, spec, provider.EphemerisUpdate{ECEF: spec.NTN.EphemerisECEF})
}

// TestRoundTrip_PushMatchesGenerateConfig_Orbital pins the two-renderer contract for the
// Keplerian orbital path (semi_major_axis, eccentricity, inclination, longitude, periapsis,
// mean_anomaly). It is a distinct branch of both renderers from ECEF, so it needs its own
// pin. See assertPushRoundTripNoChurn.
func TestRoundTrip_PushMatchesGenerateConfig_Orbital(t *testing.T) {
	spec := orbitalSpec()
	assertPushRoundTripNoChurn(t, spec, provider.EphemerisUpdate{Orbital: spec.NTN.EphemerisOrbital})
}
