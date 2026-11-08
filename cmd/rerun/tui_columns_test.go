package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func namedColumns(name string, cols ...string) Target {
	t := target(name)
	for _, c := range cols {
		t.Columns = append(t.Columns, Column{Name: c, Expr: "." + c})
	}
	return t
}

func tableRows(m *tuiModel) []string {
	var rows []string
	for _, line := range paneOf(m)[m.titleHeight():] {
		rows = append(rows, dropMarker(ansi.Strip(line)))
	}
	return rows
}

func TestCellsStayPutAfterTheSampleFreezes(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	lines := make([]string, sampleValues)
	for i := range lines {
		lines[i] = jsonGet
	}
	addLines(m, "web", lines...)
	m.frame()
	col := strings.Index(tableRows(m)[0], "200")

	addLines(m, "web", `{"request":{"method":"A-VERY-LONG-METHOD-NAME-THAT-KEEPS-GOING"},"response":{"status":500}}`)
	if got := strings.Index(tableRows(m)[0], "200"); got != col {
		t.Fatalf("status moved from column %d to %d when a longer row arrived", col, got)
	}
}

func TestNonJSONLineSharesTheBodyColumn(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet, "plain text here")
	m.frame()

	rows := tableRows(m)
	if got, want := strings.Index(rows[1], "plain"), strings.Index(rows[0], "GET"); got != want {
		t.Fatalf("raw text starts at column %d, table body at %d", got, want)
	}
}

func TestFixedWidthsByName(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "level", "time", "msg"))
	addLines(m, "web", `{"level":"info","time":"12:00:00","msg":"ok"}`)
	m.frame()

	budget := m.tableBudget()
	want := []int{5, 8, budget - (5 + 2 + 8 + 2)}
	if got := m.tableWidths(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("widths = %v, want %v", got, want)
	}
}

func TestLongValueEndsInEllipsis(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "msg", "status"))
	addLines(m, "web", fmt.Sprintf(`{"msg":%q,"status":200}`, strings.Repeat("x", 40)))
	m.frame()

	row := tableRows(m)[0]
	if !strings.Contains(row, strings.Repeat("x", 23)+"…  200") {
		t.Fatalf("row = %q, want msg cut to 24 cells with an ellipsis, then status", row)
	}
}

func TestTabAndNewlineStayOnOneRow(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "msg", "status"))
	addLines(m, "web", `{"msg":"a\tb\nc","status":200}`)
	m.frame()

	if got := len(m.paneLines()); got != 1 {
		t.Fatalf("pane lines = %d, want the row on one line", got)
	}
	if row := tableRows(m)[0]; !strings.Contains(row, "a b c") {
		t.Fatalf("row = %q, want the tab and newline shown as spaces", row)
	}
}

func TestColumnWidthFreezesAfterTheSample(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "msg", "status"))
	short := make([]string, sampleValues)
	for i := range short {
		short[i] = fmt.Sprintf(`{"msg":"ab","status":%d}`, i)
	}
	addLines(m, "web", short...)
	m.frame()
	frozen := m.tableWidths()[0]
	if want := displayWidth("msg") + columnMarkW; frozen != want {
		t.Fatalf("width = %d, want the name and its mark, %d", frozen, want)
	}

	addLines(m, "web", fmt.Sprintf(`{"msg":%q,"status":1}`, strings.Repeat("y", 20)))
	if got := m.tableWidths()[0]; got != frozen {
		t.Fatalf("width moved from %d to %d after the sample froze", frozen, got)
	}
}

func TestColumnWidthGrowsDuringTheSample(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "msg", "status"))
	addLines(m, "web", fmt.Sprintf(`{"msg":%q,"status":1}`, strings.Repeat("y", 12)))
	m.frame()
	if got := m.tableWidths()[0]; got != 12 {
		t.Fatalf("width = %d, want 12 while still sampling", got)
	}
}

func TestTimeColumnShowsTheLocalClock(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "time", "msg"))
	addLines(m,
		"web",
		`{"time":"2026-11-04T12:01:02Z","msg":"a"}`,
		`{"time":"not a time","msg":"b"}`,
		`{"time":1762257662,"msg":"c"}`,
		`{"time":1762257662123,"msg":"d"}`,
	)
	m.frame()
	rows := tableRows(m)

	want := time.Date(2026, 11, 4, 12, 1, 2, 0, time.UTC).In(time.Local).Format("15:04:05")
	if !strings.HasPrefix(rows[0], "web  "+want) {
		t.Fatalf("row 0 = %q, want the local clock %s", rows[0], want)
	}
	if !strings.HasPrefix(rows[1], "web  not a t…") {
		t.Fatalf("row 1 = %q, want the unparseable value cut to the 8-cell column", rows[1])
	}
	unixWant := time.Unix(1762257662, 0).Local().Format("15:04:05")
	if !strings.HasPrefix(rows[2], "web  "+unixWant) || !strings.HasPrefix(rows[3], "web  "+unixWant) {
		t.Fatalf("rows = %q, %q, want unix seconds and milliseconds shown as clocks %s", rows[2], rows[3], unixWant)
	}
}

func TestRepickedColumnsStartAFreshSample(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "msg", "status"))
	short := make([]string, sampleValues)
	for i := range short {
		short[i] = fmt.Sprintf(`{"msg":"ab","status":%d}`, i)
	}
	addLines(m, "web", short...)
	m.frame()

	m.targets[0].spec.Columns = []Column{{Name: "text", Expr: ".msg"}, {Name: "status", Expr: ".status"}}
	m.targets[0].colSpec = nil
	m.vpDirty = true
	m.frame()
	if got, want := m.tableWidths()[0], displayWidth("text")+columnMarkW; got != want {
		t.Fatalf("width = %d after re-picking, want the new name and its mark, %d", got, want)
	}
}
