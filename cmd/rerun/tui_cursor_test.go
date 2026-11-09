package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const twoTargets = `web:
  cmd: sleep 1
api:
  cmd: sleep 1
`

// cursorTo puts the pane cursor on the expansion row of the field with key.
func cursorTo(t *testing.T, m *tuiModel, key string) {
	t.Helper()
	m.ensurePane()
	for row, r := range m.disp {
		if r.field != nil && r.field.key == key {
			m.setPaneCursor(row)
			return
		}
	}
	t.Fatalf("no expansion row for key %q", key)
}

// Enter on the cursor line expands it, pauses the tail, and keeps the block on screen.
func TestEnterExpandsTheCursorLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "plain", jsonReq, "tail")
	m.frame()

	press(m, "right", "up", "enter")
	if !m.expanded[m.visibleSeq[1]] {
		t.Fatal("enter did not expand the cursor's JSON line")
	}
	if m.follow {
		t.Fatal("expanding a line did not pause the tail")
	}
	if !strings.Contains(plainPane(m), `status: 403`) {
		t.Fatalf("the block is not on screen:\n%s", plainPane(m))
	}
}

// Enter on a cursor line with nothing under it expands nothing and shows no hint.
func TestEnterOnPlainCursorLineDoesNothing(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "plain")
	m.frame()

	press(m, "right", "enter")
	if m.hint != "" {
		t.Fatalf("hint = %q, want none", m.hint)
	}
	if len(m.expanded) != 0 {
		t.Fatal("a plain line expanded")
	}
}

// Moving the cursor off the newest line pauses the tail, and the cursor stays in view.
func TestPaneCursorMovesPauseTheTailAndStayInView(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()

	press(m, "right")
	for range 30 {
		press(m, "up")
	}
	m.frame()
	if m.follow {
		t.Fatal("moving the cursor did not pause the tail")
	}
	if row := m.paneCursorRow(); row < m.vp.YOffset() || row >= m.vp.YOffset()+m.paneRows() {
		t.Fatalf("cursor row %d is outside the viewport at %d", row, m.vp.YOffset())
	}

	escToLive(m)
	if !m.follow || m.paneCursorRow() != m.paneLen()-1 {
		t.Fatal("esc did not resume the tail on the newest line")
	}
}

// a adds the cursor's key as a column and f filters on its scalar, as the clicks do.
func TestKeyboardPicksMatchTheClicks(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.liveSelect()
	press(m, "right", "enter")

	cursorTo(t, m, "level")
	press(m, "a")
	if got := m.targets[0].spec.Columns; len(got) != 1 || got[0].Name != "level" || got[0].Expr != ".level" {
		t.Fatalf("columns = %+v, want level", got)
	}
	if !m.dirty() {
		t.Fatal("a did not leave the column pick unsaved")
	}

	cursorTo(t, m, "status")
	press(m, "f")
	if len(m.pills) != 1 || m.pills[0].raw != "status=403" {
		t.Fatalf("pills = %+v, want status=403", m.pills)
	}
}

// a and f on a row that cannot take them say what to do instead.
func TestKeyboardPicksNeedAKeyOrScalar(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.liveSelect()
	press(m, "right", "enter")

	cursorTo(t, m, "request")
	press(m, "f")
	if want := "move the cursor onto a scalar value in an expanded line"; m.hint != want {
		t.Fatalf("hint = %q, want %q", m.hint, want)
	}
	if len(m.pills) != 0 {
		t.Fatalf("pills = %+v for an object value, want none", m.pills)
	}
}

// Keys stay plain and scalar values are tinted, so a key is never underlined.
func TestExpandedKeysAreNotUnderlinedAndValuesGetCues(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.toggleExpand(0)

	var key, val bool
	for _, line := range m.paneLines() {
		if strings.Contains(line, "\x1b[4m") {
			t.Fatalf("expanded key is underlined: %q", line)
		}
		key = key || strings.Contains(line, "level: ")
		val = val || strings.Contains(line, "\x1b[36minfo\x1b[39m")
	}
	if !key || !val {
		t.Fatalf("key on the pane %v, value cue %v; want both", key, val)
	}
}

// The first expansion of a session shows the click hint; later ones do not.
func TestFirstExpansionShowsTheClickHint(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq, jsonGet)
	const want = "click a key (a) to add a column · click a value (f) to filter"

	m.toggleExpand(0)
	if m.hint != want {
		t.Fatalf("hint = %q, want %q", m.hint, want)
	}
	m.hint = ""
	m.toggleExpand(1)
	if m.hint != "" {
		t.Fatalf("second expansion showed the hint again: %q", m.hint)
	}
}

// A JSON line's (+) cell takes the last cells, and the content gives way with an ellipsis.
func TestMarkerLeavesEllipsisInsteadOfOverwriting(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"msg":"`+strings.Repeat("a", 200)+`"}`)
	m.frame()

	line := paneOf(m)[m.titleHeight()]
	if row := ansi.Strip(line); !strings.HasSuffix(row, "… (+)") {
		t.Fatalf("row = %q, want an ellipsis before the (+) cell", row)
	}
	if got, want := displayWidth(line), m.width-m.layout.leftW-1; got != want {
		t.Fatalf("row is %d cells, want the pane width %d", got, want)
	}
}

// Panning reaches the end of a marked line, with the (+) cell beside its last cells.
func TestPanReachesTheEndOfAMarkedLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	line := `{"msg":"` + strings.Repeat("a", 200) + `"}`
	addLines(m, "web", line)
	m.frame()

	m.paneCol = 1000
	m.vpDirty = true
	m.frame()
	if want := displayWidth("web  "+line) - (m.vp.Width() - markW); m.paneCol != want {
		t.Fatalf("paneCol = %d, want %d", m.paneCol, want)
	}
	if row := ansi.Strip(paneOf(m)[m.titleHeight()]); !strings.HasSuffix(row, `"} (+)`) {
		t.Fatalf("row = %q, want the line's end before the (+) cell", row)
	}
}

// truncate and sliceCells count cells, not the bytes of an escape sequence.
func TestCutsKeepEscapesWhole(t *testing.T) {
	const sgr, reset = "\x1b[38;5;196m", "\x1b[0m"
	s := sgr + "abcdef" + reset

	got := sliceCells(s, 2, 4)
	if ansi.Strip(got) != "cd" {
		t.Fatalf("sliceCells = %q, want the text cd", ansi.Strip(got))
	}
	if rest := strings.NewReplacer(sgr, "", reset, "").Replace(got); strings.Contains(rest, "\x1b") {
		t.Fatalf("sliceCells split an escape: %q", got)
	}

	cut := truncate(s, 3)
	if ansi.Strip(cut) != "abc" || displayWidth(cut) != 3 {
		t.Fatalf("truncate = %q, want three cells of abc", cut)
	}
}

// A click on a column name does nothing; only its × removes the column.
func TestHeaderNameClickIsNoOp(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet, jsonPost)
	m.frame()
	m.liveSelect()

	x, y := findInPane(t, m, "method")
	m.handleMouse(mouse(x+1, y, false))
	if got := m.headerColumns(); len(got) != 2 {
		t.Fatalf("columns = %+v after a name click, want both kept", got)
	}
	if m.dirty() {
		t.Fatal("a name click marked the session dirty")
	}
}

// The dirty mark sits after a schema or target name whose columns await ctrl+s.
func TestDirtyMarkerOnSchemaAndTargetRows(t *testing.T) {
	// The longest name sets the name column; a member's own name is cut to fit beside its star.
	m := newTestModel(t, schemaTarget("payments-api", "db"), schemaTarget("payments-web", "db"), target("standalone-worker"))
	m.setEdit(colNode{schema: true, name: "db"}, []Column{{Name: "level", Expr: ".level"}})
	screen := ansi.Strip(m.View().Content)
	for _, name := range []string{"db", "payments-api", "payments-web"} {
		row := ""
		for _, line := range strings.Split(screen, "\n") {
			if strings.Contains(line, name) {
				row = line
			}
		}
		if !strings.Contains(row, "*") || strings.Contains(row, name+"*") {
			t.Fatalf("row for %q lacks a marker beside, not on, its name: %q", name, row)
		}
	}
}

// The quit modal names the unsaved columns only while there are some.
func TestQuitModalNamesUnsavedColumns(t *testing.T) {
	const line = "unsaved columns — ctrl+s to save"
	m := newTestModel(t, target("web"))
	m.confirmQuit = true
	if strings.Contains(ansi.Strip(m.View().Content), line) {
		t.Fatal("clean session names unsaved columns")
	}
	m.setEdit(colNode{name: "web"}, []Column{{Name: "level", Expr: ".level"}})
	if !strings.Contains(ansi.Strip(m.View().Content), line) {
		t.Fatal("quit modal lacks the unsaved columns line")
	}
}

// The unsaved footer outranks the new-lines marker and the search counter.
func TestUnsavedFooterOutranksMarkerAndSearch(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()
	press(m, "right", "up", "/", "o", "enter")
	addLines(m, "web", "new line")
	m.setEdit(colNode{name: "web"}, []Column{{Name: "level", Expr: ".level"}})
	m.frame()

	last := ansi.Strip(paneOf(m)[len(paneOf(m))-1])
	if !strings.Contains(last, unsavedNotice) || strings.Contains(last, "new") {
		t.Fatalf("bottom line = %q, want the unsaved footer alone", last)
	}
}

// An error that lands while a bar is open survives the keys typed into it, and goes on the next key.
func TestErrorNoticeStaysWhileABarIsOpen(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "/")
	m.showNotice("rerun: boom", true)

	press(m, "x")
	if !m.noticeErr {
		t.Fatal("typing in an open bar dismissed the error notice")
	}
	press(m, "esc", "x")
	if m.noticeErr {
		t.Fatal("the next key after the bar closed did not dismiss the error notice")
	}
}

// The saved notice holds for at least 2s against a config reload banner.
func TestSavedNoticeHoldsAgainstReloadBanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerun.yml")
	if err := os.WriteFile(path, []byte(twoTargets), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(cfg.Targets)
	m.width, m.height = 100, 20
	m.group.configPath = path
	const banner = "── config reloaded ── ~web(cmd) ──"

	m.setEdit(colNode{name: "web"}, []Column{{Name: "level", Expr: ".level"}})
	m.now = time.Now()
	press(m, "ctrl+s")
	const saved = "saved columns to rerun.yml"

	m.now = m.now.Add(time.Second)
	m.applyEvent(event{kind: evReconcile, line: banner})
	if m.notice != saved {
		t.Fatalf("notice = %q 1s after the save, want the save notice held", m.notice)
	}

	m.now = m.now.Add(2 * time.Second)
	m.applyEvent(event{kind: evReconcile, line: banner})
	if m.notice != banner {
		t.Fatalf("notice = %q 3s after the save, want the reload banner", m.notice)
	}
}

// A failing edit stays pending and the edits after it still save.
func TestSaveContinuesPastAStaleEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerun.yml")
	if err := os.WriteFile(path, []byte(twoTargets), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(cfg.Targets)
	m.width, m.height = 100, 20
	m.group.configPath = path
	m.edits = []colEdit{
		{node: colNode{name: "gone"}, cols: []Column{{Name: "level", Expr: ".level"}}},
		{node: colNode{name: "api"}, cols: []Column{{Name: "status", Expr: ".status"}}},
	}

	press(m, "ctrl+s")
	if !m.noticeErr || !strings.Contains(m.notice, "gone") {
		t.Fatalf("notice = %q, want the stale edit reported", m.notice)
	}
	if len(m.edits) != 1 || m.edits[0].node.name != "gone" {
		t.Fatalf("edits = %+v, want only the stale edit pending", m.edits)
	}
	saved, err := ParseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Targets[targetPos(saved, "api")].Columns; len(got) != 1 || got[0].Name != "status" {
		t.Fatalf("saved api columns = %+v, want status", got)
	}
}
