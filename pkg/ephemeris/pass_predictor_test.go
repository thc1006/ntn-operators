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
	"testing"
	"time"

	"github.com/akhenakh/sgp4"
)

// issOMM is a realistic ISS OMM for testing (matches akhenakh/sgp4 test data).
var issOMM = sgp4.OMM{
	ObjectName:         "ISS (ZARYA)",
	ObjectID:           "1998-067A",
	EpochStr:           "2025-09-04T02:26:38.098656",
	MeanMotion:         15.49554387,
	Eccentricity:       0.000588,
	Inclination:        51.6381,
	RAOfAscNode:        276.7884,
	ArgOfPericenter:    282.5765,
	MeanAnomaly:        192.7824,
	EphemerisType:      0,
	ClassificationType: "U",
	NoradCatID:         25544,
	ElementSetNo:       999,
	RevAtEpoch:         47189,
	BStar:              0.00025892,
	MeanMotionDot:      0.00019394,
	MeanMotionDDot:     0,
}

// onewebOMM is a OneWeb satellite OMM for testing.
var onewebOMM = sgp4.OMM{
	ObjectName:         "ONEWEB-0012",
	ObjectID:           "2019-010A",
	EpochStr:           "2026-04-15T22:27:22.252608",
	MeanMotion:         13.1659366,
	Eccentricity:       0.00021061,
	Inclination:        87.9064,
	RAOfAscNode:        241.1509,
	ArgOfPericenter:    110.8263,
	MeanAnomaly:        249.3093,
	EphemerisType:      0,
	ClassificationType: "U",
	NoradCatID:         44057,
	ElementSetNo:       999,
	RevAtEpoch:         34337,
	BStar:              -0.00034023158,
	MeanMotionDot:      -1.17e-6,
	MeanMotionDDot:     0,
}

var taipeiStation = GroundStation{
	Name:      "gs-taipei-01",
	Latitude:  25.0330,
	Longitude: 121.5654,
	Altitude:  15,
}

var montrealStation = GroundStation{
	Name:      "gs-montreal",
	Latitude:  45.5088,
	Longitude: -73.5878,
	Altitude:  60,
}

func TestPredictPasses_SingleSatellite(t *testing.T) {
	passes, err := PredictPasses(
		[]sgp4.OMM{issOMM},
		[]GroundStation{montrealStation},
		0, // all passes above horizon
		24*time.Hour,
		nil, // no filter
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(passes) == 0 {
		t.Fatal("expected at least one ISS pass over Montreal in 24h")
	}
	for _, p := range passes {
		if p.Satellite != "ISS (ZARYA)" {
			t.Errorf("expected satellite ISS (ZARYA), got %s", p.Satellite)
		}
		if p.GroundStation != "gs-montreal" {
			t.Errorf("expected ground station gs-montreal, got %s", p.GroundStation)
		}
		if p.AOS.After(p.LOS) {
			t.Errorf("AOS (%v) should be before LOS (%v)", p.AOS, p.LOS)
		}
		if p.MaxElevation <= 0 {
			t.Errorf("expected positive max elevation, got %f", p.MaxElevation)
		}
	}
}

func TestPredictPasses_MinElevationFilter(t *testing.T) {
	allPasses, err := PredictPasses(
		[]sgp4.OMM{issOMM},
		[]GroundStation{montrealStation},
		0, // no filter
		24*time.Hour,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	filteredPasses, err := PredictPasses(
		[]sgp4.OMM{issOMM},
		[]GroundStation{montrealStation},
		30, // high threshold
		24*time.Hour,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(filteredPasses) >= len(allPasses) {
		t.Errorf("expected fewer passes with minElevation=30, got %d vs %d", len(filteredPasses), len(allPasses))
	}

	for _, p := range filteredPasses {
		if p.MaxElevation < 30 {
			t.Errorf("pass max elevation %f should be >= 30", p.MaxElevation)
		}
	}
}

func TestPredictPasses_NoradFilter(t *testing.T) {
	omms := []sgp4.OMM{issOMM, onewebOMM}

	// Filter to ISS only.
	passes, err := PredictPasses(
		omms,
		[]GroundStation{montrealStation},
		0,
		24*time.Hour,
		[]int{25544}, // ISS NORAD ID
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, p := range passes {
		if p.Satellite != "ISS (ZARYA)" {
			t.Errorf("expected only ISS passes, got satellite %s", p.Satellite)
		}
	}
}

func TestPredictPasses_MultipleStations(t *testing.T) {
	passes, err := PredictPasses(
		[]sgp4.OMM{issOMM},
		[]GroundStation{montrealStation, taipeiStation},
		0,
		24*time.Hour,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stationsSeen := make(map[string]bool)
	for _, p := range passes {
		stationsSeen[p.GroundStation] = true
	}
	if !stationsSeen["gs-montreal"] || !stationsSeen["gs-taipei-01"] {
		t.Errorf("expected passes over both stations, got: %v", stationsSeen)
	}
}

func TestPredictPasses_EmptyInputs(t *testing.T) {
	// No OMMs.
	passes, err := PredictPasses(nil, []GroundStation{montrealStation}, 0, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if passes != nil {
		t.Errorf("expected nil passes for empty OMMs, got %d", len(passes))
	}

	// No stations.
	passes, err = PredictPasses([]sgp4.OMM{issOMM}, nil, 0, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if passes != nil {
		t.Errorf("expected nil passes for empty stations, got %d", len(passes))
	}
}

func TestPredictPasses_NoradFilterMatchesNone(t *testing.T) {
	passes, err := PredictPasses(
		[]sgp4.OMM{issOMM},
		[]GroundStation{montrealStation},
		0,
		24*time.Hour,
		[]int{99999}, // no match
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if passes != nil {
		t.Errorf("expected nil passes for non-matching filter, got %d", len(passes))
	}
}

func TestPredictPasses_SortedByAOS(t *testing.T) {
	passes, err := PredictPasses(
		[]sgp4.OMM{issOMM},
		[]GroundStation{montrealStation},
		0,
		48*time.Hour,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 1; i < len(passes); i++ {
		if passes[i].AOS.Before(passes[i-1].AOS) {
			t.Errorf("passes not sorted by AOS: [%d] %v > [%d] %v",
				i-1, passes[i-1].AOS, i, passes[i].AOS)
		}
	}
}

func TestParseElevation(t *testing.T) {
	tests := []struct {
		input string
		want  float64
		err   bool
	}{
		{"10", 10.0, false},
		{"0", 0.0, false},
		{"72.5", 72.5, false},
		{"-5", -5.0, false},
		{"", 10.0, false}, // default
		{"abc", 0, true},
	}
	for _, tc := range tests {
		got, err := ParseElevation(tc.input)
		if tc.err && err == nil {
			t.Errorf("ParseElevation(%q): expected error", tc.input)
		}
		if !tc.err && err != nil {
			t.Errorf("ParseElevation(%q): unexpected error: %v", tc.input, err)
		}
		if !tc.err && got != tc.want {
			t.Errorf("ParseElevation(%q) = %f, want %f", tc.input, got, tc.want)
		}
	}
}

func TestParseGeoCoord(t *testing.T) {
	v, err := ParseGeoCoord("25.0330")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 25.0330 {
		t.Errorf("expected 25.0330, got %f", v)
	}
}
