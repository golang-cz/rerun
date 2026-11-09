package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// paneOut appends one target line and returns the model, so a focus test can
// control exactly how wide the pane's content is.
func paneOut(m *tuiModel, name, line string) {
	m.applyEvents([]event{{kind: evOutput, target: name, line: line}})
}

func TestRightArrowFocusesThenPans(t *testing.T) {
	m := newTestModel(t, target("web"))
	paneOut(m, "web", strings.Repeat("x", 200))

	if m.paneFocus {
		t.Fatal("pane starts focused")
	}
	before := ansi.Strip(paneOf(m)[m.titleHeight()])

	press(m, "right")
	if !m.paneFocus {
		t.Fatal("right arrow did not focus the pane")
	}
	if m.paneCol != 0 {
		t.Fatalf("focusing scrolled to column %d, want 0", m.paneCol)
	}

	press(m, "right")
	if m.paneCol != 1 {
		t.Fatalf("paneCol = %d, want 1", m.paneCol)
	}
	after := ansi.Strip(paneOf(m)[m.titleHeight()])
	if after == before {
		t.Fatalf("pane line did not shift: %q", after)
	}
	if !strings.HasPrefix(after, before[1:]) {
		t.Fatalf("pane line = %q, want %q shifted one cell", after, before[1:])
	}
}

func TestLeftArrowUnfocusesAfterColumnZero(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	paneOut(m, "web", strings.Repeat("x", 200))

	press(m, "right", "right", "right")
	if m.paneCol != 2 {
		t.Fatalf("paneCol = %d, want 2", m.paneCol)
	}

	press(m, "left")
	if !m.paneFocus {
		t.Fatal("left arrow dropped focus before column 0")
	}
	if m.paneCol != 1 {
		t.Fatalf("paneCol = %d, want 1", m.paneCol)
	}

	press(m, "left")
	if !m.paneFocus || m.paneCol != 0 {
		t.Fatalf("at column 0: focus = %v, col = %d, want still focused", m.paneFocus, m.paneCol)
	}

	press(m, "left")
	if m.paneFocus || m.paneCol != 0 {
		t.Fatalf("left at column 0: focus = %v, col = %d, want unfocused", m.paneFocus, m.paneCol)
	}

	// Arrows move the cursor again once the list owns them.
	press(m, "down")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
}

// Focused up/down move the pane cursor, not the list cursor. Moving it pauses the tail; G resumes.
func TestFocusedArrowsMoveThePaneCursor(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()

	press(m, "right")
	newest := m.paneCursorRow()
	if newest == 0 {
		t.Fatal("content is not taller than the pane; test cannot move the cursor")
	}

	press(m, "up")
	if m.cursor != 0 {
		t.Fatalf("list cursor = %d, want 0", m.cursor)
	}
	if m.follow {
		t.Fatal("moving the cursor up did not pause the tail")
	}
	if got := m.paneCursorRow(); got != newest-1 {
		t.Fatalf("pane cursor = %d, want %d", got, newest-1)
	}

	press(m, "down")
	if got := m.paneCursorRow(); got != newest {
		t.Fatalf("pane cursor after down = %d, want %d", got, newest)
	}
	if m.follow {
		t.Fatal("moving the cursor down resumed the tail; only esc resumes it")
	}

	escToLive(m)
	if !m.follow {
		t.Fatal("esc did not resume the tail")
	}
}

func TestUnfocusedArrowsMoveCursor(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))

	press(m, "down", "down")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1: the last row stops it", m.cursor)
	}
	press(m, "up")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}

	// j/k stay on the cursor even while the pane holds focus.
	press(m, "right", "j")
	if m.cursor != 1 {
		t.Fatalf("j while focused: cursor = %d, want 1", m.cursor)
	}
}

func TestFocusedHomeAndEnd(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()

	press(m, "right", "right")
	press(m, "home")
	if m.vp.YOffset() != 0 {
		t.Fatalf("home: YOffset = %d, want 0", m.vp.YOffset())
	}
	if m.paneCol != 0 {
		t.Fatalf("home: paneCol = %d, want 0", m.paneCol)
	}

	press(m, "end")
	if !m.follow || !m.vp.AtBottom() {
		t.Fatalf("end: follow = %v atBottom = %v, want true", m.follow, m.vp.AtBottom())
	}
}

func TestUnfocusedHomeAndEndMoveCursor(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))

	press(m, "end")
	if m.cursor != len(m.targets)-1 {
		t.Fatalf("end: cursor = %d, want %d", m.cursor, len(m.targets)-1)
	}
	press(m, "home")
	if m.cursor != 0 {
		t.Fatalf("home: cursor = %d, want 0", m.cursor)
	}
}

func TestFocusedEscReturnsToListAndKeepsScroll(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()

	m.cursor = 0
	m.liveSelect()
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0", m.solo)
	}

	press(m, "right", "up")
	off := m.vp.YOffset()

	press(m, "esc")
	if m.paneFocus {
		t.Fatal("esc did not return focus to the list")
	}
	if m.vp.YOffset() != off {
		t.Fatalf("esc changed the scroll: YOffset = %d, want %d", m.vp.YOffset(), off)
	}
	if m.solo != 0 {
		t.Fatal("esc cleared solo while the pane was focused")
	}

	press(m, "esc", "esc") // the first esc returns to the live tail
	if m.solo != -1 {
		t.Fatal("unfocused esc did not clear solo")
	}
}

func TestPaneColClampsToWidestLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	paneOut(m, "web", strings.Repeat("x", 40))

	press(m, "right", "right")
	paneOf(m)
	if m.paneCol != 0 {
		t.Fatalf("narrow content: paneCol = %d, want 0", m.paneCol)
	}

	paneOut(m, "web", strings.Repeat("x", 400))
	press(m, "right")
	m.paneCol = 1000
	paneOf(m)

	paneW := m.paneWidth()
	widest := displayWidth(m.visible[len(m.visible)-1])
	if want := widest - paneW; m.paneCol != want {
		t.Fatalf("paneCol = %d, want %d (widest %d - pane %d)", m.paneCol, want, widest, paneW)
	}
}

// The focused divider switches to the heavy bar in cyan; the list keeps the
// plain gray bar.
func TestFocusedDividerIsHeavyCyan(t *testing.T) {
	m := newTestModel(t, target("web"))
	focused := m.styles.dividerFocus.Render("┃")
	plain := m.styles.divider.Render("│")
	if !strings.Contains(focused, "┃") || !strings.Contains(focused, "\x1b[36m") {
		t.Fatalf("focused divider = %q, want the heavy bar in cyan", focused)
	}

	view := m.View().Content
	if !strings.Contains(view, plain) {
		t.Fatal("divider is not the plain bar while the list has focus")
	}
	if strings.Contains(view, focused) {
		t.Fatal("heavy divider drawn while the list has focus")
	}

	press(m, "right")
	view = m.View().Content
	if !strings.Contains(view, focused) {
		t.Fatal("divider is not the heavy cyan bar while the pane has focus")
	}
	if strings.Contains(view, plain) {
		t.Fatal("plain divider still drawn while the pane has focus")
	}
}

// drawnRow is the left column's line for the list row named key, styling kept.
func drawnRow(t *testing.T, m *tuiModel, key string) string {
	t.Helper()
	m.frame()
	idx := m.indexOfKey(key)
	if idx < 0 {
		t.Fatalf("row %q is not listed", key)
	}
	leftW, _ := m.layoutWidths()
	return m.leftLine(idx-m.layout.offset+1, leftW)
}

// faintLine reports whether a drawn line carries the faint step-back style.
func faintLine(line string) bool { return strings.Contains(line, "\x1b[2m") }

// A focused pane dims every row it does not draw; its own row keeps full color.
func TestPaneFocusDimsRowsOutsideScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	press(m, "enter")
	if !m.paneFocus {
		t.Fatal("enter did not focus the pane")
	}

	web := drawnRow(t, m, "target:web")
	if !strings.Contains(web, "\x1b[") || faintLine(web) {
		t.Fatalf("in-scope row = %q, want full color", web)
	}
	api := drawnRow(t, m, "target:api")
	if !faintLine(api) {
		t.Fatalf("out-of-scope row = %q, want faint", api)
	}
}

// A group pane colors its own members and dims the headers and targets outside it.
func TestPaneFocusGroupDimsOutsiders(t *testing.T) {
	api, web, db := target("api"), target("web"), target("db")
	api.Schema, web.Schema, db.Schema = "svc", "svc", "ops"
	m := newTestModel(t, api, web, db)
	m.applyScope(-1, "svc")
	m.paneFocus = true

	for _, key := range []string{"schema:svc", "target:api", "target:web"} {
		if got := drawnRow(t, m, key); faintLine(got) {
			t.Fatalf("in-scope %s = %q, want full color", key, got)
		}
	}
	for _, key := range []string{"schema:ops", "target:db"} {
		if got := drawnRow(t, m, key); !faintLine(got) {
			t.Fatalf("out-of-scope %s = %q, want faint", key, got)
		}
	}
}

// The list owns the keys: no row is dimmed, and the cursor keeps its band.
func TestListFocusKeepsRowsBright(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	for _, key := range []string{"target:web", "target:api"} {
		got := drawnRow(t, m, key)
		if faintLine(got) {
			t.Fatalf("row %s = %q, want full color with the list focused", key, got)
		}
	}
	if web := drawnRow(t, m, "target:web"); !strings.Contains(web, "\x1b[48;") {
		t.Fatalf("cursor row = %q, want the cursor band", web)
	}
}

// Focusing the pane lands the cursor on the service's last line, skipping rerun's rules.
func TestFocusPutsCursorOnTheLastServiceLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "one", "two")
	m.applyEvents([]event{
		{kind: evExited, target: "web", restart: true, code: -1, status: "stopped for restart"},
		{kind: evExited, target: "web", code: 1},
	})
	m.frame()

	press(m, "right")
	if got := m.visibleRaw[m.paneCurRaw]; got != "two" {
		t.Fatalf("focus put the cursor on %q, want the service's last line", got)
	}
	if !m.follow {
		t.Fatal("focus paused the live tail under a rule line")
	}
}

// When the service's last line is off screen, focus lands on the last line instead.
func TestFocusOffScreenPutsCursorOnTheLastLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.applyEvents([]event{{kind: evExited, target: "web", code: 1}})
	m.frame()
	m.follow = false
	m.vp.SetYOffset(0)

	press(m, "right")
	if want := len(m.visibleRaw) - 1; m.paneCurRaw != want {
		t.Fatalf("cursor on raw %d, want the last line %d", m.paneCurRaw, want)
	}
}

// Enter on a list row focuses the pane the same way, with the cursor on the service's last line.
func TestEnterFocusPutsCursorOnTheLastServiceLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "one", "two")
	m.applyEvents([]event{{kind: evExited, target: "web", code: 1}})
	m.frame()

	press(m, "enter")
	if got := m.visibleRaw[m.paneCurRaw]; got != "two" {
		t.Fatalf("enter put the cursor on %q, want the service's last line", got)
	}
}
