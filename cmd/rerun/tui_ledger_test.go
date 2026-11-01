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

	// The rendered headers follow the ledger's order.
	m.cursor = 1
	var headers []string
	for _, row := range m.treeRows(0) {
		if row.kind == treeEntry {
			headers = append(headers, row.at.Format("15:04:05"))
		}
	}
	if want := []string{"14:35:07", "14:34:07", "14:33:07"}; !reflect.DeepEqual(headers, want) {
		t.Fatalf("entry headers = %v, want %v", headers, want)
	}
}

// Five files of an entry render and the rest become one `+k more` row; a
// one-file entry reads `1 file`, and a removal shows the minus glyph.
func TestLedgerCapsFilesAndCountsSingular(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.cursor = 1
	spawnWithChange(m, "web", sevenFiles(), ledgerClock)

	rows := m.treeRows(0)
	if got := rows[0].count; got != 7 {
		t.Fatalf("header count = %d, want 7", got)
	}
	files, more := 0, 0
	for _, row := range rows[1:] {
		switch row.kind {
		case treeFile:
			files++
		case treeMore:
			more = row.more
		}
	}
	if files != treeMax || more != 2 {
		t.Fatalf("files = %d, more = %d, want %d files and +2 more", files, more, treeMax)
	}

	// ALL, web, the entry header, five files, "+2 more".
	lines := ledgerFrame(m, 60)
	if len(lines) != 9 || lines[2] != "  ↺ 14:32:07 · 7 files" || lines[8] != "    +2 more" {
		t.Fatalf("ledger lines = %q, want a header, five files and +2 more", lines)
	}

	// A spawned removal becomes the newest entry: singular, with `−`.
	spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{"b.go": fsnotify.Remove}), ledgerClock.Add(time.Minute))
	lines = ledgerFrame(m, 60)
	if len(lines) < 4 || lines[2] != "  ↺ 14:33:07 · 1 file" || lines[3] != "    − b.go" {
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

// Only the cursor row grows ledger lines, and every left line stays exactly
// leftW cells wide.
func TestLedgerRendersUnderTheCursorRowOnly(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	spawnWithChange(m, "web", changeBatch(map[string]fsnotify.Op{"a.go": fsnotify.Write}), ledgerClock)
	spawnWithChange(m, "api", changeBatch(map[string]fsnotify.Op{"b.go": fsnotify.Write}), ledgerClock.Add(time.Minute))

	for _, tt := range []struct {
		cursor int
		rows   int
	}{
		{cursor: 0, rows: 3}, // the ALL row shows no ledger
		{cursor: 1, rows: 5}, // web's header and file
		{cursor: 2, rows: 5}, // api's header and file
	} {
		m.cursor = tt.cursor
		m.View()
		rows := m.visualRows()
		if len(rows) != tt.rows {
			t.Fatalf("visual rows with cursor %d = %d, want %d", tt.cursor, len(rows), tt.rows)
		}
		for _, row := range rows {
			if row.kind != treeNone && row.target != tt.cursor-1 {
				t.Fatalf("cursor %d draws a ledger line for target %d", tt.cursor, row.target)
			}
		}

		leftW, _ := m.layoutWidths()
		for y := 0; y < m.height; y++ {
			if got := displayWidth(m.leftLine(y, leftW, rows)); got != leftW {
				t.Fatalf("left line %d = %d cells, want %d: %q", y, got, leftW, m.leftLine(y, leftW, rows))
			}
		}
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
	// Waiting out a backoff is not a stop: no `not rebooted` warning.
	if got := strings.Join(ledgerFrame(m, 60), "\n"); strings.Contains(got, "not rebooted") {
		t.Fatalf("a backoff target reads as stopped:\n%s", got)
	}

	spawn := ledgerClock.Add(5 * time.Second)
	m.applyEvent(event{kind: evStarted, target: "web", at: spawn})
	if got := m.targets[0].ledger; len(got) != 1 || !got[0].at.Equal(spawn) {
		t.Fatalf("ledger after the delayed spawn = %+v, want one entry at %v", got, spawn)
	}
}
