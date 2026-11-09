package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// b cycles the left list full, names, hidden, then full again.
func TestBCyclesTheListWidth(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	fullW, _ := m.layoutWidths()

	press(m, "b")
	namesW, _ := m.layoutWidths()
	if namesW >= fullW {
		t.Fatalf("names leftW = %d, want less than the full %d", namesW, fullW)
	}

	press(m, "b")
	if leftW, rightW := m.layoutWidths(); leftW != -1 || rightW != m.width {
		t.Fatalf("hidden widths = %d, %d; want the log to take the full %d", leftW, rightW, m.width)
	}

	press(m, "b")
	if leftW, _ := m.layoutWidths(); leftW != fullW {
		t.Fatalf("back to full leftW = %d, want %d", leftW, fullW)
	}
}

// Names mode keeps the state glyph and the name, and drops the status words.
func TestNamesModeDropsStatusKeepsGlyphAndName(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateExited
	m.targets[0].exitLabel = "exit 1"
	m.frame()
	press(m, "b")
	m.frame()

	row := ansi.Strip(m.leftLine(0, m.layout.leftW))
	if !strings.Contains(row, "web") || !strings.Contains(row, "✗") {
		t.Fatalf("names row = %q, want the glyph and the name", row)
	}
	if strings.Contains(row, "exit 1") {
		t.Fatalf("names row = %q, want no status word", row)
	}
}

// The hidden list still takes the keys, so focus moves to the log pane.
func TestHidingTheListFocusesThePane(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "hello")
	m.frame()
	press(m, "b", "b")
	if !m.paneFocus {
		t.Fatal("hiding the list left the keys on the unseen list")
	}
	m.frame()

	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "hello") {
		t.Fatalf("hidden list does not show the log at full width:\n%s", got)
	}
	last := strings.Split(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(last[len(last)-1], "b list") {
		t.Fatalf("footer = %q, want the b list cue", last[len(last)-1])
	}
}

// Hidden, ← at the pane's left edge does not return to the unseen list.
func TestHiddenListKeepsFocusOnLeftArrow(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	press(m, "b", "b", "left")
	if !m.paneFocus {
		t.Fatal("← moved the focus to the hidden list")
	}
}
