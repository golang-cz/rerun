package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plainLine(s string) string { return ansiSGR.ReplaceAllString(s, "") }

func TestVersionText(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name      string
		version   string
		buildTime string
		want      string
	}{
		{"release tag", "v1.2.3", "", "rerun · v1.2.3"},
		{"release tag ignores the age", "v1.2.3", "2020-01-01T00:00:00Z", "rerun · v1.2.3"},
		{"dev just now", "dev-abc1234", "2026-01-02T03:04:00Z", "rerun · dev-abc1234 (just now)"},
		{"dev minutes", "dev-abc1234", "2026-01-02T03:01:05Z", "rerun · dev-abc1234 (3m ago)"},
		{"dev hours", "dev-abc1234", "2026-01-02T00:04:05Z", "rerun · dev-abc1234 (3h ago)"},
		{"dev days", "dev-abc1234", "2025-12-31T03:04:05Z", "rerun · dev-abc1234 (2d ago)"},
		{"dev without a stamp", "dev-abc1234", "", "rerun · dev-abc1234"},
		{"dev with a bad stamp", "dev-abc1234", "yesterday", "rerun · dev-abc1234"},
		{"future stamp", "dev-abc1234", "2027-01-02T03:04:05Z", "rerun · dev-abc1234 (just now)"},
		{"unbuilt", "", "", "rerun · dev"},
		{"bare dev", "dev", "", "rerun · dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.version, m.buildTime, m.now = tc.version, tc.buildTime, now
			if got := m.versionText(); got != tc.want {
				t.Fatalf("versionText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The version line is hint styled, left aligned, and never padded into the
// divider column: the divider keeps its cell on that row like every other.
func TestVersionRowOwnsTheLeftColumnsBottomLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.version = "v9.9.9"
	m.width, m.height = 40, 6

	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Fatalf("view has %d lines, want %d", len(lines), m.height)
	}
	bottom := lines[m.height-1]
	if got := displayWidth(bottom); got != m.width {
		t.Fatalf("version line width = %d, want %d: %q", got, m.width, bottom)
	}

	leftW, _ := m.layoutWidths()
	text := "rerun · v9.9.9 · ?"
	got := string([]rune(plainLine(bottom))[:leftW])
	if want := text + strings.Repeat(" ", leftW-displayWidth(text)); got != want {
		t.Fatalf("left column = %q, want %q", got, want)
	}
	if r := []rune(plainLine(bottom))[leftW]; r != '│' {
		t.Fatalf("divider cell = %q, want │", r)
	}
}

// The help cue rides the version line while the column has room for it, and
// the cell always fills exactly leftW cells.
func TestVersionCellEndsWithHelpCue(t *testing.T) {
	cases := []struct {
		name  string
		leftW int
		cue   bool
	}{
		{"wide column", 30, true},
		{"narrow column", 6, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.version = "v9.9.9"
			got := plainLine(m.versionCell(tc.leftW))
			if w := displayWidth(got); w != tc.leftW {
				t.Fatalf("versionCell(%d) width = %d: %q", tc.leftW, w, got)
			}
			if hasCue := strings.HasSuffix(strings.TrimRight(got, " "), "· ?"); hasCue != tc.cue {
				t.Fatalf("versionCell(%d) = %q, cue %v, want %v", tc.leftW, got, hasCue, tc.cue)
			}
		})
	}
}

// The list loses the version row, so a list longer than the screen still
// scrolls the cursor's row into view and never draws a target on that row.
func TestVersionRowShrinksTheListArea(t *testing.T) {
	var targets []Target
	for i := 0; i < 6; i++ {
		targets = append(targets, target(fmt.Sprintf("t%d", i)))
	}
	m := newTestModel(t, targets...)
	m.height = 4 // three list rows plus the version row
	m.cursor = 6 // the last target

	lines := strings.Split(m.View(), "\n")
	if m.layout.offset == 0 {
		t.Fatal("the list did not scroll to keep the cursor row visible")
	}
	if got := m.rowAt(2, m.height-2); got != 6 {
		t.Fatalf("cursor row = %d at the last list line, want 6", got)
	}
	if got := m.rowAt(2, m.height-1); got != -1 {
		t.Fatalf("rowAt on the version row = %d, want no row", got)
	}
	bottom := plainLine(lines[m.height-1])
	if !strings.HasPrefix(bottom, "rerun · dev") {
		t.Fatalf("version line = %q, want the dev fallback", bottom)
	}
	for _, tg := range targets {
		if strings.Contains(bottom, tg.Name) {
			t.Fatalf("version line %q shows target %q", bottom, tg.Name)
		}
	}
}
