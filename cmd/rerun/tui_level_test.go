package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	blueAdaptive   = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	greenAdaptive  = lipgloss.AdaptiveColor{Light: "28", Dark: "46"}
	yellowAdaptive = lipgloss.AdaptiveColor{Light: "136", Dark: "220"}
	redAdaptive    = lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
)

func levelTableTarget(name string) Target {
	t := target(name)
	t.Columns = []Column{
		{Name: "method", Expr: ".request.method"},
		{Name: "level", Expr: ".level"},
	}
	return t
}

// levelOfRow is the span of the row's level, failing the test when it has none.
func levelOfRow(t *testing.T, m *tuiModel, row int) levelSpan {
	t.Helper()
	span, ok := m.levelFor(row, m.visible[row], nil, m.paneWidth())
	if !ok {
		t.Fatalf("row %d has no level", row)
	}
	return span
}

func TestLevelPaintMapping(t *testing.T) {
	cases := []struct {
		word  string
		want  levelPaint
		known bool
	}{
		{"debug", levelPaint{fg: blueAdaptive}, true},
		{"TRACE", levelPaint{fg: blueAdaptive}, true},
		{" Default ", levelPaint{fg: blueAdaptive}, true},
		{"info", levelPaint{fg: greenAdaptive}, true},
		{"Notice", levelPaint{fg: greenAdaptive}, true},
		{"WARN", levelPaint{fg: yellowAdaptive}, true},
		{"warning", levelPaint{fg: yellowAdaptive}, true},
		{"error", levelPaint{fg: redAdaptive, whole: true}, true},
		{"ERR", levelPaint{fg: redAdaptive, whole: true}, true},
		{"alert", levelPaint{fg: lipgloss.Color("15"), bg: redAdaptive, bold: true, whole: true}, true},
		{"Critical", levelPaint{fg: lipgloss.Color("15"), bg: redAdaptive, bold: true, whole: true}, true},
		{"crit", levelPaint{fg: lipgloss.Color("15"), bg: redAdaptive, bold: true, whole: true}, true},
		{"FATAL", levelPaint{fg: lipgloss.Color("15"), bg: redAdaptive, bold: true, whole: true}, true},
		{"emergency", levelPaint{fg: lipgloss.Color("15"), bg: redAdaptive, bold: true, whole: true}, true},
		{"panic", levelPaint{fg: lipgloss.Color("15"), bg: redAdaptive, bold: true, whole: true}, true},
		{"bogus", levelPaint{}, false},
	}
	for _, c := range cases {
		got, ok := levelPaintOf(c.word)
		if ok != c.known || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: paint %+v known %v, want %+v known %v", c.word, got, ok, c.want, c.known)
		}
	}
}

func TestJSONLevelKeyOrderAndCase(t *testing.T) {
	cases := []struct {
		line, word, value string
	}{
		{`{"msg":"x","severity":"WARN","level":"info"}`, "info", `"info"`},
		{`{"lvl":"error","severity":"debug"}`, "debug", `"debug"`},
		{`{"LEVEL":"debug"}`, "debug", `"debug"`},
		{`{"Severity":"alert"}`, "alert", `"alert"`},
		{`{"level":42}`, "42", "42"},
		{`{"msg":"no level here"}`, "", ""},
	}
	for _, c := range cases {
		hit := jsonLevel(c.line)
		if hit.word != c.word {
			t.Errorf("%s: word %q, want %q", c.line, hit.word, c.word)
		}
		if got := c.line[hit.from:hit.to]; hit.word != "" && got != c.value {
			t.Errorf("%s: span %q, want %q", c.line, got, c.value)
		}
	}
}

func TestINFOColorsOnlyTheLevelCell(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"level":"INFO","msg":"up"}`)
	span := levelOfRow(t, m, 0)
	if span.paint.whole || span.paint.bg != nil || span.paint.fg != greenAdaptive {
		t.Fatalf("paint = %+v, want green on the cell only", span.paint)
	}
	if got := sliceCells(ansi.Strip(m.visible[0]), span.from, span.to); got != `"INFO"` {
		t.Fatalf("painted cells = %q, want the level value", got)
	}
}

func TestERRORLineIsWholeRed(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"level":"error","msg":"boom"}`)
	span := levelOfRow(t, m, 0)
	if !span.paint.whole || span.paint.fg != redAdaptive || span.paint.bg != nil {
		t.Fatalf("paint = %+v, want red over the whole text", span.paint)
	}
	if span.text != m.prefixW+2 || span.to != displayWidth(m.visible[0]) {
		t.Fatalf("text starts at %d, to %d; want %d and the line end", span.text, span.to, m.prefixW+2)
	}
}

func TestALERTIsScreaming(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"level":"ALERT","msg":"disk"}`)
	span := levelOfRow(t, m, 0)
	if span.paint.bg != redAdaptive || !span.paint.bold {
		t.Fatalf("paint = %+v, want bold on a red background", span.paint)
	}
	if got := paintSpan(m.visible[0], span); ansi.Strip(got) != m.visible[0] {
		t.Fatalf("painted line text = %q, want %q", ansi.Strip(got), m.visible[0])
	}
}

func TestPlainTextLinesAreNotColored(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "ERROR: not json")
	if _, ok := m.levelFor(0, m.visible[0], nil, m.paneWidth()); ok {
		t.Fatal("a plain-text line got a level color")
	}
}

func TestLevelPaintKeepsWidths(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web",
		`{"level":"info","msg":"a"}`,
		`{"level":"error","msg":"bb"}`,
		`{"level":"alert","msg":"ccc"}`,
		"plain text",
	)
	out := m.paintLevels(m.visible)
	for i, line := range out {
		if ansi.Strip(line) != m.visible[i] || displayWidth(line) != displayWidth(m.visible[i]) {
			t.Fatalf("row %d = %q, want %q at the same width", i, ansi.Strip(line), m.visible[i])
		}
	}
}

func TestTableLevelColumnIsPainted(t *testing.T) {
	m := newTestModel(t, levelTableTarget("web"))
	addLines(m, "web", `{"request":{"method":"GET"},"level":"WARN"}`)
	m.View()
	if !m.tableOn() {
		t.Fatal("expected table mode")
	}
	m.evaluateCells()
	widths := m.tableWidths()
	span, ok := m.levelFor(0, m.visible[0], widths, m.paneWidth())
	if !ok || span.paint.fg != yellowAdaptive {
		t.Fatalf("span = %+v ok %v, want the yellow level column", span, ok)
	}
	table := ansi.Strip(m.tableLines(m.visible)[0])
	if got := strings.TrimSpace(sliceCells(table, span.from, span.to)); got != "WARN" {
		t.Fatalf("painted cells = %q, want WARN", got)
	}
}

func TestCopyStaysPlainWhenLevelsAreColored(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"level":"error","msg":"boom"}`)
	for _, line := range m.copyLines(0, 0) {
		if strings.Contains(line, "\x1b") {
			t.Fatalf("copied line carries ANSI: %q", line)
		}
	}
}
