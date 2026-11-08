package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// promptCell locates the input prompt glyph in the rendered frame, so a cursor
// assertion is derived from what was drawn, not from a hard-coded cell.
func promptCell(t *testing.T, frame string) (x, y int) {
	t.Helper()
	for i, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		if j := strings.Index(plain, inputPrompt); j >= 0 {
			return displayWidth(plain[:j]), i
		}
	}
	t.Fatalf("the frame has no input prompt:\n%s", frame)
	return 0, 0
}

// caretCell is where the terminal cursor belongs: after the prompt and the visible text.
func caretCell(t *testing.T, frame, typed string) (x, y int) {
	t.Helper()
	x, y = promptCell(t, frame)
	return x + displayWidth(inputPrompt) + displayWidth(typed), y
}

// The finder draws the real terminal cursor at its caret, and typing moves it.
func TestFinderCursorSitsAtTheCaret(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.Update(ctrlK)

	view := m.View()
	if view.Cursor == nil {
		t.Fatal("the open finder drew no terminal cursor")
	}
	if wantX, wantY := caretCell(t, view.Content, ""); view.Cursor.X != wantX || view.Cursor.Y != wantY {
		t.Fatalf("cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
	}

	press(m, "a", "b")
	view = m.View()
	if wantX, wantY := caretCell(t, view.Content, "ab"); view.Cursor.X != wantX || view.Cursor.Y != wantY {
		t.Fatalf("after typing ab cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
	}

	// A wide rune takes two cells, so the caret moves by two.
	press(m, "世")
	view = m.View()
	if wantX, wantY := caretCell(t, view.Content, "ab世"); view.Cursor.X != wantX || view.Cursor.Y != wantY {
		t.Fatalf("after a wide rune cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
	}
}

// The search box draws the real terminal cursor at its caret, over the pane's overlay column.
func TestSearchBoxCursorSitsAtTheCaret(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "alpha", "beta")
	m.frame()
	press(m, "ctrl+f")

	view := m.View()
	if view.Cursor == nil {
		t.Fatal("the open search box drew no terminal cursor")
	}
	if wantX, wantY := caretCell(t, view.Content, ""); view.Cursor.X != wantX || view.Cursor.Y != wantY {
		t.Fatalf("cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
	}

	typeString(m, "alpha")
	view = m.View()
	if wantX, wantY := caretCell(t, view.Content, "alpha"); view.Cursor.X != wantX || view.Cursor.Y != wantY {
		t.Fatalf("after typing cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
	}
}

// With no input open the frame declares no cursor, so the terminal keeps its own.
func TestNoInputDrawsNoTerminalCursor(t *testing.T) {
	m := newTestModel(t, target("web"))
	if m.View().Cursor != nil {
		t.Fatal("a closed finder and bar still drew a terminal cursor")
	}
	m.Update(ctrlK)
	if m.View().Cursor == nil {
		t.Fatal("the finder drew no cursor, so the check below is vacuous")
	}
	press(m, "esc")
	if m.View().Cursor != nil {
		t.Fatal("closing the finder left the terminal cursor behind")
	}

	// The config-error modal owns the keys over an open input, so the caret goes with them.
	press(m, "ctrl+f")
	m.cfgErr = "boom"
	if m.View().Cursor != nil {
		t.Fatal("a config-error modal left the input's terminal cursor drawn")
	}
}
