package main

import (
	"os"
	"testing"

	"charm.land/lipgloss/v2/compat"
)

// TestMain pins the background, so no rendering test depends on the terminal's.
func TestMain(m *testing.M) {
	compat.HasDarkBackground = true
	os.Exit(m.Run())
}
