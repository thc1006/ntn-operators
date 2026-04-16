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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// ConfigMapName is the name of the ConfigMap that holds the generated NTN config.
const ConfigMapName = "ocudu-ntn-config"

// Provider implements provider.NTNProvider for OCUDU/srsRAN gNB.
// It generates a geo_ntn.yml configuration and stores it in a ConfigMap
// that can be mounted by the OCUDU Helm chart.
type Provider struct {
	client        client.Client
	lastSpec      *ntnv1alpha1.NTNCellConfigSpec
	lastNamespace string
}

// NewProvider creates an OCUDU Provider with the given K8s client.
func NewProvider(c client.Client) *Provider {
	return &Provider{client: c}
}

// ApplyCellConfig generates OCUDU-compatible NTN config YAML and writes it
// to a ConfigMap in the provider's target namespace.
func (p *Provider) ApplyCellConfig(ctx context.Context, spec *ntnv1alpha1.NTNCellConfigSpec) error {
	yamlData, err := GenerateConfig(spec)
	if err != nil {
		return fmt.Errorf("generating OCUDU config: %w", err)
	}

	namespace := spec.Provider.Namespace
	if namespace == "" {
		namespace = "default"
	}

	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapName, Namespace: namespace}
	err = p.client.Get(ctx, key, cm)

	if apierrors.IsNotFound(err) {
		// Create new ConfigMap.
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "ntn-operators",
					"app.kubernetes.io/component":  "ocudu-ntn-config",
				},
			},
			Data: map[string]string{
				"geo_ntn.yml": string(yamlData),
			},
		}
		if err := p.client.Create(ctx, cm); err != nil {
			return fmt.Errorf("creating ConfigMap: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting ConfigMap: %w", err)
	} else {
		// Update existing ConfigMap.
		cm.Data["geo_ntn.yml"] = string(yamlData)
		if err := p.client.Update(ctx, cm); err != nil {
			return fmt.Errorf("updating ConfigMap: %w", err)
		}
	}

	p.lastSpec = spec
	p.lastNamespace = namespace
	return nil
}

// GetCellStatus returns the current status of the applied configuration.
func (p *Provider) GetCellStatus(ctx context.Context) (*ntnv1alpha1.NTNCellConfigStatus, error) {
	status := &ntnv1alpha1.NTNCellConfigStatus{}

	if p.lastSpec == nil || p.lastNamespace == "" {
		return status, nil
	}

	// Verify ConfigMap exists.
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapName, Namespace: p.lastNamespace}
	if err := p.client.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return status, nil
		}
		return status, fmt.Errorf("checking ConfigMap: %w", err)
	}

	status.AppliedKoffset = p.lastSpec.NTN.CellSpecificKoffset
	status.ConfigMapRef = ConfigMapName
	return status, nil
}
