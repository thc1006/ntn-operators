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

func TestPropagateToECEF_LEO_PositionInRange(t *testing.T) {
	omm := oneWebOMM()
	epoch, _ := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)
	propagateTime := epoch.Add(30 * time.Minute)

	ecef, err := PropagateToECEF(omm, propagateTime)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	// OneWeb LEO orbit ~1200 km altitude → radius ~7571 km
	// In 3GPP units (1.3m step): 7571 km ≈ 5,823,846 units
	// Vector magnitude should be in the ballpark.
	posMag := math.Sqrt(float64(ecef.PosX*ecef.PosX + ecef.PosY*ecef.PosY + ecef.PosZ*ecef.PosZ))
	posKm := posMag * 1.3 / 1000.0

	if posKm < 6000 || posKm > 8500 {
		t.Errorf("LEO position magnitude out of range: %.0f km (expected 6000-8500)", posKm)
	}

	// Velocity should be non-zero for LEO (~7.5 km/s)
	velMag := math.Sqrt(float64(ecef.VelX*ecef.VelX + ecef.VelY*ecef.VelY + ecef.VelZ*ecef.VelZ))
	velKmS := velMag * 0.06 / 1000.0

	if velKmS < 5.0 || velKmS > 10.0 {
		t.Errorf("LEO velocity magnitude out of range: %.2f km/s (expected 5-10)", velKmS)
	}
}

func TestPropagateToECEF_GEO_LowVelocity(t *testing.T) {
	omm := geoOMM()
	epoch, _ := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)
	propagateTime := epoch.Add(1 * time.Hour)

	ecef, err := PropagateToECEF(omm, propagateTime)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	// GEO orbit ~35786 km altitude → radius ~42157 km
	posMag := math.Sqrt(float64(ecef.PosX*ecef.PosX + ecef.PosY*ecef.PosY + ecef.PosZ*ecef.PosZ))
	posKm := posMag * 1.3 / 1000.0

	if posKm < 35000 || posKm > 50000 {
		t.Errorf("GEO position magnitude out of range: %.0f km (expected 35000-50000)", posKm)
	}

	// GEO velocity in ECEF should be very low (satellite is stationary relative to Earth)
	// In ECI it's ~3 km/s, but ECEF subtracts Earth rotation → near-zero for true GEO
	velMag := math.Sqrt(float64(ecef.VelX*ecef.VelX + ecef.VelY*ecef.VelY + ecef.VelZ*ecef.VelZ))
	velKmS := velMag * 0.06 / 1000.0

	if velKmS > 1.0 {
		t.Errorf("GEO ECEF velocity too high: %.2f km/s (expected near 0 for true GEO)", velKmS)
	}
}

func TestPropagateToECEF_FitsIn3GPPRange(t *testing.T) {
	omm := oneWebOMM()
	epoch, _ := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	const maxPos = 67108863
	const minPos = -67108864
	for _, v := range []struct {
		name string
		val  int
	}{
		{"PosX", ecef.PosX}, {"PosY", ecef.PosY}, {"PosZ", ecef.PosZ},
	} {
		if v.val < minPos || v.val > maxPos {
			t.Errorf("%s = %d out of 3GPP range [%d, %d]", v.name, v.val, minPos, maxPos)
		}
	}
}

func TestPropagateToECEF_DifferentTimesYieldDifferentPositions(t *testing.T) {
	omm := oneWebOMM()
	epoch, _ := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)

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

func TestPropagateToECEF_ReturnsCorrectType(t *testing.T) {
	omm := oneWebOMM()
	epoch, _ := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)

	ecef, err := PropagateToECEF(omm, epoch)
	if err != nil {
		t.Fatalf("PropagateToECEF failed: %v", err)
	}

	// Verify non-nil and fields are populated
	if ecef.PosX == 0 && ecef.PosY == 0 && ecef.PosZ == 0 {
		t.Error("all position fields are zero — propagation likely failed")
	}
}
