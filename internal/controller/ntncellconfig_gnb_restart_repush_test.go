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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider/ocudu"
)

// mockGNB is an in-process stand-in for an OCUDU gNB's remote_control WebSocket: it accepts each
// (freshly-dialed) push, captures the frame, and replies with a success ack. restart() clears the
// captured frames to model a gNB that lost its runtime NTN config — the operator dials fresh on
// every push, so from its side a restarted gNB is indistinguishable from the next push cycle.
type mockGNB struct {
	mu     sync.Mutex
	frames [][]byte
	srv    *httptest.Server
}

func newMockGNB(t *testing.T) *mockGNB {
	t.Helper()
	g := &mockGNB{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow() //nolint:errcheck
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		g.mu.Lock()
		g.frames = append(g.frames, data)
		g.mu.Unlock()
		// Success ack: any JSON object without an "error" field (see wsclient pushNTNConfigUpdate).
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"cmd":"ntn_config_update","timestamp":"2030-01-01T00:00:00"}`))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *mockGNB) endpoint() string { return strings.TrimPrefix(g.srv.URL, "http://") }

func (g *mockGNB) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.frames)
}

func (g *mockGNB) lastFrame() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.frames) == 0 {
		return ""
	}
	return string(g.frames[len(g.frames)-1])
}

func (g *mockGNB) restart() { // gNB loses its runtime NTN config
	g.mu.Lock()
	defer g.mu.Unlock()
	g.frames = nil
}

// setPushedMarker records the dedup marker exactly as a successful reconcile would (a
// ConditionEphemerisPushed=True whose Message is the marker, stamped at the current generation),
// so isEphemerisPushUpToDate can short-circuit a same-epoch re-reconcile.
func setPushedMarker(cc *ntnv1alpha1.NTNCellConfig, marker string) {
	meta.SetStatusCondition(&cc.Status.Conditions, metav1.Condition{
		Type:               ntnv1alpha1.ConditionEphemerisPushed,
		Status:             metav1.ConditionTrue,
		Reason:             "Pushed",
		Message:            marker,
		ObservedGeneration: cc.Generation,
	})
}

// TestRuntimePush_ReassertsToRestartedGNB is the wired gNB-restart → re-push seam that no single
// prior test covered end to end: it drives the NTNCellConfig reconcile through the REAL OCUDU
// provider and a REAL WebSocket to a mock gNB, restarts the gNB, and proves the operator
// re-asserts the current propagated ephemeris to it on the next propagation refresh.
//
// The leader-failover half (a real new process restoring the durable OMM cache and keeping a
// future, push-ready epoch) is proven separately by the kind e2e TestHAOutageContinuityAcrossFailover;
// this test proves the consumer half — that a fresh epoch actually reaches a restarted gNB over the
// wire — and documents the bounded (≤ one propagation refresh) window in which it does not.
func TestRuntimePush_ReassertsToRestartedGNB(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ntnv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	ctx := context.Background()

	gnb := newMockGNB(t)

	future := time.Now().Add(time.Hour).UnixMilli()
	eph := ephWithPropagatedState(future) // single sat 25544, fresh source epoch, input hash stamped
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(eph).Build()
	r := &NTNCellConfigReconciler{Client: c}
	prov := ocudu.NewProvider(c) // the real WS-capable provider, not a mock

	cc := ccWithRemoteControl()
	cc.Spec.Provider.RemoteControl.Endpoint = gnb.endpoint() // dial the mock gNB

	// ---- Push 1: the operator delivers the propagated ephemeris to the gNB over a real WS frame.
	pushed, marker1, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, prov)
	if err != nil || !pushed {
		t.Fatalf("initial push: pushed=%v err=%v", pushed, err)
	}
	if gnb.count() != 1 {
		t.Fatalf("gNB did not receive the initial ntn_config_update; frames=%d", gnb.count())
	}
	if !strings.Contains(gnb.lastFrame(), `"cmd":"ntn_config_update"`) {
		t.Fatalf("captured frame is not an ntn_config_update: %s", gnb.lastFrame())
	}
	setPushedMarker(cc, marker1)

	// ---- gNB RESTART: it loses its runtime NTN config.
	gnb.restart()

	// B1: a same-epoch re-reconcile dedups (marker unchanged) — NO redundant push. This is the
	// bounded window (≤ propagationRefreshInterval) in which a just-restarted gNB is not yet
	// re-asserted, because the operator has no gNB-restart signal to react to.
	pushed, _, err = r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, prov)
	if err != nil {
		t.Fatalf("dedup reconcile: %v", err)
	}
	if pushed {
		t.Fatal("same-epoch reconcile must dedup, not re-push")
	}
	if gnb.count() != 0 {
		t.Fatalf("a deduped reconcile still pushed to the gNB; frames=%d", gnb.count())
	}

	// B2: the SatelliteEphemeris re-propagates a FRESH epoch (its ~3-min heartbeat) → the marker
	// changes → the operator re-pushes, re-asserting the current config to the restarted gNB.
	eph.Status.PropagatedStates[0].EpochUnixMs = time.Now().Add(2 * time.Hour).UnixMilli()
	if err := c.Update(ctx, eph); err != nil {
		t.Fatalf("advance propagated epoch: %v", err)
	}
	pushed, marker2, err := r.pushEphemerisUpdateIfNeeded(ctx, cc, &cc.Spec, prov)
	if err != nil || !pushed {
		t.Fatalf("re-push after refresh: pushed=%v err=%v", pushed, err)
	}
	if marker2 == marker1 {
		t.Fatal("a fresh epoch must change the dedup marker")
	}
	if gnb.count() != 1 {
		t.Fatalf("restarted gNB did not receive the re-asserted config; frames=%d", gnb.count())
	}
	if !strings.Contains(gnb.lastFrame(), `"cmd":"ntn_config_update"`) {
		t.Fatalf("re-asserted frame is not an ntn_config_update: %s", gnb.lastFrame())
	}
}
