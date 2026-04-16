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
			EphemerisECEF: ntnv1alpha1.EphemerisECEF{
				PosX: 20922195, PosY: 1967783, PosZ: 19770302,
			},
			PayloadType: "transparent",
		},
	}
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = ntnv1alpha1.AddToScheme(scheme)

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

	err := p.ApplyCellConfig(ctx, geoSpec())
	if err != nil {
		t.Fatalf("ApplyCellConfig error: %v", err)
	}

	// Verify ConfigMap was created.
	var cm corev1.ConfigMap
	err = p.client.Get(ctx, types.NamespacedName{
		Name:      ConfigMapName,
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
	err := p.ApplyCellConfig(ctx, spec)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Update koffset and re-apply.
	spec.NTN.CellSpecificKoffset = 500
	err = p.ApplyCellConfig(ctx, spec)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}

	// Verify updated.
	var cm corev1.ConfigMap
	err = p.client.Get(ctx, types.NamespacedName{Name: ConfigMapName, Namespace: "ntn-system"}, &cm)
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
	_ = p.ApplyCellConfig(ctx, spec)

	status, err := p.GetCellStatus(ctx)
	if err != nil {
		t.Fatalf("GetCellStatus error: %v", err)
	}
	if status.AppliedKoffset != 150 {
		t.Errorf("expected appliedKoffset=150, got %d", status.AppliedKoffset)
	}
	if status.ConfigMapRef != ConfigMapName {
		t.Errorf("expected configMapRef=%s, got %s", ConfigMapName, status.ConfigMapRef)
	}
}

func TestGetCellStatus_NoConfigMap(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	status, err := p.GetCellStatus(ctx)
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
	err := p.ApplyCellConfig(ctx, spec)
	if err == nil {
		t.Fatal("expected error for empty namespace")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
