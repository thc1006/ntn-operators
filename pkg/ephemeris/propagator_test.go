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
	specECEFPosMax   = 33554431  // 3GPP TS 38.331 positionX-r17 max: 2^25 - 1
	specECEFPosMin   = -33554432 // 3GPP TS 38.331 positionX-r17 min: -2^25
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

	// These ECEF values are self-generated: this test is a change detector for the
	// TEME→ECEF rotation direction and 3GPP quantization. The underlying SGP4
	// propagation is independently verified against Vallado's SGP4-VER reference in
	// TestPropagateSGP4_ValladoReference (findings.md I-16).
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

// valladoRefPoint is one TEME state-vector sample from Vallado's SGP4-VER reference
// output (tsince in minutes; position km; velocity km/s).
type valladoRefPoint struct{ tsince, x, y, z, vx, vy, vz float64 }

// TestPropagateSGP4_ValladoReference verifies the SGP4 propagation that PropagateToECEF
// relies on against Vallado's SGP4-VER suite (AIAA 2006-6753, "Revisiting Spacetrack
// Report #3") — reference TEME position/velocity from the published tcppver.out. Unlike
// TestPropagateToECEF_GoldenVector (self-generated ECEF), these vectors come from an
// EXTERNAL authority, so a numerically-wrong-but-stable propagation regression can no
// longer pass CI (findings.md I-16). NORAD 5 is the canonical TEME example; NORAD 88888
// is the original Spacetrack Report #3 object. Both are near-earth (period < 225 min).
//
// Observed agreement of the bundled akhenakh/sgp4 propagator with these vectors is
// ~1e-8 km / ~1e-9 km/s; the tolerances are deliberately loose to stay robust across
// platforms while still catching any real (km-scale) regression.
func TestPropagateSGP4_ValladoReference(t *testing.T) {
	cases := []struct {
		name   string
		l1, l2 string
		pts    []valladoRefPoint
	}{
		{
			name: "NORAD 5 (TEME example)",
			l1:   "1 00005U 58002B   00179.78495062  .00000023  00000-0  28098-4 0  4753",
			l2:   "2 00005  34.2682 348.7242 1859667 331.7664  19.3264 10.82419157413667",
			pts: []valladoRefPoint{
				{0.0, 7022.46529266, -1400.08296755, 0.03995155, 1.893841015, 6.405893759, 4.534807250},
				{360.0, -7154.03120202, -3783.17682504, -3536.19412294, 4.741887409, -4.151817765, -2.093935425},
				{720.0, -7134.59340119, 6531.68641334, 3260.27186483, -4.113793027, -2.911922039, -2.557327851},
				{1440.0, -938.55923943, -6268.18748831, -4294.02924751, 7.536105209, -0.427127707, 0.989878080},
			},
		},
		{
			name: "NORAD 88888 (Spacetrack Report #3)",
			l1:   "1 88888U          80275.98708465  .00073094  13844-3  66816-4 0    87",
			l2:   "2 88888  72.8435 115.9689 0086731  52.6988 110.5714 16.05824518  1058",
			pts: []valladoRefPoint{
				{0.0, 2328.96975262, -5995.22051338, 1719.97297192, 2.912073281, -0.983417956, -7.090816210},
				{120.0, 1020.69234558, 2286.56260634, -6191.55565927, -3.746543902, 6.467532721, 1.827985678},
				{240.0, -3226.54349155, 3503.70977525, 4532.80979343, 1.000992116, -5.788042888, 5.162585826},
				{360.0, 2456.10706533, -6071.93855503, 1222.89768554, 2.679390040, -0.448290811, -7.228792155},
			},
		},
	}
	const posTol, velTol = 1e-4, 1e-6 // km, km/s

	mag := func(a, b, c float64) float64 { return math.Sqrt(a*a + b*b + c*c) }
	for _, c := range cases {
		tle, err := sgp4.ParseTLELines([]string{c.l1, c.l2})
		if err != nil {
			t.Fatalf("%s: ParseTLELines: %v", c.name, err)
		}
		for _, p := range c.pts {
			eci, err := tle.FindPosition(p.tsince)
			if err != nil {
				t.Fatalf("%s @ %.0f min: FindPosition: %v", c.name, p.tsince, err)
			}
			if dp := mag(eci.Position.X-p.x, eci.Position.Y-p.y, eci.Position.Z-p.z); dp > posTol {
				t.Errorf("%s @ %.0f min: TEME position off by %.3e km (tol %.0e)", c.name, p.tsince, dp, posTol)
			}
			if dv := mag(eci.Velocity.X-p.vx, eci.Velocity.Y-p.vy, eci.Velocity.Z-p.vz); dv > velTol {
				t.Errorf("%s @ %.0f min: TEME velocity off by %.3e km/s (tol %.0e)", c.name, p.tsince, dv, velTol)
			}
		}
	}
}
