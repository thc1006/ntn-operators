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

package lifecycle

import (
	"fmt"
	"regexp"
	"time"
)

var maintenanceWindowRE = regexp.MustCompile(`^(\d{2}:\d{2})-(\d{2}:\d{2})\s+UTC$`)

// IsWithinMaintenanceWindow checks if the given time falls within the
// maintenance window. The window format is "HH:MM-HH:MM UTC" (24-hour).
// Handles midnight wraparound (e.g., "23:00-02:00 UTC").
// An empty window string means no maintenance window is configured;
// returns (false, nil) so callers can decide to proceed unconditionally.
// A window where start == end (e.g., "00:00-00:00 UTC") is treated as invalid.
func IsWithinMaintenanceWindow(window string, now time.Time) (bool, error) {
	if window == "" {
		return false, nil
	}

	matches := maintenanceWindowRE.FindStringSubmatch(window)
	if matches == nil {
		return false, fmt.Errorf("invalid maintenance window format %q: expected HH:MM-HH:MM UTC", window)
	}

	startHM, err := parseHHMM(matches[1])
	if err != nil {
		return false, fmt.Errorf("invalid start time in window: %w", err)
	}
	endHM, err := parseHHMM(matches[2])
	if err != nil {
		return false, fmt.Errorf("invalid end time in window: %w", err)
	}

	if startHM == endHM {
		return false, fmt.Errorf("invalid maintenance window: start and end are the same (%s)", matches[1])
	}

	nowUTC := now.UTC()
	nowMinutes := nowUTC.Hour()*60 + nowUTC.Minute()

	if startHM < endHM {
		// Normal window: e.g., "02:00-04:00 UTC"
		return nowMinutes >= startHM && nowMinutes < endHM, nil
	}
	// Midnight wraparound: e.g., "23:00-02:00 UTC"
	return nowMinutes >= startHM || nowMinutes < endHM, nil
}

// parseHHMM parses "HH:MM" into minutes since midnight.
func parseHHMM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}
