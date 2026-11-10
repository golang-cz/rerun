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
		{"release tag", "v1.2.3", "", "v1.2.3"},
		{"dev build", "dev-abc1234", "2026-01-02T03:01:05Z", "dev-abc1234"},
		{"unbuilt", "", "", "dev"},
		{"bare dev", "dev", "", "dev"},
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
	m.width, m.height = 120, 6

	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != m.height {
		t.Fatalf("view has %d lines, want %d", len(lines), m.height)
	}
	bottom := lines[m.height-1]
	if got := displayWidth(bottom); got != m.width {
		t.Fatalf("version line width = %d, want %d: %q", got, m.width, bottom)
	}

	leftW, _ := m.layoutWidths()
	text := "v9.9.9"
	got := string([]rune(plainLine(bottom))[:leftW])
	if want := text + strings.Repeat(" ", leftW-displayWidth(text)-len("?help")) + "?help"; got != want {
		t.Fatalf("left column = %q, want %q", got, want)
	}
	if r := []rune(plainLine(bottom))[leftW]; r != '│' {
		t.Fatalf("divider cell = %q, want │", r)
	}
}

// The cell always fills exactly leftW cells and ends in `?help` flush right,
// even in a column too narrow for the version.
func TestVersionCellEndsWithHelpCue(t *testing.T) {
	cases := []struct {
		name  string
		leftW int
		want  string
	}{
		{"wide column", 30, "v9.9.9" + strings.Repeat(" ", 30-6-5) + "?help"},
		{"narrow column", 8, "v…" + strings.Repeat(" ", 8-2-5) + "?help"},
		{"cue only", 6, " ?help"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.version = "v9.9.9"
			got := plainLine(m.versionCell(tc.leftW))
			if w := displayWidth(got); w != tc.leftW {
				t.Fatalf("versionCell(%d) width = %d: %q", tc.leftW, w, got)
			}
			if got != tc.want {
				t.Fatalf("versionCell(%d) = %q, want %q", tc.leftW, got, tc.want)
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
	m.height = 4 // two list rows plus the fs events line and the version row
	m.cursor = 5 // the last target

	lines := strings.Split(m.View().Content, "\n")
	if m.layout.offset == 0 {
		t.Fatal("the list did not scroll to keep the cursor row visible")
	}
	if got := m.rowAt(2, m.height-2); got != 5 {
		t.Fatalf("cursor row = %d at the last list line, want 5", got)
	}
	if got := m.rowAt(2, 0); got != -1 {
		t.Fatalf("rowAt on the fs events line = %d, want no row", got)
	}
	if got := m.rowAt(2, m.height-1); got != -1 {
		t.Fatalf("rowAt on the version row = %d, want no row", got)
	}
	bottom := plainLine(lines[m.height-1])
	if !strings.HasPrefix(bottom, "dev") {
		t.Fatalf("version line = %q, want the dev fallback", bottom)
	}
	for _, tg := range targets {
		if strings.Contains(bottom, tg.Name) {
			t.Fatalf("version line %q shows target %q", bottom, tg.Name)
		}
	}
}

// With t on the version reads `version · age ago`; t off, or no usable buildTime, drops the age.
func TestVersionRowShowsBuildAgeWithTimes(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const ver = "dev-abc1234"
	cases := []struct {
		name      string
		times     bool
		buildTime string
		left      string
	}{
		{"times on", true, "2026-01-02T02:53:05Z", ver + " · 11m ago"},
		{"times off", false, "2026-01-02T02:53:05Z", ver},
		{"empty buildTime", true, "", ver},
		{"bad buildTime", true, "yesterday", ver},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.version, m.buildTime, m.now, m.showTimes = "dev-abc1234", tc.buildTime, now, tc.times
			got := plainLine(m.versionCell(40))
			want := tc.left + strings.Repeat(" ", 40-displayWidth(tc.left)-len("?help")) + "?help"
			if got != want {
				t.Fatalf("cell = %q, want %q", got, want)
			}
		})
	}
}

// Narrow columns cut the version first, then the age; `?help` stays.
func TestVersionRowTruncationOrder(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name  string
		leftW int
		left  string
	}{
		{"room for both", 30, "v9.9.9 · 11m ago"},
		{"version cut", 21, "v9.… · 11m ago"},
		{"version dropped", 17, "11m ago"},
		{"age cut", 11, "11m …"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.version, m.buildTime, m.now, m.showTimes = "v9.9.9", "2026-01-02T02:53:05Z", now, true
			got := plainLine(m.versionCell(tc.leftW))
			want := tc.left + strings.Repeat(" ", tc.leftW-displayWidth(tc.left)-len("?help")) + "?help"
			if got != want {
				t.Fatalf("cell = %q, want %q", got, want)
			}
		})
	}
}

// With t on at a normal list width the age shows beside the version, and `?help` stays flush right.
func TestVersionRowKeepsAgeAtNormalWidth(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := newTestModel(t)
	m.version, m.buildTime, m.now, m.showTimes = "dev-70aa7ff", "2026-01-02T02:53:05Z", now, true
	for _, leftW := range []int{33, 29, 27} {
		got := plainLine(m.versionCell(leftW))
		if !strings.Contains(got, "11m ago") || !strings.HasSuffix(got, "?help") {
			t.Errorf("versionCell(%d) = %q, want the age and `?help`", leftW, got)
		}
	}
}
