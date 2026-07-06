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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

func ecefRuntimeUpdate() provider.RuntimeUpdate {
	return provider.RuntimeUpdate{
		Cell:              provider.CellIdentity{PLMN: "00101", NCI: 1},
		EpochUnixMs:       1893456000000,
		UlSyncValidityDur: 5,
		Ephemeris: provider.EphemerisUpdate{
			ECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1000000, PosY: 2000000, PosZ: 3000000,
				VelX: 0, VelY: 0, VelZ: 0,
			},
		},
	}
}

// The wire envelope must match OCUDU's ntn_config_update schema exactly:
// {"cmd":...,"cells":[{plmn,nci,epoch_timestamp,ntn_ul_sync_validity_duration,
// ephemeris_info:{ecef:{position_x,...}}}]} with PHYSICAL values (codepoint×step).
func TestBuildNTNConfigUpdate_ECEF_WireFormat(t *testing.T) {
	env, err := buildNTNConfigUpdate(ecefRuntimeUpdate())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, want := range []string{
		`"cmd":"ntn_config_update"`,
		`"plmn":"00101"`,
		`"nci":1`,
		`"epoch_timestamp":1893456000000`,
		`"ntn_ul_sync_validity_duration":5`,
		`"ephemeris_info":{"ecef":{`,
		`"position_x":1300000`, // 1000000 codepoint × 1.3 m
		`"position_y":2600000`,
		`"position_z":3900000`,
		`"velocity_vx":0`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("envelope missing %q\n%s", want, js)
		}
	}
	// ECEF path must not emit an orbital object.
	if strings.Contains(js, `"orbital"`) {
		t.Errorf("ECEF update leaked an orbital block:\n%s", js)
	}
}

func TestBuildNTNConfigUpdate_Orbital_RadiansAndKeys(t *testing.T) {
	u := provider.RuntimeUpdate{
		Cell:              provider.CellIdentity{PLMN: "00101", NCI: 2},
		EpochUnixMs:       1893456000000,
		UlSyncValidityDur: 10,
		Ephemeris: provider.EphemerisUpdate{
			Orbital: &ntnv1alpha1.EphemerisOrbital{
				SemiMajorAxis: 6921000, Eccentricity: 1, Inclination: 900000,
				RightAscension: 1800000, ArgOfPeriapsis: 900000, MeanAnomaly: 1800000,
			},
		},
	}
	env, err := buildNTNConfigUpdate(u)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, _ := json.Marshal(env)
	js := string(b)
	for _, want := range []string{
		`"orbital":{`,
		`"semi_major_axis":6921000`, // metres, passthrough
		`"longitude":`,              // RAAN emitted under OCUDU key "longitude"
		`"periapsis":`,              // argOfPeriapsis emitted under "periapsis"
	} {
		if !strings.Contains(js, want) {
			t.Errorf("orbital envelope missing %q\n%s", want, js)
		}
	}
	// inclination 900000 (90°) → π/2 rad.
	if !strings.Contains(js, `"inclination":1.5707963267948966`) {
		t.Errorf("inclination not converted to radians:\n%s", js)
	}
}

func TestBuildNTNConfigUpdate_NoEphemeris_Errors(t *testing.T) {
	_, err := buildNTNConfigUpdate(provider.RuntimeUpdate{Cell: provider.CellIdentity{PLMN: "00101", NCI: 1}})
	if err == nil {
		t.Fatal("expected error when neither ECEF nor orbital is set")
	}
}

// wsTestServer starts an in-process remote_control-like WS server that captures
// the first frame and replies with `reply`. Returns host:port + a payload getter.
func wsTestServer(t *testing.T, reply string) (string, func() []byte) {
	t.Helper()
	var mu sync.Mutex
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow() //nolint:errcheck
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		mu.Lock()
		got = data
		mu.Unlock()
		_ = c.Write(r.Context(), websocket.MessageText, []byte(reply))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func TestPushNTNConfigUpdate_Success(t *testing.T) {
	endpoint, captured := wsTestServer(t, `{"cmd":"ntn_config_update","timestamp":"2030-01-01T00:00:00"}`)
	env, _ := buildNTNConfigUpdate(ecefRuntimeUpdate())
	if err := pushNTNConfigUpdate(context.Background(), endpoint, env); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.Contains(string(captured()), `"cmd":"ntn_config_update"`) {
		t.Errorf("server did not receive the ntn_config_update frame: %s", captured())
	}
}

func TestPushNTNConfigUpdate_Rejected(t *testing.T) {
	endpoint, _ := wsTestServer(t, `{"error":"epoch_timestamp value is in past","cmd":"ntn_config_update"}`)
	env, _ := buildNTNConfigUpdate(ecefRuntimeUpdate())
	err := pushNTNConfigUpdate(context.Background(), endpoint, env)
	var we *wsError
	if !errors.As(err, &we) || we.kind != wsRejected {
		t.Fatalf("expected wsRejected, got %v", err)
	}
	if we.retryable() {
		t.Error("a gNB rejection must not be retryable")
	}
}

func TestPushNTNConfigUpdate_Unreachable(t *testing.T) {
	env, _ := buildNTNConfigUpdate(ecefRuntimeUpdate())
	// 127.0.0.1:1 is reserved/closed — dial fails.
	err := pushNTNConfigUpdate(context.Background(), "127.0.0.1:1", env)
	var we *wsError
	if !errors.As(err, &we) || we.kind != wsUnreachable {
		t.Fatalf("expected wsUnreachable, got %v", err)
	}
	if !we.retryable() {
		t.Error("an unreachable gNB should be retryable")
	}
}

func TestProviderPushRuntimeUpdate_UnsupportedWhenNoEndpoint(t *testing.T) {
	p := &Provider{}
	err := p.PushRuntimeUpdate(context.Background(), provider.ResolvedRemoteControl{}, ecefRuntimeUpdate())
	if !errors.Is(err, provider.ErrRuntimeUnsupported) {
		t.Fatalf("expected ErrRuntimeUnsupported, got %v", err)
	}
}

func TestProviderPushRuntimeUpdate_Success(t *testing.T) {
	endpoint, captured := wsTestServer(t, `{"cmd":"ntn_config_update","timestamp":"x"}`)
	p := &Provider{}
	if err := p.PushRuntimeUpdate(
		context.Background(), provider.ResolvedRemoteControl{Endpoint: endpoint}, ecefRuntimeUpdate(),
	); err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.Contains(string(captured()), `"position_x":1300000`) {
		t.Errorf("server did not receive converted physical values: %s", captured())
	}
}
