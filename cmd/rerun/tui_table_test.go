package main

import (
	"fmt"
	"strings"
	"testing"
)

// tableTarget is a target whose rows render as a columns table.
func tableTarget(name string) Target {
	t := target(name)
	t.Columns = []Column{
		{Name: "method", Expr: ".request.method"},
		{Name: "status", Expr: ".response.status"},
	}
	return t
}

const (
	jsonGet  = `{"request":{"method":"GET"},"response":{"status":200}}`
	jsonPost = `{"request":{"method":"DELETE"},"response":{"status":500}}`
)

func TestTableToggleAlignsColumnValues(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet, jsonPost)
	m.frame()

	if !m.tableOn() {
		t.Fatal("a target with columns did not start in table mode")
	}
	lines := paneOf(m)[m.titleHeight():]
	row0, row1 := dropMarker(lines[0]), dropMarker(lines[1])
	if !strings.HasPrefix(row0, "web  GET") || !strings.HasPrefix(row1, "web  DELETE") {
		t.Fatalf("rows = %q, %q, want the name prefix then the method", row0, row1)
	}
	if strings.Index(row0, "200") != strings.Index(row1, "500") {
		t.Fatalf("status values start at %d and %d, want the same column", strings.Index(row0, "200"), strings.Index(row1, "500"))
	}

	press(m, "c")
	if m.tableOn() {
		t.Fatal("c did not turn table mode off")
	}
	if got := dropMarker(paneOf(m)[m.titleHeight()]); got != "web  "+jsonGet {
		t.Fatalf("raw row = %q, want the untouched JSON line", got)
	}
}

// Table mode is presentation only: the raw line, and so search, filter, and
// copy, stays exactly as it was.
func TestTableModeKeepsCopyAndFilterOnRawLines(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet)
	m.frame()
	press(m, "c")

	if got := m.visibleRaw[0]; got != jsonGet {
		t.Fatalf("visibleRaw = %q, want the raw JSON line", got)
	}

	press(m, "ctrl+f")
	typeString(m, "method")
	press(m, "enter")
	if len(m.searchMatches) != 1 {
		t.Fatalf("search matched %v in table mode, want the raw JSON line", m.searchMatches)
	}
	press(m, "esc")

	press(m, "v")
	typeString(m, ".request.method")
	press(m, "enter")
	if m.paneLen() != 1 {
		t.Fatalf("filter kept %d rows, want the raw line to match", m.paneLen())
	}
	press(m, "esc")

	x, y := paneX(m), m.headerRows()
	m.handleMouse(mouse(x, y, false))
	m.handleMouse(release(x, y))
	if got := decodeCopy(t, m.copySeq); got != jsonGet+"\n" {
		t.Fatalf("copied %q, want the raw line", got)
	}
}

// A row with no columns, or one whose line is not a JSON object, keeps its
// raw text, so the merged view can mix both kinds.
func TestTableMixesRawAndColumnRows(t *testing.T) {
	m := newTestModel(t, tableTarget("web"), target("api"))
	addLines(m, "web", jsonGet)
	addLines(m, "api", "plain log line")
	m.frame()

	press(m, "c")
	lines := paneOf(m)[m.titleHeight():]
	if got := strings.TrimRight(lines[0], " "); !strings.Contains(got, "GET") || strings.Contains(got, jsonGet) {
		t.Fatalf("web row = %q, want a table row", got)
	}
	if got := strings.TrimRight(lines[1], " "); got != "api  plain log line" {
		t.Fatalf("api row = %q, want the raw line", got)
	}
}

// A non-JSON line and a bad expression both fall back to raw text.
func TestTableFallsBackToRawOnBadLineOrExpr(t *testing.T) {
	bad := target("bad")
	bad.Columns = []Column{{Name: "a", Expr: ".["}}
	m := newTestModel(t, bad)
	addLines(m, "bad", jsonGet, "not json at all")
	m.frame()

	press(m, "c")
	lines := paneOf(m)[m.titleHeight():]
	if got := dropMarker(lines[0]); got != "bad  "+jsonGet {
		t.Fatalf("bad-expr row = %q, want raw text", got)
	}
	if got := strings.TrimRight(lines[1], " "); got != "bad  not json at all" {
		t.Fatalf("non-JSON row = %q, want raw text", got)
	}
}

// c without any columns to show says so instead of toggling.
func TestTableHintsWithoutColumns(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "hi")
	m.frame()

	press(m, "c")
	if m.tableOn() {
		t.Fatal("c turned table mode on without any columns")
	}
	if got := bottomPaneLine(m); !strings.Contains(got, tableHint) {
		t.Fatalf("bottom line = %q, want the table hint", got)
	}
}

// A chatty stream evaluates each arriving line once: the rows already on
// screen are never parsed again.
func TestTableEvaluatesEachLineOnce(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet, jsonPost)
	m.frame()
	first := m.cellEvals
	if first != 2 {
		t.Fatalf("cellEvals = %d, want the two rows evaluated once", first)
	}

	addLines(m, "web", jsonGet, jsonGet, jsonGet)
	m.frame()
	if got := m.cellEvals; got != first+3 {
		t.Fatalf("cellEvals = %d, want %d: old rows were re-evaluated", got, first+3)
	}
}

// One pathological cell cannot blow the row or the frame.
func TestTableWidthsAreCappedByThePane(t *testing.T) {
	huge := strings.Repeat("x", 500)
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", fmt.Sprintf(`{"request":{"method":%q},"response":{"status":1}}`, huge))
	m.frame()

	out := m.tableLines(m.paneLines())
	if got := m.tableWidths()[0]; got > m.tableBudget() {
		t.Fatalf("width = %d, want it capped at %d", got, m.tableBudget())
	}
	if got := displayWidth(out[0]); got > m.paneWidth() {
		t.Fatalf("row width = %d, want it inside the %d-cell pane", got, m.paneWidth())
	}
}

func TestTableCellsReusedOnSoloFlip(t *testing.T) {
	m := newTestModel(t, tableTarget("web"), tableTarget("api"))
	addLines(m, "web", jsonGet)
	addLines(m, "api", jsonPost)
	m.frame()
	press(m, "c")
	m.frame()
	before := m.cellEvals

	m.cursor = 1 // web
	m.liveSelect()
	m.frame()
	if got := m.cellEvals; got != before {
		t.Fatalf("cellEvals = %d after a solo flip, want %d: cached cells were re-decoded", got, before)
	}
}

// The hint is transient, like the m/s one.
func TestTableHintExpires(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	press(m, "c")
	m.now = m.hintUntil
	if got := bottomPaneLine(m); strings.Contains(got, tableHint) {
		t.Fatalf("the table hint outlived its window: %q", got)
	}
}

// dropMarker is a JSON row without its (+) cell, which every JSON row carries.
func dropMarker(row string) string {
	return strings.TrimRight(strings.TrimSuffix(strings.TrimRight(row, " "), "(+)"), " ")
}
