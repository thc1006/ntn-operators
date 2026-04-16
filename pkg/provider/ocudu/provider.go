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
	"strconv"
	"strings"

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
// It is stateless — status is derived from the ConfigMap, not from
// in-memory state, making it safe for concurrent reconciles.
type Provider struct {
	client client.Client
}

// NewProvider creates an OCUDU Provider with the given K8s client.
func NewProvider(c client.Client) *Provider {
	return &Provider{client: c}
}

// ApplyCellConfig generates OCUDU-compatible NTN config YAML and writes it
// to a ConfigMap in the provider's target namespace.
// The namespace MUST be set on spec.Provider.Namespace by the controller
// before calling this method.
func (p *Provider) ApplyCellConfig(ctx context.Context, spec *ntnv1alpha1.NTNCellConfigSpec) error {
	if spec.Provider.Namespace == "" {
		return fmt.Errorf("provider namespace must be set")
	}

	yamlData, err := GenerateConfig(spec)
	if err != nil {
		return fmt.Errorf("generating OCUDU config: %w", err)
	}

	namespace := spec.Provider.Namespace
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapName, Namespace: namespace}
	err = p.client.Get(ctx, key, cm)

	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "ntn-operators",
					"app.kubernetes.io/component":  "ocudu-ntn-config",
				},
				Annotations: map[string]string{
					"ntn.operators.dev/koffset": strconv.Itoa(spec.NTN.CellSpecificKoffset),
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
		// Update existing ConfigMap. Initialize Data if nil.
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		cm.Data["geo_ntn.yml"] = string(yamlData)
		if cm.Annotations == nil {
			cm.Annotations = make(map[string]string)
		}
		cm.Annotations["ntn.operators.dev/koffset"] = strconv.Itoa(spec.NTN.CellSpecificKoffset)
		if err := p.client.Update(ctx, cm); err != nil {
			return fmt.Errorf("updating ConfigMap: %w", err)
		}
	}

	return nil
}

// GetCellStatus derives status from the ConfigMap (stateless).
// Safe for concurrent reconciles and multiple NTNCellConfig CRs.
func (p *Provider) GetCellStatus(ctx context.Context) (*ntnv1alpha1.NTNCellConfigStatus, error) {
	status := &ntnv1alpha1.NTNCellConfigStatus{}

	// Search all namespaces for the ConfigMap (simplified — in practice,
	// the controller should pass the target namespace).
	var cmList corev1.ConfigMapList
	if err := p.client.List(ctx, &cmList, client.MatchingLabels{
		"app.kubernetes.io/component": "ocudu-ntn-config",
	}); err != nil {
		return status, nil
	}

	if len(cmList.Items) == 0 {
		return status, nil
	}

	cm := cmList.Items[0]
	status.ConfigMapRef = cm.Name

	if koffsetStr, ok := cm.Annotations["ntn.operators.dev/koffset"]; ok {
		if v, err := strconv.Atoi(koffsetStr); err == nil {
			status.AppliedKoffset = v
		}
	}

	// Verify geo_ntn.yml exists.
	if _, ok := cm.Data["geo_ntn.yml"]; !ok {
		return status, fmt.Errorf("ConfigMap %s/%s missing geo_ntn.yml key", cm.Namespace, cm.Name)
	}
	_ = strings.Contains // suppress unused import if needed

	return status, nil
}
