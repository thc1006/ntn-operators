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
	"math"
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

// satSwitchRuntimeUpdate is an ECEF serving-cell update carrying a full
// sat_switch_with_resync block, including k_mac (issue #52).
func satSwitchRuntimeUpdate() provider.RuntimeUpdate {
	kmac := 300
	dur := 30
	rep := true
	u := ecefRuntimeUpdate()
	u.SatSwitch = &ntnv1alpha1.SatSwitchWithResync{
		EpochUnixMs:            1893456000000,
		TServiceStartUnixMs:    1893456060000,
		SSBTimeOffsetSubframes: 4,
		GatewayLocation:        &ntnv1alpha1.NTNGatewayLocation{Latitude: 248500, Longitude: 1210000, Altitude: 100},
		NTNConfig: ntnv1alpha1.SatSwitchNTNConfig{
			EphemerisECEF:        &ntnv1alpha1.EphemerisECEF{PosX: 1000000, PosY: 2000000, PosZ: 3000000},
			KMac:                 &kmac,
			CellSpecificKoffset:  20,
			NTNUlSyncValidityDur: &dur,
			TAReport:             &rep,
			TAInfo:               &ntnv1alpha1.TAInfo{TACommon: 1000, TACommonDrift: 50, TACommonDriftVariant: 10},
		},
	}
	return u
}

// The sat_switch_with_resync wire block must match OCUDU's per-cell schema:
// nested key is ntn_cfg, the k_mac field is a plain int (no conversion), the
// UL-sync key is ntn_ul_sync_validity_dur (NOT the serving-cell _duration), and
// physical fields (gateway degrees, ta µs, nested ephemeris metres) are converted.
func TestBuildNTNConfigUpdate_SatSwitch_KMac_WireFormat(t *testing.T) {
	b, err := json.Marshal(mustBuild(t, satSwitchRuntimeUpdate()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, want := range []string{
		`"sat_switch_with_resync":{`,
		`"ntn_cfg":{`,
		`"k_mac":300`, // issue #52 — passthrough int, the whole point
		`"cell_specific_koffset":20`,
		`"ntn_ul_sync_validity_dur":30`, // nested key is _dur, not _duration
		`"ta_report":true`,
		`"ssb_time_offset_sf":4`,
		`"t_service_start":1893456060000`,
		`"position_x":1300000`, // nested ephemeris converted (1000000 × 1.3)
	} {
		if !strings.Contains(js, want) {
			t.Errorf("sat-switch envelope missing %q\n%s", want, js)
		}
	}
	// The serving cell keeps its own ntn_ul_sync_validity_duration; the nested
	// block must NOT reuse that key. Exactly one _duration in the whole frame.
	if n := strings.Count(js, `"ntn_ul_sync_validity_duration"`); n != 1 {
		t.Errorf("expected exactly one serving-cell _duration key, got %d:\n%s", n, js)
	}

	// Verify physical float conversions numerically (json float formatting is
	// fragile as a substring). Round-trip through the same wire types.
	var rt ntnConfigUpdateEnvelope
	if err := json.Unmarshal(b, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ss := rt.Cells[0].SatSwitch
	if ss == nil {
		t.Fatal("sat_switch_with_resync missing after round-trip")
	}
	if ss.GatewayLocation == nil || math.Abs(ss.GatewayLocation.Latitude-24.85) > 1e-9 {
		t.Errorf("gateway latitude = %+v, want 24.85 (248500 × 1e-4)", ss.GatewayLocation)
	}
	if ss.NTNCfg.TAInfo == nil {
		t.Fatal("nested ta_info missing")
	}
	if got := ss.NTNCfg.TAInfo.TACommon; math.Abs(got-4.072) > 1e-6 { // 1000 × 0.004072
		t.Errorf("ta_common = %v, want ~4.072", got)
	}
	if ss.NTNCfg.TAInfo.TACommonDrift == nil || math.Abs(*ss.NTNCfg.TAInfo.TACommonDrift-0.01) > 1e-9 { // 50 × 2e-4
		t.Errorf("ta_common_drift = %v, want 0.01", ss.NTNCfg.TAInfo.TACommonDrift)
	}
}

// A sat switch with no nested ephemeris is meaningless, so the CRD (and this
// builder) require exactly one ephemeris. This is STRICTER than OCUDU, whose
// parser treats sat_switch_with_resync.ntn_cfg.ephemeris_info as optional.
func TestBuildNTNConfigUpdate_SatSwitch_MissingEphemeris_Errors(t *testing.T) {
	u := ecefRuntimeUpdate()
	u.SatSwitch = &ntnv1alpha1.SatSwitchWithResync{} // NTNConfig has no ephemeris
	if _, err := buildNTNConfigUpdate(u); err == nil {
		t.Fatal("expected error when the sat-switch nested ntn_cfg has no ephemeris")
	}
}

// No pending switch → the frame must omit sat_switch_with_resync entirely.
func TestBuildNTNConfigUpdate_NoSatSwitch_OmitsBlock(t *testing.T) {
	b, err := json.Marshal(mustBuild(t, ecefRuntimeUpdate()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "sat_switch_with_resync") {
		t.Errorf("frame leaked a sat_switch_with_resync block:\n%s", b)
	}
}

func mustBuild(t *testing.T, u provider.RuntimeUpdate) ntnConfigUpdateEnvelope {
	t.Helper()
	env, err := buildNTNConfigUpdate(u)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return env
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

// A non-JSON reply must NOT be reported as success (it would mask a failed push).
// It is treated as retryable (the idempotent push is safe to retry).
func TestPushNTNConfigUpdate_UnparseableReply(t *testing.T) {
	endpoint, _ := wsTestServer(t, `not json at all`)
	env, _ := buildNTNConfigUpdate(ecefRuntimeUpdate())
	err := pushNTNConfigUpdate(context.Background(), endpoint, env)
	var we *wsError
	if !errors.As(err, &we) {
		t.Fatalf("expected a *wsError for an unparseable reply, got %v", err)
	}
	if !we.retryable() {
		t.Error("an unparseable reply should be retryable, not a silent success")
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

// A frame exceeding OCUDU's 16 KB cap must be rejected BEFORE dialing, as a
// permanent (non-retryable) error.
func TestPushNTNConfigUpdate_PayloadTooLarge(t *testing.T) {
	env := ntnConfigUpdateEnvelope{
		Cmd:   ntnConfigUpdateCmd,
		Cells: []ntnConfigCellJSON{{PLMN: strings.Repeat("x", wsMaxPayload+1), NCI: 1}},
	}
	// Endpoint is never dialed — the cap is checked before the dial.
	err := pushNTNConfigUpdate(context.Background(), "127.0.0.1:1", env)
	var we *wsError
	if !errors.As(err, &we) || we.kind != wsPayloadTooLarge {
		t.Fatalf("expected wsPayloadTooLarge, got %v", err)
	}
	if we.retryable() {
		t.Error("an oversized frame is a permanent error, not retryable")
	}
}

// A reply exceeding the client read limit must fail (not hang, not succeed).
func TestPushNTNConfigUpdate_OversizedReply(t *testing.T) {
	endpoint, _ := wsTestServer(t, strings.Repeat("A", wsMaxPayload+1))
	env, _ := buildNTNConfigUpdate(ecefRuntimeUpdate())
	err := pushNTNConfigUpdate(context.Background(), endpoint, env)
	var we *wsError
	if !errors.As(err, &we) || we.kind != wsUnreachable {
		t.Fatalf("expected wsUnreachable on an oversized reply, got %v", err)
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

// TestProviderPushRuntimeUpdate_GNBRejectionIsPermanent verifies that a gNB
// rejection ({"error": ...}) is classified as a permanent ErrRuntimePushRejected,
// so the reconciler gives up rather than tight-requeueing a config the gNB will
// never accept. This is the "give-up" path the audit (findings.md B-8) flagged as
// untested — provider.go:264-265 wrapping a non-retryable wsError.
func TestProviderPushRuntimeUpdate_GNBRejectionIsPermanent(t *testing.T) {
	endpoint, _ := wsTestServer(t, `{"error":"invalid ntn config"}`)
	p := &Provider{}
	err := p.PushRuntimeUpdate(
		context.Background(), provider.ResolvedRemoteControl{Endpoint: endpoint}, ecefRuntimeUpdate(),
	)
	if !errors.Is(err, provider.ErrRuntimePushRejected) {
		t.Fatalf("gNB rejection must be permanent (ErrRuntimePushRejected), got %v", err)
	}
}

// TestProviderPushRuntimeUpdate_UnreachableIsRetryable verifies the other side of
// the classification: an unreachable endpoint must stay retryable (NOT
// ErrRuntimePushRejected), so a transient gNB/network blip is requeued rather than
// abandoned. A plain-HTTP server that never completes the WebSocket handshake
// forces the dial to fail → wsUnreachable.
func TestProviderPushRuntimeUpdate_UnreachableIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not a websocket endpoint", http.StatusBadGateway)
	}))
	defer srv.Close()
	endpoint := strings.TrimPrefix(srv.URL, "http://")

	p := &Provider{}
	err := p.PushRuntimeUpdate(
		context.Background(), provider.ResolvedRemoteControl{Endpoint: endpoint}, ecefRuntimeUpdate(),
	)
	if err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
	if errors.Is(err, provider.ErrRuntimePushRejected) {
		t.Fatalf("an unreachable endpoint must be retryable, not permanent: %v", err)
	}
	var we *wsError
	if !errors.As(err, &we) || !we.retryable() {
		t.Fatalf("expected a retryable wsError (wsUnreachable), got %v", err)
	}
}
