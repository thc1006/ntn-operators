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
	"strings"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// issOMMWithEpoch builds an ISS OMM whose element-set epoch is `age` before now.
func issOMMWithEpoch(age time.Duration) sgp4.OMM {
	o := testISSOMM()
	o.EpochStr = time.Now().UTC().Add(-age).Format("2006-01-02T15:04:05.000000")
	return o
}

func newEphForEpochTest(name string, refresh time.Duration) *ntnv1alpha1.SatelliteEphemeris {
	return &ntnv1alpha1.SatelliteEphemeris{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: ntnv1alpha1.SatelliteEphemerisSpec{
			Source: ntnv1alpha1.EphemerisSource{
				Type: "CelesTrak", URL: "https://celestrak.org/test",
				RefreshInterval: metav1.Duration{Duration: refresh},
			},
		},
	}
}

// TestReconcile_EpochStale_SetsCondition pins I-17's epoch-staleness signal: an
// element set whose epoch is older than maxEpochAge (7d) drives
// EphemerisEpochStale=True; a fresh epoch drives False.
func TestReconcile_EpochStale_SetsCondition(t *testing.T) {
	sch := makeScheme(t)
	run := func(age time.Duration) *metav1.Condition {
		eph := newEphForEpochTest("eph-epoch", 4*time.Hour)
		cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
		r := &SatelliteEphemerisReconciler{
			Client: cli, Scheme: sch, Recorder: events.NewFakeRecorder(10),
			Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{
				OMMs: []sgp4.OMM{issOMMWithEpoch(age)}, SatelliteCount: 1, FetchedAt: time.Now(),
			}},
		}
		key := types.NamespacedName{Name: "eph-epoch", Namespace: "default"}
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		up := &ntnv1alpha1.SatelliteEphemeris{}
		if err := cli.Get(context.Background(), key, up); err != nil {
			t.Fatalf("get: %v", err)
		}
		return meta.FindStatusCondition(up.Status.Conditions, ntnv1alpha1.ConditionEphemerisEpochStale)
	}

	if c := run(30 * 24 * time.Hour); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "EpochStale" {
		t.Fatalf("30-day-old epoch must be flagged stale, got %+v", c)
	}
	if c := run(1 * time.Hour); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("a fresh epoch must not be flagged stale, got %+v", c)
	}
}

// TestReconcile_RefreshIntervalClampedAbove pins I-17's ceiling: a 720h interval
// is clamped and a Warning event names the 24h maximum.
func TestReconcile_RefreshIntervalClampedAbove(t *testing.T) {
	sch := makeScheme(t)
	eph := newEphForEpochTest("eph-clamp", 720*time.Hour)
	cli := fake.NewClientBuilder().WithScheme(sch).WithObjects(eph).WithStatusSubresource(eph).Build()
	rec := events.NewFakeRecorder(10)
	r := &SatelliteEphemerisReconciler{
		Client: cli, Scheme: sch, Recorder: rec,
		Fetcher: &mockGPFetcher{result: ephemeris.GPFetchResult{SatelliteCount: 1, FetchedAt: time.Now()}},
	}
	key := types.NamespacedName{Name: "eph-clamp", Namespace: "default"}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	found := false
	for {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, "exceeds maximum 24h") {
				found = true
			}
			continue
		default:
		}
		break
	}
	if !found {
		t.Error("a 720h refreshInterval must emit a RefreshIntervalClamped event naming the 24h maximum")
	}
}
