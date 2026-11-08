package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const jsonReq = `{"level":"info","status":403,"request":{"method":"GET"}}`

// findInPane is the screen cell and row where needle first appears in the pane.
func findInPane(t *testing.T, m *tuiModel, needle string) (int, int) {
	t.Helper()
	leftW, _ := m.layoutWidths()
	for y, line := range paneOf(m) {
		plain := ansi.Strip(line)
		if i := strings.Index(plain, needle); i >= 0 {
			return leftW + 1 + displayWidth(plain[:i]), y
		}
	}
	t.Fatalf("%q is not on the pane", needle)
	return 0, 0
}

func plainPane(m *tuiModel) string {
	var b strings.Builder
	for _, line := range paneOf(m) {
		b.WriteString(ansi.Strip(line) + "\n")
	}
	return b.String()
}

// A JSON object line ends in the (+) cell at the pane's right edge; other lines do not.
func TestJSONLinesGetMarkerAtRightEdge(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq, "plain text")
	lines := paneOf(m)
	_, rightW := m.layoutWidths()

	row := ansi.Strip(lines[m.titleHeight()])
	if !strings.HasSuffix(row, " (+)") {
		t.Fatalf("JSON row = %q, want the (+) cell at the right edge", row)
	}
	if got := displayWidth(lines[m.titleHeight()]); got != rightW {
		t.Fatalf("JSON row is %d cells, want the full pane width %d", got, rightW)
	}
	if plain := ansi.Strip(lines[m.titleHeight()+1]); strings.Contains(plain, "(+)") {
		t.Fatalf("plain row = %q, want no marker", plain)
	}
}

// Enter on the focused pane expands the bottom line, and enter again collapses it.
func TestEnterExpandsAndCollapsesTheBottomLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()

	press(m, "right", "enter")
	if !strings.Contains(plainPane(m), `status: 403`) {
		t.Fatalf("expanded pane lacks the pretty block:\n%s", plainPane(m))
	}
	if !strings.Contains(plainPane(m), "(−)") {
		t.Fatalf("expanded line lacks the (−) marker:\n%s", plainPane(m))
	}

	press(m, "enter")
	if strings.Contains(plainPane(m), `status: 403`) {
		t.Fatalf("collapsed pane still shows the block:\n%s", plainPane(m))
	}
}

// → on the focused pane expands the cursor line, exactly like enter; → again changes nothing.
func TestRightArrowExpandsTheCursorLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()

	press(m, "right", "right") // focus the pane, then expand
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatalf("→ did not expand the cursor line:\n%s", plainPane(m))
	}
	if m.paneCol != 0 {
		t.Fatalf("paneCol = %d, want the expansion instead of a pan", m.paneCol)
	}
	rows := m.paneLen()

	press(m, "right")
	if !m.expanded[m.visibleSeq[0]] || m.paneLen() != rows || m.paneCol != 0 {
		t.Fatalf("→ on an open line: expanded=%v rows=%d col=%d, want no change",
			m.expanded[m.visibleSeq[0]], m.paneLen(), m.paneCol)
	}
}

// ← on a row of an expansion collapses the line and puts the cursor back on it.
func TestLeftArrowCollapsesAnExpansionRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	press(m, "right", "right", "down") // focus, expand, step onto the first expansion row
	if row := m.paneCursorRow(); row != 1 {
		t.Fatalf("pane cursor = row %d, want 1, an expansion row", row)
	}

	press(m, "left")
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("← on an expansion row left the line expanded")
	}
	if m.paneCurRaw != 0 || m.paneCurSub != -1 {
		t.Fatalf("cursor at raw=%d sub=%d, want the parent line", m.paneCurRaw, m.paneCurSub)
	}
	if !m.paneFocus {
		t.Fatal("← on an expansion row dropped the pane focus")
	}
	if got := plainPane(m); strings.Contains(got, `status: 403`) {
		t.Fatalf("pane still shows the block:\n%s", got)
	}
}

// ← on the expanded line itself collapses it and keeps its focus.
func TestLeftArrowOnAnExpandedLineCollapsesIt(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	press(m, "right", "right")
	if m.paneCurSub != -1 {
		t.Fatalf("cursor sub = %d, want the expanded line itself", m.paneCurSub)
	}

	press(m, "left")
	if m.expanded[m.visibleSeq[0]] || !m.paneFocus || m.paneCurRaw != 0 {
		t.Fatalf("← on an open line: expanded=%v focus=%v raw=%d, want it collapsed in place",
			m.expanded[m.visibleSeq[0]], m.paneFocus, m.paneCurRaw)
	}
}

// ← on a collapsed line keeps the old behavior: focus goes back to the list.
func TestLeftArrowOnACollapsedLineReturnsToList(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	press(m, "right") // focus the pane only

	press(m, "left")
	if m.paneFocus || m.paneCol != 0 {
		t.Fatalf("← on a collapsed line: focus=%v col=%d, want the list", m.paneFocus, m.paneCol)
	}
}

// Clicking the (+) cell expands the line, and clicking the (−) cell collapses it.
func TestClickingTheMarkerExpandsAndCollapses(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := m.width-2, m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("clicking (+) did not expand the line")
	}
	m.handleMouse(mouse(x, y, false))
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("clicking (−) did not collapse the line")
	}
}

// An expanded line stays expanded when newer lines arrive.
func TestExpansionSurvivesNewLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.toggleExpand(0)
	addLines(m, "web", "later", `{"status":200}`)

	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("new lines collapsed the expanded line")
	}
	if !strings.Contains(plainPane(m), `status: 403`) {
		t.Fatalf("the block vanished after new lines:\n%s", plainPane(m))
	}
}

// Clicking a key on a schema's selection adds the column to the schema and marks
// the session dirty, and the footer says so.
func TestKeyClickAddsColumnToSelectedSchema(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	addLines(m, "api", jsonReq)
	m.frame()
	m.liveSelect()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `status: `)
	m.handleMouse(mouse(x+1, y, false))

	want := []colEdit{{node: colNode{schema: true, name: "backend"}, cols: []Column{{Name: "status", Expr: ".status"}}}}
	if !reflect.DeepEqual(m.edits, want) {
		t.Fatalf("edits = %+v, want %+v", m.edits, want)
	}
	if got := m.targets[0].spec.Columns; len(got) != 1 || got[0].Name != "status" {
		t.Fatalf("api columns = %+v, want the picked column in the table", got)
	}
	if !m.tableOn() {
		t.Fatal("picking a column did not switch the table on")
	}
	if !strings.Contains(plainPane(m), unsavedNotice) {
		t.Fatalf("footer lacks the unsaved notice:\n%s", plainPane(m))
	}
}

// Clicking a key with only a target soloed adds the column to that target.
func TestKeyClickAddsColumnToSoloedTarget(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.liveSelect()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `level: `)
	m.handleMouse(mouse(x+1, y, false))

	if len(m.edits) != 1 || m.edits[0].node != (colNode{name: "web"}) || m.edits[0].cols[0].Expr != ".level" {
		t.Fatalf("edits = %+v, want a column on target web", m.edits)
	}
}

// With nothing selected, a key click is a no-op with a hint.
func TestKeyClickWithoutSelectionHints(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `status: `)
	m.handleMouse(mouse(x+1, y, false))

	if m.hint != selectHint {
		t.Fatalf("hint = %q, want %q", m.hint, selectHint)
	}
	if m.dirty() {
		t.Fatal("a no-op key click marked the session dirty")
	}
}

// Clicking a scalar adds a chip filter that keeps only matching lines.
func TestValueClickAddsChipFilter(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq, `{"status":200}`, "plain")
	m.frame()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `status: 403`)
	m.handleMouse(mouse(x+len(`status: `)+1, y, false))

	if len(m.chips) != 1 || m.chips[0].expr != ".status == 403" {
		t.Fatalf("chips = %+v, want .status == 403", m.chips)
	}
	if got := strings.Join(m.paneLines(), "\n"); strings.Contains(got, "200") || strings.Contains(got, "plain") {
		t.Fatalf("filtered pane = %q, want only the 403 line", got)
	}
	if !strings.Contains(plainPane(m), "[.status == 403 ×]") {
		t.Fatalf("header lacks the chip:\n%s", plainPane(m))
	}
}

// A chip goes when its × is clicked, and the last one goes on backspace with the pane focused.
func TestChipRemovalByCrossAndBackspace(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `status: 403`)
	m.handleMouse(mouse(x+len(`status: `)+1, y, false))
	x, y = findInPane(t, m, "×")
	m.handleMouse(mouse(x, y, false))
	if len(m.chips) != 0 {
		t.Fatalf("chips = %+v after clicking ×, want none", m.chips)
	}

	x, y = findInPane(t, m, `status: 403`)
	m.handleMouse(mouse(x+len(`status: `)+1, y, false))
	press(m, "right", "backspace")
	if len(m.chips) != 0 {
		t.Fatalf("chips = %+v after backspace, want none", m.chips)
	}
}

// Clicking a column's × in the table header removes that column and marks the session dirty.
func TestHeaderClickRemovesAColumn(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet, jsonPost)
	m.frame()
	m.liveSelect()

	x, y := findInPane(t, m, "method ×")
	m.handleMouse(mouse(x+len("method "), y, false))

	if got := m.headerColumns(); len(got) != 1 || got[0].Name != "status" {
		t.Fatalf("columns = %+v, want only status", got)
	}
	if !m.dirty() {
		t.Fatal("removing a column did not mark the session dirty")
	}
}

// An error notice outranks the unsaved footer, and the footer returns once it clears.
func TestErrorNoticeOutranksUnsavedFooter(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.liveSelect()
	m.toggleExpand(0)
	x, y := findInPane(t, m, `level: `)
	m.handleMouse(mouse(x+1, y, false))

	m.showNotice("rerun: boom", true)
	if got := plainPane(m); !strings.Contains(got, "rerun: boom") || strings.Contains(got, unsavedNotice) {
		t.Fatalf("footer = %q, want the error alone", got)
	}
	press(m, "esc")
	if got := plainPane(m); !strings.Contains(got, unsavedNotice) {
		t.Fatalf("footer = %q, want the unsaved notice back", got)
	}
}

const schemaConfig = `schemas:
  backend:
    columns: ['level {.level}']

api:
  schema: backend
  cmd: sleep 1

web:
  cmd: sleep 1
`

// ctrl+s writes the selected schema's columns, clears the dirty flag, and keeps the chips
// and the selection across the config reload that follows.
func TestSaveWritesSchemaAndSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerun.yml")
	if err := os.WriteFile(path, []byte(schemaConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(cfg.Targets)
	m.width, m.height = 100, 20
	m.group.configPath = path

	m.liveSelect()
	addLines(m, "api", jsonReq)
	m.frame()
	m.toggleExpand(0)
	x, y := findInPane(t, m, `status: `)
	m.handleMouse(mouse(x+1, y, false))
	m.pickValue(&jsonField{path: ".level", lit: `"info"`})

	press(m, "ctrl+s")
	if m.dirty() {
		t.Fatal("ctrl+s left the session dirty")
	}
	if !strings.Contains(plainPane(m), "saved columns to rerun.yml") {
		t.Fatalf("footer lacks the save notice:\n%s", plainPane(m))
	}
	saved, err := ParseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Targets[targetPos(saved, "api")].Columns; len(got) != 2 || got[1].Name != "status" {
		t.Fatalf("saved api columns = %+v, want level and status", got)
	}
	if got := saved.Targets[targetPos(saved, "web")].Columns; len(got) != 0 {
		t.Fatalf("web columns = %+v, want none", got)
	}

	runners := make([]*targetRunner, len(saved.Targets))
	for i, spec := range saved.Targets {
		runners[i] = m.targets[m.targetIndex(spec.Name)].runner
	}
	m.applyEvent(event{kind: evReconcile, cfg: &configChange{
		targets: saved.Targets, runners: runners, stopped: make([]bool, len(runners)),
	}})
	if m.schemaSel != "backend" || len(m.chips) != 1 || !m.expanded[m.visibleSeq[0]] {
		t.Fatalf("reload reset state: schema=%q chips=%d expanded=%v", m.schemaSel, len(m.chips), m.expanded[m.visibleSeq[0]])
	}
	if m.noticeErr {
		t.Fatalf("reload flashed an error: %q", m.notice)
	}
}

func targetPos(cfg *Config, name string) int {
	for i, t := range cfg.Targets {
		if t.Name == name {
			return i
		}
	}
	return -1
}
