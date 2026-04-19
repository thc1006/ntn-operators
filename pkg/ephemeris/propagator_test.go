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

package ephemeris

import (
	"math"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

const epochLayout = "2006-01-02T15:04:05.000000"

// parseEpoch parses an OMM epoch string, failing the test on error.
func parseEpoch(t *testing.T, s string) time.Time {
	t.Helper()
	epoch, err := time.Parse(epochLayout, s)
	if err != nil {
		t.Fatalf("failed to parse epoch %q: %v", s, err)
	}
	return epoch
}

// 3GPP TS 38.331 spec values — hardcoded in tests as independent check
// against the exported constants in api/v1alpha1.
const (
	specPositionStep = 1.3
	specVelocityStep = 0.06
	specECEFPosMax   = 67108863
	specECEFPosMin   = -67108864
)

// posMagnitudeKm computes the ECEF position vector magnitude in km.
func posMagnitudeKm(px, py, pz int) float64 {
	x, y, z := float64(px), float64(py), float64(pz)
	return math.Sqrt(x*x+y*y+z*z) * specPositionStep / 1000.0
}

// velMagnitudeKmS computes the ECEF velocity vector magnitude in km/s.
func velMagnitudeKmS(vx, vy, vz int) float64 {
	x, y, z := float64(vx), float64(vy), float64(vz)
	return math.Sqrt(x*x+y*y+z*z) * specVelocityStep / 1000.0
}

// OneWeb satellite OMM (NORAD 56700) — known test vector.
func oneWebOMM() sgp4.OMM {
	return sgp4.OMM{
		ObjectName:      "ONEWEB-0569",
		ObjectID:        "2023-068A",
		NoradCatID:      56700,
		EpochStr:        "2026-04-01T12:00:00.000000",
		MeanMotion:      12.85,
		Eccentricity:    0.0012,
		Inclination:     87.9,
		RAOfAscNode:     120.5,
		ArgOfPericenter: 90.2,
		MeanAnomaly:     270.0,
		BStar:           0.0001,
		MeanMotionDot:   0.0,
		MeanMotionDDot:  0.0,
	}
}

// GEO satellite OMM — geostationary orbit (nearly zero velocity in ECEF).
func geoOMM() sgp4.OMM {
	return sgp4.OMM{
		ObjectName:      "INTELSAT-10",
		ObjectID:        "2004-022A",
		NoradCatID:      28358,
		EpochStr:        "2026-04-01T00:00:00.000000",
		MeanMotion:      1.00272,
		Eccentricity:    0.0003,
		Inclination:     0.05,
		RAOfAscNode:     75.0,
		ArgOfPericenter: 0.0,
		MeanAnomaly:     0.0,
		BStar:           0.0,
		MeanMotionDot:   0.0,
		MeanMotionDDot:  0.0,
	}
}

// TestPropagateToECEF_GoldenVector verifies exact ECEF output at epoch time
// against precomputed reference values. Catches sign errors, axis swaps,
// and GMST rotation direction mistakes that magnitude-only checks would miss.
func TestPropagateToECEF_GoldenVector(t *testing.T) {
	omm := oneWebOMM()
	epoch := parseEpoch(t, omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	// Reference values generated from this implementation at epoch.
	// If these change, the TEME→ECEF rotation or quantization has regressed.
	want := struct{ PosX, PosY, PosZ, VelX, VelY, VelZ int }{
		PosX: -2088154, PosY: 5544785, PosZ: -5042,
		VelX: 4706, VelY: 1603, VelZ: 119837,
	}

	if ecef.PosX != want.PosX || ecef.PosY != want.PosY || ecef.PosZ != want.PosZ {
		t.Errorf("position mismatch:\n  got  (%d, %d, %d)\n  want (%d, %d, %d)",
			ecef.PosX, ecef.PosY, ecef.PosZ, want.PosX, want.PosY, want.PosZ)
	}
	if ecef.VelX != want.VelX || ecef.VelY != want.VelY || ecef.VelZ != want.VelZ {
		t.Errorf("velocity mismatch:\n  got  (%d, %d, %d)\n  want (%d, %d, %d)",
			ecef.VelX, ecef.VelY, ecef.VelZ, want.VelX, want.VelY, want.VelZ)
	}
}

func TestPropagateToECEF_LEO_PositionInRange(t *testing.T) {
	omm := oneWebOMM()
	epoch := parseEpoch(t, omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	posKm := posMagnitudeKm(ecef.PosX, ecef.PosY, ecef.PosZ)
	if posKm < 6000 || posKm > 8500 {
		t.Errorf("LEO position magnitude out of range: %.0f km (expected 6000-8500)", posKm)
	}

	velKmS := velMagnitudeKmS(ecef.VelX, ecef.VelY, ecef.VelZ)
	if velKmS < 5.0 || velKmS > 10.0 {
		t.Errorf("LEO velocity magnitude out of range: %.2f km/s (expected 5-10)", velKmS)
	}
}

func TestPropagateToECEF_GEO_LowVelocity(t *testing.T) {
	omm := geoOMM()
	epoch := parseEpoch(t, omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	posKm := posMagnitudeKm(ecef.PosX, ecef.PosY, ecef.PosZ)
	if posKm < 35000 || posKm > 50000 {
		t.Errorf("GEO position magnitude out of range: %.0f km (expected 35000-50000)", posKm)
	}

	velKmS := velMagnitudeKmS(ecef.VelX, ecef.VelY, ecef.VelZ)
	if velKmS > 1.0 {
		t.Errorf("GEO ECEF velocity too high: %.2f km/s (expected near 0 for true GEO)", velKmS)
	}
}

func TestPropagateToECEF_FitsIn3GPPRange(t *testing.T) {
	omm := oneWebOMM()
	epoch := parseEpoch(t, omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	for _, v := range []struct {
		name string
		val  int
	}{
		{"PosX", ecef.PosX}, {"PosY", ecef.PosY}, {"PosZ", ecef.PosZ},
	} {
		if v.val < specECEFPosMin || v.val > specECEFPosMax {
			t.Errorf("%s = %d out of 3GPP range [%d, %d]", v.name, v.val, specECEFPosMin, specECEFPosMax)
		}
	}
}

func TestPropagateToECEF_DifferentTimesYieldDifferentPositions(t *testing.T) {
	omm := oneWebOMM()
	epoch := parseEpoch(t, omm.EpochStr)

	ecef1, err := PropagateToECEF(omm, epoch)
	if err != nil {
		t.Fatalf("first propagation failed: %v", err)
	}

	ecef2, err := PropagateToECEF(omm, epoch.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("second propagation failed: %v", err)
	}

	if ecef1.PosX == ecef2.PosX && ecef1.PosY == ecef2.PosY && ecef1.PosZ == ecef2.PosZ {
		t.Error("LEO satellite position should change over 10 minutes")
	}
}

func TestPropagateToECEF_NonZeroResult(t *testing.T) {
	omm := oneWebOMM()
	epoch := parseEpoch(t, omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	if ecef.PosX == 0 && ecef.PosY == 0 && ecef.PosZ == 0 {
		t.Error("all position fields are zero — propagation likely failed")
	}
}
