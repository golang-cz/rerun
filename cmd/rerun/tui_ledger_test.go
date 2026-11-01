package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

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

// ledgerFrame is the left column as drawn at a width a long target name would
// give it, so a whole entry line fits. Styling is stripped: a test reads text.
func ledgerFrame(m *tuiModel, leftW int) []string {
	m.View()
	rows := m.visualRows()
	out := make([]string, 0, len(rows))
	for y := 0; y+m.layout.offset < len(rows); y++ {
		out = append(out, strings.TrimRight(ansi.Strip(m.leftLine(y, leftW, rows)), " "))
	}
	return out
}

// spawnWithChange drives one change batch and the spawn it triggered, the way
// a target runner reports them.
func spawnWithChange(m *tuiModel, name string, cs rerun.ChangeSet, at time.Time) {
	m.applyEvent(event{kind: evChanged, target: name, count: len(cs.Files), first: cs.FirstFile, change: cs})
	m.applyEvent(event{kind: evStarted, target: name, at: at})
}

// A change batch writes no pane line of its own: the ledger owns the record.
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

// The ledger holds the three newest spawns, newest first; a fourth evicts the
// oldest.
func TestLedgerKeepsTheThreeNewestSpawns(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i, name := range []string{"a.go", "b.go", "c.go", "d.go"} {
		at := ledgerClock.Add(time.Duration(i) * time.Minute)
		spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{name: fsnotify.Write}), at)
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
	if got := entries[0].at; !got.Equal(ledgerClock.Add(3 * time.Minute)) {
		t.Fatalf("newest entry time = %v, want the last spawn", got)
	}

	// Only the newest entry renders; the header carries the total.
	m.cursor = 1
	var headers []string
	for _, row := range m.treeRows(0) {
		if row.kind == treeEntry {
			headers = append(headers, row.at.Format("15:04:05"))
		}
	}
	if want := []string{"14:35:07"}; !reflect.DeepEqual(headers, want) {
		t.Fatalf("entry headers = %v, want only the newest %v", headers, want)
	}
}

// A compact entry renders its header and at most two file lines; the header
// count still totals the whole batch. A one-file entry reads `1 file`, and a
// removal shows the minus glyph.
func TestLedgerCapsFilesAndCountsSingular(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.cursor = 1
	spawnWithChange(m, "web", sevenFiles(), ledgerClock)

	rows := m.treeRows(0)
	if len(rows) != 1+treeFiles {
		t.Fatalf("tree rows = %d, want the header and %d files", len(rows), treeFiles)
	}
	if got := rows[0].count; got != 7 {
		t.Fatalf("header count = %d, want the whole batch of 7", got)
	}
	if rows[1].kind != treeFile || rows[1].file != "a.go" || rows[2].file != "b.go" {
		t.Fatalf("file rows = %+v, want the two newest sorted paths", rows[1:])
	}

	// ALL, web, the entry header, two files — no `+k more` row.
	lines := ledgerFrame(m, 60)
	if len(lines) != 5 || lines[2] != "  ↺ 14:32:07 · 7 files" || lines[3] != "    + a.go" {
		t.Fatalf("ledger lines = %q, want a header and two files", lines)
	}
	if got := strings.Join(lines, "\n"); strings.Contains(got, "more") {
		t.Fatalf("the compact ledger grew a `+k more` row:\n%s", got)
	}

	// A spawned removal becomes the newest entry: singular, with `−`.
	spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{"b.go": fsnotify.Remove}), ledgerClock.Add(time.Minute))
	lines = ledgerFrame(m, 60)
	if len(lines) != 4 || lines[2] != "  ↺ 14:33:07 · 1 file" || lines[3] != "    − b.go" {
		t.Fatalf("newest entry lines = %q, want `1 file` singular and the removal glyph", lines)
	}
}

// A change that lands while the target is stopped waits in pending: the row
// says so, and a manual start files the batch.
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
	if got := strings.Join(ledgerFrame(m, 60), "\n"); !strings.Contains(got, "· 2 files recorded, not rebooted (stopped)") {
		t.Fatalf("the dim pending line is missing from the frame:\n%s", got)
	}

	// Space starts it; the spawn files the batch and the warning goes away.
	press(m, " ")
	m.applyEvent(event{kind: evStarted, target: "web", at: ledgerClock})
	if got := m.targets[0].ledger; len(got) != 1 || len(got[0].files) != 2 {
		t.Fatalf("ledger after the start = %+v, want one two-file entry", got)
	}
	if got := strings.Join(ledgerFrame(m, 60), "\n"); strings.Contains(got, "not rebooted") {
		t.Fatalf("the pending warning survived the spawn:\n%s", got)
	}
}

// A row the group holds down (off) words its pending line `(off)`, not
// `(stopped)`: pending carried across a reload must read as group-held.
func TestPendingLineWordsOffForGroupHeldRows(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateOff
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", change: changeBatch(map[string]fsnotify.Op{"a.go": fsnotify.Write})})

	if got := strings.Join(ledgerFrame(m, 60), "\n"); !strings.Contains(got, "· 1 file recorded, not rebooted (off)") {
		t.Fatalf("an off row's pending line = %q, want the (off) word", got)
	}
}

// Every target row grows its own ledger by default, the ALL row never does,
// and every left line stays exactly leftW cells wide.
func TestLedgerRendersForEveryExpandedRow(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{"a.go": fsnotify.Write}), ledgerClock)
	spawnWithChange(m, "api", changeBatch(map[string]fsnotify.Op{"b.go": fsnotify.Write}), ledgerClock.Add(time.Minute))

	for _, cursor := range []int{0, 1, 2} {
		m.cursor = cursor
		m.View()
		rows := m.visualRows()
		if len(rows) != 7 { // ALL, web + its 2 lines, api + its 2 lines
			t.Fatalf("visual rows with cursor %d = %d, want 7", cursor, len(rows))
		}
		if rows[0].kind != treeNone || rows[0].target != -1 {
			t.Fatalf("the ALL row grew a ledger line: %+v", rows[0])
		}
		seen := map[int]bool{}
		for _, row := range rows {
			if row.kind != treeNone {
				seen[row.target] = true
			}
		}
		if !seen[0] || !seen[1] {
			t.Fatalf("cursor %d does not show both target ledgers: %v", cursor, seen)
		}

		leftW, _ := m.layoutWidths()
		for y := 0; y < m.height; y++ {
			if got := displayWidth(m.leftLine(y, leftW, rows)); got != leftW {
				t.Fatalf("left line %d = %d cells, want %d: %q", y, got, leftW, m.leftLine(y, leftW, rows))
			}
		}
	}

	// Collapsing one row leaves every other row's ledger in place.
	m.cursor = 1
	press(m, "t")
	for _, row := range m.visualRows() {
		if row.kind != treeNone && row.target == 0 {
			t.Fatal("t did not collapse the cursor row's ledger")
		}
	}
	if got := len(m.visualRows()); got != 5 {
		t.Fatalf("visual rows after collapsing web = %d, want 5", got)
	}

	// A narrow column truncates the entry lines instead of wrapping them.
	m.width = 30
	m.cursor = 1
	m.View()
	leftW, _ := m.layoutWidths()
	for y, row := range m.visualRows() {
		if row.kind == treeNone {
			continue
		}
		if got := displayWidth(m.leftLine(y, leftW, m.visualRows())); got != leftW {
			t.Fatalf("narrow ledger line %d = %d cells, want %d", y, got, leftW)
		}
	}
}

// A backoff-delayed spawn still files the batch: the change waits, the entry is
// stamped with the spawn time.
func TestBackoffDelayedSpawnCommitsToTheLedger(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.cursor = 1
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", change: changeBatch(map[string]fsnotify.Op{"a.go": fsnotify.Write})})
	m.applyEvent(event{kind: evBackoff, target: "web", delay: 5 * time.Second, ordinal: 3})

	if got := m.targets[0].ledger; len(got) != 0 {
		t.Fatalf("a backoff filed %+v, want the batch still pending", got)
	}
	// Waiting out a backoff is not a stop: no `not rebooted` warning, but the
	// pending changes still make the row's +/− cell non-blank.
	if got := strings.Join(ledgerFrame(m, 60), "\n"); strings.Contains(got, "not rebooted") {
		t.Fatalf("a backoff target reads as stopped:\n%s", got)
	}
	if got := m.treeCell(0); got != "−" {
		t.Fatalf("tree cell during a backoff = %q, want −", got)
	}

	spawn := ledgerClock.Add(5 * time.Second)
	m.applyEvent(event{kind: evStarted, target: "web", at: spawn})
	if got := m.targets[0].ledger; len(got) != 1 || !got[0].at.Equal(spawn) {
		t.Fatalf("ledger after the delayed spawn = %+v, want one entry at %v", got, spawn)
	}
}
