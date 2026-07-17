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
	"context"
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

func benchOMM() sgp4.OMM {
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
	}
}

func BenchmarkPropagateToECEF(b *testing.B) {
	omm := benchOMM()
	t, err := time.Parse("2006-01-02T15:04:05.000000", omm.EpochStr)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := PropagateToECEF(omm, t.Add(time.Duration(i)*time.Minute))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPredictPasses_SingleSatSingleStation(b *testing.B) {
	omm := benchOMM()
	stations := []GroundStation{{
		Name:      "taipei",
		Latitude:  25.033,
		Longitude: 121.565,
		Altitude:  15,
	}}
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	horizon := 24 * time.Hour

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := PredictPasses(context.Background(),
			[]sgp4.OMM{omm}, stations, 10.0, horizon, nil, start,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPredictPasses_10Sats2Stations(b *testing.B) {
	omms := make([]sgp4.OMM, 10)
	for i := range omms {
		omm := benchOMM()
		omm.NoradCatID = 56700 + i
		omm.MeanAnomaly = float64(i * 36) // spread across orbit
		omms[i] = omm
	}
	stations := []GroundStation{
		{Name: "taipei", Latitude: 25.033, Longitude: 121.565, Altitude: 15},
		{Name: "hsinchu", Latitude: 24.796, Longitude: 120.997, Altitude: 50},
	}
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := PredictPasses(context.Background(),
			omms, stations, 10.0, 24*time.Hour, nil, start,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPredictPasses_7d_1Sat1Station and _7d_10Sats2Stations measure the WORST-CASE horizon
// (the 7-day maxPassHorizon clamp). Because ctx cancellation is honoured only BETWEEN work items,
// a single item's wall-clock cost is the upper bound on how long a cancelled PredictPasses can
// take to return, so these numbers justify the coarse-grained cancellation design.
func BenchmarkPredictPasses_7d_1Sat1Station(b *testing.B) {
	omm := benchOMM()
	stations := []GroundStation{{Name: "taipei", Latitude: 25.033, Longitude: 121.565, Altitude: 15}}
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := PredictPasses(context.Background(), []sgp4.OMM{omm}, stations, 10.0, 7*24*time.Hour, nil, start)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPredictPasses_7d_10Sats2Stations(b *testing.B) {
	omms := make([]sgp4.OMM, 10)
	for i := range omms {
		omm := benchOMM()
		omm.NoradCatID = 56700 + i
		omm.MeanAnomaly = float64(i * 36)
		omms[i] = omm
	}
	stations := []GroundStation{
		{Name: "taipei", Latitude: 25.033, Longitude: 121.565, Altitude: 15},
		{Name: "hsinchu", Latitude: 24.796, Longitude: 120.997, Altitude: 50},
	}
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PredictPasses(context.Background(), omms, stations, 10.0, 7*24*time.Hour, nil, start); err != nil {
			b.Fatal(err)
		}
	}
}
