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

package ocudu

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

func geoSpec() *ntnv1alpha1.NTNCellConfigSpec {
	return &ntnv1alpha1.NTNCellConfigSpec{
		Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: "ntn-system"},
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			TACommon:            0,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 20922195, PosY: 1967783, PosZ: 19770302,
			},
			PayloadType: "transparent",
		},
	}
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(ntnv1alpha1): %v", err)
	}

	// Pre-create the target namespace.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ntn-system"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()

	return NewProvider(client)
}

// Compile-time check: Provider implements NTNProvider.
var _ provider.NTNProvider = &Provider{}

func TestApplyCellConfig_CreatesConfigMap(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	err := p.ApplyCellConfig(ctx, "test-cr", geoSpec())
	if err != nil {
		t.Fatalf("ApplyCellConfig error: %v", err)
	}

	// Verify ConfigMap was created.
	var cm corev1.ConfigMap
	err = p.client.Get(ctx, types.NamespacedName{
		Name:      ConfigMapNameFor("test-cr"),
		Namespace: "ntn-system",
	}, &cm)
	if err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}

	yamlContent, ok := cm.Data["geo_ntn.yml"]
	if !ok {
		t.Fatal("ConfigMap missing geo_ntn.yml key")
	}
	if len(yamlContent) == 0 {
		t.Fatal("geo_ntn.yml content is empty")
	}
}

func TestApplyCellConfig_UpdatesExistingConfigMap(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	// First apply.
	spec := geoSpec()
	err := p.ApplyCellConfig(ctx, "test-cr", spec)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Update koffset and re-apply.
	spec.NTN.CellSpecificKoffset = 500
	err = p.ApplyCellConfig(ctx, "test-cr", spec)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}

	// Verify updated.
	var cm corev1.ConfigMap
	err = p.client.Get(ctx, types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: "ntn-system"}, &cm)
	if err != nil {
		t.Fatalf("Get ConfigMap after update: %v", err)
	}
	if !contains(cm.Data["geo_ntn.yml"], "cell_specific_koffset: 500") {
		t.Error("ConfigMap should have updated koffset=500")
	}
}

func TestGetCellStatus_ReturnsAppliedConfig(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	spec := geoSpec()
	err := p.ApplyCellConfig(ctx, "test-cr", spec)
	if err != nil {
		t.Fatalf("ApplyCellConfig: %v", err)
	}

	status, err := p.GetCellStatus(ctx, "test-cr", "ntn-system")
	if err != nil {
		t.Fatalf("GetCellStatus error: %v", err)
	}
	if status.AppliedKoffset != 150 {
		t.Errorf("expected appliedKoffset=150, got %d", status.AppliedKoffset)
	}
	if status.ConfigMapRef != ConfigMapNameFor("test-cr") {
		t.Errorf("expected configMapRef=%s, got %s", ConfigMapNameFor("test-cr"), status.ConfigMapRef)
	}
}

func TestGetCellStatus_NoConfigMap(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	status, err := p.GetCellStatus(ctx, "test-cr", "ntn-system")
	if err != nil {
		t.Fatalf("GetCellStatus should not error: %v", err)
	}
	if status.ConfigMapRef != "" {
		t.Errorf("expected empty configMapRef, got %s", status.ConfigMapRef)
	}
}

func TestApplyCellConfig_EmptyNamespace(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	spec := geoSpec()
	spec.Provider.Namespace = ""
	err := p.ApplyCellConfig(ctx, "test-cr", spec)
	if err == nil {
		t.Fatal("expected error for empty namespace")
	}
}

func TestApplyCellConfig_UpdateExisting(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	// First apply creates the ConfigMap.
	spec := geoSpec()
	if err := p.ApplyCellConfig(ctx, "test-cr", spec); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Second apply with different koffset should update.
	spec.NTN.CellSpecificKoffset = 200
	if err := p.ApplyCellConfig(ctx, "test-cr", spec); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	// Verify updated.
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapNameFor("test-cr"), Namespace: spec.Provider.Namespace}
	if err := p.client.Get(ctx, key, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Annotations["ntn.operators.dev/koffset"] != "200" {
		t.Errorf("expected koffset=200, got %s", cm.Annotations["ntn.operators.dev/koffset"])
	}
}

func TestApplyCellConfig_NilSpec(t *testing.T) {
	p := newTestProvider(t)
	err := p.ApplyCellConfig(context.Background(), "cr", nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestConfigMapNameFor_Truncation(t *testing.T) {
	// CR name with 250 chars should be truncated
	longName := strings.Repeat("a", 250)
	name := ConfigMapNameFor(longName)
	if len(name) > maxK8sNameLen {
		t.Errorf("name length %d exceeds %d", len(name), maxK8sNameLen)
	}
	if strings.HasSuffix(name, "-") || strings.HasSuffix(name, ".") {
		t.Errorf("name ends with invalid char: %q", name)
	}
}

func TestConfigMapNameFor_Short(t *testing.T) {
	name := ConfigMapNameFor("my-cell")
	if name != "ocudu-ntn-my-cell" {
		t.Errorf("expected ocudu-ntn-my-cell, got %s", name)
	}
}

func TestGetCellStatus_MissingGeoNtn(t *testing.T) {
	// ConfigMap exists but missing geo_ntn.yml key.
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapNameFor("test"),
			Namespace: "ns",
			Annotations: map[string]string{
				"ntn.operators.dev/koffset": "100",
			},
		},
		Data: map[string]string{}, // missing geo_ntn.yml
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
	p := &Provider{client: c}
	_, err := p.GetCellStatus(context.Background(), "test", "ns")
	if err == nil {
		t.Fatal("expected error for missing geo_ntn.yml")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestConfigMapNameFor_NoCollision(t *testing.T) {
	// Two different long names that share a 243-char prefix should produce
	// different ConfigMap names (hash suffix prevents collision).
	base := strings.Repeat("a", 245)
	nameA := ConfigMapNameFor(base + "-alpha")
	nameB := ConfigMapNameFor(base + "-bravo")

	if nameA == nameB {
		t.Errorf("collision: different CR names produced same ConfigMap name %q", nameA)
	}
	if len(nameA) > maxK8sNameLen {
		t.Errorf("name exceeds K8s limit: %d", len(nameA))
	}
}

func TestPushEphemerisUpdate_ECEF(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	// First apply to create the ConfigMap.
	spec := geoSpec()
	if err := p.ApplyCellConfig(ctx, "test-cr", spec); err != nil {
		t.Fatalf("ApplyCellConfig: %v", err)
	}

	// Push updated ECEF.
	update := provider.EphemerisUpdate{
		ECEF: &ntnv1alpha1.EphemerisECEF{
			PosX: 99999, PosY: 88888, PosZ: 77777,
			VelX: 10, VelY: 20, VelZ: 30,
		},
	}
	if err := p.PushEphemerisUpdate(ctx, "test-cr", "ntn-system", update); err != nil {
		t.Fatalf("PushEphemerisUpdate: %v", err)
	}

	// Verify ConfigMap was updated.
	var cm corev1.ConfigMap
	key := types.NamespacedName{
		Name:      ConfigMapNameFor("test-cr"),
		Namespace: "ntn-system",
	}
	if err := p.client.Get(ctx, key, &cm); err != nil {
		t.Fatalf("Get ConfigMap: %v", err)
	}
	yaml := cm.Data["geo_ntn.yml"]
	if !contains(yaml, "pos_x: 99999") {
		t.Errorf("expected updated pos_x in YAML:\n%s", yaml)
	}
	if !contains(yaml, "vel_z: 30") {
		t.Errorf("expected updated vel_z in YAML:\n%s", yaml)
	}
	// Old values should be gone.
	if contains(yaml, "pos_x: 20922195") {
		t.Error("old pos_x should have been replaced")
	}
}

func TestPushEphemerisUpdate_Orbital(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	// First apply to create the ConfigMap.
	if err := p.ApplyCellConfig(ctx, "test-cr", geoSpec()); err != nil {
		t.Fatalf("ApplyCellConfig: %v", err)
	}

	// Push orbital update (replaces the ECEF block).
	update := provider.EphemerisUpdate{
		Orbital: &ntnv1alpha1.EphemerisOrbital{
			SemiMajorAxis:  7000000,
			Eccentricity:   100,
			Inclination:    970000,
			RightAscension: 500000,
			ArgOfPeriapsis: 300000,
			MeanAnomaly:    100000,
		},
	}
	if err := p.PushEphemerisUpdate(ctx, "test-cr", "ntn-system", update); err != nil {
		t.Fatalf("PushEphemerisUpdate: %v", err)
	}

	var cm corev1.ConfigMap
	key := types.NamespacedName{
		Name:      ConfigMapNameFor("test-cr"),
		Namespace: "ntn-system",
	}
	if err := p.client.Get(ctx, key, &cm); err != nil {
		t.Fatalf("Get ConfigMap: %v", err)
	}
	yaml := cm.Data["geo_ntn.yml"]
	if !contains(yaml, "ephemeris_orbital:") {
		t.Errorf("expected ephemeris_orbital block:\n%s", yaml)
	}
	if !contains(yaml, "semi_major_axis: 7000000") {
		t.Errorf("expected semi_major_axis:\n%s", yaml)
	}
	// Old ECEF block should be gone.
	if contains(yaml, "ephemeris_info_ecef:") {
		t.Error("old ECEF block should have been replaced")
	}
}

func TestPushEphemerisUpdate_BothSet(t *testing.T) {
	p := newTestProvider(t)
	err := p.PushEphemerisUpdate(
		context.Background(), "cr", "ns",
		provider.EphemerisUpdate{
			ECEF:    &ntnv1alpha1.EphemerisECEF{PosX: 1},
			Orbital: &ntnv1alpha1.EphemerisOrbital{SemiMajorAxis: 1},
		},
	)
	if err == nil {
		t.Fatal("expected error when both ECEF and Orbital are set")
	}
}

func TestPushEphemerisUpdate_NoConfigMap(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	update := provider.EphemerisUpdate{
		ECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1},
	}
	err := p.PushEphemerisUpdate(ctx, "nonexistent", "ntn-system", update)
	if err == nil {
		t.Fatal("expected error when ConfigMap doesn't exist")
	}
}

func TestPushEphemerisUpdate_NeitherSet(t *testing.T) {
	p := newTestProvider(t)
	err := p.PushEphemerisUpdate(
		context.Background(), "cr", "ns", provider.EphemerisUpdate{},
	)
	if err == nil {
		t.Fatal("expected error when neither ECEF nor Orbital is set")
	}
}
