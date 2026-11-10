package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// b shows or hides the left list; hidden gives the log the full width.
func TestBTogglesTheList(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	fullW, _ := m.layoutWidths()

	press(m, "b")
	if leftW, rightW := m.layoutWidths(); leftW != -1 || rightW != m.width {
		t.Fatalf("hidden widths = %d, %d; want the log to take the full %d", leftW, rightW, m.width)
	}

	press(m, "b")
	if leftW, _ := m.layoutWidths(); leftW != fullW {
		t.Fatalf("back to shown leftW = %d, want %d", leftW, fullW)
	}
}

// t shows or hides the times column on its own, whatever b does.
func TestTTogglesTheTimesColumn(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateRunning
	m.targets[0].lastStart = m.now.Add(-4 * time.Second)
	m.frame()
	if got := ansi.Strip(m.leftLine(1, m.layout.leftW)); strings.Contains(got, "4s") {
		t.Fatalf("row = %q, want no times column before t", got)
	}

	press(m, "t")
	m.frame()
	if got := ansi.Strip(m.leftLine(1, m.layout.leftW)); !strings.Contains(got, "4s") {
		t.Fatalf("row = %q, want the uptime after t", got)
	}

	press(m, "b", "b")
	if !m.showTimes {
		t.Fatal("b cleared the times column")
	}
	press(m, "t")
	if m.showTimes || m.listHidden {
		t.Fatalf("t: showTimes=%v listHidden=%v, want the column off and the list shown", m.showTimes, m.listHidden)
	}
}

// The default list keeps the state glyph and the name, and drops the times column.
func TestDefaultListDropsTimesKeepsGlyphAndName(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateExited
	m.targets[0].exitLabel = "exit 1"
	m.frame()

	row := ansi.Strip(m.leftLine(1, m.layout.leftW))
	if !strings.Contains(row, "web") || !strings.Contains(row, "✗") {
		t.Fatalf("row = %q, want the glyph and the name", row)
	}
	if strings.Contains(row, "exit 1") {
		t.Fatalf("row = %q, want no times word", row)
	}
}

// The hidden list still takes the keys, so focus moves to the log pane.
func TestHidingTheListFocusesThePane(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "hello")
	m.frame()
	press(m, "b")
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
	press(m, "b", "left")
	if !m.paneFocus {
		t.Fatal("← moved the focus to the hidden list")
	}
}

// Hidden, ctrl+k's jump keeps the keys on the pane, not the unseen list.
func TestHiddenListFinderJumpKeepsKeysOnPane(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("worker"))
	m.frame()
	press(m, "b")
	m.Update(ctrlK)
	press(m, "w", "r", "k", "enter")
	if !m.paneFocus || m.listFocused() {
		t.Fatalf("after the jump: paneFocus=%v listFocused=%v, want the pane", m.paneFocus, m.listFocused())
	}

	cursor := m.cursor
	press(m, "up")
	if m.cursor != cursor {
		t.Fatalf("up moved the hidden list's cursor from %d to %d", cursor, m.cursor)
	}

	idx := targetIndex(m, "worker")
	state := m.targets[idx].state
	press(m, " ")
	if m.targets[idx].state != state {
		t.Fatal("space on the pane started or stopped the jumped-to target")
	}
}

// Hidden, esc reveals the list instead of silently widening the scope.
func TestHiddenListEscRevealsListAndKeepsScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.frame()
	press(m, "b")
	m.solo = 1

	press(m, "esc")
	if m.listHidden || m.paneFocus {
		t.Fatalf("esc: listHidden=%v paneFocus=%v, want the list shown and unfocused", m.listHidden, m.paneFocus)
	}
	if m.solo != 1 {
		t.Fatalf("esc cleared the scope while the list was hidden: solo=%d", m.solo)
	}

	press(m, "esc")
	if m.solo != -1 {
		t.Fatalf("second esc with the list shown: solo=%d, want the scope cleared", m.solo)
	}
}

// Hidden at 80 columns, the bottom line names the list and the help key.
func TestHiddenListBottomLineCarriesHelpCue(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 20
	press(m, "b")
	lines := paneOf(m)
	if got := strings.TrimRight(ansi.Strip(lines[len(lines)-1]), " "); got != "b list · ? help" {
		t.Fatalf("bottom line = %q, want the list and help cues", got)
	}
}
