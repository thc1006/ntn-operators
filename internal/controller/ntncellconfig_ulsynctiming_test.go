/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestSib19CadenceWarning is the pure-logic table for #228 G4's locally-decidable invariant: the
// SIB19 broadcast period (siPeriod × 10ms) must be short enough that at least two SIB19 broadcasts fit
// inside the ntn-UlSyncValidityDur window, else a UE risks not re-acquiring SIB19 before UL-sync validity
// expires. Boundary math: warn iff 2×(siPeriod×10ms) > validity.
func TestSib19CadenceWarning(t *testing.T) {
	withPeriod := func(validity *int, siPeriod int) *ntnv1alpha1.NTNCellConfigSpec {
		s := &ntnv1alpha1.NTNCellConfigSpec{NTN: ntnv1alpha1.NTNParams{NTNUlSyncValidityDur: validity}}
		if siPeriod > 0 {
			s.CellOverrides = &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: siPeriod}}
		}
		return s
	}
	cases := []struct {
		name     string
		validity *int
		siPeriod int // 0 = leave sibSchedule unset (defaults to 16 frames)
		wantWarn bool
	}{
		// Unset validity is NOT no-opinion: it is evaluated against the operator's 5 s applied default
		// (defaultNTNUlSyncValidityDurSeconds), which is what the runtime push actually sends OCUDU.
		{"validity unset + siPeriod 512 → warn against the 5s default", nil, 512, true},
		{"validity unset + default siPeriod → sane against the 5s default", nil, 0, false},
		{"default siPeriod (16=160ms) with min validity 5s is fine", new(5), 0, false},
		{"default siPeriod with max validity is fine", new(900), 0, false},
		{"siPeriod 512 (5.12s) vs validity 5s → warn", new(5), 512, true},
		{"siPeriod 256 (2.56s) vs validity 5s → warn (2x=5.12>5)", new(5), 256, true},
		{"siPeriod 128 (1.28s) vs validity 5s → fine (2x=2.56<5)", new(5), 128, false},
		{"siPeriod 512 (5.12s) vs validity 60s → fine", new(60), 512, false},
		{"siPeriod 512 vs validity 10s → warn (2x=10.24>10)", new(10), 512, true},
		{"siPeriod 512 vs validity 15s → fine (2x=10.24<15)", new(15), 512, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sib19CadenceWarning(withPeriod(tc.validity, tc.siPeriod))
			if (got != "") != tc.wantWarn {
				t.Fatalf("sib19CadenceWarning warn=%v (msg=%q), want warn=%v", got != "", got, tc.wantWarn)
			}
		})
	}
}

// TestReconcile_SIB19CadenceLifecycle drives the full NTNCellConfig reconcile and proves the
// advisory is (1) raised as SIB19CadenceSane=False + a SIB19CadenceRisk Warning event on a risky config,
// (2) episode-gated (no duplicate event on an unchanged re-reconcile), and (3) cleared to True with no new
// Warning once the config is fixed — all WITHOUT blocking ConfigApplied.
func TestReconcile_SIB19CadenceLifecycle(t *testing.T) {
	const (
		ns   = "default"
		name = "cc-ulsync"
	)
	sch := makeScheme(t)
	key := types.NamespacedName{Name: name, Namespace: ns}

	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Generation: 1,
			// Pre-add the finalizer so the first reconcile does real work instead of the add-finalizer requeue.
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns},
			NTN: ntnv1alpha1.NTNParams{
				CellSpecificKoffset:  150,
				PayloadType:          "transparent",
				NTNUlSyncValidityDur: new(5), // short LEO validity
			},
			// siPeriod 512 frames = 5.12s → 2×5.12 > 5 → risky.
			CellOverrides: &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 512}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(cc).WithStatusSubresource(cc).Build()
	rec := events.NewFakeRecorder(50)
	r := &NTNCellConfigReconciler{
		Client:    cli,
		Scheme:    sch,
		Recorder:  rec,
		Providers: map[string]provider.NTNProvider{"ocudu": &provider.MockProvider{}},
	}
	ctx := context.Background()

	riskEvents := func() int {
		n := 0
		for {
			select {
			case ev := <-rec.Events:
				// FakeRecorder formats as "<type> <reason> <note>", so matching "Warning SIB19CadenceRisk"
				// asserts BOTH the Warning event type and the reason — a Normal-type regression won't match.
				if strings.Contains(ev, "Warning SIB19CadenceRisk") {
					n++
				}
			default:
				return n
			}
		}
	}
	get := func() *ntnv1alpha1.NTNCellConfig {
		o := &ntnv1alpha1.NTNCellConfig{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return o
	}

	// (1) risky config → condition False + one Warning event; ConfigApplied still True.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #1: %v", err)
	}
	o := get()
	cond := meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InsufficientSIB19Margin" {
		t.Fatalf("want SIB19CadenceSane=False/InsufficientSIB19Margin, got %+v", cond)
	}
	if cond != nil && !strings.Contains(cond.Message, "SIB19") {
		t.Errorf("the False condition Message must describe the SIB19 timing risk; got %q", cond.Message)
	}
	if applied := meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionConfigApplied); applied == nil || applied.Status != metav1.ConditionTrue {
		t.Fatalf("the advisory must NOT block ConfigApplied; got %+v", applied)
	}
	if got := riskEvents(); got != 1 {
		t.Fatalf("risky config must emit exactly one SIB19CadenceRisk Warning; got %d", got)
	}

	// (2) unchanged re-reconcile (same generation) → episode-gated, no new event.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #2: %v", err)
	}
	if got := riskEvents(); got != 0 {
		t.Fatalf("an unchanged re-reconcile must not re-emit the Warning (episode gate); got %d", got)
	}

	// (3) fix the config (shorten siPeriod) + bump generation → condition True, no new Warning.
	o = get()
	o.Spec.CellOverrides.SIBSchedule.SIPeriod = 16 // 160ms → sane
	o.Generation = 2
	if err := cli.Update(ctx, o); err != nil {
		t.Fatalf("spec fix update: %v", err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #3: %v", err)
	}
	cond = meta.FindStatusCondition(get().Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "WithinValidity" {
		t.Fatalf("after the fix, want SIB19CadenceSane=True/WithinValidity, got %+v", cond)
	}
	// The True message must NOT overclaim overall UL-sync health — it must state that runtime epoch /
	// ephemeris-push headroom are out of scope. Guards the round-2 message narrowing against regression.
	if cond != nil && !strings.Contains(cond.Message, "not evaluated") {
		t.Fatalf("True condition message must scope itself (runtime epoch/push headroom not evaluated); got %q", cond.Message)
	}
	if got := riskEvents(); got != 0 {
		t.Fatalf("a fixed config must not emit a Warning; got %d", got)
	}
}

// TestReconcile_SIB19Cadence_NoOpinionForNonReanchoringProvider closes the #228 round-3 gap: the cadence
// check is only valid for a provider that re-anchors the SIB19 epoch per regeneration (T430 counts from
// epochTime, so re-acquiring a pinned-epoch SIB19 does not extend validity). For any other provider the
// operator must express NO opinion — a risky config yields NO SIB19CadenceSane condition and NO Warning.
// Mutation: dropping the providerReanchorsSIB19Epoch guard makes a non-OCUDU provider set the condition,
// which this fails.
func TestReconcile_SIB19Cadence_NoOpinionForNonReanchoringProvider(t *testing.T) {
	const (
		ns   = "default"
		name = "cc-ulsync-otherprov"
	)
	sch := makeScheme(t)
	key := types.NamespacedName{Name: name, Namespace: ns}
	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Generation: 1,
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			// A provider NOT verified to re-anchor the SIB19 epoch. (The CRD enum is ocudu-only today; this
			// exercises the future-provider guard directly at the controller layer.)
			Provider: ntnv1alpha1.ProviderRef{Type: "some-future-provider", Namespace: ns},
			NTN: ntnv1alpha1.NTNParams{
				CellSpecificKoffset:  150,
				PayloadType:          "transparent",
				NTNUlSyncValidityDur: new(5), // risky if it were evaluated
			},
			CellOverrides: &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 512}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(cc).WithStatusSubresource(cc).Build()
	rec := events.NewFakeRecorder(50)
	r := &NTNCellConfigReconciler{
		Client: cli, Scheme: sch, Recorder: rec,
		// Register the mock under the non-ocudu type so provider validation passes and the reconcile
		// reaches applySIB19CadenceCondition (where the re-anchoring guard applies).
		Providers: map[string]provider.NTNProvider{"some-future-provider": &provider.MockProvider{}},
	}
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	o := &ntnv1alpha1.NTNCellConfig{}
	if err := cli.Get(ctx, key, o); err != nil {
		t.Fatalf("get: %v", err)
	}
	if c := meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane); c != nil {
		t.Fatalf("a non-re-anchoring provider must carry NO SIB19CadenceSane condition (no opinion); got %+v", c)
	}
	if got := countRiskWarnings(rec); got != 0 {
		t.Fatalf("a non-re-anchoring provider must emit no SIB19CadenceRisk Warning; got %d", got)
	}
}

// countRiskWarnings drains a FakeRecorder and counts Warning SIB19CadenceRisk events (asserting the type).
func countRiskWarnings(rec *events.FakeRecorder) int {
	n := 0
	for {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, "Warning SIB19CadenceRisk") {
				n++
			}
		default:
			return n
		}
	}
}

// TestReconcile_SIB19Cadence_FiresOnEphemerisPushFailure closes the round-5-review coverage gap
// (Gap A, HIGH): the SIB19CadenceRisk Warning is emitted at TWO persist points, and the ephemeris-push
// FAILURE path is the one that stops a first-reconcile push failure from swallowing the Warning forever
// (once the condition is persisted False, a later success reconcile is episode-gated and never re-fires).
// A risky config whose ephemeris push fails on the first reconcile must STILL emit the Warning post-persist.
// Mutation: deleting the emit at the push-failure persist point passes every other test but fails this one.
func TestReconcile_SIB19Cadence_FiresOnEphemerisPushFailure(t *testing.T) {
	const (
		ns   = "default"
		name = "cc-ulsync-pushfail"
	)
	sch := makeScheme(t)
	key := types.NamespacedName{Name: name, Namespace: ns}
	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Generation: 1,
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns},
			// A dangling ephemerisRef → the push Get returns NotFound → the ephemeris-push FAILURE
			// early-return persist path runs (before the final Status().Update).
			EphemerisRef: "does-not-exist",
			NTN: ntnv1alpha1.NTNParams{
				CellSpecificKoffset:  150,
				PayloadType:          "transparent",
				NTNUlSyncValidityDur: new(5),
			},
			CellOverrides: &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 512}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(cc).WithStatusSubresource(cc).Build()
	rec := events.NewFakeRecorder(50)
	r := &NTNCellConfigReconciler{
		Client: cli, Scheme: sch, Recorder: rec,
		Providers: map[string]provider.NTNProvider{"ocudu": &provider.MockProvider{}},
	}
	ctx := context.Background()
	// The push fails (NotFound); we assert on the persisted status + emitted Warning, not the requeue error.
	_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})

	o := &ntnv1alpha1.NTNCellConfig{}
	if err := cli.Get(ctx, key, o); err != nil {
		t.Fatalf("get: %v", err)
	}
	// Positive control: the ephemeris push really DID fail, so the push-failure persist path was taken.
	if pc := meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed); pc == nil || pc.Status != metav1.ConditionFalse {
		t.Fatalf("precondition: the ephemeris push must have FAILED (EphemerisPushed=False) so the push-fail persist path runs; got %+v", pc)
	}
	if cond := meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("a risky config must persist SIB19CadenceSane=False even when the ephemeris push fails; got %+v", cond)
	}
	// The marquee assertion: the Warning fired via the push-FAILURE persist path (not the success path,
	// which this reconcile never reaches).
	if got := countRiskWarnings(rec); got != 1 {
		t.Fatalf("the SIB19CadenceRisk Warning must fire once via the push-failure persist path; got %d", got)
	}
}

// TestReconcile_SIB19Cadence_FieldUnset closes Gap B (MEDIUM): the default (field-unset) branch. A
// cell with no ntnUlSyncValidityDur must carry NO SIB19CadenceSane condition; and a cell that had the field
// (raising the condition) then removed it must have the condition CLEARED. Mutation: deleting the
// `default: RemoveStatusCondition` branch leaves a stale condition after the field is removed → step (iii)
// fails.
// reconcilerForCC builds a fake-client reconciler + recorder for a single NTNCellConfig, registering the
// mock provider under the config's provider type so provider validation passes.
func reconcilerForCC(t *testing.T, cc *ntnv1alpha1.NTNCellConfig) (*NTNCellConfigReconciler, client.Client, *events.FakeRecorder) {
	t.Helper()
	sch := makeScheme(t)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(cc).WithStatusSubresource(cc).Build()
	rec := events.NewFakeRecorder(50)
	r := &NTNCellConfigReconciler{
		Client: cli, Scheme: sch, Recorder: rec,
		Providers: map[string]provider.NTNProvider{cc.Spec.Provider.Type: &provider.MockProvider{}},
	}
	return r, cli, rec
}

// TestReconcile_SIB19Cadence_UnsetValidityUsesOperatorDefault is the round-4 fix: an UNSET
// ntnUlSyncValidityDur is NOT "no opinion" — the runtime push sends OCUDU a real 5 s validity
// (effectiveUlSyncValidityDurSeconds), and the advisory evaluates against that SAME default. So unset + a
// slow siPeriod must WARN — the reproducible false-negative the round-4 review found — while unset + the
// default siPeriod stays sane. Mutation: treating unset as no-opinion (the old default branch) makes the
// risky step below carry NO condition, which this fails.
func TestReconcile_SIB19Cadence_UnsetValidityUsesOperatorDefault(t *testing.T) {
	const (
		ns   = "default"
		name = "cc-ulsync-unset"
	)
	key := types.NamespacedName{Name: name, Namespace: ns}
	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Generation: 1,
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns},
			// NTNUlSyncValidityDur nil; sibSchedule nil → default siPeriod 16 → sane against the 5 s default.
			NTN: ntnv1alpha1.NTNParams{CellSpecificKoffset: 150, PayloadType: "transparent"},
		},
	}
	r, cli, rec := reconcilerForCC(t, cc)
	ctx := context.Background()
	cond := func() *metav1.Condition {
		o := &ntnv1alpha1.NTNCellConfig{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane)
	}

	// (i) unset validity + default siPeriod → EVALUATED against the 5 s default → sane (True), not no-opinion.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #1: %v", err)
	}
	if c := cond(); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("unset validity + default siPeriod must be evaluated (True/sane against the 5s default), not no-opinion; got %+v", c)
	}
	if got := countRiskWarnings(rec); got != 0 {
		t.Fatalf("a sane config must emit no Warning; got %d", got)
	}

	// (ii) THE ROUND-4 BUG: unset validity + slow siPeriod (512 = 5.12s) exceeds the effective 5 s validity
	//      → must WARN (False). Keep validity unset; bump generation.
	o := &ntnv1alpha1.NTNCellConfig{}
	if err := cli.Get(ctx, key, o); err != nil {
		t.Fatalf("get: %v", err)
	}
	o.Spec.CellOverrides = &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 512}}
	o.Generation = 2
	if err := cli.Update(ctx, o); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #2: %v", err)
	}
	if c := cond(); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "InsufficientSIB19Margin" {
		t.Fatalf("unset validity + siPeriod 512 must WARN against the 5s default (SIB19CadenceSane=False); got %+v", c)
	}
	if got := countRiskWarnings(rec); got != 1 {
		t.Fatalf("the unset-validity risk must emit exactly one Warning; got %d", got)
	}
}

// TestReconcile_SIB19Cadence_PinnedEpochModeNoOpinion is the round-4 MEDIUM-2 escape hatch: a deployment on
// a gNB whose broadcast SIB19 epoch is PINNED (not re-anchored) sets provider.sib19EpochMode="pinned", and
// the advisory then expresses NO opinion (no condition, no Warning) even for an otherwise-risky config —
// so an unverified/changed gNB build is not silently trusted. Mutation: dropping the sib19EpochMode override
// (using only the provider-type default) makes ocudu+pinned still set the condition, which this fails.
func TestReconcile_SIB19Cadence_PinnedEpochModeNoOpinion(t *testing.T) {
	const (
		ns   = "default"
		name = "cc-ulsync-pinned"
	)
	key := types.NamespacedName{Name: name, Namespace: ns}
	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Generation: 1,
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			// OCUDU by type, but the deployer declares the broadcast epoch is PINNED on their build.
			Provider:      ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns, SIB19EpochMode: new("pinned")},
			NTN:           ntnv1alpha1.NTNParams{CellSpecificKoffset: 150, PayloadType: "transparent", NTNUlSyncValidityDur: new(5)},
			CellOverrides: &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 512}}, // risky if evaluated
		},
	}
	r, cli, rec := reconcilerForCC(t, cc)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	o := &ntnv1alpha1.NTNCellConfig{}
	if err := cli.Get(ctx, key, o); err != nil {
		t.Fatalf("get: %v", err)
	}
	if c := meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane); c != nil {
		t.Fatalf("sib19EpochMode=pinned must express NO opinion (no SIB19CadenceSane condition); got %+v", c)
	}
	if got := countRiskWarnings(rec); got != 0 {
		t.Fatalf("sib19EpochMode=pinned must emit no Warning; got %d", got)
	}
}

// TestReconcile_SIB19Cadence_ProviderTransitionClearsStaleCondition is the round-4 LOW-2 transition test:
// a config that RAISED SIB19CadenceSane=False and then moves out of the advisory's scope (here by declaring
// sib19EpochMode=pinned) must have the stale condition CLEARED, not left behind. Mutation: replacing the
// gate's RemoveStatusCondition with a bare `return` leaves the stale False condition, which this fails.
func TestReconcile_SIB19Cadence_ProviderTransitionClearsStaleCondition(t *testing.T) {
	const (
		ns   = "default"
		name = "cc-ulsync-transition"
	)
	key := types.NamespacedName{Name: name, Namespace: ns}
	cc := &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Generation: 1,
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider:      ntnv1alpha1.ProviderRef{Type: "ocudu", Namespace: ns}, // re-anchoring by default
			NTN:           ntnv1alpha1.NTNParams{CellSpecificKoffset: 150, PayloadType: "transparent", NTNUlSyncValidityDur: new(5)},
			CellOverrides: &ntnv1alpha1.CellOverrides{SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 512}},
		},
	}
	r, cli, rec := reconcilerForCC(t, cc)
	ctx := context.Background()
	cond := func() *metav1.Condition {
		o := &ntnv1alpha1.NTNCellConfig{}
		if err := cli.Get(ctx, key, o); err != nil {
			t.Fatalf("get: %v", err)
		}
		return meta.FindStatusCondition(o.Status.Conditions, ntnv1alpha1.ConditionSIB19CadenceSane)
	}

	// (i) risky ocudu → SIB19CadenceSane=False present.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #1: %v", err)
	}
	if c := cond(); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("precondition: risky ocudu must raise SIB19CadenceSane=False; got %+v", c)
	}
	_ = countRiskWarnings(rec) // drain

	// (ii) move OUT of scope (declare the gNB pins its epoch) + bump generation → stale condition CLEARED.
	o := &ntnv1alpha1.NTNCellConfig{}
	if err := cli.Get(ctx, key, o); err != nil {
		t.Fatalf("get: %v", err)
	}
	o.Spec.Provider.SIB19EpochMode = new("pinned")
	o.Generation = 2
	if err := cli.Update(ctx, o); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile #2: %v", err)
	}
	if c := cond(); c != nil {
		t.Fatalf("moving out of the advisory's scope must CLEAR the stale SIB19CadenceSane condition; got %+v", c)
	}
	if got := countRiskWarnings(rec); got != 0 {
		t.Fatalf("the transition must emit no new Warning; got %d", got)
	}
}
