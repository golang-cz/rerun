package main

import (
	"strings"
	"testing"
)

func hasLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func clearedMarkers(m *tuiModel) int {
	n := 0
	for _, line := range rawPane(m) {
		if strings.Contains(line, " cleared ") {
			n++
		}
	}
	return n
}

// ctrl+l marks the target's log cleared without touching its process or state.
func TestClearLeavesTheProcessAndOldLinesAlone(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	addLines(m, "api", "old-1", "old-2")
	m.frame()

	before := m.targets[0].state
	press(m, "ctrl+l")
	m.frame()

	if got := m.targets[0].state; got != before {
		t.Fatalf("state = %v, want the process untouched", got)
	}
	raw := rawPane(m)
	if !hasLine(raw, "api cleared 15:04:05") {
		t.Fatalf("pane = %q, want the cleared marker", raw)
	}
	if !hasLine(raw, "old-1") || !hasLine(raw, "old-2") {
		t.Fatalf("pane = %q, want the old lines kept in the scrollback", raw)
	}
}

// The marker is a sepLine, so the pane draws it as a dim rule, not a target line.
func TestClearMarkerIsASepLine(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	press(m, "ctrl+l")

	var sep sepLine
	found := false
	for i, s := range m.visibleSep {
		if s.label == "api cleared 15:04:05" {
			sep, found = s, true
			if m.visibleRaw[i] != s.raw() {
				t.Fatalf("raw = %q, want the label %q", m.visibleRaw[i], s.raw())
			}
		}
	}
	if !found {
		t.Fatalf("visibleSep = %+v, want the cleared marker", m.visibleSep)
	}
	if !sep.isSep() || sep.tone != toneNone {
		t.Fatalf("sep = %+v, want a dim rule", sep)
	}
	if got := renderSep(sep, 40); !strings.Contains(got, "── api cleared 15:04:05 ") {
		t.Fatalf("render = %q, want a ruled marker", got)
	}
}

// A header row clears every target in its group, running or off.
func TestClearOnGroupHeaderClearsEveryMember(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.targets[1].spec.Selected = false
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "ctrl+l")
	m.frame()

	if got := clearedMarkers(m); got != 2 {
		t.Fatalf("cleared markers = %d, want 2 (every backend member)", got)
	}
}

// The clear only marks the pane: exit and error state stay as they were.
func TestClearKeepsExitAndErrorState(t *testing.T) {
	m := newTestModel(t, target("api"))
	tg := m.targets[0]
	tg.state = stateExited
	tg.exitCode = 2
	tg.exitLabel = "exit 2"
	press(m, "ctrl+l")

	if tg.state != stateExited || tg.exitCode != 2 || tg.exitLabel != "exit 2" {
		t.Fatalf("state=%v code=%d label=%q, want the exit untouched", tg.state, tg.exitCode, tg.exitLabel)
	}
}

// The marker holds at the pane's top the way a restart's rule does, while the
// old lines scroll above it.
func TestClearPinsTheMarkerAtThePaneTop(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()

	press(m, "ctrl+l")
	m.frame()

	if m.pinSeq == 0 {
		t.Fatal("pinSeq = 0, want the cleared marker pinned")
	}
	row, ok := m.ruleRow(m.pinSeq)
	if !ok || m.vp.YOffset() != row {
		t.Fatalf("row=%v row=%d YOffset=%d, want the marker at the top", ok, row, m.vp.YOffset())
	}
}

// The help overlay names ctrl+l in both its views.
func TestHelpNamesClearKey(t *testing.T) {
	for _, keys := range [][][2]string{helpShortKeys, helpKeys} {
		found := false
		for _, kv := range keys {
			if kv[0] == "ctrl+l" {
				found = true
			}
		}
		if !found {
			t.Fatalf("help keys = %v, want ctrl+l", keys)
		}
	}
}

// Ctrl+l with the pane focused clears what the pane shows, not a stale list cursor.
func TestClearInTheFocusedPaneClearsWhatItShows(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	m.now = restartAt
	m.cursor = 0
	m.liveSelect()
	press(m, "right")
	if !m.paneFocus {
		t.Fatal("right did not focus the pane")
	}
	m.cursor = 1 // the pane owns the keys now, so the list cursor is stale

	press(m, "ctrl+l")
	m.frame()

	raw := rawPane(m)
	if !hasLine(raw, "api cleared 15:04:05") {
		t.Fatalf("pane = %q, want the shown target cleared", raw)
	}
	if hasLine(raw, "web cleared 15:04:05") {
		t.Fatalf("pane = %q, want the stale cursor row left alone", raw)
	}
}

// A pane scoped to a group clears the group's members, named one by one.
func TestClearInTheFocusedPaneClearsAGroupScope(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.now = restartAt
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "right")
	m.cursor = 2 // the stale cursor sits on web, outside the scope

	press(m, "ctrl+l")
	m.frame()

	raw := rawPane(m)
	if !hasLine(raw, "api cleared 15:04:05") || !hasLine(raw, "worker cleared 15:04:05") {
		t.Fatalf("pane = %q, want every scoped member cleared", raw)
	}
	if hasLine(raw, "web cleared 15:04:05") {
		t.Fatalf("pane = %q, want the out-of-scope target left alone", raw)
	}
}

// Every marker names its target, so two cleared groups do not draw identical rules.
func TestClearMarkersNameTheirTarget(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.now = restartAt
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "ctrl+l")
	m.frame()

	raw := rawPane(m)
	if !hasLine(raw, "api cleared 15:04:05") || !hasLine(raw, "worker cleared 15:04:05") {
		t.Fatalf("pane = %q, want both markers named", raw)
	}
}

// A frozen pane keeps its offset: the marker counts as pending instead of pinning.
func TestClearInAFrozenPaneDoesNotJump(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "right", "up") // focus the pane, then freeze the tail

	off, pending := m.vp.YOffset(), m.pending
	press(m, "ctrl+l")
	m.frame()

	if m.pinSeq != 0 {
		t.Fatalf("pinSeq = %d, want no pin while the tail is frozen", m.pinSeq)
	}
	if m.vp.YOffset() != off {
		t.Fatalf("YOffset = %d, want %d (no jump)", m.vp.YOffset(), off)
	}
	if m.pending != pending+1 {
		t.Fatalf("pending = %d, want %d", m.pending, pending+1)
	}
}

// A folded header still clears every member it hides.
func TestClearOnACollapsedHeaderClearsEveryMember(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.frame()
	m.cursor = 0
	press(m, "left")
	if !m.collapsed["backend"] {
		t.Fatal("left did not fold the backend header")
	}

	press(m, "ctrl+l")
	m.frame()

	if got := clearedMarkers(m); got != 2 {
		t.Fatalf("cleared markers = %d, want 2 (both folded members)", got)
	}
}

// An active filter narrows the output but never swallows the marker.
func TestClearUnderAFilterKeepsTheMarker(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	addLines(m, "api", "keep this", "drop this")
	commitText(m, "/", "keep")
	m.frame()
	m.cursor = 0
	m.liveSelect()

	press(m, "ctrl+l")
	m.frame()

	raw := rawPane(m)
	if !hasLine(raw, "api cleared 15:04:05") {
		t.Fatalf("pane = %q, want the marker kept under the filter", raw)
	}
	if hasLine(raw, "drop this") {
		t.Fatalf("pane = %q, want the filter still narrowing", raw)
	}
}

// A second clear moves the pin to the newer marker.
func TestClearingTwicePinsTheSecondMarker(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()

	press(m, "ctrl+l")
	m.frame()
	first := m.pinSeq
	press(m, "ctrl+l")
	m.frame()

	if m.pinSeq == first {
		t.Fatalf("pinSeq = %d, want the second marker pinned", m.pinSeq)
	}
	row, ok := m.ruleRow(m.pinSeq)
	if !ok || m.vp.YOffset() != row {
		t.Fatalf("row = %v %d YOffset = %d, want the newer marker at the top", ok, row, m.vp.YOffset())
	}
}
