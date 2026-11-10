package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func hasLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// clearedText counts any cleared-marker text left in the pane or its rules.
func clearedText(m *tuiModel) int {
	n := 0
	for _, line := range rawPane(m) {
		if strings.Contains(line, " cleared ") {
			n++
		}
	}
	for _, s := range m.visibleSep {
		if strings.Contains(s.label, "cleared") {
			n++
		}
	}
	return n
}

// paneTop is the pane's first content row as drawn, styling stripped.
func paneTop(m *tuiModel) string {
	lines := paneOf(m)
	return strings.TrimRight(ansi.Strip(lines[m.titleHeight()]), " ")
}

// noMarkerText fails when any cleared-marker text survives the clear.
func noMarkerText(t *testing.T, m *tuiModel) {
	t.Helper()
	if got := clearedText(m); got != 0 {
		t.Fatalf("cleared marker text = %d, want none", got)
	}
}

// blankTop fails when the pane's first content row is not blank.
func blankTop(t *testing.T, m *tuiModel) {
	t.Helper()
	if got := paneTop(m); got != "" {
		t.Fatalf("pane top = %q, want it blank", got)
	}
}

// ctrl+l writes no marker, blanks the pane's top, and leaves the process and
// the old lines in the scrollback above it.
func TestClearWritesNoMarkerAndBlanksTheTop(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	addLines(m, "api", "old-1", "old-2")
	m.frame()
	m.cursor = 0
	m.liveSelect()

	before := m.targets[0].state
	press(m, "ctrl+l")

	if got := m.targets[0].state; got != before {
		t.Fatalf("state = %v, want the process untouched", got)
	}
	noMarkerText(t, m)
	blankTop(t, m)

	// The old lines stay above: one page up reaches them.
	press(m, "pgup")
	if got := plainPane(m); !strings.Contains(got, "old-1") || !strings.Contains(got, "old-2") {
		t.Fatalf("scrolling up did not reach the old lines:\n%s", got)
	}
}

// A clear works before the cursor has ever scoped the pane.
func TestClearWithoutAScopeBlanksTheCursorRow(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	addLines(m, "api", "api-1")
	addLines(m, "web", "web-1")
	m.frame()
	if m.solo >= 0 || m.schemaSel != "" {
		t.Fatal("the fixture must start unscoped")
	}

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
	if m.solo != 0 {
		t.Fatalf("solo = %d, want the cursor row scoped", m.solo)
	}
}

// A clear adds no line at all, so the pane never draws a rule for it.
func TestClearAddsNoLine(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.cursor = 0
	m.liveSelect()
	addLines(m, "api", "old-1")
	m.frame()
	before := m.paneLen()

	press(m, "ctrl+l")
	m.frame()

	if got := m.paneLen(); got != before {
		t.Fatalf("paneLen = %d, want %d (a clear must not add a line)", got, before)
	}
	for _, s := range m.visibleSep {
		if strings.Contains(s.label, "cleared") {
			t.Fatalf("sepLine = %+v, want no cleared rule", s)
		}
	}
}

// A header row clears every target it groups, and blanks the group's pane.
func TestClearOnGroupHeaderBlanksTheGroupPane(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.cursor = 0
	m.liveSelect()
	addLines(m, "api", "api-1")
	addLines(m, "worker", "worker-1")
	addLines(m, "web", "web-1")
	m.frame()

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
}

// The clear only holds the pane: exit and error state stay as they were.
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

// The cleared pane holds its top on the row after the last line, so new output
// starts at the top row and the old lines stay above.
func TestClearPinsTheLineAfterTheLast(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	last := m.paneLen() - 1

	press(m, "ctrl+l")
	m.frame()

	if m.pinSeq == 0 || !m.pinGap {
		t.Fatalf("pinSeq = %d gap = %v, want the clear pinned", m.pinSeq, m.pinGap)
	}
	row, ok := m.pinTop()
	if !ok || row != last+1 {
		t.Fatalf("pinTop = %d %v, want %d", row, ok, last+1)
	}
	if m.vp.YOffset() != last+1 {
		t.Fatalf("YOffset = %d, want %d", m.vp.YOffset(), last+1)
	}

	addLines(m, "api", "fresh")
	if got := paneTop(m); !strings.Contains(got, "fresh") {
		t.Fatalf("pane top = %q, want the newest line", got)
	}
}

// ctrl+l with the last shown line expanded holds the row after the block,
// so no expansion text shows at the pane's top.
func TestClearWithLastLineExpandedBlanksTheTop(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	press(m, "right", "enter") // focus the pane and expand the last line
	press(m, "end")            // resume the tail with the block still open
	if !strings.Contains(plainPane(m), `status: 403`) {
		t.Fatalf("fixture is not expanded:\n%s", plainPane(m))
	}

	press(m, "ctrl+l")
	m.frame()

	noMarkerText(t, m)
	blankTop(t, m)
	if got := plainPane(m); strings.Contains(got, `status: 403`) {
		t.Fatalf("pane shows the expansion text:\n%s", got)
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
	m.cursor = 0
	m.liveSelect()
	addLines(m, "api", "api-1")
	addLines(m, "web", "web-1")
	m.frame()
	press(m, "right")
	if !m.paneFocus {
		t.Fatal("right did not focus the pane")
	}
	m.cursor = 1 // the pane owns the keys now, so the list cursor is stale

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
	if !hasLine(rawPane(m), "api-1") {
		t.Fatal("the cleared target's old line left the scrollback")
	}
}

// A pane scoped to a group clears the group's members, stale cursor and all.
func TestClearInTheFocusedPaneClearsAGroupScope(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.cursor = 0
	m.liveSelect()
	addLines(m, "api", "api-1")
	addLines(m, "worker", "worker-1")
	addLines(m, "web", "web-1")
	m.frame()
	press(m, "right")
	m.cursor = 3 // the stale cursor sits on web, outside the scope

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
}

// A frozen pane resumes the tail first, then clears: ctrl+l is one gesture.
func TestClearInAFrozenPaneResumesTheTail(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "right", "up") // focus the pane, then freeze the tail
	if m.follow {
		t.Fatal("setup: the pane did not freeze")
	}

	press(m, "ctrl+l")
	m.frame()

	if !m.follow {
		t.Fatal("ctrl+l did not resume the tail")
	}
	if m.pinSeq == 0 || !m.pinGap {
		t.Fatalf("pinSeq = %d gap = %v, want the clear pinned", m.pinSeq, m.pinGap)
	}
	if m.pending != 0 {
		t.Fatalf("pending = %d, want 0", m.pending)
	}
	noMarkerText(t, m)
	blankTop(t, m)
}

// A folded header still clears every member it hides.
func TestClearOnAFoldedHeaderBlanksTheGroupPane(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	idle(m)
	m.cursor = 1 // the backend header
	m.liveSelect()
	addLines(m, "api", "api-1")
	addLines(m, "worker", "worker-1")
	m.frame()
	press(m, "left")
	if !m.foldState["backend"] {
		t.Fatal("left did not fold the backend header")
	}

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
}

// An active filter narrows the output but never blocks the clear.
func TestClearUnderAFilterStillBlanksThePane(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	addLines(m, "api", "keep this", "drop this")
	commitText(m, "/", "keep")
	m.frame()
	m.cursor = 0
	m.liveSelect()

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
	if got := plainPane(m); strings.Contains(got, "drop this") {
		t.Fatalf("pane = %q, want the filter still narrowing", got)
	}
}

// A second clear re-holds the same anchor: no line was added between them.
func TestClearingTwiceKeepsThePaneBlank(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()

	press(m, "ctrl+l")
	m.frame()
	first := m.pinSeq
	press(m, "ctrl+l")

	if m.pinSeq == 0 || m.pinSeq != first {
		t.Fatalf("pinSeq = %d, want %d", m.pinSeq, first)
	}
	blankTop(t, m)

	addLines(m, "api", "fresh")
	if got := paneTop(m); !strings.Contains(got, "fresh") {
		t.Fatalf("pane top = %q, want the newest line after a second clear", got)
	}
}

// A target row clear works from scrollback: it resumes the tail first.
func TestClearOnATargetRowFromScrollbackResumesAndBlanks(t *testing.T) {
	m := newTestModel(t, target("api"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "pgup")
	if m.follow {
		t.Fatal("setup: scrolling up did not freeze the tail")
	}

	press(m, "ctrl+l")
	m.frame()

	if !m.follow {
		t.Fatal("ctrl+l from scrollback did not resume the tail")
	}
	noMarkerText(t, m)
	blankTop(t, m)
}

// A group header clear works from scrollback: it resumes the tail first.
func TestClearOnAGroupHeaderFromScrollbackResumesAndBlanks(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	addLines(m, "api", streamLines("api", 30)...)
	addLines(m, "worker", streamLines("worker", 10)...)
	m.frame()
	m.cursor = 1 // the backend header
	m.liveSelect()
	press(m, "pgup")
	if m.follow {
		t.Fatal("setup: scrolling up did not freeze the tail")
	}

	press(m, "ctrl+l")
	m.frame()

	if !m.follow {
		t.Fatal("ctrl+l from scrollback did not resume the tail")
	}
	noMarkerText(t, m)
	blankTop(t, m)
}

// A focused unscoped pane clears the all-targets view without scoping the pane.
func TestClearInAFocusedUnscopedPaneKeepsTheView(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	addLines(m, "api", "api-1")
	addLines(m, "web", "web-1")
	m.frame()
	press(m, "right")
	if !m.paneFocus {
		t.Fatal("right did not focus the pane")
	}
	if m.solo >= 0 || m.schemaSel != "" {
		t.Fatal("setup: the pane must start unscoped")
	}

	press(m, "ctrl+l")
	m.frame()

	noMarkerText(t, m)
	blankTop(t, m)
	if m.solo >= 0 || m.schemaSel != "" {
		t.Fatalf("ctrl+l changed the scope: solo=%d schema=%q", m.solo, m.schemaSel)
	}
}

// ctrl+l also asks for a full repaint, since iTerm2's Cmd+K clears behind the app.
func TestCtrlLRepaintsTheScreen(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.frame()
	cmd := press(m, "ctrl+l")
	if cmd == nil || cmd() != tea.ClearScreen() {
		t.Fatal("ctrl+l did not return the screen repaint")
	}
}
