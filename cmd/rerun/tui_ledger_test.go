package main

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// ledgerClock is a fixed spawn time, so an entry header renders a known
// HH:MM:SS in the view.
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

// spawnWithChange drives one change batch and the spawn it triggered, the way
// a target runner reports them.
func spawnWithChange(m *tuiModel, name string, cs rerun.ChangeSet, at time.Time) {
	m.applyEvent(event{kind: evChanged, target: name, count: len(cs.Files), first: cs.FirstFile, change: cs})
	m.applyEvent(event{kind: evStarted, target: name, at: at})
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

// drawerWidth is a column wide enough for a whole entry line, so a test reads
// the drawer's text instead of its truncation.
const drawerWidth = 60

// drawerOf is the fs drawer as drawn at drawerWidth: its separator and its
// entries. The blank divider and the version line sit below it.
func drawerOf(m *tuiModel) []string {
	m.View()
	n := len(m.layout.drawer)
	out := make([]string, 0, n)
	for y := m.height - 2 - n; y < m.height-2; y++ {
		out = append(out, strings.TrimRight(ansi.Strip(m.leftLine(y, drawerWidth)), " "))
	}
	return out
}

// spawnHeaders is the drawer's entry headers, in draw order.
func spawnHeaders(m *tuiModel) []string {
	var out []string
	for _, line := range drawerOf(m) {
		if strings.HasPrefix(line, "↺") {
			out = append(out, line)
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

// The drawer merges every target's spawns into one ledger, newest first, and
// the entry header keeps the target's state color.
func TestDrawerMergesEveryTargetNewestFirst(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	spawnWithChange(m, "web", oneFile("a.go"), ledgerClock)
	spawnWithChange(m, "api", oneFile("b.go"), ledgerClock.Add(time.Minute))
	spawnWithChange(m, "web", oneFile("c.go"), ledgerClock.Add(2*time.Minute))

	m.now = ledgerClock.Add(2*time.Minute + 12*time.Second)
	lines := drawerOf(m)
	if !strings.HasPrefix(lines[0], "fs ·") {
		t.Fatalf("the drawer does not lead with its separator: %q", lines[0])
	}
	want := []string{
		"↺ 12s · web · 1 file",
		"↺ 1m · api · 1 file",
		"↺ 2m · web · 1 file",
	}
	if got := spawnHeaders(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("drawer headers = %q, want %q", got, want)
	}

	// A red row reads red in the drawer too.
	web := m.targets[0]
	web.state, web.exitCode = stateExited, 1
	m.View()
	leftW, _ := m.layoutWidths()
	header := m.leftLine(m.height-2-len(m.layout.drawer)+1, leftW)
	if got := m.styles.base.Foreground(lipgloss.Color(colorRed)).Render("web"); !strings.Contains(header, got) {
		t.Fatalf("the entry header does not carry the row's state color: %q", header)
	}
}

// A target keeps its three newest spawns; older ones fall out of the data and
// so out of the drawer.
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

	// Only what the target still holds reaches the drawer, one collapsed
	// header each.
	m.now = ledgerClock.Add(3*time.Minute + 5*time.Second)
	lines := drawerOf(m)
	wantLines := []string{
		"↺ 5s · web · 1 file",
		"↺ 1m · web · 1 file",
		"↺ 2m · web · 1 file",
	}
	if got := lines[1:]; !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("drawer entries = %q, want %q", got, wantLines)
	}
}

// A new entry renders one header line with a relative age; its files appear
// only once the entry is expanded, and clicking again collapses it.
func TestDrawerEntryCollapsesToHeaderAndExpandsOnClick(t *testing.T) {
	m := newTestModel(t, target("web"))
	spawnWithChange(m, "web", oneFile("a.go"), ledgerClock)
	m.now = ledgerClock.Add(12 * time.Second)

	want := []string{
		"fs ·" + strings.Repeat("─", drawerWidth-4),
		"↺ 12s · web · 1 file",
	}
	if got := drawerOf(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("collapsed drawer = %q, want %q", got, want)
	}
	if got := strings.Join(drawerOf(m), "\n"); strings.Contains(got, "a.go") {
		t.Fatalf("the collapsed entry showed a file:\n%s", got)
	}

	id := m.targets[0].ledger[0].id
	m.toggleExpanded(id)
	lines := drawerOf(m)
	if len(lines) != 3 || lines[2] != "  ~ a.go" {
		t.Fatalf("expanded entry = %q, want its one file", lines)
	}

	m.toggleExpanded(id)
	if got := len(drawerOf(m)); got != 2 {
		t.Fatalf("re-collapsed entry lines = %d, want the header only", got)
	}
}

// Two entries stay independent: expanding one leaves the other a one-liner.
func TestExpandingOneEntryLeavesTheOtherCollapsed(t *testing.T) {
	m := newTestModel(t, target("web"))
	spawnWithChange(m, "web", oneFile("a.go"), ledgerClock)
	spawnWithChange(m, "web", oneFile("b.go"), ledgerClock.Add(time.Minute))
	m.now = ledgerClock.Add(2 * time.Minute)

	m.toggleExpanded(m.targets[0].ledger[0].id) // the newest, b.go
	want := []string{
		"fs ·" + strings.Repeat("─", drawerWidth-4),
		"↺ 1m · web · 1 file",
		"  ~ b.go",
		"↺ 2m · web · 1 file",
	}
	if got := drawerOf(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("drawer = %q, want %q", got, want)
	}
}

// An expanded entry lists three files and counts the rest in a `+k more` row.
func TestDrawerShowsThreeFilesAndAMoreRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	spawnWithChange(m, "web", sevenFiles(), ledgerClock)
	m.now = ledgerClock.Add(2 * time.Minute)
	m.toggleExpanded(m.targets[0].ledger[0].id)

	want := []string{
		"fs ·" + strings.Repeat("─", drawerWidth-4),
		"↺ 2m · web · 7 files",
		"  + a.go",
		"  ~ b.go",
		"  ~ c.go",
		"  +4 more",
	}
	if got := drawerOf(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("drawer = %q, want %q", got, want)
	}

	// A spawned removal becomes the newest entry: singular, with `−`.
	spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{"b.go": fsnotify.Remove}), ledgerClock.Add(time.Minute))
	m.toggleExpanded(m.targets[0].ledger[0].id)
	lines := drawerOf(m)
	if len(lines) < 3 || lines[1] != "↺ 1m · web · 1 file" || lines[2] != "  − b.go" {
		t.Fatalf("newest entry lines = %q, want `1 file` singular and the removal glyph", lines)
	}
}

// The drawer stays hidden while nothing has ever changed: the list then owns
// every row above the version line. The first spawn grows it.
func TestDrawerStaysHiddenUntilSomethingChanged(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()
	if got := len(m.layout.drawer); got != 0 {
		t.Fatalf("drawer rows with no changes = %d, want none", got)
	}
	if got := m.listHeight(); got != m.height-1 {
		t.Fatalf("list height with no drawer = %d, want %d", got, m.height-1)
	}
	if got := len(drawerOf(m)); got != 0 {
		t.Fatalf("drawer lines with no changes = %d, want none", got)
	}

	spawnWithChange(m, "web", oneFile("a.go"), ledgerClock)
	m.View()
	if got := len(m.layout.drawer); got == 0 {
		t.Fatal("the drawer did not grow with the first spawn")
	}
	if got := m.listHeight(); got >= m.height-1 {
		t.Fatalf("the drawer took no rows from the list: list height = %d", got)
	}

	// Every ledger line lives in the drawer; the list is one line per target.
	seen := 0
	for _, line := range leftPane(m)[:m.listHeight()] {
		if line == "" {
			continue
		}
		seen++
		if strings.Contains(line, "↺") || strings.HasPrefix(line, "  +") {
			t.Fatalf("a ledger line leaked into the target list: %q", line)
		}
	}
	if seen != m.listLen() {
		t.Fatalf("list lines = %d, want one per row plus ALL (%d)", seen, m.listLen())
	}
}

// The drawer never claims more than half the column: entries past the cap are
// not drawn, and the rows they belong to keep them.
func TestDrawerIsCappedAtHalfTheColumnHeight(t *testing.T) {
	defs := make([]Target, 0, 6)
	for i := 0; i < 6; i++ {
		defs = append(defs, target(fmt.Sprintf("t%d", i)))
	}
	m := newTestModel(t, defs...)
	m.height = 12
	for i, d := range defs {
		spawnWithChange(m, d.Name, oneFile("a.go"), ledgerClock.Add(time.Duration(i)*time.Second))
	}
	m.now = ledgerClock.Add(5 * time.Second)
	m.View()

	limit := (m.height - 1) / 2
	drawer := m.layout.drawer
	if len(drawer) == 0 || len(drawer) > limit {
		t.Fatalf("drawer rows = %d, want between 1 and half of %d", len(drawer), m.height-1)
	}
	// The newest entries win the rows; the ones that do not fit are not drawn.
	want := []string{
		"↺ 0s · t5 · 1 file",
		"↺ 1s · t4 · 1 file",
		"↺ 2s · t3 · 1 file",
		"↺ 3s · t2 · 1 file",
	}
	if got := spawnHeaders(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("drawn entries = %q, want the newest that fit %q", got, want)
	}

	// The data behind the overflow is intact.
	for i := range m.targets {
		if got := len(m.targets[i].ledger); got != 1 {
			t.Fatalf("target %d holds %d entries, want its spawn kept", i, got)
		}
	}
	if got := len(m.drawerEntries()); got != 6 {
		t.Fatalf("merged entries = %d, want all 6", got)
	}
}

// A change that lands while the target is stopped waits in pending: the drawer
// says so in a dim line, and a manual start files the batch.
func TestStoppedTargetRecordsChangesWithoutSpawning(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.cursor = 1
	press(m, " ") // stop
	if m.targets[0].state != stateStopped {
		t.Fatalf("state = %v, want stopped", m.targets[0].state)
	}

	m.applyEvent(event{kind: evChanged, target: "web", count: 2, first: "a.go", change: changeBatch(map[string]fsnotify.Op{
		"a.go": fsnotify.Write,
		"b.go": fsnotify.Write,
	})})
	if got := m.targets[0].ledger; len(got) != 0 {
		t.Fatalf("a stopped target filed %+v, want nothing in the ledger", got)
	}
	if got := m.targets[0].pending; len(got) != 2 {
		t.Fatalf("pending = %v, want the two recorded files", got)
	}
	if got := strings.Join(drawerOf(m), "\n"); !strings.Contains(got, "· web · 2 files recorded, not rebooted (stopped)") {
		t.Fatalf("the dim pending line is missing from the drawer:\n%s", got)
	}

	// Space starts it; the spawn files the batch and the warning goes away.
	press(m, " ")
	m.applyEvent(event{kind: evStarted, target: "web", at: ledgerClock})
	if got := m.targets[0].ledger; len(got) != 1 || len(got[0].files) != 2 {
		t.Fatalf("ledger after the start = %+v, want one two-file entry", got)
	}
	if got := strings.Join(drawerOf(m), "\n"); strings.Contains(got, "not rebooted") {
		t.Fatalf("the pending warning survived the spawn:\n%s", got)
	}
}

// A row the group holds down (off) words its pending line `(off)`, not
// `(stopped)`: pending carried across a reload must read as group-held.
func TestPendingLineWordsOffForGroupHeldRows(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateOff
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", change: oneFile("a.go")})

	if got := strings.Join(drawerOf(m), "\n"); !strings.Contains(got, "· web · 1 file recorded, not rebooted (off)") {
		t.Fatalf("an off row's pending line = %q, want the (off) word", got)
	}
}

// t is one global toggle: it opens and closes the drawer from any row, and the
// target list stays one line per row either way.
func TestDrawerTogglesGloballyWithT(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	spawnWithChange(m, "web", oneFile("a.go"), ledgerClock)
	spawnWithChange(m, "api", oneFile("b.go"), ledgerClock.Add(time.Minute))
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

	// The ALL row toggles it too.
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

// Clicking a drawer entry's header expands its files in place and clicking it
// again collapses it. It never solos the target or moves the cursor: a file
// line, the separator, and the pending line all do nothing.
func TestDrawerEntryClickExpandsItsFiles(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	spawnWithChange(m, "web", sevenFiles(), ledgerClock)
	spawnWithChange(m, "api", oneFile("b.go"), ledgerClock.Add(time.Minute))
	m.now = ledgerClock.Add(time.Minute + 30*time.Second)
	m.View()

	drawer := m.layout.drawer
	if len(drawer) < 3 || drawer[1].kind != drawerSpawn || drawer[1].target != 1 {
		t.Fatalf("setup: the newest entry (api) is not right under the separator: %+v", drawer)
	}
	top := m.height - 2 - len(drawer) // the separator's screen row
	if got := m.rowAt(2, top); got != -1 {
		t.Fatalf("the separator resolves to row %d, want no row", got)
	}
	if got := m.rowAt(2, top+1); got != 2 {
		t.Fatalf("the api entry header resolves to row %d, want 2", got)
	}

	m.handleMouse(mouse(2, top+1, false))
	if m.solo != -1 || m.cursor != 0 {
		t.Fatalf("the drawer click soloed or moved the cursor: solo=%d cursor=%d", m.solo, m.cursor)
	}
	if got := strings.Join(drawerOf(m), "\n"); !strings.Contains(got, "  ~ b.go") {
		t.Fatalf("the expanded entry did not show its file:\n%s", got)
	}

	// The file line of the expanded entry does nothing.
	top = m.height - 2 - len(m.layout.drawer)
	m.handleMouse(mouse(2, top+2, false))
	if got := strings.Join(drawerOf(m), "\n"); !strings.Contains(got, "  ~ b.go") {
		t.Fatalf("clicking the file line collapsed the entry:\n%s", got)
	}

	// A second click on the header collapses it.
	m.handleMouse(mouse(2, top+1, false))
	lines := drawerOf(m)
	if got := strings.Join(lines, "\n"); strings.Contains(got, "  ~ b.go") {
		t.Fatal("the second click did not collapse the entry")
	}

	// The last column of an entry is inside the drawer, not a mute cell.
	top = m.height - 2 - len(m.layout.drawer)
	slowClick(m)
	m.handleMouse(mouse(m.layout.leftW-1, top+1, false))
	if m.targets[1].muted {
		t.Fatal("clicking a drawer entry muted the target")
	}

	// The separator belongs to no row.
	m.clearSolo()
	m.handleMouse(mouse(2, top, false))
	if m.solo != -1 || m.cursor != 0 {
		t.Fatalf("the separator click did something: solo=%d cursor=%d", m.solo, m.cursor)
	}
}

// The cursor row stays visible in the list area above a tall drawer, and the
// drawer never takes more than half the column.
func TestListScrollKeepsTheCursorVisibleUnderATallDrawer(t *testing.T) {
	defs := make([]Target, 0, 9)
	for i := 0; i < 9; i++ {
		defs = append(defs, target(fmt.Sprintf("t%d", i)))
	}
	m := newTestModel(t, defs...)
	m.height = 10
	m.cursor = len(m.targets)
	for _, d := range defs {
		spawnWithChange(m, d.Name, oneFile("a.go"), ledgerClock)
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
		m := newTestModel(t, target("web"), target("backoffice-webapp"))
		m.width = width
		m.cursor = 2
		spawnWithChange(m, "web", sevenFiles(), ledgerClock)
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
// the drawer's separator starting above that blank.
func TestVersionLineOwnsTheBottomRowUnderTheDrawer(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.height = 14 // room for the separator, the header, the blank, and the version
	spawnWithChange(m, "web", sevenFiles(), ledgerClock)
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

// Ten entries render at most the eight drawer lines: seven headers under the
// separator, newest first, and the rest are simply not drawn.
func TestDrawerRendersAtMostEightLines(t *testing.T) {
	defs := make([]Target, 0, 10)
	for i := 0; i < 10; i++ {
		defs = append(defs, target(fmt.Sprintf("t%d", i)))
	}
	m := newTestModel(t, defs...)
	for i, d := range defs {
		spawnWithChange(m, d.Name, oneFile("a.go"), ledgerClock.Add(time.Duration(i)*time.Second))
	}
	m.now = ledgerClock.Add(9 * time.Second)
	m.View()

	if got := len(m.layout.drawer); got != drawerMax {
		t.Fatalf("drawer lines = %d, want the %d-line cap", got, drawerMax)
	}
	want := []string{
		"↺ 0s · t9 · 1 file",
		"↺ 1s · t8 · 1 file",
		"↺ 2s · t7 · 1 file",
		"↺ 3s · t6 · 1 file",
		"↺ 4s · t5 · 1 file",
		"↺ 5s · t4 · 1 file",
		"↺ 6s · t3 · 1 file",
	}
	if got := spawnHeaders(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("drawn entries = %q, want the newest %d %q", got, len(want), want)
	}
	if got := len(m.drawerEntries()); got != 10 {
		t.Fatalf("merged entries = %d, want all 10 kept in the data", got)
	}
}

// An entry pushed out of the twenty-entry cap forgets its expansion, and no
// surviving entry inherits it.
func TestDrawerExpansionForgetsEntriesPushedOutOfTheCap(t *testing.T) {
	defs := make([]Target, 0, 8)
	for i := 0; i < 8; i++ {
		defs = append(defs, target(fmt.Sprintf("t%d", i)))
	}
	m := newTestModel(t, defs...)
	for i, d := range defs {
		for j := 0; j < 3; j++ {
			spawnWithChange(m, d.Name, oneFile("a.go"), ledgerClock.Add(time.Duration(i*3+j)*time.Second))
		}
	}
	m.View()

	entries := m.drawerEntries()
	if len(entries) != drawerCap {
		t.Fatalf("merged entries = %d, want the %d-entry cap", len(entries), drawerCap)
	}
	oldest := entries[len(entries)-1].id
	m.toggleExpanded(oldest)
	if !m.isExpanded(oldest) {
		t.Fatal("setup: the oldest entry did not expand")
	}

	// A newer spawn pushes the oldest entry out of the cap.
	spawnWithChange(m, "t0", oneFile("z.go"), ledgerClock.Add(time.Hour))
	m.View()
	if m.isExpanded(oldest) {
		t.Fatal("an entry pushed out of the cap kept its expansion")
	}
	for _, e := range m.drawerEntries() {
		if m.isExpanded(e.id) {
			t.Fatalf("entry %d read as expanded", e.id)
		}
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
	// Waiting out a backoff is not a stop: no `not rebooted` warning.
	if got := strings.Join(drawerOf(m), "\n"); strings.Contains(got, "not rebooted") {
		t.Fatalf("a backoff target reads as stopped:\n%s", got)
	}

	spawn := ledgerClock.Add(5 * time.Second)
	m.applyEvent(event{kind: evStarted, target: "web", at: spawn})
	if got := m.targets[0].ledger; len(got) != 1 || !got[0].at.Equal(spawn) {
		t.Fatalf("ledger after the delayed spawn = %+v, want one entry at %v", got, spawn)
	}
}
