package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
		b.WriteString(ansi.Strip(line))
		b.WriteString("\n")
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

	click(m, x, y)
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("clicking (+) did not expand the line")
	}
	click(m, x, y)
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("clicking (−) did not collapse the line")
	}
}

// The (+) cell toggles in table mode too; only the rest of a table line copies.
func TestClickingTheMarkerTogglesInTableMode(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet)
	m.frame()
	if !m.tableHeaderOn() {
		t.Fatal("the target did not start as a table")
	}
	x, y := m.width-2, m.titleHeight()

	click(m, x, y)
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatalf("clicking (+) in table mode did not expand the line:\n%s", plainPane(m))
	}
	if m.copySeq != "" {
		t.Fatal("clicking (+) in table mode copied the line")
	}
	click(m, x, y)
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("clicking (−) in table mode did not collapse the line")
	}
}

// A click — press and release on one cell, no motion — on a line that can
// expand toggles it like →/enter and focuses the pane; it never copies.
func TestClickTogglesAJSONLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	click(m, x, y)
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatalf("the click did not expand the line:\n%s", plainPane(m))
	}
	if !m.paneFocus {
		t.Fatal("the click did not focus the pane")
	}
	if m.copySeq != "" {
		t.Fatal("the click copied the line it expanded")
	}

	click(m, x, y)
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("the second click did not collapse the line")
	}
}

// A click on any row of an open expansion collapses the line, like ←.
func TestClickOnAnExpansionRowCollapsesIt(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.toggleExpand(0)

	// The indent cells of the first expansion row are neither key nor value.
	click(m, m.layout.leftW+1, m.titleHeight()+1)
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("clicking an expansion row did not collapse the line")
	}
	if !m.paneFocus {
		t.Fatal("the click dropped the pane focus")
	}
}

// A press that moves even one cell is a drag: it selects and never toggles.
func TestOneCellDragSelectsAndNeverToggles(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.handleMouse(motion(x+1, y))
	m.handleMouse(release(x+1, y))

	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("the one-cell drag expanded the line")
	}
	if got := decodeCopy(t, m.copySeq); got != m.visibleRaw[0]+"\n" {
		t.Fatalf("payload = %q, want the dragged line", got)
	}
}

// A click on a scalar cell of an open block still picks its pill and wins over collapsing.
func TestClickOnAValueCellPicksWithoutCollapsing(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `status: 403`)
	click(m, x+len(`status: `)+1, y)

	if len(m.pills) != 1 || m.pills[0].raw != "status=403" {
		t.Fatalf("pills = %+v, want status=403", m.pills)
	}
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("the value click collapsed the expansion")
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

	if len(m.pills) != 1 || m.pills[0].raw != "status=403" {
		t.Fatalf("pills = %+v, want status=403", m.pills)
	}
	if got := strings.Join(m.paneLines(), "\n"); strings.Contains(got, "200") || strings.Contains(got, "plain") {
		t.Fatalf("filtered pane = %q, want only the 403 line", got)
	}
	if !strings.Contains(plainPane(m), "[status=403 ×]") {
		t.Fatalf("header lacks the pill:\n%s", plainPane(m))
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
	if len(m.pills) != 0 {
		t.Fatalf("pills = %+v after clicking ×, want none", m.pills)
	}

	x, y = findInPane(t, m, `status: 403`)
	m.handleMouse(mouse(x+len(`status: `)+1, y, false))
	press(m, "right", "backspace")
	if len(m.pills) != 0 {
		t.Fatalf("pills = %+v after backspace, want none", m.pills)
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
	if m.schemaSel != "backend" || len(m.pills) != 1 || !m.expanded[m.visibleSeq[0]] {
		t.Fatalf("reload reset state: schema=%q pills=%d expanded=%v", m.schemaSel, len(m.pills), m.expanded[m.visibleSeq[0]])
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

// A rebuild between press and release shifts the rows: the release resolves
// the pressed line by its identity, not by the stale anchor index.
func TestReleaseResolvesThePressedLineAfterRowsShift(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq, `{"status":200}`)
	m.frame()
	x, y := paneX(m), m.titleHeight()+1

	m.handleMouse(mouse(x, y, false))
	m.toggleExpand(0) // inserts rows above the press, moving it down
	m.handleMouse(release(x, y))

	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("the mid-press expansion closed")
	}
	if !m.expanded[m.visibleSeq[1]] {
		t.Fatal("the release did not expand the pressed line after its row shifted")
	}
}

// A pressed line that leaves the pane before the release clears the press:
// no panic, no toggle, no copy.
func TestReleaseAfterThePressedLineLeftThePane(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.solo = 1
	m.mergedDirty = true
	m.handleMouse(release(x, y))

	if m.selAnchor != nil {
		t.Fatal("the release kept a press whose line is gone")
	}
	if len(m.expanded) != 0 {
		t.Fatal("the release toggled a line that replaced the pressed one")
	}
	if m.copySeq != "" {
		t.Fatal("the release copied a line that replaced the pressed one")
	}
}

// A focus loss can swallow the mouse-up; the blur itself clears the press.
func TestBlurClearsAnArmedPress(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.Update(tea.BlurMsg{})
	if m.selAnchor != nil {
		t.Fatal("the blur kept the press armed")
	}
	m.handleMouse(release(x, y))
	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("the late release toggled the line")
	}
	if m.copySeq != "" {
		t.Fatal("the late release copied the line")
	}
}

// A wheel tick that cannot move the window keeps the press: the release clicks.
func TestUnmovedWheelKeepsThePress(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.handleMouse(wheelDown(x, y)) // already at the bottom: nothing moves
	m.handleMouse(release(x, y))

	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("the unmoved wheel ate the click")
	}
}

// A wheel tick that scrolls drops the press, so its release does nothing.
func TestMovedWheelClearsThePress(t *testing.T) {
	m := frozenPane(t)
	x, y := paneX(m), 2+m.titleHeight()
	row := m.vp.YOffset() + y - m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.handleMouse(wheelUp(x, y))
	m.handleMouse(release(x, y))

	if m.selAnchor != nil {
		t.Fatal("the scrolled wheel kept the press armed")
	}
	if m.copySeq != "" {
		t.Fatal("the release copied after a scroll")
	}
	if m.expanded[m.visibleSeq[row]] {
		t.Fatal("the release toggled after a scroll")
	}
}

// A release one cell off the press, with no motion between, is no click but
// still copies the pressed line, as a release always did.
func TestReleaseOnADifferentCellCopies(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.handleMouse(release(x+1, y))

	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("the off-cell release expanded the line")
	}
	if got := decodeCopy(t, m.copySeq); got != m.visibleRaw[0]+"\n" {
		t.Fatalf("payload = %q, want the pressed line", got)
	}
}

// A motion report on the press cell is no drag: the release still clicks.
func TestSameCellMotionStaysAClick(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.handleMouse(motion(x, y))
	m.handleMouse(release(x, y))

	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("a same-cell motion turned the click into a drag")
	}
	if m.copySeq != "" {
		t.Fatal("a same-cell motion made the release copy")
	}
}

// A new press drops one that never saw its release; the release then resolves
// the second press only.
func TestNewPressClearsAStalePress(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq, `{"status":200}`)
	m.frame()
	x := paneX(m)
	y0, y1 := m.titleHeight(), m.titleHeight()+1

	m.handleMouse(mouse(x, y0, false))
	m.handleMouse(mouse(x, y1, false))
	m.handleMouse(release(x, y1))

	if m.expanded[m.visibleSeq[0]] {
		t.Fatal("the stale press on the first line toggled it")
	}
	if !m.expanded[m.visibleSeq[1]] {
		t.Fatal("the second press did not click its line")
	}
}

// A release landing while a modal is up still ends the press; the modal only
// swallows new presses.
func TestModalReleaseStillClearsThePress(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.help = true
	m.handleMouse(release(x, y))
	if m.selAnchor != nil {
		t.Fatal("the modal swallowed the release and kept the press armed")
	}

	m.handleMouse(mouse(x, y, false))
	if m.selAnchor != nil {
		t.Fatal("a press over the help modal armed a selection")
	}
	m.help = false
}

// A click on a block's key cell picks its column; the release must not
// collapse the block.
func TestClickOnAKeyCellPicksWithoutCollapsing(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.liveSelect()
	m.toggleExpand(0)

	x, y := findInPane(t, m, `status: 403`)
	click(m, x+1, y)

	got := m.headerColumns()
	if len(got) != 3 || got[2].Name != "status" || got[2].Expr != ".status" {
		t.Fatalf("columns = %+v, want the picked .status column added", got)
	}
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("the key click's release collapsed the expansion")
	}
}

// clearSelection resets the whole press state, not only the anchor.
func TestClearSelectionResetsThePressState(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	x, y := paneX(m), m.titleHeight()

	m.handleMouse(mouse(x, y, false))
	m.handleMouse(motion(x+1, y))
	m.clearSelection()

	if m.selAnchor != nil || m.selMoved {
		t.Fatal("clearSelection left press state behind")
	}
}
