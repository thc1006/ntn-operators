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
	"testing"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

func benchSpec() *ntnv1alpha1.NTNCellConfigSpec {
	return &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			TACommon:            0,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 20922195,
				PosY: 1967783,
				PosZ: 19770302,
			},
			PayloadType: "transparent",
			TAInfo: &ntnv1alpha1.TAInfo{
				TACommon:      58629666,
				TACommonDrift: 100,
			},
			NeighborCells: []ntnv1alpha1.NTNNeighborCell{
				{PhysicalCellID: 101, Frequency: 437000},
				{PhysicalCellID: 102, Frequency: 437100},
			},
		},
	}
}

func BenchmarkGenerateConfig_Minimal(b *testing.B) {
	spec := &ntnv1alpha1.NTNCellConfigSpec{
		NTN: ntnv1alpha1.NTNParams{
			CellSpecificKoffset: 150,
			EphemerisECEF: &ntnv1alpha1.EphemerisECEF{
				PosX: 20922195, PosY: 1967783, PosZ: 19770302,
			},
			PayloadType: "transparent",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := GenerateConfig(spec)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateConfig_Full(b *testing.B) {
	spec := benchSpec()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := GenerateConfig(spec)
		if err != nil {
			b.Fatal(err)
		}
	}
}
