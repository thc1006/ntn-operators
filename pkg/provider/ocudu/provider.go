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
	"errors"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// ConfigMapPrefix is the prefix for ConfigMap names. Final name = prefix + CR name.
const ConfigMapPrefix = "ocudu-ntn-"

// configDataKey is the ConfigMap data key holding the OCUDU NTN config document.
// Its presence also marks a ConfigMap as a genuine operator artifact for adoption.
const configDataKey = "geo_ntn.yml"

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

// Provider implements provider.NTNProvider for OCUDU gNB.
// It is stateless — status is derived from the ConfigMap.
type Provider struct {
	client client.Client
	// apiReader performs uncached reads straight from the API server. The controller
	// wires it to mgr.GetAPIReader() so ownership checks and the status read-back see a
	// just-created ConfigMap immediately, instead of racing the informer cache — which
	// is eventually consistent and can lag a Create by a cache-sync interval, making a
	// same-reconcile read-back return NotFound and persist an empty configMapRef. Nil
	// in unit tests, where reader() falls back to the (already-consistent) fake client.
	apiReader client.Reader
}

// reader returns the uncached API reader when wired, else the cached client.
func (p *Provider) reader() client.Reader {
	if p.apiReader != nil {
		return p.apiReader
	}
	return p.client
}

// WithAPIReader wires an uncached reader (mgr.GetAPIReader()) for read-back
// consistency and returns the receiver for chaining.
func (p *Provider) WithAPIReader(r client.Reader) *Provider {
	p.apiReader = r
	return p
}

// Management labels stamped on every ConfigMap this provider creates. They let the
// provider recognize (and safely adopt) its own artifact even when a controller
// owner reference is missing — e.g. a ConfigMap created by a pre-atomic-reference
// operator version — while never touching an unlabeled foreign object.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "ntn-operators"
	componentLabel = "app.kubernetes.io/component"
	componentValue = "ocudu-ntn-config"
)

// isOperatorManaged reports whether cm carries this provider's management labels.
func isOperatorManaged(cm *corev1.ConfigMap) bool {
	return cm.Labels[managedByLabel] == managedByValue && cm.Labels[componentLabel] == componentValue
}

// Cleanup deletes the provider's ConfigMap for owner, but only when it is
// controller-owned by owner (UID match via metav1.IsControlledBy) — a same-named
// ConfigMap the operator does not own is left untouched. Called by the finalizer
// during CR deletion.
func (p *Provider) Cleanup(ctx context.Context, owner *ntnv1alpha1.NTNCellConfig) error {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapNameFor(owner.GetName()), Namespace: owner.GetNamespace()}
	if err := p.reader().Get(ctx, key, cm); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(cm, owner) {
		return nil
	}
	return client.IgnoreNotFound(p.client.Delete(ctx, cm))
}

// NewProvider creates an OCUDU Provider with the given K8s client.
func NewProvider(c client.Client) *Provider {
	return &Provider{client: c}
}

// ApplyCellConfig generates OCUDU-compatible NTN config YAML and writes it to a
// ConfigMap in the provider's target namespace. On create it sets a controller
// reference to owner ATOMICALLY; on update it overwrites only a ConfigMap it owns
// (or adopts an unowned but operator-labeled one from a pre-atomic-ref version),
// returning ErrConfigMapNotOwned for a foreign object.
func (p *Provider) ApplyCellConfig(
	ctx context.Context, owner *ntnv1alpha1.NTNCellConfig, spec *ntnv1alpha1.NTNCellConfigSpec, scheme *runtime.Scheme,
) error {
	if spec == nil {
		return fmt.Errorf("spec must not be nil")
	}
	if spec.Provider.Namespace == "" {
		return fmt.Errorf("provider namespace must be set")
	}
	// Defense-in-depth: the ConfigMap is created in spec.Provider.Namespace with a
	// controller reference to owner, which Kubernetes forbids across namespaces — so
	// require them equal here rather than trusting the caller to have aligned them.
	if owner.GetNamespace() != spec.Provider.Namespace {
		return fmt.Errorf("owner namespace %q must equal provider namespace %q",
			owner.GetNamespace(), spec.Provider.Namespace)
	}

	yamlData, err := GenerateConfig(spec)
	if err != nil {
		return fmt.Errorf("generating OCUDU config: %w", err)
	}

	crName := owner.GetName()
	namespace := spec.Provider.Namespace
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: ConfigMapNameFor(crName), Namespace: namespace}
	err = p.reader().Get(ctx, key, cm)

	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ConfigMapNameFor(crName),
				Namespace: namespace,
				Labels: map[string]string{
					managedByLabel: managedByValue,
					componentLabel: componentValue,
				},
				Annotations: map[string]string{
					"ntn.operators.dev/koffset": strconv.Itoa(spec.NTN.CellSpecificKoffset),
				},
			},
			Data: map[string]string{
				configDataKey: string(yamlData),
			},
		}
		// Set the controller reference ATOMICALLY, before Create, so the ConfigMap
		// is owned (and GC-cascaded with the CR) from the instant it exists — no
		// best-effort second write that can fail and leave an unowned artifact.
		if err := controllerutil.SetControllerReference(owner, cm, scheme); err != nil {
			return fmt.Errorf("setting controller reference: %w", err)
		}
		if err := p.client.Create(ctx, cm); err != nil {
			return fmt.Errorf("creating ConfigMap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting ConfigMap: %w", err)
	}

	// Existing ConfigMap: overwrite only if we own it, or adopt it if it is unowned
	// but is a genuine operator artifact — carrying our management labels AND the
	// geo_ntn.yml config key (a pre-atomic-ref leftover). A ConfigMap owned by a
	// different controller, an unlabeled foreign object, or a labeled-but-empty
	// impostor that never held our config is refused.
	if !metav1.IsControlledBy(cm, owner) {
		if metav1.GetControllerOf(cm) != nil || !isOperatorManaged(cm) || cm.Data[configDataKey] == "" {
			return fmt.Errorf("%w: %s/%s", provider.ErrConfigMapNotOwned, namespace, ConfigMapNameFor(crName))
		}
		if err := controllerutil.SetControllerReference(owner, cm, scheme); err != nil {
			return fmt.Errorf("adopting ConfigMap: %w", err)
		}
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[configDataKey] = string(yamlData)
	if cm.Annotations == nil {
		cm.Annotations = make(map[string]string)
	}
	cm.Annotations["ntn.operators.dev/koffset"] = strconv.Itoa(spec.NTN.CellSpecificKoffset)
	if err := p.client.Update(ctx, cm); err != nil {
		return fmt.Errorf("updating ConfigMap: %w", err)
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
	if err := p.reader().Get(ctx, key, cm); err != nil {
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

	if _, ok := cm.Data[configDataKey]; !ok {
		return status, fmt.Errorf("ConfigMap %s/%s missing %s key", namespace, ConfigMapNameFor(crName), configDataKey)
	}

	return status, nil
}

// PushEphemerisUpdate updates the ephemeris section of the existing ConfigMap.
// Phase 1 implementation: reads the current config, replaces the ephemeris
// data, and writes it back. Future Phase 2 will use OCUDU's WebSocket API.
func (p *Provider) PushEphemerisUpdate(
	ctx context.Context, owner *ntnv1alpha1.NTNCellConfig, update provider.EphemerisUpdate,
) error {
	if owner.GetNamespace() == "" {
		return fmt.Errorf("owner namespace must not be empty")
	}
	if update.ECEF == nil && update.Orbital == nil {
		return fmt.Errorf("either ECEF or Orbital must be set")
	}
	if update.ECEF != nil && update.Orbital != nil {
		return fmt.Errorf("ECEF and Orbital are mutually exclusive")
	}

	crName := owner.GetName()
	namespace := owner.GetNamespace()
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      ConfigMapNameFor(crName),
		Namespace: namespace,
	}
	if err := p.reader().Get(ctx, key, cm); err != nil {
		return fmt.Errorf("reading ConfigMap %s/%s: %w",
			namespace, ConfigMapNameFor(crName), err)
	}
	// Re-verify ownership before mutating: never rewrite a ConfigMap the operator
	// does not control — e.g. a same-named foreign object created between the
	// ApplyCellConfig that made this CM and this bootstrap ephemeris rewrite.
	if !metav1.IsControlledBy(cm, owner) {
		return fmt.Errorf("%w: %s/%s", provider.ErrConfigMapNotOwned, namespace, ConfigMapNameFor(crName))
	}

	yamlContent, ok := cm.Data[configDataKey]
	if !ok {
		return fmt.Errorf(
			"ConfigMap %s/%s missing %s",
			namespace, ConfigMapNameFor(crName), configDataKey)
	}

	// Replace the ephemeris section in the existing YAML.
	updated, replaced := replaceEphemeris(yamlContent, update)
	if !replaced {
		return fmt.Errorf(
			"ConfigMap %s/%s has no ephemeris block to replace",
			namespace, ConfigMapNameFor(crName))
	}
	cm.Data[configDataKey] = updated

	if err := p.client.Update(ctx, cm); err != nil {
		return fmt.Errorf("updating ConfigMap: %w", err)
	}
	return nil
}

// PushRuntimeUpdate pushes a live NTN config update to the gNB's remote_control
// WebSocket via the ntn_config_update command (OCUDU MR !798) — no ConfigMap
// rewrite or gNB reload. Returns provider.ErrRuntimeUnsupported when no
// remote-control endpoint is configured (the caller falls back to the ConfigMap
// bootstrap path via PushEphemerisUpdate).
func (p *Provider) PushRuntimeUpdate(
	ctx context.Context, target provider.ResolvedRemoteControl, update provider.RuntimeUpdate,
) error {
	if target.Endpoint == "" {
		return provider.ErrRuntimeUnsupported
	}
	env, err := buildNTNConfigUpdate(update)
	if err != nil {
		// A malformed update can't succeed on retry — surface it as permanent.
		return fmt.Errorf("%w: %v", provider.ErrRuntimePushRejected, err)
	}
	err = pushNTNConfigUpdate(ctx, target, env)
	// Classify: gNB rejection / oversized / marshal are permanent (no tight
	// requeue); an unreachable endpoint is transient and returned as-is.
	var we *wsError
	if errors.As(err, &we) && !we.retryable() {
		return fmt.Errorf("%w: %v", provider.ErrRuntimePushRejected, we)
	}
	return err
}

// leadingSpaces returns the number of leading ASCII space characters in s.
func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// renderEphemerisBlock renders an ephemeris block indented to `indent` spaces
// for the key line and `indent+2` for its children, using OCUDU's key names
// (orbital elements are longitude/periapsis, not right_ascension/arg_of_periapsis).
//
// It applies the SAME codepoint→physical-SI conversions as GenerateConfig (see
// the conversion-factor constants in config.go): OCUDU's config parser expects
// physical metres / m·s⁻¹ / radians / dimensionless values, not the CRD's 3GPP
// codepoints. Emitting raw codepoints here would make the runtime ephemeris push
// either out-of-range-rejected (velocity/eccentricity) or silently off by 1.3×
// (position). semi_major_axis is already metres in the CRD (passthrough).
func renderEphemerisBlock(update provider.EphemerisUpdate, indent int) []string {
	pad := strings.Repeat(" ", indent)
	child := strings.Repeat(" ", indent+2)
	if update.Orbital != nil {
		o := update.Orbital
		return []string{
			pad + "ephemeris_orbital:",
			fmt.Sprintf("%ssemi_major_axis: %d", child, o.SemiMajorAxis),
			fmt.Sprintf("%seccentricity: %s", child, flt(float64(o.Eccentricity)*eccentricityScale)),
			fmt.Sprintf("%sinclination: %s", child, flt(float64(o.Inclination)*milliDegToRad)),
			fmt.Sprintf("%slongitude: %s", child, flt(float64(o.RightAscension)*milliDegToRad)),
			fmt.Sprintf("%speriapsis: %s", child, flt(float64(o.ArgOfPeriapsis)*milliDegToRad)),
			fmt.Sprintf("%smean_anomaly: %s", child, flt(float64(o.MeanAnomaly)*milliDegToRad)),
		}
	}
	e := update.ECEF
	return []string{
		pad + "ephemeris_info_ecef:",
		fmt.Sprintf("%spos_x: %s", child, flt(float64(e.PosX)*ntnv1alpha1.ECEFPositionStep)),
		fmt.Sprintf("%spos_y: %s", child, flt(float64(e.PosY)*ntnv1alpha1.ECEFPositionStep)),
		fmt.Sprintf("%spos_z: %s", child, flt(float64(e.PosZ)*ntnv1alpha1.ECEFPositionStep)),
		fmt.Sprintf("%svel_x: %s", child, flt(float64(e.VelX)*ntnv1alpha1.ECEFVelocityStep)),
		fmt.Sprintf("%svel_y: %s", child, flt(float64(e.VelY)*ntnv1alpha1.ECEFVelocityStep)),
		fmt.Sprintf("%svel_z: %s", child, flt(float64(e.VelZ)*ntnv1alpha1.ECEFVelocityStep)),
	}
}

// replaceEphemeris replaces the ephemeris block in OCUDU config YAML in place,
// preserving the block's existing indentation (the block now lives under
// cell_cfg.ntn, so its key is indented 4 spaces and its body 6). Returns the
// updated content and whether a replacement was made.
//
// It matches the ephemeris_info_ecef:/ephemeris_orbital: key line (tolerating an
// inline comment), re-renders the block at the same indent, then drops the old
// body — every following line indented strictly deeper than the key — stopping
// at the first sibling/parent key (indent <= key) or blank line. This is
// indentation-relative, so it never consumes sibling ntn keys the way a fixed
// "skip 4-space lines" rule would once the block was nested.
func replaceEphemeris(
	content string, update provider.EphemerisUpdate,
) (string, bool) {
	lines := strings.Split(content, "\n")
	var result []string
	found := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if !found && (strings.HasPrefix(trimmed, "ephemeris_info_ecef:") ||
			strings.HasPrefix(trimmed, "ephemeris_orbital:")) {
			found = true
			indent := leadingSpaces(line)
			result = append(result, renderEphemerisBlock(update, indent)...)
			// Drop the old block body: skip blank lines and lines indented
			// strictly deeper than the key, stopping at the first non-blank
			// sibling/parent key (indent <= key). Skipping blanks keeps an
			// externally-introduced blank inside the body from orphaning the
			// remaining block lines.
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) == "" || leadingSpaces(next) > indent {
					i++
					continue
				}
				break
			}
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n"), found
}
