//go:build e2e_wss

package e2e

import "testing"

// TestGuardsFailOnAmbiguity is a unit-level check of the two guards that were reading a different
// value than the manager they guard. It needs no cluster: it drives the parsing directly.
func TestGuardsFailOnAmbiguity(t *testing.T) {
	t.Run("repeated flag is fatal, not silently first-wins", func(t *testing.T) {
		args := []string{"--a=1", "--ephemeris-allowed-private-hosts=good", "--ephemeris-allowed-private-hosts=clobber"}
		if got := flagOccurrences(args, "--ephemeris-allowed-private-hosts"); len(got) != 2 {
			t.Fatalf("occurrences = %v, want both recorded so the caller can refuse", got)
		}
	})
	t.Run("single flag parses", func(t *testing.T) {
		got := flagOccurrences([]string{"--ephemeris-allowed-private-hosts=a,b"}, "--ephemeris-allowed-private-hosts")
		if len(got) != 1 || got[0] != "a,b" {
			t.Fatalf("occurrences = %v", got)
		}
	})
}
