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
			EphemerisECEF: ntnv1alpha1.EphemerisECEF{
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

	// Verify NTN section.
	assertContains(t, yaml, "cell_specific_koffset: 150")
	assertContains(t, yaml, "ta_common: 0")
	assertContains(t, yaml, "pos_x: 20922195")
	assertContains(t, yaml, "pos_y: 1967783")
	assertContains(t, yaml, "pos_z: 19770302")
	assertContains(t, yaml, "vel_x: 0")
	assertContains(t, yaml, "vel_y: 0")
	assertContains(t, yaml, "vel_z: 0")

	// Verify cell_cfg defaults for NTN.
	assertContains(t, yaml, "sr_period_ms: 320")
	assertContains(t, yaml, "max_nof_harq_retxs: 0")
	assertContains(t, yaml, "rrc_procedure_guard_time_ms: 12800")
}

func TestGenerateConfig_CustomKoffset(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 500,
			TACommon:            1000,
			EphemerisECEF:       ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	assertContains(t, yaml, "cell_specific_koffset: 500")
	assertContains(t, yaml, "ta_common: 1000")
}

func TestGenerateConfig_CustomCellOverrides(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF:       ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
		CellOverrides: &ntnv1alpha1.CellOverrides{
			PucchSRPeriodMs:      640,
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
	assertContains(t, yaml, "sr_period_ms: 640")
	assertContains(t, yaml, "max_nof_harq_retxs: 2")
	assertContains(t, yaml, "max_msg3_harq_retx: 1")
	assertContains(t, yaml, "rrc_procedure_guard_time_ms: 25600")
}

func TestGenerateConfig_LEOWithVelocity(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 50,
			EphemerisECEF: ntnv1alpha1.EphemerisECEF{
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

func TestGenerateConfig_SIB19Scheduling(t *testing.T) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			EphemerisECEF: ntnv1alpha1.EphemerisECEF{PosX: 1, PosY: 2, PosZ: 3},
		},
	}

	data, err := GenerateConfig(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yaml := string(data)
	// SIB19 must always be scheduled for NTN.
	assertContains(t, yaml, "sib_mapping:")
	assertContains(t, yaml, "19")
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected YAML to contain %q, got:\n%s", needle, haystack)
	}
}
