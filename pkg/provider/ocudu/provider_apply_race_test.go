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
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thc1006/ntn-operators/pkg/provider"
)

// raceProvider builds a Provider whose FIRST Get reports NotFound even though seeded already
// exists — the exact window ApplyCellConfig races against: it reads (uncached) NotFound,
// decides to Create, and something else wins the name in between. Every later Get is real, so
// the recovery path sees the object that actually landed.
func raceProvider(t *testing.T, seeded *corev1.ConfigMap) *Provider {
	t.Helper()
	firstGet := true
	c := fake.NewClientBuilder().
		WithScheme(ownerScheme(t)).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ntn-system"}}, seeded).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(
				ctx context.Context, c client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, isCM := obj.(*corev1.ConfigMap); isCM && firstGet {
					firstGet = false
					return apierrors.NewNotFound(corev1.Resource("configmaps"), key.Name)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	return NewProvider(c)
}

// Losing the create race must be CLASSIFIED, not surfaced as a raw AlreadyExists. A foreign
// object that wins the name is an ownership conflict — the reconciler turns that into an
// OwnershipConflict condition. Returning AlreadyExists instead opened a misleading ApplyFailed
// episode (Warning event included) that only self-corrected a requeue interval later.
func TestApplyCellConfig_CreateRace_ForeignWinner(t *testing.T) {
	ctx := context.Background()
	p := raceProvider(t, seedConfigMap(t, false, nil)) // unlabeled → not ours

	err := p.ApplyCellConfig(ctx, ownerFor("cell-a"), geoSpec(), ownerScheme(t))
	if !errors.Is(err, provider.ErrConfigMapNotOwned) {
		t.Fatalf("a foreign object winning the create race must be ErrConfigMapNotOwned, got %v", err)
	}
	if apierrors.IsAlreadyExists(err) {
		t.Error("the raw AlreadyExists must not escape: it reads as a transient ApplyFailed, not an ownership conflict")
	}

	// And it must be left alone.
	var cm corev1.ConfigMap
	if err := p.client.Get(ctx, types.NamespacedName{
		Name: ConfigMapNameFor("cell-a"), Namespace: "ntn-system",
	}, &cm); err != nil {
		t.Fatalf("get: %v", err)
	}
	if cm.Data["geo_ntn.yml"] != "seed" {
		t.Error("a foreign ConfigMap must not be overwritten after a lost create race")
	}
}

// The other half: when the object that won the race IS ours (a pre-atomic-ref leftover), the
// recovery path must adopt it and converge, with no error at all.
func TestApplyCellConfig_CreateRace_AdoptableWinner(t *testing.T) {
	ctx := context.Background()
	owner := ownerFor("cell-a")
	p := raceProvider(t, seedConfigMap(t, true, nil)) // labeled + geo_ntn.yml, unowned

	if err := p.ApplyCellConfig(ctx, owner, geoSpec(), ownerScheme(t)); err != nil {
		t.Fatalf("losing the race to our own leftover must converge, got %v", err)
	}
	var cm corev1.ConfigMap
	if err := p.client.Get(ctx, types.NamespacedName{
		Name: ConfigMapNameFor("cell-a"), Namespace: "ntn-system",
	}, &cm); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !metav1.IsControlledBy(&cm, owner) {
		t.Error("the winner should have been adopted (controller ref set)")
	}
	if cm.Data["geo_ntn.yml"] == "seed" {
		t.Error("an adopted ConfigMap must be rewritten with the generated config")
	}
}

// The management labels are desired state, not a create-time stamp. They are how a ConfigMap
// is recognized as ours once an owner reference is gone (isAdoptableLeftover), so a stripped
// label silently makes a future leftover unreclaimable — and it breaks `kubectl get -l`.
// The no-op guard compared only data + koffset, so a label-stripped ConfigMap whose content
// already matched was reported applied and never repaired.
func TestApplyCellConfig_RestoresStrippedManagementLabels(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	owner := ownerFor("cell-a")
	spec := geoSpec()
	key := types.NamespacedName{Name: ConfigMapNameFor("cell-a"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, owner, spec, ownerScheme(t)); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Someone strips the labels; data and koffset stay exactly as applied.
	var cm corev1.ConfigMap
	if err := p.client.Get(ctx, key, &cm); err != nil {
		t.Fatalf("get: %v", err)
	}
	delete(cm.Labels, managedByLabel)
	delete(cm.Labels, componentLabel)
	if err := p.client.Update(ctx, &cm); err != nil {
		t.Fatalf("strip labels: %v", err)
	}

	if err := p.ApplyCellConfig(ctx, owner, spec, ownerScheme(t)); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	var got corev1.ConfigMap
	if err := p.client.Get(ctx, key, &got); err != nil {
		t.Fatalf("get after re-apply: %v", err)
	}
	if !isOperatorManaged(&got) {
		t.Fatalf("stripped management labels must be restored; got %v", got.Labels)
	}
}

// Restoring labels must not cost the #204-G3 no-op: a fully-correct ConfigMap still must not
// be rewritten on the ~3-minute fan-out cadence.
func TestApplyCellConfig_LabelCheckKeepsTheNoOp(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	spec := geoSpec()
	key := types.NamespacedName{Name: ConfigMapNameFor("cell-a"), Namespace: "ntn-system"}

	if err := p.ApplyCellConfig(ctx, ownerFor("cell-a"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var before corev1.ConfigMap
	if err := p.client.Get(ctx, key, &before); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := p.ApplyCellConfig(ctx, ownerFor("cell-a"), spec, ownerScheme(t)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var after corev1.ConfigMap
	if err := p.client.Get(ctx, key, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatalf("an identical re-apply must stay a no-op (#204-G3): resourceVersion %q -> %q",
			before.ResourceVersion, after.ResourceVersion)
	}
}

// A truncated name's disambiguating suffix must be wide enough that a collision cannot be
// CONSTRUCTED. The loser of a collision sits in a permanent OwnershipConflict, and a 32-bit
// truncated digest is searchable offline in seconds, so a principal able to create an
// NTNCellConfig could aim a long name at another CR's ConfigMap.
func TestConfigMapNameFor_SuffixIsWideEnoughToResistConstruction(t *testing.T) {
	const minSuffixBits = 80 // the floor; the implementation uses 128
	if got := configMapNameHashBytes * 8; got < minSuffixBits {
		t.Fatalf("hash suffix is %d bits, want at least %d — a narrower digest is offline-searchable", got, minSuffixBits)
	}

	long := strings.Repeat("a", 250)
	name := ConfigMapNameFor(long)
	if len(name) > maxK8sNameLen {
		t.Errorf("name length %d exceeds %d", len(name), maxK8sNameLen)
	}
	suffix := name[strings.LastIndex(name, "-")+1:]
	if len(suffix) != configMapNameHashBytes*2 {
		t.Errorf("suffix %q is %d hex chars, want %d", suffix, len(suffix), configMapNameHashBytes*2)
	}

	// Deterministic, and two long names that differ only past the truncation point must not
	// collapse onto the same ConfigMap.
	if ConfigMapNameFor(long) != name {
		t.Error("ConfigMapNameFor must be deterministic")
	}
	other := strings.Repeat("a", 249) + "b"
	if ConfigMapNameFor(other) == name {
		t.Error("two distinct long CR names must not map to the same ConfigMap name")
	}
}
