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
	"testing"
	"time"
)

func utc(hour, minute int) time.Time {
	return time.Date(2026, 4, 16, hour, minute, 0, 0, time.UTC)
}

func TestIsWithinMaintenanceWindow(t *testing.T) {
	tests := []struct {
		name   string
		window string
		now    time.Time
		want   bool
		err    bool
	}{
		{
			name:   "within normal window",
			window: "02:00-04:00 UTC",
			now:    utc(3, 0),
			want:   true,
		},
		{
			name:   "outside normal window",
			window: "02:00-04:00 UTC",
			now:    utc(5, 0),
			want:   false,
		},
		{
			name:   "at start boundary (inclusive)",
			window: "02:00-04:00 UTC",
			now:    utc(2, 0),
			want:   true,
		},
		{
			name:   "at end boundary (exclusive)",
			window: "02:00-04:00 UTC",
			now:    utc(4, 0),
			want:   false,
		},
		{
			name:   "midnight wraparound — inside (after start)",
			window: "23:00-02:00 UTC",
			now:    utc(23, 30),
			want:   true,
		},
		{
			name:   "midnight wraparound — inside (before end)",
			window: "23:00-02:00 UTC",
			now:    utc(1, 0),
			want:   true,
		},
		{
			name:   "midnight wraparound — outside",
			window: "23:00-02:00 UTC",
			now:    utc(22, 0),
			want:   false,
		},
		{
			name:   "empty window returns false",
			window: "",
			now:    utc(3, 0),
			want:   false,
		},
		{
			name:   "invalid format",
			window: "garbage",
			now:    utc(3, 0),
			err:    true,
		},
		{
			name:   "invalid time",
			window: "25:00-04:00 UTC",
			now:    utc(3, 0),
			err:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsWithinMaintenanceWindow(tc.window, tc.now)
			if tc.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsWithinMaintenanceWindow(%q, %v) = %v, want %v",
					tc.window, tc.now, got, tc.want)
			}
		})
	}
}
