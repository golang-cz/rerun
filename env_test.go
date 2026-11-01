package rerun

import (
	"strings"
	"testing"
)

// The child environment keeps the parent's entries and adds exactly one
// RERUN marker, replacing any inherited value.
func TestChildEnvAddsRerunMarker(t *testing.T) {
	got := childEnv([]string{"PATH=/bin", "RERUN=", "HOME=/root"})
	joined := strings.Join(got, "\n")

	if !strings.HasSuffix(joined, "RERUN=1") {
		t.Fatalf("child env = %v, want RERUN=1 at the end", got)
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "HOME=/root") {
		t.Fatalf("child env = %v, want the user environment preserved", got)
	}
	if strings.Count(joined, "RERUN=") != 1 {
		t.Fatalf("child env = %v, want exactly one RERUN entry", got)
	}
}
