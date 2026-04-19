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
	"fmt"
	"math"
	"time"

	"github.com/akhenakh/sgp4"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// 3GPP TS 38.331 ECEF scaling factors and range for ntn-Config-r17.
const (
	// positionStep is the position quantization step in metres (1.3 m per LSB).
	positionStep = 1.3
	// velocityStep is the velocity quantization step in m/s (0.06 m/s per LSB).
	velocityStep = 0.06
	// maxECEFPos is the maximum 3GPP ECEF position value (2^26 - 1).
	maxECEFPos = 67108863
	// minECEFPos is the minimum 3GPP ECEF position value (-2^26).
	minECEFPos = -67108864
)

// PropagateToECEF propagates an OMM orbital element set to the given UTC time
// using SGP4, converts the result from TEME to ECEF coordinates, and returns
// a 3GPP TS 38.331 quantized EphemerisECEF suitable for NTNCellConfig.
//
// The SGP4 propagator outputs position/velocity in TEME (True Equator Mean
// Equinox) frame in km and km/s. This function applies a GMST-based rotation
// to convert to ECEF, then quantizes to 3GPP integer units:
//   - Position: 1 LSB = 1.3 metres
//   - Velocity: 1 LSB = 0.06 m/s
func PropagateToECEF(omm sgp4.OMM, t time.Time) (*ntnv1alpha1.EphemerisECEF, error) {
	tle, err := omm.ToTLE()
	if err != nil {
		return nil, fmt.Errorf("OMM to TLE conversion: %w", err)
	}

	eci, err := tle.FindPositionAtTime(t)
	if err != nil {
		return nil, fmt.Errorf("SGP4 propagation: %w", err)
	}

	// Convert TEME → ECEF via GMST rotation.
	gmst := eci.GreenwichSiderealTime()
	ecefX, ecefY, ecefZ := temeToECEF(eci.Position.X, eci.Position.Y, eci.Position.Z, gmst)
	ecefVX, ecefVY, ecefVZ := temeVelToECEF(
		eci.Position.X, eci.Position.Y,
		eci.Velocity.X, eci.Velocity.Y, eci.Velocity.Z,
		gmst,
	)

	// Quantize to 3GPP integer units.
	result := &ntnv1alpha1.EphemerisECEF{
		PosX: kmToPos(ecefX),
		PosY: kmToPos(ecefY),
		PosZ: kmToPos(ecefZ),
		VelX: kmsToVel(ecefVX),
		VelY: kmsToVel(ecefVY),
		VelZ: kmsToVel(ecefVZ),
	}

	// Validate against 3GPP range.
	for _, v := range []struct {
		name string
		val  int
	}{
		{"posX", result.PosX}, {"posY", result.PosY}, {"posZ", result.PosZ},
	} {
		if v.val < minECEFPos || v.val > maxECEFPos {
			return nil, fmt.Errorf("ECEF %s = %d exceeds 3GPP range [%d, %d]", v.name, v.val, minECEFPos, maxECEFPos)
		}
	}

	return result, nil
}

// temeToECEF rotates a TEME position vector to ECEF using GMST.
// Input: position in km (TEME), gmst in radians.
// Output: position in km (ECEF).
func temeToECEF(xTEME, yTEME, zTEME, gmst float64) (float64, float64, float64) {
	cosG := math.Cos(gmst)
	sinG := math.Sin(gmst)
	x := cosG*xTEME + sinG*yTEME
	y := -sinG*xTEME + cosG*yTEME
	z := zTEME // Z-axis is the same in TEME and ECEF
	return x, y, z
}

// temeVelToECEF converts TEME velocity to ECEF velocity.
// Must also account for Earth rotation rate (omega_e).
// v_ecef = R(gmst) * v_teme - omega_e x r_ecef
// where omega_e = [0, 0, 7.2921150e-5] rad/s
func temeVelToECEF(xTEME, yTEME, vxTEME, vyTEME, vzTEME, gmst float64) (float64, float64, float64) {
	cosG := math.Cos(gmst)
	sinG := math.Sin(gmst)

	// Rotate velocity TEME → ECEF
	vxRot := cosG*vxTEME + sinG*vyTEME
	vyRot := -sinG*vxTEME + cosG*vyTEME
	vzRot := vzTEME

	// Subtract Earth rotation contribution: omega_e x r_ecef
	// omega_e = 7.2921150e-5 rad/s, r in km → need km/s
	const omegaE = 7.2921150e-5 // rad/s
	xECEF := cosG*xTEME + sinG*yTEME
	yECEF := -sinG*xTEME + cosG*yTEME

	// cross product [0, 0, omega_e] x [x, y, z] = [-omega_e*y, omega_e*x, 0]
	vx := vxRot - (-omegaE * yECEF)
	vy := vyRot - (omegaE * xECEF)
	vz := vzRot

	return vx, vy, vz
}

// kmToPos converts km to 3GPP position integer (1 LSB = 1.3 m).
func kmToPos(km float64) int {
	return int(math.Round(km * 1000.0 / positionStep))
}

// kmsToVel converts km/s to 3GPP velocity integer (1 LSB = 0.06 m/s).
func kmsToVel(kms float64) int {
	return int(math.Round(kms * 1000.0 / velocityStep))
}
