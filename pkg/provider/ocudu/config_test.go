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
	assertContains(t, yaml, "pos_x: 20922195")
	assertContains(t, yaml, "pos_y: 1967783")
	assertContains(t, yaml, "pos_z: 19770302")
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
	assertContains(t, yaml, "ta_common: 1000")
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
	assertContains(t, yaml, "vel_x: 100")
	assertContains(t, yaml, "vel_y: -200")
	assertContains(t, yaml, "vel_z: 50")
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
	assertContains(t, yaml, "eccentricity: 1")
	assertContains(t, yaml, "inclination: 879000")
	assertContains(t, yaml, "right_ascension: 1000000")
	assertContains(t, yaml, "arg_of_periapsis: 900000")
	assertContains(t, yaml, "mean_anomaly: 2700000")
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
	assertContains(t, yaml, "ta_common: 1000")
	assertContains(t, yaml, "ta_common_drift: 50")
	assertContains(t, yaml, "ta_common_drift_variant: 10")
	assertContains(t, yaml, "ta_common_offset: 200")
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
	assertContains(t, yaml, "feeder_link_info:")
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
	assertContains(t, yaml, "ntn_gateway_location:")
	assertContains(t, yaml, "latitude: 248500")
	assertContains(t, yaml, "longitude: 1210000")
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
	assertContains(t, yaml, "latitude: 248500")
	assertContains(t, yaml, "longitude: 1210000")
}

func TestGenerateConfig_SatSwitchWithResync(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			SatSwitchWithResync: &ntnv1alpha1.SatSwitchWithResync{
				TargetPCI: 100,
				T304:      150,
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "sat_switch_with_resync:")
	assertContains(t, yaml, "target_pci: 100")
	assertContains(t, yaml, "t304: 150")
}

func TestGenerateConfig_NilSpec(t *testing.T) {
	_, err := GenerateConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestGenerateConfig_MatchesLiveGNBFormat(t *testing.T) {
	// This test generates config and validates it matches the exact format
	// that was verified to work with srsRAN gNB (commit 4bf1543).
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

	// These exact strings were verified against a live srsRAN gNB that
	// successfully started, connected to AMF, and broadcast NTN cell.
	requiredFragments := []string{
		"ntn:",
		"  cell_specific_koffset: 150",
		"  ta_info:",
		"    ta_common: 0",
		"  ephemeris_info_ecef:",
		"    pos_x: 20922195",
		"cell_cfg:",
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
	assertContains(t, yaml, "phys_cell_id: 100")
	assertContains(t, yaml, "frequency: 632628")
	assertContains(t, yaml, "phys_cell_id: 200")
	// Second cell has no frequency — must not emit frequency line.
	// Count occurrences of "frequency:" — should be exactly 1.
	if strings.Count(yaml, "frequency:") != 1 {
		t.Errorf(
			"expected exactly 1 frequency line, got %d\n%s",
			strings.Count(yaml, "frequency:"), yaml,
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
	assertContains(t, yaml, "latitude: 248500")
	assertContains(t, yaml, "longitude: 1210000")
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
	assertContains(t, yaml, "distance_threshold: 5000")
	assertContains(t, yaml, "t_service: 600")
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

func TestGenerateConfig_NeighborReselectionInfo(t *testing.T) {
	qhyst := 4
	qoff := -3
	sintra := 12
	threshLow := 8
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{
					PhysicalCellID: 42,
					Frequency:      632628,
					ReselectionInfo: &ntnv1alpha1.NeighborReselectionInfo{
						QHyst:             &qhyst,
						QOffsetCell:       &qoff,
						SIntraSearchP:     &sintra,
						ThreshServingLowP: &threshLow,
					},
				},
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "reselection_info:")
	assertContains(t, yaml, "q_hyst: 4")
	assertContains(t, yaml, "q_offset_cell: -3")
	assertContains(t, yaml, "s_intra_search_p: 12")
	assertContains(t, yaml, "thresh_serving_low_p: 8")
}

func TestGenerateConfig_NeighborReselectionInfoPartial(t *testing.T) {
	qhyst := 2
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{
					PhysicalCellID:  7,
					Frequency:       632628,
					ReselectionInfo: &ntnv1alpha1.NeighborReselectionInfo{QHyst: &qhyst},
				},
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)
	assertContains(t, yaml, "reselection_info:")
	assertContains(t, yaml, "q_hyst: 2")
	assertNotContains(t, yaml, "q_offset_cell:")
	assertNotContains(t, yaml, "s_intra_search_p:")
	assertNotContains(t, yaml, "thresh_serving_low_p:")
}

func TestGenerateConfig_NeighborReselectionInfoOmittedWhenNil(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{PhysicalCellID: 42, Frequency: 632628},
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNotContains(t, string(data), "reselection_info:")
}

// TestGenerateConfig_TS38331Conformance maps rendered YAML fragments back to
// their TS 38.331 IE origin so future SIB19 and idle-mode reselection
// additions (SIB2/SIB3/SIB4-derived IEs) extend by appending a row. SIB11 is
// intentionally excluded — upstream OCUDU has no SIB11 surface; see
// docs/adr/0001-sib11-measurement-config.md. Fragments are presence-only where
// the exact value is already covered by a dedicated test, keeping this suite
// stable as IE serializations evolve.
func TestGenerateConfig_TS38331Conformance(t *testing.T) {
	dur := 900
	qhyst := 2
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset:  150,
			TACommon:             0,
			NTNUlSyncValidityDur: &dur,
			Polarization:         &ntnv1alpha1.NTNPolarization{DL: "rhcp", UL: "lhcp"},
			TAInfo: &ntnv1alpha1.TAInfo{
				TACommon:      0,
				TACommonDrift: 1,
			},
			EpochTime: &ntnv1alpha1.EpochTime{SFN: 0, SubframeNumber: 0},
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 20922195, PosY: 1967783, PosZ: 19770302,
			},
			MovingRefLocation:   &ntnv1alpha1.MovingRefLocation{Latitude: 248500, Longitude: 1210000},
			SatSwitchWithResync: &ntnv1alpha1.SatSwitchWithResync{TargetPCI: 1, T304: 100},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{
					PhysicalCellID:  7,
					Frequency:       632628,
					ReselectionInfo: &ntnv1alpha1.NeighborReselectionInfo{QHyst: &qhyst},
				},
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	yaml := string(data)

	cases := []struct {
		name    string
		ieRef   string
		present string
	}{
		{"cellSpecificKoffset-r17", "NTN-Config-r17", "cell_specific_koffset: 150"},
		{"ntn-UlSyncValidityDuration-r17", "NTN-Config-r17", "ntn_ul_sync_validity_dur: 900"},
		{"ntn-PolarizationDL/UL-r17", "NTN-Config-r17", "polarization:"},
		{"ta-Common-r17", "TA-Info-r17", "ta_common: 0"},
		{"ta-CommonDrift-r17", "TA-Info-r17", "ta_common_drift: 1"},
		{"epoch-Time-r17", "EpochTime-r17", "epoch_time:"},
		{"ephemerisInfo-r17 (ECEF)", "EphemerisInfo-r17", "ephemeris_info_ecef:"},
		{"ephemeris pos_x", "EphemerisInfo-r17", "pos_x: 20922195"},
		{"movingRefLocation-r18", "SIB19-v1800", "moving_ref_location:"},
		{"satSwitchWithResync-r18", "SIB19-v1800", "sat_switch_with_resync:"},
		{"SIB19 scheduling", "SI-SchedulingInfo", "sib_mapping: 19"},
		{"q-Hyst (SIB2 cellReselectionInfoCommon)", "IntraFreqCellReselectionInfo", "q_hyst: 2"},
		{"reselection_info block (SIB2/SIB3)", "IntraFreqCellReselectionInfo", "reselection_info:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(yaml, tc.present) {
				t.Errorf("TS 38.331 %s (%s) expected fragment %q missing from output:\n%s",
					tc.ieRef, tc.name, tc.present, yaml)
			}
		})
	}
}

// TestGenerateConfig_YAMLRoundTrip asserts the renderer emits structurally
// valid YAML for a maximal spec combining all new fields from PRs #45-#47.
// Catches indentation drift from future template edits.
func TestGenerateConfig_YAMLRoundTrip(t *testing.T) {
	dur := 900
	qhyst := 2
	qoff := -3
	sintra := 12
	threshLow := 8
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
			SatSwitchWithResync: &ntnv1alpha1.SatSwitchWithResync{TargetPCI: 1, T304: 100},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{
					PhysicalCellID: 7,
					Frequency:      632628,
					ReselectionInfo: &ntnv1alpha1.NeighborReselectionInfo{
						QHyst:             &qhyst,
						QOffsetCell:       &qoff,
						SIntraSearchP:     &sintra,
						ThreshServingLowP: &threshLow,
					},
				},
				{PhysicalCellID: 42},
			},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			PdschMaxHarqRetxs:    2,
			PrachMaxMsg3HarqRetx: 1,
			RrcGuardTimeMs:       25600,
			SIBSchedule: &ntnv1alpha1.SIBSchedule{
				SIWindowLength: 20,
				SIPeriod:       32,
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
	if _, ok := parsed["ntn"]; !ok {
		t.Errorf("round-trip parse missing ntn key: %s", data)
	}
	if _, ok := parsed["cell_cfg"]; !ok {
		t.Errorf("round-trip parse missing cell_cfg key: %s", data)
	}
}

// TestGenerateConfig_PolarizationStringInjectionResistant verifies the
// renderer does not allow a crafted polarization.dl/ul string to inject
// sibling YAML keys even if apiserver enum validation were bypassed. This
// is defense-in-depth — the normal path is the CRD Enum marker.
func TestGenerateConfig_PolarizationStringInjectionResistant(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization: &ntnv1alpha1.NTNPolarization{
				DL: "rhcp\n    forbidden_key: attacker_value",
			},
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := k8syaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("injection corrupted YAML structure: %v\n\n%s", err, data)
	}
	ntn, _ := parsed["ntn"].(map[string]any)
	if _, leaked := ntn["forbidden_key"]; leaked {
		t.Errorf("string injection leaked 'forbidden_key' into ntn map:\n%s", data)
	}
	if pol, ok := ntn["polarization"].(map[string]any); ok {
		if _, leaked := pol["forbidden_key"]; leaked {
			t.Errorf("string injection leaked 'forbidden_key' into polarization map:\n%s", data)
		}
		if dlStr, _ := pol["dl"].(string); !strings.Contains(dlStr, "forbidden_key") {
			t.Errorf("expected dl to round-trip the literal payload, got %q", dlStr)
		}
	}
}

// TestGenerateConfig_PayloadTypeStringInjectionResistant applies the same
// defense-in-depth check to PayloadType, which also interpolates a
// user-controlled string into the template.
func TestGenerateConfig_PayloadTypeStringInjectionResistant(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			PayloadType:   "transparent\nforbidden_top_level: attacker",
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := k8syaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("injection corrupted YAML structure: %v\n\n%s", err, data)
	}
	if _, leaked := parsed["forbidden_top_level"]; leaked {
		t.Errorf("payloadType string injection leaked top-level key:\n%s", data)
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
