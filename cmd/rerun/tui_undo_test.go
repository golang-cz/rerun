package main

import (
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

func undoSnap(s string) inputSnap { return inputSnap{s, utf8.RuneCountInString(s)} }

// undoTypeInto types s at the end of text one rune per edit, as keypresses do.
func undoTypeInto(u *undoStack, text, s string) string {
	for _, r := range s {
		to := text + string(r)
		u.edit(undoSnap(text), undoSnap(to), r)
		text = to
	}
	return text
}

// undoAlt is alt plus a letter; the shifted form carries its uppercase text.
func undoAlt(letter rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: letter, Mod: tea.ModAlt} }

func undoAltShift(letter rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: letter, Mod: tea.ModAlt | tea.ModShift, Text: string(letter - 'a' + 'A')}
}

func TestUndoStackGroupsWordsAndBreaksAtSpace(t *testing.T) {
	var u undoStack
	cur := undoTypeInto(&u, "", "hello world")
	for _, want := range []string{"hello ", ""} {
		s, ok := u.undoStep(undoSnap(cur))
		if !ok || s.text != want {
			t.Fatalf("undo = %q, %v; want %q", s.text, ok, want)
		}
		cur = s.text
	}
	if _, ok := u.undoStep(undoSnap(cur)); ok {
		t.Fatal("undo past the first step succeeded")
	}
}

func TestUndoStackRedoRoundTripsAndEditClearsIt(t *testing.T) {
	var u undoStack
	undoTypeInto(&u, "", "ab")
	s, _ := u.undoStep(undoSnap("ab"))
	if r, ok := u.redoStep(undoSnap(s.text)); !ok || r.text != "ab" {
		t.Fatalf("redo = %q, %v; want %q", r.text, ok, "ab")
	}
	s, _ = u.undoStep(undoSnap("ab"))
	cur := undoTypeInto(&u, s.text, "c")
	if r, ok := u.redoStep(undoSnap(cur)); ok {
		t.Fatalf("redo after a new edit restored %q; want nothing", r.text)
	}
}

func TestUndoStackBoundsAreNoOps(t *testing.T) {
	var u undoStack
	if s, ok := u.undoStep(undoSnap("x")); ok || s != undoSnap("x") {
		t.Fatalf("undo on empty = %v, %v; want the state unchanged", s, ok)
	}
	if s, ok := u.redoStep(undoSnap("x")); ok || s != undoSnap("x") {
		t.Fatalf("redo on empty = %v, %v; want the state unchanged", s, ok)
	}
}

func TestUndoStackDeletesAndPastesAreOwnSteps(t *testing.T) {
	var u undoStack
	undoTypeInto(&u, "", "ab")
	u.edit(undoSnap("ab"), undoSnap("a"), 0)
	u.edit(undoSnap("a"), undoSnap(""), 0)
	for _, want := range []string{"a", "ab", ""} {
		s, ok := u.undoStep(undoSnap("x"))
		if !ok || s.text != want {
			t.Fatalf("undo = %q, %v; want %q", s.text, ok, want)
		}
	}

	var p undoStack
	undoTypeInto(&p, "", "a")
	p.edit(undoSnap("a"), undoSnap("axyz"), 0)
	cur := undoTypeInto(&p, "axyz", "q")
	if s, _ := p.undoStep(undoSnap(cur)); s.text != "axyz" {
		t.Fatalf("undo after typing past a paste = %q; want the paste kept", s.text)
	}
	if s, _ := p.undoStep(undoSnap("axyz")); s.text != "a" {
		t.Fatalf("undo of the paste = %q; want %q", s.text, "a")
	}
}

func TestUndoStackTypingAfterMoveStartsNewStep(t *testing.T) {
	var u undoStack
	undoTypeInto(&u, "", "ab")
	// The cursor moved to the start before this letter, so it cannot join "ab".
	u.edit(inputSnap{"ab", 0}, inputSnap{"cab", 1}, 'c')
	if s, _ := u.undoStep(undoSnap("cab")); s != (inputSnap{"ab", 0}) {
		t.Fatalf("undo = %v; want the text and cursor before the move", s)
	}
}

func TestBarAltZUndoesWordAndAltYRedoes(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "alpha beta")

	m.Update(undoAlt('z'))
	if m.input != "alpha " {
		t.Fatalf("input after alt+z = %q, want %q", m.input, "alpha ")
	}
	m.Update(undoAlt('z'))
	if m.input != "" || m.draftOn {
		t.Fatalf("input = %q draftOn = %v after two undos, want empty with no draft", m.input, m.draftOn)
	}
	m.Update(undoAlt('y'))
	if m.input != "alpha " {
		t.Fatalf("input after alt+y = %q, want %q", m.input, "alpha ")
	}
	m.Update(undoAltShift('z'))
	if m.input != "alpha beta" || !m.draftOn {
		t.Fatalf("input = %q draftOn = %v after alt+shift+z, want the full text drafted", m.input, m.draftOn)
	}
}

func TestBarAltShiftZAndAltUpperZRedo(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "ab")
	m.Update(undoAlt('z'))
	m.Update(tea.KeyPressMsg{Code: 'Z', Mod: tea.ModAlt})
	if m.input != "ab" {
		t.Fatalf("input after alt+Z = %q, want %q", m.input, "ab")
	}
}

func TestBarUndoRestoresCursor(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "alpha beta")
	press(m, "left", "left", "left", "left")
	typeString(m, "X")
	m.Update(undoAlt('z'))
	if m.input != "alpha beta" || m.inputCursor != 6 {
		t.Fatalf("input = %q cursor = %d after undo, want %q at 6", m.input, m.inputCursor, "alpha beta")
	}
}

func TestBarUndoAtBottomIsNoOpAndRedoRestores(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "ab")
	for range 5 {
		m.Update(undoAlt('z'))
	}
	if m.input != "" {
		t.Fatalf("input = %q after undo past the bottom, want empty", m.input)
	}
	m.Update(undoAlt('y'))
	if m.input != "ab" {
		t.Fatalf("input = %q after redo, want %q", m.input, "ab")
	}
}

func TestBarNewEditAfterUndoClearsRedo(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "ab")
	m.Update(undoAlt('z'))
	typeString(m, "c")
	m.Update(undoAlt('y'))
	if m.input != "c" {
		t.Fatalf("input = %q after redo, want %q: the redo stack should be empty", m.input, "c")
	}
}

func TestBarBackspaceAndPasteAreOwnSteps(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "abc")
	press(m, "backspace")
	m.Update(undoAlt('z'))
	if m.input != "abc" {
		t.Fatalf("input after undo of backspace = %q, want %q", m.input, "abc")
	}
	m.Update(tea.PasteMsg{Content: "def"})
	m.Update(undoAlt('z'))
	if m.input != "abc" {
		t.Fatalf("input after undo of paste = %q, want %q", m.input, "abc")
	}
}

func TestBarHistoryStartsFreshOnOpen(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "alpha")
	press(m, "esc")
	press(m, "/")
	m.Update(undoAlt('z'))
	if m.input != "" {
		t.Fatalf("input = %q after alt+z on a fresh bar, want empty", m.input)
	}
}

func TestFinderAltZUndoesAndRefilters(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("db"))
	all := len(m.finderResults(""))
	m.Update(ctrlK)
	typeString(m, "api")
	if n := len(m.finderResults(m.finder.query)); n >= all {
		t.Fatalf("query %q kept %d of %d rows, want a filter", m.finder.query, n, all)
	}
	m.finder.sel = 1
	m.Update(undoAlt('z'))
	if m.finder.query != "" || m.finder.sel != 0 {
		t.Fatalf("query = %q sel = %d after alt+z, want empty with the selection reset", m.finder.query, m.finder.sel)
	}
	if n := len(m.finderResults(m.finder.query)); n != all {
		t.Fatalf("rows after undo = %d, want the unfiltered %d", n, all)
	}
	m.Update(undoAlt('y'))
	if m.finder.query != "api" {
		t.Fatalf("query after alt+y = %q, want %q", m.finder.query, "api")
	}
	m.Update(undoAltShift('z'))
	if m.finder.query != "api" {
		t.Fatalf("query after alt+shift+z = %q, want %q", m.finder.query, "api")
	}
}

func TestFinderBackspaceAndPasteAreOwnSteps(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.Update(ctrlK)
	typeString(m, "abc")
	press(m, "backspace")
	m.Update(undoAlt('z'))
	if m.finder.query != "abc" {
		t.Fatalf("query after undo of backspace = %q, want %q", m.finder.query, "abc")
	}
	m.Update(tea.PasteMsg{Content: "def"})
	m.Update(undoAlt('z'))
	if m.finder.query != "abc" {
		t.Fatalf("query after undo of paste = %q, want %q", m.finder.query, "abc")
	}
}

func TestFinderHistoryStartsFreshOnOpen(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.Update(ctrlK)
	typeString(m, "web")
	press(m, "esc")
	m.Update(ctrlK)
	m.Update(undoAlt('z'))
	m.Update(undoAlt('y'))
	if m.finder.query != "" || !m.finder.open {
		t.Fatalf("query = %q open = %v on a fresh finder, want empty and open", m.finder.query, m.finder.open)
	}
}

func TestLinkPickerAltZUndoes(t *testing.T) {
	web := target("web")
	web.Open = "http://localhost:8080"
	m := newTestModel(t, web, target("api"))
	press(m, "o")
	typeString(m, "80")
	m.Update(undoAlt('z'))
	if !m.finder.open || !m.finder.links || m.finder.query != "" {
		t.Fatalf("open = %v links = %v query = %q after alt+z, want the link picker open and cleared", m.finder.open, m.finder.links, m.finder.query)
	}
}

// alt+z right after a pill loads into the bar restores the pill's text, not an empty bar.
func TestUndoAfterPillEditKeepsPill(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	press(m, "/", "up", "enter")
	if m.input != "alpha" {
		t.Fatalf("input after editing the pill = %q, want %q", m.input, "alpha")
	}
	m.Update(undoAlt('z'))
	if m.input != "alpha" {
		t.Fatalf("input after alt+z = %q, want the pill text kept", m.input)
	}
	press(m, "enter")
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" {
		t.Fatalf("pills = %+v after enter, want one alpha pill", m.pills)
	}
}

// The @ pick clears the input in code; alt+z must not bring back the picked text.
func TestUndoAfterAtPickDoesNotDuplicatePill(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@hi")
	press(m, "enter")
	m.Update(undoAlt('z'))
	if m.input != "" {
		t.Fatalf("input after alt+z = %q, want empty", m.input)
	}
	press(m, "enter")
	if len(m.pills) != 1 || m.pills[0].raw != "@hits" {
		t.Fatalf("pills = %+v, want one [@hits] pill", m.pills)
	}
}
