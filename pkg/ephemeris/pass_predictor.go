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
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
)

// MaxPassWindows is the maximum number of pass windows stored in status
// to stay within etcd object size limits (~1.5 MB).
const MaxPassWindows = 500

// DefaultStepSeconds is the propagation step size for pass prediction.
const DefaultStepSeconds = 60

// GroundStation holds parsed ground station coordinates for pass prediction.
type GroundStation struct {
	Name      string
	Latitude  float64 // degrees
	Longitude float64 // degrees
	Altitude  float64 // meters
}

// PassResult holds a predicted pass window.
type PassResult struct {
	Satellite     string
	GroundStation string
	AOS           time.Time
	LOS           time.Time
	MaxElevation  float64 // degrees
}

// PredictPasses computes satellite pass windows for the given OMMs over ground stations.
// It filters by minElevation, limits results to MaxPassWindows, and uses concurrent workers.
// The startTime parameter sets the prediction window start; pass time.Time{} to use time.Now().
func PredictPasses(
	omms []sgp4.OMM,
	stations []GroundStation,
	minElevation float64,
	horizon time.Duration,
	noradFilter []int,
	startTime time.Time,
) ([]PassResult, error) {
	if len(omms) == 0 || len(stations) == 0 {
		return nil, nil
	}

	// Apply NORAD ID filter if specified.
	filtered := filterOMMs(omms, noradFilter)
	if len(filtered) == 0 {
		return nil, nil
	}

	start := startTime
	if start.IsZero() {
		start = time.Now()
	}
	stop := start.Add(horizon)

	// Build work items.
	type workItem struct {
		omm     sgp4.OMM
		station GroundStation
	}
	var work []workItem
	for _, omm := range filtered {
		for _, gs := range stations {
			work = append(work, workItem{omm: omm, station: gs})
		}
	}

	// Run pass predictions concurrently with a bounded worker pool.
	numWorkers := min(8, len(work))

	var mu sync.Mutex
	var allPasses []PassResult
	var firstErr error

	workCh := make(chan workItem, len(work))
	for _, w := range work {
		workCh <- w
	}
	close(workCh)

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Go(func() {
			for item := range workCh {
				passes, err := predictSingle(item.omm, item.station, start, stop, minElevation)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				allPasses = append(allPasses, passes...)
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if firstErr != nil && len(allPasses) == 0 {
		return nil, firstErr
	}

	// Sort by AOS time ascending.
	sort.Slice(allPasses, func(i, j int) bool {
		return allPasses[i].AOS.Before(allPasses[j].AOS)
	})

	// Cap results to MaxPassWindows.
	if len(allPasses) > MaxPassWindows {
		allPasses = allPasses[:MaxPassWindows]
	}

	return allPasses, nil
}

// predictSingle computes pass windows for a single satellite over a single ground station.
func predictSingle(
	omm sgp4.OMM,
	gs GroundStation,
	start, stop time.Time,
	minElevation float64,
) ([]PassResult, error) {
	tle, err := omm.ToTLE()
	if err != nil {
		return nil, fmt.Errorf("converting OMM to TLE for %s (NORAD %d): %w",
			omm.ObjectName, omm.NoradCatID, err)
	}

	passes, err := tle.GeneratePasses(
		gs.Latitude, gs.Longitude, gs.Altitude,
		start, stop, DefaultStepSeconds,
	)
	if err != nil {
		// Propagation errors (e.g., decayed satellite) are non-fatal; skip this satellite.
		return nil, nil
	}

	var results []PassResult
	for _, p := range passes {
		if p.MaxElevation < minElevation {
			continue
		}
		results = append(results, PassResult{
			Satellite:     omm.ObjectName,
			GroundStation: gs.Name,
			AOS:           p.AOS,
			LOS:           p.LOS,
			MaxElevation:  p.MaxElevation,
		})
	}
	return results, nil
}

// filterOMMs returns OMMs matching the NORAD ID filter. If filter is empty, returns all.
func filterOMMs(omms []sgp4.OMM, noradFilter []int) []sgp4.OMM {
	if len(noradFilter) == 0 {
		return omms
	}
	allowed := make(map[int]bool, len(noradFilter))
	for _, id := range noradFilter {
		allowed[id] = true
	}
	var filtered []sgp4.OMM
	for _, omm := range omms {
		if allowed[omm.NoradCatID] {
			filtered = append(filtered, omm)
		}
	}
	return filtered
}

// ParseElevation parses a string elevation value (e.g., "10") to float64.
func ParseElevation(s string) (float64, error) {
	if s == "" {
		return 10.0, nil // default
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid elevation %q: %w", s, err)
	}
	return v, nil
}

// ParseGeoCoord parses a string coordinate (e.g., "25.0330") to float64.
func ParseGeoCoord(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
