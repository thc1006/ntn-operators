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
	ecefX, ecefY, ecefZ := rotateZ(eci.Position.X, eci.Position.Y, eci.Position.Z, gmst)
	ecefVX, ecefVY, ecefVZ := temeVelToECEF(
		ecefX, ecefY,
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
		if v.val < ntnv1alpha1.ECEFPosMin || v.val > ntnv1alpha1.ECEFPosMax {
			return nil, fmt.Errorf(
				"ECEF %s = %d exceeds 3GPP range [%d, %d]",
				v.name, v.val, ntnv1alpha1.ECEFPosMin, ntnv1alpha1.ECEFPosMax,
			)
		}
	}

	return result, nil
}

// rotateZ applies a Z-axis rotation by angle gmst (radians).
// Used for both TEME→ECEF position and velocity rotation.
func rotateZ(x, y, z, gmst float64) (float64, float64, float64) {
	cosG := math.Cos(gmst)
	sinG := math.Sin(gmst)
	return cosG*x + sinG*y, -sinG*x + cosG*y, z
}

// temeVelToECEF converts TEME velocity to ECEF velocity.
// v_ecef = R(gmst) * v_teme - omega_e x r_ecef
// where omega_e = [0, 0, 7.2921150e-5] rad/s
//
// ecefX/ecefY are the already-rotated position components from rotateZ,
// avoiding redundant trig computation.
func temeVelToECEF(ecefX, ecefY, vxTEME, vyTEME, vzTEME, gmst float64) (float64, float64, float64) {
	// Rotate velocity TEME → ECEF
	vxRot, vyRot, vzRot := rotateZ(vxTEME, vyTEME, vzTEME, gmst)

	// Subtract Earth rotation: omega_e x r_ecef
	// cross product [0, 0, omega_e] x [x, y, z] = [-omega_e*y, omega_e*x, 0]
	const omegaE = 7.2921150e-5 // rad/s
	return vxRot + omegaE*ecefY, vyRot - omegaE*ecefX, vzRot
}

// kmToPos converts km to 3GPP position integer (1 LSB = ECEFPositionStep m).
func kmToPos(km float64) int {
	return int(math.Round(km * 1000.0 / ntnv1alpha1.ECEFPositionStep))
}

// kmsToVel converts km/s to 3GPP velocity integer (1 LSB = ECEFVelocityStep m/s).
func kmsToVel(kms float64) int {
	return int(math.Round(kms * 1000.0 / ntnv1alpha1.ECEFVelocityStep))
}
