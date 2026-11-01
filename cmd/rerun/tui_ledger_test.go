package main

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// ledgerClock is a fixed spawn time, so a tree line renders a known age.
var ledgerClock = time.Date(2024, 5, 6, 14, 32, 7, 0, time.Local)

// changeBatch is a change set over the given paths with their ops. The sorted
// first path stands in for the first file the watcher reports.
func changeBatch(ops map[string]fsnotify.Op) rerun.ChangeSet {
	files := make(map[string]struct{}, len(ops))
	paths := make([]string, 0, len(ops))
	for path := range ops {
		files[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return rerun.ChangeSet{FirstFile: paths[0], Files: files, Ops: ops}
}

// oneFile is a single written file, the smallest batch a spawn can record.
func oneFile(path string) rerun.ChangeSet {
	return changeBatch(map[string]fsnotify.Op{path: fsnotify.Write})
}

// changeAt feeds one change batch at a fixed clock, so the drawer ages it the
// way a test expects.
func changeAt(m *tuiModel, name string, at time.Time, ops map[string]fsnotify.Op) {
	m.now = at
	m.applyEvent(event{kind: evChanged, target: name, count: len(ops), change: changeBatch(ops)})
}

// spawnWithChange drives one change batch and the spawn it triggered, the way
// a target runner reports them.
func spawnWithChange(m *tuiModel, name string, cs rerun.ChangeSet, at time.Time) {
	m.applyEvent(event{kind: evChanged, target: name, count: len(cs.Files), first: cs.FirstFile, change: cs})
	m.applyEvent(event{kind: evStarted, target: name, at: at})
}

// treeTarget is a target that watches the given on: globs, so the drawer
// derives roots from them.
func treeTarget(name string, on ...string) Target {
	t := target(name)
	t.On = on
	return t
}

// leftPane renders one frame and returns the left column as drawn, styling
// stripped and trailing blanks trimmed, so a test reads text.
func leftPane(m *tuiModel) []string {
	m.View()
	leftW, _ := m.layoutWidths()
	out := make([]string, 0, m.height)
	for y := 0; y < m.height; y++ {
		out = append(out, strings.TrimRight(ansi.Strip(m.leftLine(y, leftW)), " "))
	}
	return out
}

// drawerWidth is a column wide enough for a whole tree line, so a test reads
// the drawer's text instead of its truncation.
const drawerWidth = 60

// drawerOf is the fs drawer as drawn at drawerWidth. The blank divider and the
// version line sit below it.
func drawerOf(m *tuiModel) []string {
	m.View()
	n := len(m.layout.drawer)
	out := make([]string, 0, n)
	for y := m.height - 2 - n; y < m.height-2; y++ {
		out = append(out, strings.TrimRight(ansi.Strip(m.leftLine(y, drawerWidth)), " "))
	}
	return out
}

// treeText renders one drawer line as the drawer does at drawerWidth: the
// label padded out, the suffix right-aligned in the fixed four-cell column.
func treeText(label, suffix string) string {
	return padRight(label, drawerWidth-treeSuffixW) + fmt.Sprintf("%4s", suffix)
}

// drawerTop is the screen row of the drawer's separator.
func drawerTop(m *tuiModel) int { return m.height - 2 - len(m.layout.drawer) }

// folderRows is the drawer's folder lines, in draw order.
func folderRows(m *tuiModel) []drawerRow {
	var out []drawerRow
	for _, r := range m.drawerRows() {
		if r.kind == drawerFolder {
			out = append(out, r)
		}
	}
	return out
}

// A change batch writes no pane line of its own: the drawer owns the record.
func TestChangeBatchAddsNoPaneLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	before := m.paneLen()
	m.applyEvent(event{kind: evChanged, target: "web", count: 2, first: "a.go", change: changeBatch(map[string]fsnotify.Op{
		"a.go": fsnotify.Write,
		"b.go": fsnotify.Write,
	})})
	if got := m.paneLen(); got != before {
		t.Fatalf("pane lines after a change = %d, want %d", got, before)
	}
	if got := strings.Join(m.paneLines(), "\n"); strings.Contains(got, "changed") {
		t.Fatalf("the change batch wrote a pane line:\n%s", got)
	}
}

// A change batch followed by a spawn files exactly one ledger entry. A manual
// start with nothing recorded files nothing.
func TestSpawnCommitsTheChangeBatchToTheLedger(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{
		"src/a.go": fsnotify.Create,
		"src/b.go": fsnotify.Write,
	}), ledgerClock)

	web := m.targets[0]
	if len(web.ledger) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(web.ledger))
	}
	if web.pending != nil {
		t.Fatalf("pending after the spawn = %v, want it cleared", web.pending)
	}
	if got := web.ledger[0].at; !got.Equal(ledgerClock) {
		t.Fatalf("entry time = %v, want the spawn time %v", got, ledgerClock)
	}
	want := []changeFile{{path: "src/a.go", op: '+'}, {path: "src/b.go", op: '~'}}
	if got := web.ledger[0].files; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry files = %+v, want %+v", got, want)
	}
	// api never changed, so it has no ledger at all.
	if got := m.targets[1].ledger; len(got) != 0 {
		t.Fatalf("api ledger = %+v, want none", got)
	}

	// A later spawn with nothing recorded commits nothing.
	m.applyEvent(event{kind: evStarted, target: "web", at: ledgerClock.Add(time.Minute)})
	if got := len(web.ledger); got != 1 {
		t.Fatalf("ledger entries after the manual start = %d, want 1", got)
	}
}

// A change set repeats a path as the watcher batches it; the entry keeps the
// path once, with its latest op.
func TestPendingMergesRepeatedPaths(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", change: changeBatch(map[string]fsnotify.Op{"a.go": fsnotify.Create})})
	m.applyEvent(event{kind: evChanged, target: "web", count: 2, first: "a.go", change: changeBatch(map[string]fsnotify.Op{
		"a.go": fsnotify.Remove,
		"b.go": fsnotify.Write,
	})})
	m.applyEvent(event{kind: evStarted, target: "web", at: ledgerClock})

	want := []changeFile{{path: "a.go", op: '-'}, {path: "b.go", op: '~'}}
	if got := m.targets[0].ledger[0].files; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry files = %+v, want %+v", got, want)
	}
}

// A target keeps its three newest spawns; older ones fall out of the data.
func TestLedgerKeepsTheThreeNewestSpawns(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i, name := range []string{"a.go", "b.go", "c.go", "d.go"} {
		at := ledgerClock.Add(time.Duration(i) * time.Minute)
		spawnWithChange(m, "web", oneFile(name), at)
	}

	entries := m.targets[0].ledger
	if len(entries) != ledgerCap {
		t.Fatalf("ledger entries = %d, want %d", len(entries), ledgerCap)
	}
	want := []string{"d.go", "c.go", "b.go"}
	for i, e := range entries {
		if got := e.files[0].path; got != want[i] {
			t.Fatalf("entry %d holds %s, want %s: newest first, oldest evicted", i, got, want[i])
		}
	}
}

// Roots come from the on: globs across every target, in yml order, and a root
// with nothing changed is not drawn.
func TestTreeRootsFollowYmlOrderAndOnlyShowChanges(t *testing.T) {
	m := newTestModel(t,
		treeTarget("web", "schema/**.ridl", "apps/api/etc/**"),
		treeTarget("api", "pkg/api", "schema/**.ridl"),
	)
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
		"schema/a.ridl":        fsnotify.Write,
		"schema/b.ridl":        fsnotify.Create,
		"pkg/api/types.gen.go": fsnotify.Write,
	})

	rows := folderRows(m)
	if len(rows) != 2 {
		t.Fatalf("root lines = %d, want 2: %+v", len(rows), rows)
	}
	// apps/api/etc/** watches an empty subtree: it is dropped.
	if rows[0].label != "schema/" || rows[0].count != 2 || rows[0].glyph != "~" {
		t.Fatalf("first root = %+v, want schema/ with 2 files and a leaf glyph", rows[0])
	}
	if rows[1].label != "pkg/api/" || rows[1].count != 1 {
		t.Fatalf("second root = %+v, want pkg/api/ in yml order after schema/", rows[1])
	}
}

// A folder's count is every changed file beneath it, not just its own.
func TestTreeRootCountsAreRecursive(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
		"apps/api/a.go": fsnotify.Write,
		"apps/api/b.go": fsnotify.Write,
		"apps/web/c.go": fsnotify.Write,
	})

	rows := folderRows(m)
	if len(rows) != 1 || rows[0].label != "apps/" || rows[0].count != 3 {
		t.Fatalf("root = %+v, want apps/ counting all three files", rows)
	}
	if rows[0].glyph != "+" {
		t.Fatalf("a root with changed sub-dirs shows +, got %q", rows[0].glyph)
	}

	m.toggleTreeFolder("apps/")
	api := folderRows(m)
	if len(api) != 3 {
		t.Fatalf("expanded root rows = %+v, want sub-dirs first", api)
	}
	if api[1].label != "api/" || api[1].count != 2 || api[2].label != "web/" || api[2].count != 1 {
		t.Fatalf("child folders = %+v, want api/ then web/ with recursive counts", api)
	}
}

// Clicking a folder line folds its children out beneath it: sub-dirs first
// alphabetically, then files. Clicking again folds. A file line is a no-op.
func TestTreeFolderClicksFoldChildren(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
		"apps/api/x.go": fsnotify.Write,
		"apps/web/z.go": fsnotify.Write,
		"apps/root.go":  fsnotify.Write,
	})
	m.View()

	top := drawerTop(m)
	m.handleMouse(mouse(2, top+1, false))
	m.View()
	got := drawerOf(m)
	want := []string{
		treeText("  − apps/", "3"),
		treeText("    ~ api/", "1"),
		treeText("    ~ web/", "1"),
		treeText("    ~ root.go", "0s"),
	}
	if !reflect.DeepEqual(got[1:], want) {
		t.Fatalf("expanded tree = %q, want %q", got[1:], want)
	}

	// A file line does nothing: the tree stays open.
	m.handleMouse(mouse(4, top+4, false))
	m.View()
	if got := len(m.layout.drawer); got != 5 {
		t.Fatalf("a file click changed the tree: %d rows", got)
	}

	// A second click on the root folds it back to one root line.
	top = drawerTop(m)
	m.handleMouse(mouse(2, top+1, false))
	m.View()
	if got := len(m.layout.drawer); got != 2 {
		t.Fatalf("re-folded drawer rows = %d, want the separator and the root", got)
	}
}

// Expansion is keyed by folder path, so a new change arriving under an open
// folder leaves it open.
func TestTreeExpansionSurvivesNewArrivals(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
	m.toggleTreeFolder("apps/")
	m.View()
	if !strings.Contains(strings.Join(drawerOf(m), "\n"), "a.go") {
		t.Fatal("setup: the folder did not expand")
	}

	changeAt(m, "web", ledgerClock.Add(time.Minute), map[string]fsnotify.Op{"apps/b.go": fsnotify.Write})
	lines := strings.Join(drawerOf(m), "\n")
	if !strings.Contains(lines, "a.go") || !strings.Contains(lines, "b.go") {
		t.Fatalf("the open folder forgot its state on a new arrival:\n%s", lines)
	}
}

// An expanded folder pushed out of the 200-path cap forgets its expansion, and
// no folder that survives inherits it.
func TestTreeExpansionForgetsPrunedPaths(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{"apps/old/x.go": fsnotify.Write})
	m.toggleTreeFolder("apps/old/")
	if !m.isTreeExpanded("apps/old/") {
		t.Fatal("setup: the old folder did not expand")
	}

	for i := 0; i < treeCap; i++ {
		at := ledgerClock.Add(time.Duration(i+1) * time.Second)
		changeAt(m, "web", at, map[string]fsnotify.Op{fmt.Sprintf("apps/new/f%d.go", i): fsnotify.Write})
	}
	m.View()
	if len(m.treeFiles) != treeCap {
		t.Fatalf("tree paths = %d, want the %d cap", len(m.treeFiles), treeCap)
	}
	if m.isTreeExpanded("apps/old/") {
		t.Fatal("a folder pushed out of the cap kept its expansion")
	}
	for _, r := range folderRows(m) {
		if m.isTreeExpanded(r.path) {
			t.Fatalf("folder %q read as expanded", r.path)
		}
	}
}

// The folder glyph tells the three states apart: a leaf folder, a collapsed
// folder with changed sub-dirs, and an expanded one.
func TestTreeFolderGlyphsPerState(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
		"apps/root.go":  fsnotify.Write,
		"apps/sub/x.go": fsnotify.Write,
	})

	rows := folderRows(m)
	if rows[0].label != "apps/" || rows[0].glyph != "+" {
		t.Fatalf("collapsed folder with sub-dirs = %+v, want +", rows[0])
	}
	m.toggleTreeFolder("apps/")
	rows = folderRows(m)
	if rows[0].glyph != "−" {
		t.Fatalf("expanded folder glyph = %q, want −", rows[0].glyph)
	}
	if rows[1].label != "sub/" || rows[1].glyph != "~" {
		t.Fatalf("leaf folder = %+v, want sub/ with ~", rows[1])
	}
}

// A file line carries its op glyph and its age from the fixed clock.
func TestTreeFileLineCarriesOpAndAge(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
		"apps/new.go":  fsnotify.Create,
		"apps/gone.go": fsnotify.Remove,
	})
	m.now = ledgerClock.Add(4 * time.Minute)
	m.toggleTreeFolder("apps/")

	if got := drawerOf(m); !reflect.DeepEqual(got[1:], []string{
		treeText("  − apps/", "2"),
		treeText("    − gone.go", "4m"),
		treeText("    + new.go", "4m"),
	}) {
		t.Fatalf("file lines = %q, want sorted names with the 4m age", got[1:])
	}
}

// The drawer never draws more than eight lines, separator included. With more
// roots than room, the newest roots win and the oldest is not drawn.
func TestTreeDrawerRendersAtMostEightLines(t *testing.T) {
	defs := make([]Target, 0, 10)
	for i := 0; i < 10; i++ {
		defs = append(defs, treeTarget(fmt.Sprintf("t%d", i), fmt.Sprintf("root%d", i)))
	}
	m := newTestModel(t, defs...)
	for i, d := range defs {
		at := ledgerClock.Add(time.Duration(i) * time.Second)
		changeAt(m, d.Name, at, map[string]fsnotify.Op{fmt.Sprintf("root%d/a.go", i): fsnotify.Write})
	}
	m.now = ledgerClock.Add(9 * time.Second)
	m.View()

	if got := len(m.layout.drawer); got != drawerMax {
		t.Fatalf("drawer lines = %d, want the %d-line cap", got, drawerMax)
	}
	labels := map[string]bool{}
	for _, r := range folderRows(m) {
		labels[r.label] = true
	}
	if labels["root0/"] || labels["root1/"] || labels["root2/"] {
		t.Fatalf("the oldest roots were drawn despite the cap: %v", labels)
	}
	if !labels["root9/"] {
		t.Fatalf("the newest root is missing: %v", labels)
	}

	// An expansion renders inside the cap: the file is drawn, nothing exceeds 8.
	m.toggleTreeFolder("root9/")
	m.View()
	if got := len(m.layout.drawer); got != drawerMax {
		t.Fatalf("expanded drawer lines = %d, want the %d-line cap", got, drawerMax)
	}
	if got := strings.Join(drawerOf(m), "\n"); !strings.Contains(got, "a.go") {
		t.Fatalf("the newest root's expanded file is missing:\n%s", got)
	}
}

// The drawer stays hidden while nothing has ever changed: the list then owns
// every row above the version line. The first change grows it.
func TestTreeDrawerStaysHiddenUntilSomethingChanged(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"), target("api"))
	m.View()
	if got := len(m.layout.drawer); got != 0 {
		t.Fatalf("drawer rows with no changes = %d, want none", got)
	}
	if got := m.listHeight(); got != m.height-1 {
		t.Fatalf("list height with no drawer = %d, want %d", got, m.height-1)
	}

	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
	m.View()
	if got := len(m.layout.drawer); got == 0 {
		t.Fatal("the drawer did not grow with the first change")
	}
	if got := m.listHeight(); got >= m.height-1 {
		t.Fatalf("the drawer took no rows from the list: list height = %d", got)
	}
}

// A change that lands while the target is stopped still feeds the tree like any
// other change. The old dim pending line is gone.
func TestStoppedTargetFeedsTheTree(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	m.cursor = 1
	press(m, " ") // stop

	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
		"apps/a.go": fsnotify.Write,
		"apps/b.go": fsnotify.Write,
	})
	if got := m.targets[0].ledger; len(got) != 0 {
		t.Fatalf("a stopped target filed %+v, want nothing in the ledger", got)
	}
	if got := m.targets[0].pending; len(got) != 2 {
		t.Fatalf("pending = %v, want the two recorded files", got)
	}
	rows := folderRows(m)
	if len(rows) != 1 || rows[0].label != "apps/" || rows[0].count != 2 {
		t.Fatalf("stopped changes = %+v, want apps/ with 2 files in the tree", rows)
	}
	if got := strings.Join(drawerOf(m), "\n"); strings.Contains(got, "not rebooted") {
		t.Fatalf("the pending one-liner still renders:\n%s", got)
	}

	// Space starts it; the spawn files the batch.
	press(m, " ")
	m.applyEvent(event{kind: evStarted, target: "web", at: ledgerClock})
	if got := m.targets[0].ledger; len(got) != 1 || len(got[0].files) != 2 {
		t.Fatalf("ledger after the start = %+v, want one two-file entry", got)
	}
}

// t is one global toggle: it opens and closes the drawer from any row, and the
// target list stays one line per row either way.
func TestTreeDrawerTogglesGloballyWithT(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"), treeTarget("api", "pkg"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
	changeAt(m, "api", ledgerClock.Add(time.Minute), map[string]fsnotify.Op{"pkg/b.go": fsnotify.Write})
	m.View()
	if got := len(m.layout.drawer); got == 0 {
		t.Fatal("the drawer is closed by default")
	}

	for _, cursor := range []int{0, 1, 2} {
		m.cursor = cursor
		if got := m.listLen(); got != len(m.targets)+1 {
			t.Fatalf("cursor %d: list lines = %d, want one per row plus ALL", cursor, got)
		}
	}

	m.cursor = 0
	press(m, "t")
	m.View()
	if got := len(m.layout.drawer); got != 0 {
		t.Fatalf("drawer rows after t = %d, want none", got)
	}
	if got := m.listHeight(); got != m.height-1 {
		t.Fatalf("list height with the drawer closed = %d, want %d", got, m.height-1)
	}
	press(m, "t")
	m.View()
	if got := len(m.layout.drawer); got == 0 {
		t.Fatal("the second t did not reopen the drawer")
	}
}

// A folder click toggles the tree and never solos a target or moves the cursor.
func TestTreeFolderClickDoesNotSolo(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"), target("api"))
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
	m.View()

	top := drawerTop(m)
	if got := m.rowAt(2, top); got != -1 {
		t.Fatalf("the separator resolves to row %d, want none", got)
	}
	if got := m.rowAt(2, top+1); got != -1 {
		t.Fatalf("a folder line resolves to row %d, want none", got)
	}
	m.handleMouse(mouse(2, top+1, false))
	if m.solo != -1 || m.cursor != 0 {
		t.Fatalf("the drawer click soloed or moved the cursor: solo=%d cursor=%d", m.solo, m.cursor)
	}
	if !m.isTreeExpanded("apps/") {
		t.Fatal("the folder click did not expand the folder")
	}
}

// The cursor row stays visible in the list area above a tall drawer.
func TestListScrollKeepsTheCursorVisibleUnderATallDrawer(t *testing.T) {
	defs := make([]Target, 0, 9)
	for i := 0; i < 9; i++ {
		defs = append(defs, treeTarget(fmt.Sprintf("t%d", i), fmt.Sprintf("root%d", i)))
	}
	m := newTestModel(t, defs...)
	m.height = 10
	m.cursor = len(m.targets)
	for i, d := range defs {
		changeAt(m, d.Name, ledgerClock, map[string]fsnotify.Op{fmt.Sprintf("root%d/a.go", i): fsnotify.Write})
	}
	m.View()

	if got := len(m.layout.drawer); got > (m.height-1)/2 {
		t.Fatalf("drawer rows = %d, want at most half of %d", got, m.height-1)
	}
	visible := m.listHeight()
	if visible >= m.height-1 {
		t.Fatalf("list height = %d, want the drawer to take rows from it", visible)
	}
	line := m.cursor - m.layout.offset
	if line < 0 || line >= visible {
		t.Fatalf("the cursor row is at list line %d, outside the %d-row list area", line, visible)
	}
	if got := m.rowAt(2, line); got != m.cursor {
		t.Fatalf("the cursor row is off screen: line %d maps to %d, want %d", line, got, m.cursor)
	}
}

// Every left line is exactly leftW cells wide, drawer included, and a narrow
// column truncates rather than wraps.
func TestEveryLeftLineIsExactlyLeftWide(t *testing.T) {
	for _, width := range []int{24, 30, 100} {
		m := newTestModel(t, treeTarget("web", "apps"), target("backoffice-webapp"))
		m.width = width
		m.cursor = 2
		changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{
			"apps/api/deep/types.gen.go": fsnotify.Write,
			"apps/root.go":               fsnotify.Write,
		})
		m.toggleTreeFolder("apps/")
		m.View()

		leftW, _ := m.layoutWidths()
		if got := len(m.layout.drawer); got == 0 {
			t.Fatalf("width %d: setup drew no drawer", width)
		}
		for y := 0; y < m.height; y++ {
			line := m.leftLine(y, leftW)
			if got := displayWidth(line); got != leftW {
				t.Fatalf("width %d, line %d = %d cells, want %d: %q", width, y, got, leftW, line)
			}
			if strings.Contains(line, "\n") {
				t.Fatalf("width %d, line %d wrapped: %q", width, y, line)
			}
		}
	}
}

// The version line still owns the bottom row, one blank divider above it, with
// the drawer starting above that blank.
func TestVersionLineOwnsTheBottomRowUnderTheTree(t *testing.T) {
	m := newTestModel(t, treeTarget("web", "apps"))
	m.height = 14
	changeAt(m, "web", ledgerClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
	lines := leftPane(m)

	if got := lines[m.height-1]; !strings.Contains(got, "rerun") {
		t.Fatalf("the bottom row is not the version line: %q", got)
	}
	if got := lines[m.height-2]; got != "" {
		t.Fatalf("the row above the version line = %q, want the blank divider", got)
	}
	top := m.height - 2 - len(m.layout.drawer)
	if got := lines[top]; !strings.HasPrefix(got, "fs ·") {
		t.Fatalf("the drawer does not start above the blank divider: %q", got)
	}
	if got := m.rowAt(2, m.height-1); got != -1 {
		t.Fatalf("the version row resolved to row %d, want none", got)
	}
}

// A backoff-delayed spawn still files the batch: the change waits, the entry is
// stamped with the spawn time.
func TestBackoffDelayedSpawnCommitsToTheLedger(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.cursor = 1
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", change: oneFile("a.go")})
	m.applyEvent(event{kind: evBackoff, target: "web", delay: 5 * time.Second, ordinal: 3})

	if got := m.targets[0].ledger; len(got) != 0 {
		t.Fatalf("a backoff filed %+v, want the batch still pending", got)
	}
	spawn := ledgerClock.Add(5 * time.Second)
	m.applyEvent(event{kind: evStarted, target: "web", at: spawn})
	if got := m.targets[0].ledger; len(got) != 1 || !got[0].at.Equal(spawn) {
		t.Fatalf("ledger after the delayed spawn = %+v, want one entry at %v", got, spawn)
	}
}
