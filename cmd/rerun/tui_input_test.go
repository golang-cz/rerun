package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// boxTop is the frame's right-pane top five rows — where the typing box is anchored — styling stripped.
func boxTop(m *tuiModel) string {
	frame := strings.Split(ansi.Strip(m.frame()), "\n")
	leftW, rightW := m.layoutWidths()
	out := make([]string, 0, min(5, len(frame)))
	for _, l := range frame[:min(5, len(frame))] {
		out = append(out, ansi.Cut(l, leftW+1, leftW+1+rightW))
	}
	return strings.Join(out, "\n")
}

func TestSearchBoxAtPaneTop(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "alpha", "beta")
	m.frame()
	press(m, "ctrl+f")

	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, "level=error") {
		t.Fatalf("box = %q, want the query placeholder", box)
	}
	if !strings.Contains(box, "enter add pill · esc close") {
		t.Fatalf("box = %q, want the keys footer", box)
	}
	if top := boxTop(m); !strings.Contains(top, "level=error") {
		t.Fatalf("pane top = %q, want the box anchored there", top)
	}
	if mid := strings.Split(ansi.Strip(m.frame()), "\n")[m.height/2]; strings.Contains(mid, "level=error") {
		t.Fatalf("box reached the middle of the logs: %q", mid)
	}

	_, rightW := m.layoutWidths()
	if got, want := m.inputBoxW(), min(rightW, max(inputMinWidth, rightW-2)); got != want {
		t.Fatalf("box width = %d, want the pane's width less two: %d", got, want)
	}
}

// The box's header counts the lines the typed draft keeps against the total.
func TestFilterBoxPlaceholderAndCount(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "alpha")

	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, "alpha") {
		t.Fatalf("box = %q, want the typed query", box)
	}
	if !strings.Contains(box, "2/4") {
		t.Fatalf("box = %q, want the 2 kept of 4 lines in the header", box)
	}
}

// Every printable key lands in the query, n and N included.
func TestSearchBoxTypesN(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 5).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "naN")

	if m.input != "naN" {
		t.Fatalf("input=%q, want n, a and N typed into the query", m.input)
	}
	if !strings.Contains(ansi.Strip(m.inputBoxView()), "naN") {
		t.Fatalf("box = %q, want the typed query", ansi.Strip(m.inputBoxView()))
	}
}

// ctrl+g steps to the next match with the box still open.
func TestSearchBoxCtrlGJumpsNext(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()
	commitPill(m, "line")
	before := m.searchRow

	press(m, "ctrl+f")
	press(m, "ctrl+g")
	if m.inputMode != inputQuery {
		t.Fatalf("inputMode = %d, want ctrl+g to keep the box open", m.inputMode)
	}
	if m.searchRow <= before {
		t.Fatalf("searchRow = %d after ctrl+g, want a row past %d", m.searchRow, before)
	}
}

// ctrl+shift+g is ctrl+g's mirror: it steps back a match, box open or closed.
func TestSearchCtrlShiftGJumpsPrevious(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()

	commitPill(m, "line")
	start := m.searchRow
	press(m, "ctrl+f")
	press(m, "ctrl+g")
	if m.searchRow <= start {
		t.Fatalf("ctrl+g did not advance from %d", start)
	}
	press(m, "ctrl+shift+g")
	if m.searchRow != start {
		t.Fatalf("ctrl+shift+g = %d, want back at %d", m.searchRow, start)
	}
	if m.inputMode != inputQuery {
		t.Fatalf("inputMode = %d, want the box to stay open", m.inputMode)
	}

	press(m, "esc")
	row := m.searchRow
	press(m, "ctrl+g")
	if m.searchRow <= row {
		t.Fatalf("closed ctrl+g did not advance from %d", row)
	}
	press(m, "ctrl+shift+g")
	if m.searchRow != row {
		t.Fatalf("closed ctrl+shift+g = %d, want back at %d", m.searchRow, row)
	}
}

// enter closes the box but keeps the pill and its hits; n and N then step.
func TestSearchBoxEnterKeepsQueryThenNJJumps(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "line")
	press(m, "enter")

	if m.inputMode != inputNone || len(m.pills) != 1 || m.pills[0].raw != "line" {
		t.Fatalf("enter: mode=%d pills=%v, want the box closed and the pill kept", m.inputMode, m.pills)
	}
	if len(m.searchMatches) != 60 {
		t.Fatalf("hits = %d, want 60 kept", len(m.searchMatches))
	}
	before := m.searchRow
	press(m, "n")
	if m.searchRow <= before {
		t.Fatalf("n did not advance from %d", before)
	}
	press(m, "N")
	if m.searchRow != before {
		t.Fatalf("N did not return to %d, got %d", before, m.searchRow)
	}
}

// esc in the box commits the typed pill and closes; the hits stay.
func TestSearchBoxEscKeepsHits(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 10).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "line")

	press(m, "esc")
	if len(m.pills) != 1 || len(m.searchMatches) == 0 || m.inputMode != inputNone {
		t.Fatalf("esc: pills=%v matches=%d mode=%d, want the pill kept and the box closed",
			m.pills, len(m.searchMatches), m.inputMode)
	}
}

// The bar shows only committed pills; the draft lives in the box alone.
func TestBarHasNoPromptWhileBoxOpen(t *testing.T) {
	m := alphaModel(t)
	m.frame()

	press(m, "ctrl+f")
	typeString(m, "alpha")
	if m.barRows() != 0 {
		t.Fatalf("pills row shown with no pills while the draft types")
	}
	press(m, "esc")
	if got := ansi.Strip(paneOf(m)[0]); !strings.Contains(got, "[alpha ×]") {
		t.Fatalf("bar line = %q, want the committed pill", got)
	}
}

// Kitty's disambiguation spells backspace as ctrl+h; the input must delete on both.
func TestCtrlHDeletesLikeBackspaceInInput(t *testing.T) {
	m := alphaModel(t)
	press(m, "ctrl+f")
	typeString(m, "alpha")
	press(m, "ctrl+h")
	if m.input != "alph" {
		t.Fatalf("input = %q after ctrl+h, want %q", m.input, "alph")
	}
	if m.inputMode != inputQuery {
		t.Fatalf("mode = %d after ctrl+h, want the query box still open", m.inputMode)
	}
}

// ctrl+h on an empty input removes the last chip, exactly as backspace does.
func TestCtrlHOnEmptyInputRemovesLastChip(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "/", "beta")

	press(m, "/", "ctrl+h")
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" || m.inputMode != inputQuery {
		t.Fatalf("pills = %+v mode = %d after ctrl+h, want alpha kept and the input open", m.pills, m.inputMode)
	}
}

// The box footer names its keys; the @ picker names its own.
func TestInputBoxFootersPerMode(t *testing.T) {
	m := alphaModel(t)

	press(m, "ctrl+f")
	if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, "enter add pill · esc close") {
		t.Fatalf("box = %q, want its footer", box)
	}
	typeString(m, "@")
	if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, "enter add · esc close") {
		t.Fatalf("picker box = %q, want the picker footer", box)
	}
	press(m, "esc")
}

// A narrow pane keeps the filter's line count beside the query.
func TestNarrowInputBoxKeepsTheCount(t *testing.T) {
	m := alphaModel(t)
	m.width, m.height = 30, 20
	press(m, "/")
	typeString(m, "alpha")

	count := m.inputCount()
	if count == "" {
		t.Fatal("the filter status is empty, so the check is vacuous")
	}
	if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, count) {
		t.Fatalf("box = %q, want the count %q kept", box, count)
	}
}
