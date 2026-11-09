package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// originalBody is the modal as drawn, styling stripped.
func originalBody(m *tuiModel) string { return ansi.Strip(m.originalView()) }

// openPaneOriginal focuses the pane and presses o on it.
func openPaneOriginal(m *tuiModel) {
	m.focusPane()
	press(m, "o")
}

func TestOriginalOpensCursorLineRawWithoutANSI(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "one", "\x1b[31mred\x1b[0m line")
	m.frame()
	openPaneOriginal(m)

	if !m.original.open {
		t.Fatal("o in the focused pane did not open the modal")
	}
	if want := []string{"red line"}; !reflect.DeepEqual(m.original.lines, want) {
		t.Fatalf("lines = %q, want %q", m.original.lines, want)
	}
	if strings.Contains(originalBody(m), "\x1b") {
		t.Fatal("modal drew escape sequences")
	}
}

func TestOriginalWrapsWideRunesInsideTheModal(t *testing.T) {
	m := newTestModel(t, target("web"))
	long := strings.Repeat("漢字", 30) + " tail"
	addLines(m, "web", long)
	m.width, m.height = 80, 24
	m.frame()
	openPaneOriginal(m)

	outerW, _ := m.modalSize()
	rows := m.originalRows(m.modalInnerW(outerW))
	if len(rows) < 2 {
		t.Fatalf("rows = %d, want the long line wrapped", len(rows))
	}
	for _, r := range rows {
		if w := displayWidth(r); w > m.modalInnerW(outerW) {
			t.Fatalf("row %q is %d cells, inner width is %d", r, w, m.modalInnerW(outerW))
		}
	}
	if got := strings.Join(rows, ""); got != long {
		t.Fatalf("wrapped rows join to %q, want the raw line", got)
	}
}

func TestOriginalFitsAn80x24Terminal(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	addLines(m, "web", strings.Repeat("x", 300), `{"a":1}`, "plain")
	m.frame()
	openPaneOriginal(m)

	lines := strings.Split(originalBody(m), "\n")
	if len(lines) > 24 {
		t.Fatalf("modal is %d rows on a 24-row terminal", len(lines))
	}
	for _, l := range lines {
		if displayWidth(l) > 80 {
			t.Fatalf("modal row %q is wider than 80 cells", l)
		}
	}
}

func TestOriginalSelectionShowsEachLineRaw(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"a":1}`, "plain", `{"b":2}`)
	m.frame()
	m.pauseTail()
	m.focusPane()
	press(m, "shift+up", "shift+up")
	press(m, "o")

	want := []string{`{"a":1}`, "plain", `{"b":2}`}
	if !reflect.DeepEqual(m.original.lines, want) {
		t.Fatalf("lines = %q, want %q", m.original.lines, want)
	}
	if m.original.pretty != nil {
		t.Fatal("pretty offered although one line is not JSON")
	}
}

func TestOriginalAltISelectsKeptLinesForTheModal(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "GET /a 200", "POST /b 500", "GET /c 200")
	m.frame()
	commitPill(m, "GET")
	m.focusPane()
	press(m, "alt+i", "o")

	want := []string{"GET /a 200", "GET /c 200"}
	if !reflect.DeepEqual(m.original.lines, want) {
		t.Fatalf("lines = %q, want the filtered log %q", m.original.lines, want)
	}
	if !strings.Contains(originalBody(m), "[GET]") {
		t.Fatalf("header does not name the active pill:\n%s", originalBody(m))
	}
}

func TestOriginalAltINeedsThePaneFocus(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "one", "two")
	m.frame()
	press(m, "alt+i")

	if m.selAnchor != nil {
		t.Fatal("alt+i selected lines with the list focused")
	}
}

func TestOriginalFTogglesPrettyJSON(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"a":1,"b":{"c":2}}`)
	m.frame()
	openPaneOriginal(m)

	if !strings.Contains(originalBody(m), "f pretty") {
		t.Fatalf("footer does not offer f on JSON lines:\n%s", originalBody(m))
	}
	press(m, "f")
	body := originalBody(m)
	if !strings.Contains(body, "pretty") || !strings.Contains(body, `"a": 1`) {
		t.Fatalf("f did not show pretty JSON:\n%s", body)
	}
	if !strings.Contains(body, "f raw") {
		t.Fatalf("footer does not offer f raw in pretty mode:\n%s", body)
	}
}

func TestOriginalFIsNoOpOnMixedLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"a":1}`, "plain")
	m.frame()
	m.pauseTail()
	m.focusPane()
	press(m, "alt+i", "o")

	body := originalBody(m)
	if strings.Contains(body, "f pretty") || strings.Contains(body, "f raw") {
		t.Fatalf("footer lists f on mixed lines:\n%s", body)
	}
	press(m, "f")
	if m.original.prettyOn || !strings.Contains(originalBody(m), "raw") {
		t.Fatal("f changed the view on mixed lines")
	}
}

func TestOriginalCopyPayloadEqualsModalText(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"a":1}`)
	m.frame()
	openPaneOriginal(m)
	press(m, "f")

	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatal("c returned no clipboard command")
	}
	got := reflect.ValueOf(cmd()).String()
	want := strings.Join(m.original.text(), "\n") + "\n"
	if got != want {
		t.Fatalf("clipboard = %q, want the modal text %q", got, want)
	}
	if !strings.Contains(originalBody(m), "copied 1 line") {
		t.Fatalf("footer has no copy notice:\n%s", originalBody(m))
	}
}

func TestOriginalScrollsAndCloses(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.height = 12
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "row")
	}
	addLines(m, "web", lines...)
	m.frame()
	m.focusPane()
	press(m, "alt+i", "o")

	press(m, "pgdown")
	if m.original.top == 0 {
		t.Fatal("pgdown did not scroll the modal")
	}
	press(m, "k")
	before := m.original.top
	press(m, "j")
	if m.original.top != before+1 {
		t.Fatalf("j moved top to %d, want %d", m.original.top, before+1)
	}
	press(m, "esc")
	if m.original.open {
		t.Fatal("esc did not close the modal")
	}
}

func TestOriginalListFocusedOKeepsOpeningTheTarget(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "one")
	m.frame()
	press(m, "o")

	if m.original.open {
		t.Fatal("o with the list focused opened the original modal")
	}
}

// alt+i over the whole pane keeps only service lines: rerun's exit rule is not output.
func TestOriginalAltISkipsRerunRules(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "one", "two")
	m.applyEvents([]event{{kind: evExited, target: "web", code: 1}})
	m.frame()
	m.focusPane()
	press(m, "alt+i", "o")

	if want := []string{"one", "two"}; !reflect.DeepEqual(m.original.lines, want) {
		t.Fatalf("lines = %q, want only the service lines %q", m.original.lines, want)
	}
}

// The copy payload keeps tabs; only the drawn modal expands them.
func TestOriginalCopyKeepsTabs(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "a\tb")
	m.frame()
	openPaneOriginal(m)

	if !strings.Contains(originalBody(m), "a       b") {
		t.Fatalf("modal does not draw the tab as spaces:\n%s", originalBody(m))
	}
	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatal("c returned no clipboard command")
	}
	if got := reflect.ValueOf(cmd()).String(); got != "a\tb\n" {
		t.Fatalf("clipboard = %q, want the tab kept", got)
	}
}

// A wheel or click behind the open modal must not unpause the tail or move the list.
func TestOriginalModalSwallowsMouse(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	addLines(m, "web", "w1", "w2")
	m.applyEvents(streamEvents("api", "a", 60).events)
	m.frame()
	openPaneOriginal(m)
	if !m.original.open {
		t.Fatal("o did not open the modal")
	}
	cursor := m.cursor

	m.handleMouse(wheelUp(paneX(m), 5))
	m.handleMouse(wheelDown(1, 1))
	click(m, 1, 1)

	if !m.follow {
		t.Error("a wheel behind the modal paused the tail")
	}
	if m.cursor != cursor {
		t.Errorf("cursor = %d, want %d: a click behind the modal moved the list", m.cursor, cursor)
	}
	if !m.original.open {
		t.Error("the modal closed")
	}
	if m.selAnchor != nil {
		t.Error("a click behind the modal armed a pane selection")
	}
}

// The modal's c shares the pane's clipboard cap and says when it cut the copy short.
func TestOriginalCopyIsCapped(t *testing.T) {
	m := newTestModel(t, target("web"))
	big := strings.Repeat("x", 40<<10)
	addLines(m, "web", big, big)
	m.frame()
	m.focusPane()
	press(m, "alt+i", "o")

	_, cmd := m.Update(keyMsg("c"))
	got := clipboardText(t, cmd)
	if len(got) > copyCap {
		t.Fatalf("clipboard payload is %d bytes, want at most the %d cap", len(got), copyCap)
	}
	if want := big + "\n"; got != want {
		t.Fatalf("clipboard = %d bytes, want the first line only", len(got))
	}
	if !strings.Contains(originalBody(m), "copied 1 line (truncated)") {
		t.Fatalf("footer has no truncation notice:\n%s", originalBody(m))
	}
}
