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

func TestTableAlignsColumnValues(t *testing.T) {
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
}

// Table mode is presentation only: the raw line, and so search, filter, and
// copy, stays exactly as it was.
func TestTableModeKeepsCopyAndFilterOnRawLines(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet)
	m.frame()
	if !m.tableHeaderOn() {
		t.Fatal("a target with columns did not start as a table")
	}

	if got := m.visibleRaw[0]; got != jsonGet {
		t.Fatalf("visibleRaw = %q, want the raw JSON line", got)
	}

	press(m, "f")
	typeString(m, "method")
	press(m, "enter")
	if len(m.searchMatches) != 1 {
		t.Fatalf("search matched %v in table mode, want the raw JSON line", m.searchMatches)
	}
	press(m, "esc")

	press(m, "/")
	typeString(m, ".request.method")
	press(m, "enter")
	if m.paneLen() != 1 {
		t.Fatalf("filter kept %d rows, want the raw line to match", m.paneLen())
	}
	press(m, "esc")

	x, y := paneX(m), m.headerRows()
	copied := clipboardText(t, click(m, x, y))
	if copied != jsonGet+"\n" {
		t.Fatalf("copied %q, want the raw line", copied)
	}
}

// A row with no columns, or one whose line is not a JSON object, keeps its
// raw text, so the merged view can mix both kinds.
func TestTableMixesRawAndColumnRows(t *testing.T) {
	web, api := tableTarget("web"), target("api")
	web.Schema, api.Schema = "backend", "backend"
	m := newTestModel(t, web, api)
	addLines(m, "web", jsonGet)
	addLines(m, "api", "plain log line")
	m.applyScope(-1, "backend")
	m.frame()

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

	lines := paneOf(m)[m.titleHeight():]
	if got := dropMarker(lines[0]); got != "bad  "+jsonGet {
		t.Fatalf("bad-expr row = %q, want raw text", got)
	}
	if got := strings.TrimRight(lines[1], " "); got != "bad  not json at all" {
		t.Fatalf("non-JSON row = %q, want raw text", got)
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
	before := m.cellEvals

	m.cursor = 1 // web
	m.liveSelect()
	m.frame()
	if got := m.cellEvals; got != before {
		t.Fatalf("cellEvals = %d after a solo flip, want %d: cached cells were re-decoded", got, before)
	}
}

// dropMarker is a JSON row without its (+) cell, which every JSON row carries.
func dropMarker(row string) string {
	return strings.TrimRight(strings.TrimSuffix(strings.TrimRight(row, " "), "(+)"), " ")
}
