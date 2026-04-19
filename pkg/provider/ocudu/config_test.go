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

func TestGenerateConfig_Polarization(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
			Polarization:  "circular",
		},
	}
	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, string(data), "polarization: circular")
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
