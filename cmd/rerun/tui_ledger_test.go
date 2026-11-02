package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

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

// spawnWithChange drives one change batch and the spawn it triggered, the way
// a target runner reports them.
func spawnWithChange(m *tuiModel, name string, cs rerun.ChangeSet, at time.Time) {
	m.applyEvent(event{kind: evChanged, target: name, count: len(cs.Files), first: cs.FirstFile, change: cs})
	m.applyEvent(event{kind: evStarted, target: name, at: at})
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
