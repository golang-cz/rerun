package main

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// spawnClock is a fixed spawn time, so a feed line renders a known age.
var spawnClock = time.Date(2024, 5, 6, 14, 32, 7, 0, time.Local)

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
	m.applyEvent(event{kind: evChanged, target: name, count: len(cs.Files), first: cs.FirstFile, change: cs, restarts: true})
	m.applyEvent(event{kind: evStarted, target: name, at: at})
}

// A change batch writes no pane line of its own: the fs events feed owns the record.
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

// A target is named only once its spawn lands.
func TestRestartNamesTheTargetOnlyWhenItsSpawnLands(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.now = spawnClock
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", at: spawnClock, change: oneFile("a.go"), restarts: true})
	m.applyEvent(event{kind: evRunnerErr, target: "web", err: errors.New("no exec")})
	m.applyEvent(event{kind: evStarted, target: "web", at: spawnClock})

	if got := m.feed[0].targets; len(got) != 0 {
		t.Fatalf("targets after a failed spawn = %v, want none", got)
	}
	if web := m.targets[0]; web.flashing(spawnClock.Add(500 * time.Millisecond)) {
		t.Fatalf("a failed spawn flashed the row")
	}
}
