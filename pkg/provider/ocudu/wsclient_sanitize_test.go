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

package ocudu

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Unsafe/invisible runes are built from numeric code points so this test's own source
// carries no literal control, bidi, or zero-width characters to mistype or to trip a
// bidi linter.
var (
	rlo   = string(rune(0x202e)) // RIGHT-TO-LEFT OVERRIDE (bidi, Cf)
	zwsp  = string(rune(0x200b)) // ZERO WIDTH SPACE (Cf)
	zwj   = string(rune(0x200d)) // ZERO WIDTH JOINER (Cf)
	bom   = string(rune(0xfeff)) // ZERO WIDTH NO-BREAK SPACE / BOM (Cf)
	wj    = string(rune(0x2060)) // WORD JOINER (Cf)
	hanZi = string(rune(0x4e16)) // 世 — a 3-byte printable rune
)

func TestSanitizeRemoteError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "invalid epoch", "invalid epoch"},
		{"empty", "", "(no detail)"},
		{"only control and format", "\x00\n\t" + zwsp + rlo, "(no detail)"},
		{"newlines and tabs collapse to space", "line1\nline2\ttab", "line1 line2 tab"},
		{"nul becomes space", "bad\x00cell", "bad cell"},
		{"bidi override dropped", "safe" + rlo + "gndiffer", "safegndiffer"},
		{"zero-width runes dropped", "a" + zwsp + "b" + zwj + "c" + bom + "d" + wj + "e", "abcde"},
		{"printable multibyte kept", hanZi + "-epoch", hanZi + "-epoch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeRemoteError(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizeRemoteError(%q) = %q, want %q", tt.in, got, tt.want)
			}
			assertSanitized(t, got)
		})
	}
}

func TestSanitizeRemoteError_HardCapsLength(t *testing.T) {
	// 4 KiB of attacker text (well within the 16 KiB reply cap) must be bounded.
	got := sanitizeRemoteError(strings.Repeat("A", 4096))
	if !strings.HasSuffix(got, " (truncated)") {
		t.Fatalf("oversized input must be marked truncated: %q...", got[:min(len(got), 48)])
	}
	body := strings.TrimSuffix(got, " (truncated)")
	// Allow at most one over-bound rune (the loop checks the cap before each rune).
	if len(body) > maxRemoteErrorBytes+utf8.UTFMax {
		t.Fatalf("bounded body = %d bytes, want <= %d", len(body), maxRemoteErrorBytes+utf8.UTFMax)
	}
	assertSanitized(t, got)
}

func TestSanitizeRemoteError_TruncationIsRuneSafe(t *testing.T) {
	// 3-byte runes filling past the cap: a naive byte cut would split one and produce
	// invalid UTF-8. Truncation must land on a rune boundary.
	got := sanitizeRemoteError(strings.Repeat(hanZi, maxRemoteErrorBytes))
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8: %q", got)
	}
	assertSanitized(t, got)
}

// assertSanitized fails if out is invalid UTF-8 or retains any control (Cc) or format
// (Cf — bidi/zero-width) rune, i.e. anything that could corrupt or spoof a Condition,
// Event, or kubectl-describe rendering.
func assertSanitized(t *testing.T, out string) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
	for _, r := range out {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			t.Fatalf("output retains unsafe rune %U in %q", r, out)
		}
	}
}
