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
	"fmt"
	"time"

	"github.com/coder/websocket"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

const (
	// ntnConfigUpdateCmd is the OCUDU remote_control command name (MR !798).
	ntnConfigUpdateCmd = "ntn_config_update"
	// wsDialTimeout bounds the whole dial+send+reply exchange.
	wsDialTimeout = 5 * time.Second
	// wsMaxPayload mirrors OCUDU's remote_control 16 KB frame cap.
	wsMaxPayload = 16 * 1024
)

// ntnConfigUpdateEnvelope is the JSON frame OCUDU's remote_control WebSocket
// server expects: {"cmd":"ntn_config_update","cells":[...]}. Values are PHYSICAL
// SI (metres, m/s, radians, µs, degrees) — OCUDU deserializes them into the same
// physical structs the CLI config uses, so the codepoint→physical conversion
// below matches config.go / renderEphemerisBlock exactly. (Ephemeris ECEF keys
// are position_x/velocity_vx here, NOT the static YAML's pos_x/vel_x.)
type ntnConfigUpdateEnvelope struct {
	Cmd   string              `json:"cmd"`
	Cells []ntnConfigCellJSON `json:"cells"`
}

type ntnConfigCellJSON struct {
	PLMN              string            `json:"plmn"`
	NCI               uint64            `json:"nci"`
	EpochTimestamp    int64             `json:"epoch_timestamp"`
	UlSyncValidityDur int               `json:"ntn_ul_sync_validity_duration"`
	EphemerisInfo     ephemerisInfoJSON `json:"ephemeris_info"`
}

type ephemerisInfoJSON struct {
	ECEF    *ecefJSON    `json:"ecef,omitempty"`
	Orbital *orbitalJSON `json:"orbital,omitempty"`
}

type ecefJSON struct {
	PositionX  float64 `json:"position_x"`
	PositionY  float64 `json:"position_y"`
	PositionZ  float64 `json:"position_z"`
	VelocityVX float64 `json:"velocity_vx"`
	VelocityVY float64 `json:"velocity_vy"`
	VelocityVZ float64 `json:"velocity_vz"`
}

type orbitalJSON struct {
	SemiMajorAxis float64 `json:"semi_major_axis"`
	Eccentricity  float64 `json:"eccentricity"`
	Inclination   float64 `json:"inclination"`
	Longitude     float64 `json:"longitude"`
	Periapsis     float64 `json:"periapsis"`
	MeanAnomaly   float64 `json:"mean_anomaly"`
}

// buildNTNConfigUpdate converts a RuntimeUpdate into the wire envelope, applying
// the same codepoint→physical-SI conversions as the static config emitter.
func buildNTNConfigUpdate(u provider.RuntimeUpdate) (ntnConfigUpdateEnvelope, error) {
	cell := ntnConfigCellJSON{
		PLMN:              u.Cell.PLMN,
		NCI:               u.Cell.NCI,
		EpochTimestamp:    u.EpochUnixMs,
		UlSyncValidityDur: u.UlSyncValidityDur,
	}
	switch {
	case u.Ephemeris.Orbital != nil:
		o := u.Ephemeris.Orbital
		cell.EphemerisInfo.Orbital = &orbitalJSON{
			SemiMajorAxis: float64(o.SemiMajorAxis), // metres, passthrough
			Eccentricity:  float64(o.Eccentricity) * eccentricityScale,
			Inclination:   float64(o.Inclination) * milliDegToRad,
			Longitude:     float64(o.RightAscension) * milliDegToRad,
			Periapsis:     float64(o.ArgOfPeriapsis) * milliDegToRad,
			MeanAnomaly:   float64(o.MeanAnomaly) * milliDegToRad,
		}
	case u.Ephemeris.ECEF != nil:
		e := u.Ephemeris.ECEF
		cell.EphemerisInfo.ECEF = &ecefJSON{
			PositionX:  float64(e.PosX) * ntnv1alpha1.ECEFPositionStep,
			PositionY:  float64(e.PosY) * ntnv1alpha1.ECEFPositionStep,
			PositionZ:  float64(e.PosZ) * ntnv1alpha1.ECEFPositionStep,
			VelocityVX: float64(e.VelX) * ntnv1alpha1.ECEFVelocityStep,
			VelocityVY: float64(e.VelY) * ntnv1alpha1.ECEFVelocityStep,
			VelocityVZ: float64(e.VelZ) * ntnv1alpha1.ECEFVelocityStep,
		}
	default:
		return ntnConfigUpdateEnvelope{}, fmt.Errorf("runtime update has neither ECEF nor orbital ephemeris")
	}
	return ntnConfigUpdateEnvelope{Cmd: ntnConfigUpdateCmd, Cells: []ntnConfigCellJSON{cell}}, nil
}

// wsErrorKind classifies a runtime-push failure for the reconciler's
// requeue/condition logic.
type wsErrorKind int

const (
	// wsUnreachable: dial/write/read failed — transient, worth a requeue.
	wsUnreachable wsErrorKind = iota
	// wsPayloadTooLarge: frame exceeds OCUDU's 16 KB cap — permanent.
	wsPayloadTooLarge
	// wsMarshal: could not build the JSON frame — permanent.
	wsMarshal
	// wsRejected: the gNB replied {"error": ...} — permanent (bad config).
	wsRejected
)

// wsError is a typed runtime-push error carrying a kind for requeue decisions.
type wsError struct {
	kind wsErrorKind
	msg  string
}

func (e *wsError) Error() string { return e.msg }

// retryable reports whether the failure is worth requeuing (transient) vs a
// permanent config/protocol error the operator should not hammer.
func (e *wsError) retryable() bool { return e.kind == wsUnreachable }

// pushNTNConfigUpdate dials the gNB remote_control WebSocket at endpoint, sends a
// single ntn_config_update text frame, reads the one-line reply, and returns nil
// on success or a *wsError. endpoint is host:port (scheme ws:// is added here).
func pushNTNConfigUpdate(ctx context.Context, endpoint string, env ntnConfigUpdateEnvelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return &wsError{wsMarshal, fmt.Sprintf("marshal ntn_config_update: %v", err)}
	}
	if len(payload) > wsMaxPayload {
		return &wsError{wsPayloadTooLarge, fmt.Sprintf("frame %d bytes exceeds OCUDU %d-byte cap", len(payload), wsMaxPayload)}
	}

	dialCtx, cancel := context.WithTimeout(ctx, wsDialTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, "ws://"+endpoint, nil)
	if err != nil {
		return &wsError{wsUnreachable, fmt.Sprintf("dial %s: %v", endpoint, err)}
	}
	defer conn.CloseNow() //nolint:errcheck // best-effort close; Close below is the graceful path
	conn.SetReadLimit(wsMaxPayload)

	if err := conn.Write(dialCtx, websocket.MessageText, payload); err != nil {
		return &wsError{wsUnreachable, fmt.Sprintf("write to %s: %v", endpoint, err)}
	}

	_, reply, err := conn.Read(dialCtx)
	if err != nil {
		return &wsError{wsUnreachable, fmt.Sprintf("read reply from %s: %v", endpoint, err)}
	}

	// Success reply is {"cmd":...,"timestamp":...}; failure is {"error":"..."}.
	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &r); err == nil && r.Error != "" {
		return &wsError{wsRejected, fmt.Sprintf("gNB rejected ntn_config_update: %s", r.Error)}
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
	return nil
}
