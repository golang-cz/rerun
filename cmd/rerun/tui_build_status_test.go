package main

import (
	"testing"
	"time"
)

// The list row's status column reads a build with the same formatter as the rules.
func TestListRowBuildStatusUsesFmtDur(t *testing.T) {
	m := newTestModel(t, target("web"))
	row := m.targets[0]
	row.building = true
	row.buildStart = m.now.Add(-200 * time.Millisecond)

	if got := m.status(row); got != "⚙ <1s" {
		t.Fatalf("status = %q, want ⚙ <1s", got)
	}
}
