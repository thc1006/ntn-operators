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
	// SatSwitch is the per-cell sat_switch_with_resync block (issue #52 / #49
	// mechanism); omitted unless a switch is pending.
	SatSwitch *satSwitchJSON `json:"sat_switch_with_resync,omitempty"`
}

// satSwitchJSON mirrors OCUDU's per-cell sat_switch_with_resync object. All
// scalars are optional except the nested ntn_cfg (OCUDU rejects a switch without
// it). Timestamps are Unix ms; the sat-switch epoch is NOT future-checked by
// OCUDU (unlike the serving-cell epoch_timestamp).
type satSwitchJSON struct {
	EpochTimestamp  int64               `json:"epoch_timestamp,omitempty"`
	GatewayLocation *geodeticJSON       `json:"ntn_gateway_location,omitempty"`
	TServiceStart   int64               `json:"t_service_start,omitempty"`
	SSBTimeOffsetSf int                 `json:"ssb_time_offset_sf,omitempty"`
	NTNCfg          satSwitchNtnCfgJSON `json:"ntn_cfg"`
}

// geodeticJSON is OCUDU's geodetic_coordinates_t: degrees, degrees, metres.
type geodeticJSON struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude"`
}

// satSwitchNtnCfgJSON is the nested ntn_cfg of a sat switch. It is the ONLY
// OCUDU surface accepting k_mac. Note the key is ntn_ul_sync_validity_dur here
// (NOT the serving-cell's ntn_ul_sync_validity_duration), and ta_info accepts
// only ta_common/ta_common_drift/ta_common_drift_variant (no offset).
type satSwitchNtnCfgJSON struct {
	EphemerisInfo        ephemerisInfoJSON `json:"ephemeris_info"`
	KMac                 *int              `json:"k_mac,omitempty"`
	CellSpecificKoffset  *int              `json:"cell_specific_koffset,omitempty"`
	NTNUlSyncValidityDur *int              `json:"ntn_ul_sync_validity_dur,omitempty"`
	TAInfo               *taInfoJSON       `json:"ta_info,omitempty"`
	TAReport             *bool             `json:"ta_report,omitempty"`
}

// taInfoJSON is OCUDU's runtime ta_info: physical values (µs, µs/s, µs/s²).
type taInfoJSON struct {
	TACommon             float64  `json:"ta_common"`
	TACommonDrift        *float64 `json:"ta_common_drift,omitempty"`
	TACommonDriftVariant *float64 `json:"ta_common_drift_variant,omitempty"`
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

// buildEphemerisInfo converts an EphemerisUpdate to the wire ephemeris_info,
// applying the same codepoint→physical-SI conversions as the static config
// emitter. Shared by the serving cell and the sat-switch nested ntn_cfg.
func buildEphemerisInfo(e provider.EphemerisUpdate) (ephemerisInfoJSON, error) {
	var info ephemerisInfoJSON
	// ECEF and orbital are mutually exclusive (the CRD CEL rules enforce this at
	// admission; guard here too so a bypassing caller can't emit an ambiguous frame).
	if e.ECEF != nil && e.Orbital != nil {
		return info, fmt.Errorf("ephemeris update has both ECEF and orbital data (mutually exclusive)")
	}
	switch {
	case e.Orbital != nil:
		o := e.Orbital
		info.Orbital = &orbitalJSON{
			SemiMajorAxis: float64(o.SemiMajorAxis), // metres, passthrough
			Eccentricity:  float64(o.Eccentricity) * eccentricityScale,
			Inclination:   float64(o.Inclination) * milliDegToRad,
			Longitude:     float64(o.RightAscension) * milliDegToRad,
			Periapsis:     float64(o.ArgOfPeriapsis) * milliDegToRad,
			MeanAnomaly:   float64(o.MeanAnomaly) * milliDegToRad,
		}
	case e.ECEF != nil:
		ec := e.ECEF
		info.ECEF = &ecefJSON{
			PositionX:  float64(ec.PosX) * ntnv1alpha1.ECEFPositionStep,
			PositionY:  float64(ec.PosY) * ntnv1alpha1.ECEFPositionStep,
			PositionZ:  float64(ec.PosZ) * ntnv1alpha1.ECEFPositionStep,
			VelocityVX: float64(ec.VelX) * ntnv1alpha1.ECEFVelocityStep,
			VelocityVY: float64(ec.VelY) * ntnv1alpha1.ECEFVelocityStep,
			VelocityVZ: float64(ec.VelZ) * ntnv1alpha1.ECEFVelocityStep,
		}
	default:
		return info, fmt.Errorf("ephemeris update has neither ECEF nor orbital data")
	}
	return info, nil
}

// buildTAInfo converts a CRD TAInfo to the runtime ta_info wire object. Only
// ta_common / ta_common_drift / ta_common_drift_variant exist on this surface;
// ta_common_offset is YAML-only and is intentionally dropped.
func buildTAInfo(ta *ntnv1alpha1.TAInfo) *taInfoJSON {
	out := &taInfoJSON{TACommon: float64(ta.TACommon) * taCommonStepUs}
	if ta.TACommonDrift != 0 {
		d := float64(ta.TACommonDrift) * taCommonDriftStep
		out.TACommonDrift = &d
	}
	if ta.TACommonDriftVariant != 0 {
		v := float64(ta.TACommonDriftVariant) * taCommonDriftVarStep
		out.TACommonDriftVariant = &v
	}
	return out
}

// buildSatSwitch converts a CRD SatSwitchWithResync to the wire
// sat_switch_with_resync object. The nested ntn_cfg ephemeris is required.
func buildSatSwitch(s *ntnv1alpha1.SatSwitchWithResync) (*satSwitchJSON, error) {
	info, err := buildEphemerisInfo(provider.EphemerisUpdate{
		ECEF:    s.NTNConfig.EphemerisECEF,
		Orbital: s.NTNConfig.EphemerisOrbital,
	})
	if err != nil {
		return nil, fmt.Errorf("sat_switch_with_resync.ntn_cfg: %w", err)
	}
	// Copy scalar pointers by value into fresh allocations so the wire envelope
	// never aliases live CR-spec memory (consistent with the fields allocated below).
	cfg := satSwitchNtnCfgJSON{EphemerisInfo: info}
	if s.NTNConfig.KMac != nil {
		k := *s.NTNConfig.KMac
		cfg.KMac = &k
	}
	if s.NTNConfig.NTNUlSyncValidityDur != nil {
		d := *s.NTNConfig.NTNUlSyncValidityDur
		cfg.NTNUlSyncValidityDur = &d
	}
	if s.NTNConfig.TAReport != nil {
		rep := *s.NTNConfig.TAReport
		cfg.TAReport = &rep
	}
	if s.NTNConfig.CellSpecificKoffset > 0 {
		k := s.NTNConfig.CellSpecificKoffset
		cfg.CellSpecificKoffset = &k
	}
	if s.NTNConfig.TAInfo != nil {
		cfg.TAInfo = buildTAInfo(s.NTNConfig.TAInfo)
	}
	out := &satSwitchJSON{
		EpochTimestamp:  s.EpochUnixMs,
		TServiceStart:   s.TServiceStartUnixMs,
		SSBTimeOffsetSf: s.SSBTimeOffsetSubframes,
		NTNCfg:          cfg,
	}
	if s.GatewayLocation != nil {
		out.GatewayLocation = &geodeticJSON{
			Latitude:  float64(s.GatewayLocation.Latitude) * oneEMinus4DegToDeg,
			Longitude: float64(s.GatewayLocation.Longitude) * oneEMinus4DegToDeg,
			Altitude:  float64(s.GatewayLocation.Altitude),
		}
	}
	return out, nil
}

// buildNTNConfigUpdate converts a RuntimeUpdate into the wire envelope, applying
// the same codepoint→physical-SI conversions as the static config emitter.
func buildNTNConfigUpdate(u provider.RuntimeUpdate) (ntnConfigUpdateEnvelope, error) {
	info, err := buildEphemerisInfo(u.Ephemeris)
	if err != nil {
		return ntnConfigUpdateEnvelope{}, fmt.Errorf("runtime update: %w", err)
	}
	cell := ntnConfigCellJSON{
		PLMN:              u.Cell.PLMN,
		NCI:               u.Cell.NCI,
		EpochTimestamp:    u.EpochUnixMs,
		UlSyncValidityDur: u.UlSyncValidityDur,
		EphemerisInfo:     info,
	}
	if u.SatSwitch != nil {
		ss, err := buildSatSwitch(u.SatSwitch)
		if err != nil {
			return ntnConfigUpdateEnvelope{}, err
		}
		cell.SatSwitch = ss
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
