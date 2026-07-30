/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ephemeris

import (
	"math"
	"testing"

	"github.com/akhenakh/sgp4"
	"github.com/go-logr/logr"
)

// validLEO is a physically-sound ISS-like element set (the baseline each case mutates one field of).
func validLEO() sgp4.OMM {
	return sgp4.OMM{
		NoradCatID: 25544, ObjectName: "ISS",
		MeanMotion: 15.5, Eccentricity: 0.0007, Inclination: 51.6,
		RAOfAscNode: 120.0, ArgOfPericenter: 80.0, MeanAnomaly: 280.0,
	}
}

func TestValidOMM(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*sgp4.OMM)
		ok   bool
	}{
		{"valid LEO", func(*sgp4.OMM) {}, true},
		{"NaN inclination", func(o *sgp4.OMM) { o.Inclination = math.NaN() }, false},
		{"Inf mean motion", func(o *sgp4.OMM) { o.MeanMotion = math.Inf(1) }, false},
		{"NaN bstar", func(o *sgp4.OMM) { o.BStar = math.NaN() }, false},
		{"eccentricity >= 1 (hyperbolic)", func(o *sgp4.OMM) { o.Eccentricity = 1.2 }, false},
		{"eccentricity negative", func(o *sgp4.OMM) { o.Eccentricity = -0.01 }, false},
		{"eccentricity 0 (circular) is ok", func(o *sgp4.OMM) { o.Eccentricity = 0 }, true},
		{"inclination > 180", func(o *sgp4.OMM) { o.Inclination = 200 }, false},
		{"inclination 180 (retrograde) is ok", func(o *sgp4.OMM) { o.Inclination = 180 }, true},
		{"mean motion 0", func(o *sgp4.OMM) { o.MeanMotion = 0 }, false},
		{"mean motion negative", func(o *sgp4.OMM) { o.MeanMotion = -1 }, false},
		{"large but finite angles are ok (SGP4 normalises)", func(o *sgp4.OMM) { o.MeanAnomaly = 359.999 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validLEO()
			tc.mut(&o)
			err := validOMM(o)
			if tc.ok && err != nil {
				t.Fatalf("want valid, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want invalid, got nil")
			}
		})
	}
}

// TestFilterValidOMMs_DropsOnlyInvalid pins the pool behaviour: a malformed member is dropped while the
// valid members survive, so one bad element set never poisons or discards a whole constellation.
func TestFilterValidOMMs_DropsOnlyInvalid(t *testing.T) {
	good1 := validLEO()
	good1.NoradCatID = 111
	good2 := validLEO()
	good2.NoradCatID = 222
	bad := validLEO()
	bad.NoradCatID = 999
	bad.Eccentricity = 2.0 // hyperbolic -> must be dropped

	out := filterValidOMMs(logr.Discard(), []sgp4.OMM{good1, bad, good2})
	if len(out) != 2 {
		t.Fatalf("want the 2 valid members kept, got %d", len(out))
	}
	for _, o := range out {
		if o.NoradCatID == 999 {
			t.Fatal("the hyperbolic element set must have been dropped")
		}
	}
}

// TestParseValidOMMs_DropsInvalidFromJSON pins the SHARED validated entry point used by the CelesTrak
// fetch, the Space-Track fetch, and the durable-cache restore: garbage that parses as valid JSON is
// dropped at parse time, so no entry path can feed SGP4 a malformed element set.
func TestParseValidOMMs_DropsInvalidFromJSON(t *testing.T) {
	body := []byte(`[
	  {
	    "OBJECT_NAME":"GOOD", "EPOCH":"2026-04-17T00:00:00.000000", "MEAN_MOTION":12.85,
	    "ECCENTRICITY":0.001, "INCLINATION":87.9, "RA_OF_ASC_NODE":100.0,
	    "ARG_OF_PERICENTER":90.0, "MEAN_ANOMALY":270.0, "NORAD_CAT_ID":111, "BSTAR":0.0001
	  },
	  {
	    "OBJECT_NAME":"HYPERBOLIC", "EPOCH":"2026-04-17T00:00:00.000000", "MEAN_MOTION":12.85,
	    "ECCENTRICITY":2.0, "INCLINATION":87.9, "RA_OF_ASC_NODE":100.0,
	    "ARG_OF_PERICENTER":90.0, "MEAN_ANOMALY":270.0, "NORAD_CAT_ID":999, "BSTAR":0.0001
	  }
	]`)
	omms, err := ParseValidOMMs(logr.Discard(), body)
	if err != nil {
		t.Fatalf("ParseValidOMMs: %v", err)
	}
	if len(omms) != 1 || omms[0].NoradCatID != 111 {
		t.Fatalf("must keep only the valid member (NORAD 111), got %d: %+v", len(omms), omms)
	}
}
