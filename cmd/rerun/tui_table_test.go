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
	m.View()

	press(m, "c")
	if !m.table {
		t.Fatal("c did not turn table mode on")
	}
	lines := paneOf(m)
	if got := strings.TrimRight(lines[0], " "); got != "web  GET     200" {
		t.Fatalf("row 0 = %q, want the padded table row", got)
	}
	if got := strings.TrimRight(lines[1], " "); got != "web  DELETE  500" {
		t.Fatalf("row 1 = %q, want the wider method column", got)
	}

	press(m, "c")
	if m.table {
		t.Fatal("c did not turn table mode off")
	}
	if got := strings.TrimRight(paneOf(m)[0], " "); got != "web  "+jsonGet {
		t.Fatalf("raw row = %q, want the untouched JSON line", got)
	}
}

// Table mode is presentation only: the raw line, and so search, filter, and
// copy, stays exactly as it was.
func TestTableModeKeepsCopyAndFilterOnRawLines(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet)
	m.View()
	press(m, "c")

	if got := m.visibleRaw[0]; got != jsonGet {
		t.Fatalf("visibleRaw = %q, want the raw JSON line", got)
	}

	press(m, "/")
	typeString(m, "method")
	m.commitInput()
	if len(m.searchMatches) != 1 {
		t.Fatalf("search matched %v in table mode, want the raw JSON line", m.searchMatches)
	}
	press(m, "esc")

	press(m, "v")
	typeString(m, ".request.method")
	m.commitInput()
	if m.paneLen() != 1 {
		t.Fatalf("filter kept %d rows, want the raw line to match", m.paneLen())
	}
	press(m, "esc")

	x := paneX(m)
	m.handleMouse(mouse(x, 0, false))
	m.handleMouse(release(x, 0))
	if got := decodeCopy(t, m.copySeq); got != jsonGet+"\n" {
		t.Fatalf("copied %q, want the raw line", got)
	}
}

// A row with no columns, or one whose line is not a JSON object, keeps its
// raw text, so the ALL view can mix both kinds.
func TestTableMixesRawAndColumnRows(t *testing.T) {
	m := newTestModel(t, tableTarget("web"), target("api"))
	addLines(m, "web", jsonGet)
	addLines(m, "api", "plain log line")
	m.View()

	press(m, "c")
	lines := paneOf(m)
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
	m.View()

	press(m, "c")
	lines := paneOf(m)
	if got := strings.TrimRight(lines[0], " "); got != "bad  "+jsonGet {
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
	m.View()

	press(m, "c")
	if m.table {
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
	m.View()
	press(m, "c")
	m.View()
	first := m.cellEvals
	if first != 2 {
		t.Fatalf("cellEvals = %d, want the two rows evaluated once", first)
	}

	addLines(m, "web", jsonGet, jsonGet, jsonGet)
	m.View()
	if got := m.cellEvals; got != first+3 {
		t.Fatalf("cellEvals = %d, want %d: old rows were re-evaluated", got, first+3)
	}
}

// One pathological cell cannot blow the row or the frame.
func TestTableWidthsAreCappedByThePane(t *testing.T) {
	huge := strings.Repeat("x", 500)
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", fmt.Sprintf(`{"request":{"method":%q},"response":{"status":1}}`, huge))
	m.View()
	press(m, "c")

	out := m.tableLines(m.paneLines())
	if got := m.tableWidths()[0]; got > m.tableBudget() {
		t.Fatalf("width = %d, want it capped at %d", got, m.tableBudget())
	}
	if got := displayWidth(out[0]); got > m.paneWidth() {
		t.Fatalf("row width = %d, want it inside the %d-cell pane", got, m.paneWidth())
	}
}

// A solo flip rebuilds the visible list, so the table cache is dropped and
// the surviving rows are evaluated again.
func TestTableCellsInvalidatedOnSoloFlip(t *testing.T) {
	m := newTestModel(t, tableTarget("web"), tableTarget("api"))
	addLines(m, "web", jsonGet)
	addLines(m, "api", jsonPost)
	m.View()
	press(m, "c")
	m.View()
	before := m.cellEvals

	m.cursor = 1 // web
	press(m, "s")
	m.View()
	if got := m.cellEvals; got <= before {
		t.Fatalf("cellEvals = %d after a solo flip, want more than %d", got, before)
	}
}

// The hint is transient, like the m/s one.
func TestTableHintExpires(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.View()
	press(m, "c")
	m.now = m.hintUntil
	if got := bottomPaneLine(m); strings.Contains(got, tableHint) {
		t.Fatalf("the table hint outlived its window: %q", got)
	}
}
