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
	"crypto/sha256"
	"encoding/hex"
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

// ConfigMapPrefix is the prefix for ConfigMap names. Final name = prefix + CR name.
const ConfigMapPrefix = "ocudu-ntn-"

// maxK8sNameLen is the maximum length for a Kubernetes object name.
const maxK8sNameLen = 253

// ConfigMapNameFor returns the ConfigMap name for a given NTNCellConfig CR name.
// If the resulting name exceeds K8s limits, it is truncated with a 8-char hash
// suffix to prevent collisions between different long CR names.
func ConfigMapNameFor(crName string) string {
	name := ConfigMapPrefix + crName
	if len(name) > maxK8sNameLen {
		h := sha256.Sum256([]byte(name))
		suffix := hex.EncodeToString(h[:4]) // 8 hex chars
		// Truncate to leave room for "-" + 8-char hash
		truncLen := maxK8sNameLen - 9 // 253 - 9 = 244
		name = strings.TrimRight(name[:truncLen], "-.") + "-" + suffix
	}
	return name
}

// Provider implements provider.NTNProvider for OCUDU/srsRAN gNB.
// It is stateless — status is derived from the ConfigMap.
type Provider struct {
	client client.Client
}

// NewProvider creates an OCUDU Provider with the given K8s client.
func NewProvider(c client.Client) *Provider {
	return &Provider{client: c}
}

// ApplyCellConfig generates OCUDU-compatible NTN config YAML and writes it
// to a ConfigMap in the provider's target namespace.
func (p *Provider) ApplyCellConfig(ctx context.Context, crName string, spec *ntnv1alpha1.NTNCellConfigSpec) error {
	if spec == nil {
		return fmt.Errorf("spec must not be nil")
	}
	if spec.Provider.Namespace == "" {
		return fmt.Errorf("provider namespace must be set")
	}

	yamlData, err := GenerateConfig(spec)
	if err != nil {
		return fmt.Errorf("generating OCUDU config: %w", err)
	}

	namespace := spec.Provider.Namespace
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapNameFor(crName), Namespace: namespace}
	err = p.client.Get(ctx, key, cm)

	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapNameFor(crName),
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

// GetCellStatus derives status by reading the ConfigMap in the given namespace.
func (p *Provider) GetCellStatus(
	ctx context.Context, crName, namespace string,
) (*ntnv1alpha1.NTNCellConfigStatus, error) {
	status := &ntnv1alpha1.NTNCellConfigStatus{}

	if namespace == "" {
		return status, fmt.Errorf("namespace must not be empty")
	}

	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapNameFor(crName), Namespace: namespace}
	if err := p.client.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return status, nil
		}
		return status, fmt.Errorf("reading ConfigMap %s/%s: %w", namespace, ConfigMapNameFor(crName), err)
	}

	status.ConfigMapRef = cm.Name

	if koffsetStr, ok := cm.Annotations["ntn.operators.dev/koffset"]; ok {
		if v, err := strconv.Atoi(koffsetStr); err == nil {
			status.AppliedKoffset = v
		}
	}

	if _, ok := cm.Data["geo_ntn.yml"]; !ok {
		return status, fmt.Errorf("ConfigMap %s/%s missing geo_ntn.yml key", namespace, ConfigMapNameFor(crName))
	}

	return status, nil
}
