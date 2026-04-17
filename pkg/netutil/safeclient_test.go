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

package netutil

import (
	"net"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		// Private ranges.
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"127.0.0.1", true},
		{"169.254.169.254", true}, // AWS metadata
		{"0.0.0.0", true},

		// Public ranges.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com

		// IPv6.
		{"::1", true},                   // loopback
		{"fe80::1", true},               // link-local
		{"fc00::1", true},               // unique local
		{"2001:4860:4860::8888", false}, // Google DNS
	}

	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("invalid IP: %s", tc.ip)
		}
		got := IsPrivateIP(ip)
		if got != tc.private {
			t.Errorf("IsPrivateIP(%s) = %v, want %v", tc.ip, got, tc.private)
		}
	}
}
