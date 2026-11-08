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
	if !strings.Contains(box, "find in pane…") {
		t.Fatalf("box = %q, want the find placeholder", box)
	}
	if !strings.Contains(box, "enter keep · ctrl+g next") {
		t.Fatalf("box = %q, want the keys footer", box)
	}
	if top := boxTop(m); !strings.Contains(top, "find in pane…") {
		t.Fatalf("pane top = %q, want the box anchored there", top)
	}
	if mid := strings.Split(ansi.Strip(m.frame()), "\n")[m.height/2]; strings.Contains(mid, "find in pane…") {
		t.Fatalf("box reached the middle of the logs: %q", mid)
	}

	_, rightW := m.layoutWidths()
	if got, want := m.inputBoxW(), min(rightW, max(modalMinWidth, rightW-2)); got != want {
		t.Fatalf("box width = %d, want the pane's width less two: %d", got, want)
	}
}

// The filter box names its own placeholder and counts the lines it keeps.
func TestFilterBoxPlaceholderAndCount(t *testing.T) {
	m := alphaModel(t)
	m.frame()
	press(m, "/")
	typeString(m, "alpha")

	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, "filter lines…") && !strings.Contains(box, "alpha") {
		t.Fatalf("box = %q, want the filter placeholder or the typed query", box)
	}
	if !strings.Contains(box, "2 lines") {
		t.Fatalf("box = %q, want the 2 kept lines in the header", box)
	}
}

// Every printable key lands in the query, n and N included.
func TestSearchBoxTypesN(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 5).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "naN")

	if m.input != "naN" || m.search != "naN" {
		t.Fatalf("input=%q search=%q, want n, a and N typed into the query", m.input, m.search)
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
	press(m, "ctrl+f")
	typeString(m, "line")
	before := m.searchRow

	press(m, "ctrl+g")
	if m.inputMode != inputSearch {
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

	press(m, "ctrl+f")
	typeString(m, "line")
	start := m.searchRow
	press(m, "ctrl+g")
	if m.searchRow <= start {
		t.Fatalf("ctrl+g did not advance from %d", start)
	}
	press(m, "ctrl+shift+g")
	if m.searchRow != start {
		t.Fatalf("ctrl+shift+g = %d, want back at %d", m.searchRow, start)
	}
	if m.inputMode != inputSearch {
		t.Fatalf("inputMode = %d, want the box to stay open", m.inputMode)
	}

	press(m, "enter")
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

// enter closes the box but keeps the query and its highlights; n and N then step.
func TestSearchBoxEnterKeepsQueryThenNJJumps(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "line")
	press(m, "enter")

	if m.inputMode != inputNone || m.search != "line" {
		t.Fatalf("enter: mode=%d search=%q, want the box closed and the query kept", m.inputMode, m.search)
	}
	if len(m.searchMatches) != 60 {
		t.Fatalf("highlights = %d, want 60 kept", len(m.searchMatches))
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

// esc in the box clears the query and its highlights.
func TestSearchBoxEscClears(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 10).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "line")
	if len(m.searchMatches) == 0 {
		t.Fatal("the query matched nothing")
	}

	press(m, "esc")
	if m.search != "" || m.searchRow != -1 || len(m.searchMatches) != 0 || m.inputMode != inputNone {
		t.Fatalf("esc: search=%q row=%d matches=%d mode=%d, want cleared and closed",
			m.search, m.searchRow, len(m.searchMatches), m.inputMode)
	}
}

// The bar keeps the chips but gives up the prompt to the box.
func TestBarHasNoPromptWhileBoxOpen(t *testing.T) {
	m := alphaModel(t)
	m.frame()

	press(m, "ctrl+f")
	typeString(m, "alpha")
	if got := paneOf(m)[0]; strings.Contains(got, "find alpha") {
		t.Fatalf("bar line = %q, want no find prompt", got)
	}
	press(m, "esc")

	press(m, "/")
	typeString(m, "alpha")
	if got := paneOf(m)[0]; strings.Contains(got, "/ alpha") {
		t.Fatalf("bar line = %q, want no filter prompt", got)
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
	if m.inputMode != inputSearch {
		t.Fatalf("mode = %d after ctrl+h, want the find box still open", m.inputMode)
	}
}

// ctrl+h on an empty input removes the last chip, exactly as backspace does.
func TestCtrlHOnEmptyInputRemovesLastChip(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "/", "beta")

	press(m, "v", "ctrl+h")
	if len(m.filters) != 1 || m.filters[0].query != "alpha" || m.inputMode != inputFilter {
		t.Fatalf("filters = %+v mode = %d after ctrl+h, want alpha kept and the input open", m.filters, m.inputMode)
	}
}

// The filter box has its own footer; the find box keeps the ctrl+g hint.
func TestInputBoxFootersPerMode(t *testing.T) {
	m := alphaModel(t)

	press(m, "ctrl+f")
	if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, "enter keep · ctrl+g next") {
		t.Fatalf("find box = %q, want its ctrl+g footer", box)
	}
	press(m, "esc")

	press(m, "/")
	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, "enter keep · esc close") {
		t.Fatalf("filter box = %q, want its own footer", box)
	}
	if strings.Contains(box, "ctrl+g next") {
		t.Fatalf("filter box = %q, want no ctrl+g hint", box)
	}
}

// The find box names the previous-match key only when the box can hold the longer footer.
func TestInputBoxFooterGrowsWithWidth(t *testing.T) {
	m := alphaModel(t)
	long := "enter keep · ctrl+g next · ctrl+shift+g prev · esc clear"
	short := "enter keep · ctrl+g next · esc clear"

	press(m, "ctrl+f")
	m.width, m.height = 200, 20
	if innerW := m.modalInnerW(m.inputBoxW()); innerW < displayWidth(long) {
		t.Fatalf("wide innerW = %d, want at least %d", innerW, displayWidth(long))
	}
	if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, long) {
		t.Fatalf("wide find box = %q, want the four-key footer %q", box, long)
	}

	// A middling pane keeps today's shorter text instead of showing a cut hint.
	m.width, m.height = 80, 20
	if innerW := m.modalInnerW(m.inputBoxW()); innerW < displayWidth(short) || innerW >= displayWidth(long) {
		t.Fatalf("mid innerW = %d, want it to fit %d but not %d", innerW, displayWidth(short), displayWidth(long))
	}
	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, short) {
		t.Fatalf("mid find box = %q, want %q", box, short)
	}
	if strings.Contains(box, "ctrl+shift+g") {
		t.Fatalf("narrow find box = %q, want no ctrl+shift+g hint", box)
	}
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
