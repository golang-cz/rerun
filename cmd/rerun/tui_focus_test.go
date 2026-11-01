package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
	before := paneOf(m)[0]

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
	after := paneOf(m)[0]
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

func TestFocusedArrowsScrollWithoutMovingCursor(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.View()

	press(m, "right")
	at := m.vp.YOffset
	if at == 0 {
		t.Fatal("content is not taller than the pane; test cannot scroll")
	}

	press(m, "up")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	if m.follow {
		t.Fatal("scrolling up did not freeze the tail")
	}
	if m.vp.YOffset != at-1 {
		t.Fatalf("YOffset = %d, want %d", m.vp.YOffset, at-1)
	}

	press(m, "down")
	if m.vp.YOffset != at || !m.follow {
		t.Fatalf("scrolling back: YOffset = %d follow = %v, want %d true", m.vp.YOffset, m.follow, at)
	}
}

func TestUnfocusedArrowsMoveCursor(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))

	press(m, "down", "down")
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.cursor)
	}
	press(m, "up")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	// j/k stay on the cursor even while the pane holds focus.
	press(m, "right", "j")
	if m.cursor != 2 {
		t.Fatalf("j while focused: cursor = %d, want 2", m.cursor)
	}
}

func TestFocusedHomeAndEnd(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.View()

	press(m, "right", "right")
	press(m, "home")
	if m.vp.YOffset != 0 {
		t.Fatalf("home: YOffset = %d, want 0", m.vp.YOffset)
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
	if m.cursor != len(m.targets) {
		t.Fatalf("end: cursor = %d, want %d", m.cursor, len(m.targets))
	}
	press(m, "home")
	if m.cursor != 0 {
		t.Fatalf("home: cursor = %d, want 0", m.cursor)
	}
}

func TestFocusedEscReturnsToListAndKeepsScroll(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.View()

	m.cursor = 1
	press(m, "s")
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0", m.solo)
	}

	press(m, "right", "up")
	off := m.vp.YOffset

	press(m, "esc")
	if m.paneFocus {
		t.Fatal("esc did not return focus to the list")
	}
	if m.vp.YOffset != off {
		t.Fatalf("esc changed the scroll: YOffset = %d, want %d", m.vp.YOffset, off)
	}
	if m.solo != 0 {
		t.Fatal("esc cleared solo while the pane was focused")
	}

	press(m, "esc")
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
	// TrueColor (termenv.Profile 0), named here so termenv stays an indirect
	// dependency. Without it the renderer strips styling off a pipe.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0)
	defer lipgloss.SetColorProfile(prev)

	m := newTestModel(t, target("web"))
	focused := m.styles.dividerFocus.Render("┃")
	plain := m.styles.divider.Render("│")
	if !strings.Contains(focused, "┃") || !strings.Contains(focused, "\x1b[36m") {
		t.Fatalf("focused divider = %q, want the heavy bar in cyan", focused)
	}

	view := m.View()
	if !strings.Contains(view, plain) {
		t.Fatal("divider is not the plain bar while the list has focus")
	}
	if strings.Contains(view, focused) {
		t.Fatal("heavy divider drawn while the list has focus")
	}

	press(m, "right")
	view = m.View()
	if !strings.Contains(view, focused) {
		t.Fatal("divider is not the heavy cyan bar while the pane has focus")
	}
	if strings.Contains(view, plain) {
		t.Fatal("plain divider still drawn while the pane has focus")
	}
}
