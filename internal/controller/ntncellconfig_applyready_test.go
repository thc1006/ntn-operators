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

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	ntnmetrics "github.com/thc1006/ntn-operators/pkg/metrics"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

// gaugeValue reads a single gauge series WITHOUT creating it. testutil.ToFloat64 goes through
// GaugeVec.With, which instantiates a missing series at 0 — so it cannot tell "explicitly set
// to 0" from "never set at all", and a test built on it would pass even if the code stopped
// setting the gauge entirely. The bool is what makes these assertions load-bearing.
func gaugeValue(t *testing.T, g *prometheus.GaugeVec, labels prometheus.Labels) (float64, bool) {
	t.Helper()
	ch := make(chan prometheus.Metric, 128)
	g.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		got := make(map[string]string, len(pb.Label))
		for _, lp := range pb.Label {
			got[lp.GetName()] = lp.GetValue()
		}
		match := true
		for k, v := range labels {
			if got[k] != v {
				match = false
				break
			}
		}
		if match {
			return pb.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

// counterValue is the same no-instantiate read for a CounterVec.
func counterValue(t *testing.T, c *prometheus.CounterVec, labels prometheus.Labels) (float64, bool) {
	t.Helper()
	ch := make(chan prometheus.Metric, 128)
	c.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		got := make(map[string]string, len(pb.Label))
		for _, lp := range pb.Label {
			got[lp.GetName()] = lp.GetValue()
		}
		match := true
		for k, v := range labels {
			if got[k] != v {
				match = false
				break
			}
		}
		if match {
			return pb.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

const applyReadyNS = "apply-ready-ns"

func applyReadyLabels(name string) prometheus.Labels {
	return prometheus.Labels{"namespace": applyReadyNS, "config": name}
}

// ccForApplyReady builds a CR that already carries the cleanup finalizer, so the first
// Reconcile reaches the provider-validation step instead of stopping to add it.
func ccForApplyReady(name, providerType string) *ntnv1alpha1.NTNCellConfig {
	return &ntnv1alpha1.NTNCellConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: applyReadyNS, Generation: 1,
			Finalizers: []string{"ntn.operators.dev/configmap-cleanup"},
		},
		Spec: ntnv1alpha1.NTNCellConfigSpec{
			Provider: ntnv1alpha1.ProviderRef{Type: providerType, Namespace: applyReadyNS},
		},
	}
}

func applyReadyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	return s
}

// TestConfigApplyReady_CoversTheCounterlessFailures is why this gauge exists. Each branch
// below leaves ConfigApplied non-True, increments ConfigApplyErrorsTotal ZERO times, and
// returns a NIL error — so neither NTNConfigApplyErrors (a rate alert on that counter) nor
// controller_runtime_reconcile_errors_total can see it. Before the gauge these three failures
// were invisible to every shipped alert. UnsupportedProvider additionally does not requeue,
// which is the permanent case a rate alert could never sustain on (issue #216).
func TestConfigApplyReady_CoversTheCounterlessFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		providers    map[string]provider.NTNProvider
		providerType string
		wantReason   string
	}{
		{"registry not configured", nil, "ocudu", "InternalError"},
		{"provider type unregistered", map[string]provider.NTNProvider{"ocudu": &provider.MockProvider{}}, "other", "UnsupportedProvider"},
		{"post-apply verification failed", map[string]provider.NTNProvider{
			"ocudu": &provider.MockProvider{StatusErr: errors.New("configmap read failed")},
		}, "ocudu", "StatusCheckFailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "cell-" + tc.wantReason
			ntnmetrics.ConfigApplyReady.DeletePartialMatch(applyReadyLabels(name))
			ntnmetrics.ConfigApplyErrorsTotal.DeletePartialMatch(applyReadyLabels(name))

			s := applyReadyScheme(t)
			cc := ccForApplyReady(name, tc.providerType)
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(cc).WithStatusSubresource(cc).Build()
			r := &NTNCellConfigReconciler{
				Client: c, Scheme: s, Providers: tc.providers,
				Recorder: events.NewFakeRecorder(20),
			}

			_, err := r.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: applyReadyNS},
			})
			if err != nil {
				t.Fatalf("this branch must return a nil error (that is the whole problem): %v", err)
			}

			// The condition records the failure...
			got := &ntnv1alpha1.NTNCellConfig{}
			if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: applyReadyNS}, got); err != nil {
				t.Fatalf("get: %v", err)
			}
			cond := findCond(got, ntnv1alpha1.ConditionConfigApplied)
			if cond == nil || cond.Reason != tc.wantReason {
				t.Fatalf("ConfigApplied reason = %v, want %s", cond, tc.wantReason)
			}
			// ...but the counter does not even get a series, so a rate alert stays silent...
			if v, found := counterValue(t, ntnmetrics.ConfigApplyErrorsTotal, applyReadyLabels(name)); found && v != 0 {
				t.Errorf("ConfigApplyErrorsTotal = %v, want 0: this branch is exactly the one the counter misses", v)
			}
			// ...so the gauge is the only alertable signal. It must EXIST and be 0 — a missing
			// series is not "ready 0", it is nothing for `== 0` to ever match.
			v, found := gaugeValue(t, ntnmetrics.ConfigApplyReady, applyReadyLabels(name))
			if !found {
				t.Fatalf("no config_apply_ready series for %s: an absent series can never fire `== 0`", tc.wantReason)
			}
			if v != 0 {
				t.Errorf("config_apply_ready = %v, want 0 for %s", v, tc.wantReason)
			}
		})
	}
}

// The one failure that DOES increment the counter must also drop the gauge, so the two
// signals never disagree about whether the apply is currently broken.
func TestConfigApplyReady_ZeroOnApplyFailure(t *testing.T) {
	const name = "cell-apply-fail"
	ntnmetrics.ConfigApplyReady.DeletePartialMatch(applyReadyLabels(name))

	s := applyReadyScheme(t)
	cc := ccForApplyReady(name, "ocudu")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cc).WithStatusSubresource(cc).Build()
	r := &NTNCellConfigReconciler{
		Client: c, Scheme: s, Recorder: events.NewFakeRecorder(20),
		Providers: map[string]provider.NTNProvider{"ocudu": &provider.MockProvider{ApplyErr: errors.New("boom")}},
	}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: applyReadyNS},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if v, found := gaugeValue(t, ntnmetrics.ConfigApplyReady, applyReadyLabels(name)); !found || v != 0 {
		t.Errorf("config_apply_ready = %v (found=%v), want 0 on an apply failure", v, found)
	}
}

// Ready is 1 only after the apply is VERIFIED, and a recovery clears a previous 0 — otherwise
// the alert would latch on forever once anything had ever failed.
func TestConfigApplyReady_OneOnVerifiedSuccessAndOnRecovery(t *testing.T) {
	const name = "cell-recovers"
	ntnmetrics.ConfigApplyReady.DeletePartialMatch(applyReadyLabels(name))

	s := applyReadyScheme(t)
	cc := ccForApplyReady(name, "ocudu")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cc).WithStatusSubresource(cc).Build()
	mock := &provider.MockProvider{StatusErr: errors.New("cannot verify yet")}
	r := &NTNCellConfigReconciler{
		Client: c, Scheme: s, Recorder: events.NewFakeRecorder(20),
		Providers: map[string]provider.NTNProvider{"ocudu": mock},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: applyReadyNS}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if v, found := gaugeValue(t, ntnmetrics.ConfigApplyReady, applyReadyLabels(name)); !found || v != 0 {
		t.Fatalf("config_apply_ready = %v (found=%v), want 0 while verification fails", v, found)
	}

	// Cause removed → the next reconcile must flip it back to 1.
	mock.StatusErr = nil
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if v, found := gaugeValue(t, ntnmetrics.ConfigApplyReady, applyReadyLabels(name)); !found || v != 1 {
		t.Errorf("config_apply_ready = %v (found=%v), want 1 once the apply verifies", v, found)
	}
}

// The series must be released on CR deletion, like every other per-CR series, or /metrics
// accumulates dead series across create/delete churn — and a stale 0 would alert forever.
// Checked via DeletePartialMatch's return count, because reading a deleted gauge with
// ToFloat64 would silently re-create it at 0 and look identical to "not deleted".
func TestConfigApplyReady_ReleasedOnDelete(t *testing.T) {
	const name = "cell-deleted"
	ntnmetrics.ConfigApplyReady.With(applyReadyLabels(name)).Set(0)

	s := applyReadyScheme(t)
	cc := ccForApplyReady(name, "ocudu")
	cc.DeletionTimestamp = &metav1.Time{Time: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cc).WithStatusSubresource(cc).Build()
	r := &NTNCellConfigReconciler{Client: c, Scheme: s, Recorder: events.NewFakeRecorder(20)}

	if _, _, err := r.handleFinalizer(context.Background(), cc, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if n := ntnmetrics.ConfigApplyReady.DeletePartialMatch(applyReadyLabels(name)); n != 0 {
		t.Errorf("the finalizer left %d config_apply_ready series behind; it must release them", n)
	}
}

func findCond(cc *ntnv1alpha1.NTNCellConfig, condType string) *metav1.Condition {
	for i := range cc.Status.Conditions {
		if cc.Status.Conditions[i].Type == condType {
			return &cc.Status.Conditions[i]
		}
	}
	return nil
}
