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
	"strings"
	"testing"

	// Aliased to k8syaml so tests can freely use a local `yaml := string(data)`
	// without shadowing the import (revive import-shadowing check).
	k8syaml "sigs.k8s.io/yaml"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

func TestGenerateConfig_GEODefault(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			TACommon:            0,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 20922195,
				PosY: 1967783,
				PosZ: 19770302,
			},
			PayloadType: "transparent",
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)

	// NTN section — must use srsRAN CLI11 format.
	assertContains(t, yaml, "cell_specific_koffset: 150")

	// ta_common MUST be under ntn.ta_info (not ntn.ta_common).
	assertContains(t, yaml, "ta_info:")
	assertContains(t, yaml, "ta_common: 0")
	assertNotContains(t, yaml, "ntn:\n  ta_common:") // Must NOT be flat

	// Ephemeris MUST use ephemeris_info_ecef (not ephemeris_info).
	assertContains(t, yaml, "ephemeris_info_ecef:")
	assertNotContains(t, yaml, "ephemeris_info:")
	// Emitted as physical metres = codepoint × 1.3 (20922195×1.3, 1967783×1.3, …).
	assertContains(t, yaml, "pos_x: 27198853.5")
	assertContains(t, yaml, "pos_y: 2558117.9")
	assertContains(t, yaml, "pos_z: 25701392.6")
	assertContains(t, yaml, "vel_x: 0")
	assertContains(t, yaml, "vel_y: 0")
	assertContains(t, yaml, "vel_z: 0")

	// SIB19 — must have si_window_position (required by srsRAN).
	assertContains(t, yaml, "sib_mapping: 19")
	assertContains(t, yaml, "si_window_position:")

	// cell_cfg NTN defaults.
	assertContains(t, yaml, "max_nof_harq_retxs: 0")
	assertContains(t, yaml, "max_msg3_harq_retx: 0")

	// cu_cp RRC guard time.
	assertContains(t, yaml, "rrc_procedure_guard_time_ms: 12800")
}

func TestGenerateConfig_CustomKoffset(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 500,
			TACommon:            1000,
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertContains(t, yaml, "cell_specific_koffset: 500")
	// ta_common under ta_info subsection
	assertContains(t, yaml, "ta_common: 4.072") // 1000 × 0.004072 µs
}

func TestGenerateConfig_CustomCellOverrides(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			PdschMaxHarqRetxs:    2,
			PrachMaxMsg3HarqRetx: 1,
			RrcGuardTimeMs:       25600,
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertContains(t, yaml, "max_nof_harq_retxs: 2")
	assertContains(t, yaml, "max_msg3_harq_retx: 1")
	assertContains(t, yaml, "rrc_procedure_guard_time_ms: 25600")
}

func TestGenerateConfig_LEOWithVelocity(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 50,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1000000, PosY: 2000000, PosZ: 3000000,
				VelX: 100, VelY: -200, VelZ: 50,
			},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	// Emitted as physical m/s = codepoint × 0.06 (100×0.06=6, -200×0.06=-12, …).
	assertContains(t, yaml, "vel_x: 6")
	assertContains(t, yaml, "vel_y: -12")
	assertContains(t, yaml, "vel_z: 3")
}

func TestGenerateConfig_OrbitalEphemeris(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 50,
			EphemerisOrbital: &ntnv1alpha1.EphemerisOrbital{
				SemiMajorAxis:  6921000,
				Eccentricity:   1,
				Inclination:    879000,
				RightAscension: 1000000,
				ArgOfPeriapsis: 900000,
				MeanAnomaly:    2700000,
			},
			PayloadType: "transparent",
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)

	// Should emit ephemeris_orbital, NOT ephemeris_info_ecef.
	assertContains(t, yaml, "ephemeris_orbital:")
	assertNotContains(t, yaml, "ephemeris_info_ecef:")

	assertContains(t, yaml, "semi_major_axis: 6921000")
	// Physical: eccentricity × 1e-6; angles × π/1.8e6 (1e-4° → radians).
	assertContains(t, yaml, "eccentricity: 0.000001")
	assertContains(t, yaml, "inclination: 1.5341444125030157")
	assertContains(t, yaml, "longitude: 1.7453292519943295")
	assertContains(t, yaml, "periapsis: 1.5707963267948966")
	assertContains(t, yaml, "mean_anomaly: 4.71238898038469")
}

func TestGenerateConfig_BothEphemerisSet(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF:    &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			EphemerisOrbital: &ntnv1alpha1.EphemerisOrbital{SemiMajorAxis: 6921000},
		},
	}
	_, err := GenerateConfig(spec)
	if err == nil {
		t.Fatal("expected error when both ephemeris types are set")
	}
	assertContains(t, err.Error(), "mutually exclusive")
}

func TestGenerateConfig_NeitherEphemerisSet(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
		},
	}
	_, err := GenerateConfig(spec)
	if err == nil {
		t.Fatal("expected error when neither ephemeris type is set")
	}
}

func TestGenerateConfig_TAInfoExtended(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			TAInfo: &ntnv1alpha1.TAInfo{
				TACommon:             1000,
				TACommonDrift:        50,
				TACommonDriftVariant: 10,
				TACommonOffset:       200,
			},
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "ta_info:")
	assertContains(t, yaml, "ta_common: 4.072") // 1000 × 0.004072 µs
	// Physical µs family: drift × 2e-4, variant × 2e-5, offset × 0.004072.
	assertContains(t, yaml, "ta_common_drift: 0.01")
	assertContains(t, yaml, "ta_common_drift_variant: 0.0002")
	assertContains(t, yaml, "ta_common_offset: 0.8144")
}

func TestGenerateConfig_EpochTime(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			EpochTime: &ntnv1alpha1.EpochTime{
				SFN:            512,
				SubframeNumber: 5,
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "epoch_time:")
	assertContains(t, yaml, "sfn: 512")
	assertContains(t, yaml, "subframe_number: 5")
}

func TestGenerateConfig_FeederLinkInfo(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			FeederLinkInfo: &ntnv1alpha1.FeederLinkInfo{
				EnableDopplerCompensation: true,
				DLFreqHz:                  2680000000,
				ULFreqHz:                  2560000000,
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "feeder_link:")
	assertContains(t, yaml, "enable_doppler_compensation: true")
	assertContains(t, yaml, "dl_freq: 2680000000")
	assertContains(t, yaml, "ul_freq: 2560000000")
}

func TestGenerateConfig_GatewayLocation(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			NTNGatewayLocation: &ntnv1alpha1.NTNGatewayLocation{
				Latitude:  248500,
				Longitude: 1210000,
				Altitude:  100,
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "gateway_location:")
	// The serving-cell gateway key is "gateway_location"; "ntn_gateway_location"
	// is only valid nested under sat_switch_with_resync and is rejected here.
	assertNotContains(t, yaml, "ntn_gateway_location:")
	// Emitted as degrees = codepoint × 1e-4 (248500 → 24.85, 1210000 → 121).
	assertContains(t, yaml, "latitude: 24.85")
	assertContains(t, yaml, "longitude: 121")
	assertContains(t, yaml, "altitude: 100")
}

func TestGenerateConfig_PolarizationBoth(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization:  &ntnv1alpha1.NTNPolarization{DL: "rhcp", UL: "lhcp"},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	// Nested OCUDU layout: polarization: { dl:, ul: }
	// Values are quoted via yamlQuote for YAML-injection defense-in-depth.
	assertContains(t, yaml, "polarization:")
	assertContains(t, yaml, `    dl: "rhcp"`)
	assertContains(t, yaml, `    ul: "lhcp"`)
	// MUST NOT emit the old flat scalar form.
	assertNotContains(t, yaml, "polarization: rhcp")
	assertNotContains(t, yaml, "polarization: lhcp")
	assertNotContains(t, yaml, "polarization: circular")
}

func TestGenerateConfig_PolarizationDLOnly(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization:  &ntnv1alpha1.NTNPolarization{DL: "linear"},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "polarization:")
	assertContains(t, yaml, `    dl: "linear"`)
	assertNotContains(t, yaml, "ul:")
}

func TestGenerateConfig_PolarizationULOnly(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization:  &ntnv1alpha1.NTNPolarization{UL: "rhcp"},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "polarization:")
	assertContains(t, yaml, `    ul: "rhcp"`)
	assertNotContains(t, yaml, "dl:")
}

func TestGenerateConfig_PolarizationOmittedWhenNil(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNotContains(t, string(data), "polarization:")
}

func TestGenerateConfig_PolarizationOmittedWhenBothEmpty(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization:  &ntnv1alpha1.NTNPolarization{},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty struct → no DL/UL set → omit entire block (matches OCUDU's if-guarded writer).
	assertNotContains(t, string(data), "polarization:")
}

func TestGenerateConfig_PolarizationStringInjectionResistant(t *testing.T) {
	// Defense-in-depth: even if apiserver enum validation is bypassed and a
	// malicious polarization value somehow reaches GenerateConfig, the rendered
	// YAML MUST NOT allow the injected string to break out and create sibling
	// YAML keys. yamlQuote escapes newlines and colons into a quoted scalar.
	const injected = "rhcp\ninjected_key: pwned"
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization: &ntnv1alpha1.NTNPolarization{
				DL: injected,
				UL: "lhcp",
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	// Surface check: the injection MUST NOT appear as a top-level sibling key.
	assertNotContains(t, yaml, "\ninjected_key: pwned")

	// Structural check: parse the YAML and verify the injected payload lives
	// entirely inside the polarization.dl scalar — no sibling key leaked into
	// the ntn map. This is stronger than substring matching because it fails
	// if escaping regresses in a way that still happens to avoid the literal
	// "\ninjected_key:" string.
	var parsed map[string]any
	if err := k8syaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated YAML did not unmarshal: %v\nYAML:\n%s", err, yaml)
	}
	cellCfg, ok := parsed["cell_cfg"].(map[string]any)
	if !ok {
		t.Fatalf("generated YAML missing cell_cfg map: %#v", parsed["cell_cfg"])
	}
	ntn, ok := cellCfg["ntn"].(map[string]any)
	if !ok {
		t.Fatalf("generated YAML missing cell_cfg.ntn map: %#v", cellCfg["ntn"])
	}
	if _, ok := ntn["injected_key"]; ok {
		t.Fatalf("injection leaked: ntn.injected_key should not exist, got %#v", ntn["injected_key"])
	}
	polarization, ok := ntn["polarization"].(map[string]any)
	if !ok {
		t.Fatalf("generated YAML missing polarization map: %#v", ntn["polarization"])
	}
	dl, ok := polarization["dl"].(string)
	if !ok {
		t.Fatalf("generated YAML missing polarization.dl string: %#v", polarization["dl"])
	}
	if dl != injected {
		t.Fatalf("unexpected polarization.dl round-trip value: got %q, want %q", dl, injected)
	}
}

func TestGenerateConfig_PayloadTypeStringInjectionResistant(t *testing.T) {
	// The # Payload type: ... comment line is line-terminated; yamlQuote does
	// not protect it. sanitizeComment replaces control chars with spaces so
	// no newline / CR can terminate the comment and inject real YAML keys.
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			PayloadType:   "transparent\ninjected_key: pwned",
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertNotContains(t, yaml, "\ninjected_key: pwned")
}

func TestGenerateConfig_PayloadTypeUnicodeLineSeparatorInjectionResistant(t *testing.T) {
	// YAML accepts multiple line-break runes (NEL/LS/PS). Ensure sanitizeComment
	// neutralizes these as well so payloadType cannot escape the comment line.
	separators := []string{"\u0085", "\u2028", "\u2029"}
	for _, sep := range separators {
		spec := &ntnv1alpha1.NTNCellConfigSpec{
			NTN: ntnv1alpha1.NTNParams{
				EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
				PayloadType:   "transparent" + sep + "injected_key: pwned",
			},
		}
		data, err := GenerateConfig(spec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		yaml := string(data)
		assertNotContains(t, yaml, "\ninjected_key: pwned")
		assertNotContains(t, yaml, sep)
	}
}

func TestGenerateConfig_TAReport(t *testing.T) {
	enabled := true
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			TAReport:      &enabled,
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, string(data), "ta_report: true")
}

func TestGenerateConfig_NTNUlSyncValidityDur(t *testing.T) {
	dur := 60
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF:        &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			NTNUlSyncValidityDur: &dur,
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, string(data), "ntn_ul_sync_validity_dur: 60")
}

func TestGenerateConfig_MovingRefLocation(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			MovingRefLocation: &ntnv1alpha1.MovingRefLocation{
				Latitude:  248500,  // 24.85° in 1e-4 degrees
				Longitude: 1210000, // 121.0° in 1e-4 degrees
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "moving_ref_location:")
	assertContains(t, yaml, "latitude: 24.85")
	assertContains(t, yaml, "longitude: 121")
}

func TestGenerateConfig_SatSwitchWithResync(t *testing.T) {
	// satSwitchWithResync is a RUNTIME-only feature: it must NEVER appear in the
	// static YAML. It is delivered via the ntn_config_update remote command (the
	// only OCUDU surface carrying its k_mac field). Setting it must be a no-op for
	// the static emitter, not a parse-breaker (emitting it would inject unknown
	// keys and fail the whole config parse — verified by booting the gNB).
	kmac := 300
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			SatSwitchWithResync: &ntnv1alpha1.SatSwitchWithResync{
				SSBTimeOffsetSubframes: 4,
				NTNConfig: ntnv1alpha1.SatSwitchNTNConfig{
					EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 10, PosY: 20, PosZ: 30},
					KMac:          &kmac,
				},
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertNotContains(t, yaml, "sat_switch_with_resync:")
	assertNotContains(t, yaml, "k_mac:")
	assertNotContains(t, yaml, "t_service_start:")
}

func TestGenerateConfig_NilSpec(t *testing.T) {
	_, err := GenerateConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestGenerateConfig_MatchesLiveGNBFormat(t *testing.T) {
	// Golden structural test: the whole NTN block must live under cell_cfg.ntn
	// (a top-level "ntn:" is rejected by the gNB at parse time,
	// config_extras_mode::error). Verified by booting the OCUDU gNB with this
	// exact output (Phase A/B).
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			TACommon:            0,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 20922195, PosY: 1967783, PosZ: 19770302,
			},
			PayloadType: "transparent",
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)

	// Indentation is significant: keys under cell_cfg.ntn sit at 4 spaces.
	requiredFragments := []string{
		"cell_cfg:",
		"  ntn:",
		"    cell_specific_koffset: 150",
		"    ta_info:",
		"      ta_common: 0",
		"    ephemeris_info_ecef:",
		"      pos_x: 27198853.5", // 20922195 codepoint × 1.3 m
		"  sib:",
		"    si_sched_info:",
		"      - si_period: 16",
		"        sib_mapping: 19",
		"        si_window_position: 1",
		"  pdsch:",
		"    max_nof_harq_retxs: 0",
		"cu_cp:",
		"  rrc:",
		"    rrc_procedure_guard_time_ms: 12800",
	}

	for _, frag := range requiredFragments {
		if !strings.Contains(yaml, frag) {
			t.Errorf("generated config missing required fragment %q\n\nFull output:\n%s", frag, yaml)
		}
	}
}

func TestGenerateConfig_NCells(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1, PosY: 2, PosZ: 3,
			},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{PhysicalCellID: 100, Frequency: 632628},
				{PhysicalCellID: 200},
			},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertContains(t, yaml, "ncells:")
	assertContains(t, yaml, "pci: 100")
	assertContains(t, yaml, "carrier_freq: 632628")
	assertContains(t, yaml, "pci: 200")
	// Second cell has no frequency — must not emit carrier_freq line.
	// Count occurrences of "carrier_freq:" — should be exactly 1.
	if strings.Count(yaml, "carrier_freq:") != 1 {
		t.Errorf(
			"expected exactly 1 carrier_freq line, got %d\n%s",
			strings.Count(yaml, "carrier_freq:"), yaml,
		)
	}
}

func TestGenerateConfig_NCellsOmittedWhenEmpty(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1, PosY: 2, PosZ: 3,
			},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNotContains(t, string(data), "ncells:")
}

func TestGenerateConfig_ReferenceLocation(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1, PosY: 2, PosZ: 3,
			},
			ReferenceLocation: &ntnv1alpha1.ReferenceLocation{
				Latitude:  248500,
				Longitude: 1210000,
			},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertContains(t, yaml, "reference_location:")
	assertContains(t, yaml, "latitude: 24.85")
	assertContains(t, yaml, "longitude: 121")
}

func TestGenerateConfig_ReferenceLocationOmittedWhenNil(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1, PosY: 2, PosZ: 3,
			},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNotContains(t, string(data), "reference_location:")
}

func TestGenerateConfig_DistanceThresholdAndTService(t *testing.T) {
	dist := 5000
	tsvc := 600
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1, PosY: 2, PosZ: 3,
			},
			DistanceThreshold: &dist,
			TService:          &tsvc,
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertContains(t, yaml, "distance_threshold: 5000") // metres, passthrough
	// t_service is intentionally NOT emitted: the CRD models a duration in
	// seconds but OCUDU's t_service is an absolute Unix-ms/UTC timestamp, so
	// emitting the duration would be wrong. Setting it must be a no-op.
	assertNotContains(t, yaml, "t_service:")
}

func TestGenerateConfig_DistanceThresholdAndTServiceOmitted(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 1, PosY: 2, PosZ: 3,
			},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertNotContains(t, yaml, "distance_threshold:")
	assertNotContains(t, yaml, "t_service:")
}

func TestGenerateConfig_SIBScheduleDefaults(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "si_window_length: 5")
	assertContains(t, yaml, "si_period: 16")
	assertContains(t, yaml, "si_window_position: 1")
}

func TestGenerateConfig_SIBScheduleFullOverride(t *testing.T) {
	pos := 4
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			SIBSchedule: &ntnv1alpha1.SIBSchedule{
				SIWindowLength:   20,
				SIPeriod:         32,
				SIWindowPosition: &pos,
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "si_window_length: 20")
	assertContains(t, yaml, "si_period: 32")
	assertContains(t, yaml, "si_window_position: 4")
}

func TestGenerateConfig_SIBSchedulePartialOverride(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			SIBSchedule: &ntnv1alpha1.SIBSchedule{SIPeriod: 8},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	// Only siPeriod overridden; others keep defaults.
	assertContains(t, yaml, "si_window_length: 5")
	assertContains(t, yaml, "si_period: 8")
	assertContains(t, yaml, "si_window_position: 1")
}

func TestGenerateConfig_SIBScheduleZeroPositionRespected(t *testing.T) {
	// Regression: position=0 is valid ("first slot") and must NOT fall
	// back to the default 1. Pointer semantics in SIBSchedule guarantee
	// this distinction.
	zero := 0
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			SIBSchedule: &ntnv1alpha1.SIBSchedule{SIWindowPosition: &zero},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, string(data), "si_window_position: 0")
}

// TestGenerateConfig_YAMLRoundTrip asserts the renderer emits structurally
// valid YAML for a maximal spec combining every field set PRs #45 and #46
// introduced. Catches indentation drift from future template edits.
func TestGenerateConfig_YAMLRoundTrip(t *testing.T) {
	dur := 900
	pos := 3
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset:  150,
			TACommon:             0,
			NTNUlSyncValidityDur: &dur,
			PayloadType:          "regenerative",
			Polarization:         &ntnv1alpha1.NTNPolarization{DL: "rhcp", UL: "lhcp"},
			TAInfo: &ntnv1alpha1.TAInfo{
				TACommon:             1000,
				TACommonDrift:        50,
				TACommonDriftVariant: 10,
				TACommonOffset:       200,
			},
			EpochTime:           &ntnv1alpha1.EpochTime{SFN: 512, SubframeNumber: 5},
			EphemerisECEF:       &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3, VelX: 10, VelY: 20, VelZ: 30},
			MovingRefLocation:   &ntnv1alpha1.MovingRefLocation{Latitude: 248500, Longitude: 1210000},
			SatSwitchWithResync: &ntnv1alpha1.SatSwitchWithResync{
				NTNConfig: ntnv1alpha1.SatSwitchNTNConfig{
					EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
				},
			},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{PhysicalCellID: 7, Frequency: 632628},
				{PhysicalCellID: 42},
			},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			PdschMaxHarqRetxs:    2,
			PrachMaxMsg3HarqRetx: 1,
			RrcGuardTimeMs:       25600,
			SIBSchedule: &ntnv1alpha1.SIBSchedule{
				SIWindowLength:   20,
				SIPeriod:         32,
				SIWindowPosition: &pos,
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := k8syaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("rendered output is not valid YAML: %v\n\n%s", err, data)
	}
	cellCfg, ok := parsed["cell_cfg"].(map[string]any)
	if !ok {
		t.Errorf("round-trip parse missing cell_cfg map: %s", data)
	}
	if _, ok := cellCfg["ntn"]; !ok {
		t.Errorf("round-trip parse missing cell_cfg.ntn key: %s", data)
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected YAML to contain %q, got:\n%s", needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected YAML to NOT contain %q, got:\n%s", needle, haystack)
	}
}
