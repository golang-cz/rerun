package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// changeAt feeds one change batch at a fixed clock, so the feed ages it the
// way a test expects.
func changeAt(m *tuiModel, name string, at time.Time, ops map[string]fsnotify.Op) {
	m.now = at
	m.applyEvent(event{kind: evChanged, target: name, count: len(ops), at: at, change: changeBatch(ops), restarts: true})
	m.applyEvent(event{kind: evStarted, target: name, at: at})
}

// leftPane renders one frame and returns the left column as drawn, styling
// stripped and trailing blanks trimmed, so a test reads text.
func leftPane(m *tuiModel) []string {
	m.frame()
	leftW, _ := m.layoutWidths()
	out := make([]string, 0, m.height)
	for y := 0; y < m.height; y++ {
		out = append(out, strings.TrimRight(ansi.Strip(m.leftLine(y, leftW)), " "))
	}
	return out
}

// A burst within 150ms of its first event is one batch; a change past that starts
// the next one.
func TestFeedJoinsChangesWithin150msOfTheBatchStart(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("worker"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	changeAt(m, "api", spawnClock.Add(100*time.Millisecond), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	changeAt(m, "worker", spawnClock.Add(200*time.Millisecond), map[string]fsnotify.Op{"c.go": fsnotify.Write})

	if len(m.feed) != 2 {
		t.Fatalf("batches = %d, want 2: the 200ms change starts its own", len(m.feed))
	}
	if got, want := m.feed[0].targets, []string{"web", "api"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first batch targets = %v, want %v", got, want)
	}
	if got, want := m.feed[1].targets, []string{"worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second batch targets = %v, want %v", got, want)
	}
}

// One file reported by two targets is one feed entry, with the create kept over
// the write that follows it.
func TestFeedDedupesFilesAcrossTargets(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Create})
	changeAt(m, "api", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write, "apps/b.go": fsnotify.Remove})

	if len(m.feed) != 1 {
		t.Fatalf("batches = %d, want 1", len(m.feed))
	}
	want := map[string]byte{"apps/a.go": '+', "apps/b.go": '-'}
	if got := m.feed[0].files; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

// A rerun.yml reload joins the batch holding that file as the config pseudo-target,
// and opens its own batch when none does.
func TestFeedConfigReloadJoinsItsBatchOrOpensOne(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "rerun.yml")

	m := newTestModel(t, target("web"))
	m.group.configPath = cfg
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{
		filepath.Join(root, "apps/a.go"): fsnotify.Write,
		cfg:                              fsnotify.Write,
	})
	m.applyEvent(event{kind: evReconcile, at: spawnClock.Add(150 * time.Millisecond), line: "reloaded"})

	if len(m.feed) != 1 {
		t.Fatalf("batches = %d, want the reload to join the batch", len(m.feed))
	}
	if got, want := m.feed[0].targets, []string{"web", configTarget}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if _, ok := m.feed[0].files["apps/a.go"]; !ok {
		t.Fatalf("files = %v, want the path keyed relative to the rerun.yml dir", m.feed[0].files)
	}

	n := newTestModel(t, target("web"))
	n.group.configPath = cfg
	n.now = spawnClock
	changeAt(n, "web", spawnClock, map[string]fsnotify.Op{filepath.Join(root, "apps/a.go"): fsnotify.Write})
	n.applyEvent(event{kind: evReconcile, at: spawnClock.Add(time.Second), line: "reloaded"})

	if len(n.feed) != 2 {
		t.Fatalf("batches = %d, want the reload in its own batch", len(n.feed))
	}
	if got, want := n.feed[1].files, map[string]byte{"rerun.yml": '~'}; !reflect.DeepEqual(got, want) {
		t.Fatalf("config batch files = %v, want %v", got, want)
	}
	if got, want := n.feed[1].targets, []string{configTarget}; !reflect.DeepEqual(got, want) {
		t.Fatalf("config batch targets = %v, want %v", got, want)
	}
}

// Batches older than ten minutes drop off; with none left the newest stays, dimmed.
func TestFeedExpiresAfterTenMinutesAndKeepsTheLatestDimmed(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock.Add(-11*time.Minute), map[string]fsnotify.Op{"old.go": fsnotify.Write})
	changeAt(m, "web", spawnClock.Add(-5*time.Minute), map[string]fsnotify.Op{"new.go": fsnotify.Write})
	m.now = spawnClock

	batches, dim := m.feedVisible()
	if dim || len(batches) != 1 || batches[0].files["new.go"] == 0 {
		t.Fatalf("visible = %d batches (dim %v), want only new.go undimmed", len(batches), dim)
	}

	m.now = spawnClock.Add(6 * time.Minute)
	batches, dim = m.feedVisible()
	if !dim || len(batches) != 1 || batches[0].files["new.go"] == 0 {
		t.Fatalf("visible = %d batches (dim %v), want the dimmed latest batch", len(batches), dim)
	}
}

// Batches are kept by age alone: none is dropped while it is inside the window.
func TestFeedPrunesByAgeNotCount(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i := range 150 {
		at := spawnClock.Add(time.Duration(i) * time.Second)
		changeAt(m, "web", at, map[string]fsnotify.Op{fmt.Sprintf("f%03d.go", i): fsnotify.Write})
	}
	if got := len(m.feed); got != 150 {
		t.Fatalf("feed = %d batches, want all 150 kept inside the window", got)
	}

	changeAt(m, "web", spawnClock.Add(10*time.Minute+time.Second), map[string]fsnotify.Op{"late.go": fsnotify.Write})
	if got, want := m.feed[0].at, spawnClock.Add(time.Second); !got.Equal(want) {
		t.Fatalf("oldest kept batch at %v, want %v: the batch at spawnClock aged past 10 minutes", got, want)
	}
	m.now = spawnClock.Add(10*time.Minute + time.Second)
	batches, dim := m.feedVisible()
	if dim || len(batches) != len(m.feed) {
		t.Fatalf("visible = %d (dim %v), want every kept batch undimmed", len(batches), dim)
	}
}

// A directory with one changed file prints its full path; 2+ files get a header.
func TestFeedFileListGroupsByDirectory(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{
		"pkg/api/a.go": fsnotify.Create,
		"pkg/api/b.go": fsnotify.Write,
		"solo/one.go":  fsnotify.Remove,
	})

	rows := feedFileRows(m.feed[0])
	var got []string
	for _, r := range rows {
		if r.dir {
			got = append(got, r.label)
			continue
		}
		got = append(got, strings.Repeat(" ", r.indent)+r.glyph+" "+r.label)
	}
	want := []string{"pkg/api/", "  + a.go", "  ~ b.go", "− solo/one.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file list = %q, want %q", got, want)
	}
}

// A created directory names itself with a trailing slash and heads the files under it.
func TestFeedCreatedDirectoryHeadsItsFiles(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{
		"pkg/api":      fsnotify.Create,
		"pkg/api/a.go": fsnotify.Create,
		"pkg/api/b.go": fsnotify.Write,
	})

	if got := feedFirstFile(m.feed[0]); got != "pkg/api/" {
		t.Fatalf("first file = %q, want the directory with a trailing slash", got)
	}
	rows := feedFileRows(m.feed[0])
	var got []string
	for _, r := range rows {
		if r.dir {
			got = append(got, r.label)
			continue
		}
		got = append(got, strings.Repeat(" ", r.indent)+r.glyph+" "+r.label)
	}
	want := []string{"pkg/api/", "  + a.go", "  ~ b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file list = %q, want %q", got, want)
	}
}

// A restarted target's row is highlighted for about a second.
func TestRestartFlashExpires(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})

	web := m.targets[0]
	if !web.flashing(spawnClock.Add(500 * time.Millisecond)) {
		t.Fatalf("row not flashing 500ms after the restart")
	}
	if web.flashing(spawnClock.Add(1500 * time.Millisecond)) {
		t.Fatalf("row still flashing 1.5s after the restart")
	}
}

// A stopped target's changes still feed the feed, but it restarted nothing,
// so the batch names no target.
func TestStoppedTargetFeedsTheFeedWithoutNamingItself(t *testing.T) {
	m := newTestModel(t, Target{Name: "web", Cmd: "true", Selected: false, Grace: target("web").Grace})
	m.now = spawnClock
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", at: spawnClock, change: oneFile("a.go")})

	if len(m.feed) != 1 || len(m.feed[0].targets) != 0 {
		t.Fatalf("feed = %+v, want one batch naming no target", m.feed)
	}
}

// A batch names only the targets that restarted for it, not the stopped one that
// saw the same files.
func TestFeedNamesOnlyTargetsThatRestarted(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.group.configPath = ""
	m.now = spawnClock
	m.targets[1].state = stateStopped
	cs := oneFile("a.go")
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", at: spawnClock, change: cs, restarts: true})
	m.applyEvent(event{kind: evChanged, target: "api", count: 1, first: "a.go", at: spawnClock, change: cs})
	m.applyEvent(event{kind: evStarted, target: "web", at: spawnClock})
	m.applyEvent(event{kind: evStarted, target: "api", at: spawnClock})

	if got, want := m.feed[0].targets, []string{"web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

// The list keeps its cursor on screen however many batches the feed holds.
func TestListScrollKeepsTheCursorVisible(t *testing.T) {
	defs := make([]Target, 0, 9)
	for i := 0; i < 9; i++ {
		defs = append(defs, target(fmt.Sprintf("t%d", i)))
	}
	m := newTestModel(t, defs...)
	m.height = 10
	m.cursor = len(m.targets) - 1
	for i, d := range defs {
		changeAt(m, d.Name, spawnClock.Add(time.Duration(i)*time.Second), map[string]fsnotify.Op{fmt.Sprintf("root%d/a.go", i): fsnotify.Write})
	}
	m.frame()

	visible := m.listHeight()
	if visible != m.height-2 {
		t.Fatalf("list height = %d, want the fs row and version row to take 2 rows", visible)
	}
	line := m.cursor - m.layout.offset
	if line < 0 || line >= visible {
		t.Fatalf("the cursor row is at list line %d, outside the %d-row list area", line, visible)
	}
	if got := m.rowAt(2, line+1); got != m.cursor {
		t.Fatalf("the cursor row is off screen: line %d maps to %d, want %d", line, got, m.cursor)
	}
}

// Every left line is exactly leftW cells wide, and a narrow column truncates
// rather than wraps.
func TestEveryLeftLineIsExactlyLeftWide(t *testing.T) {
	for _, width := range []int{24, 30, 100} {
		m := newTestModel(t, target("web"), target("backoffice-webapp"))
		m.width = width
		m.cursor = 2
		changeAt(m, "web", spawnClock, map[string]fsnotify.Op{
			"apps/api/deep/types.gen.go": fsnotify.Write,
			"apps/root.go":               fsnotify.Write,
		})
		m.frame()

		leftW, _ := m.layoutWidths()
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

// The version line owns the bottom row, the fs events line the row above it.
func TestVersionLineOwnsTheBottomRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.height = 14
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
	lines := leftPane(m)

	if got := lines[m.height-1]; !strings.Contains(got, "rerun") {
		t.Fatalf("the bottom row is not the version line: %q", got)
	}
	if got := lines[0]; !strings.HasPrefix(got, "a.go") {
		t.Fatalf("the top row = %q, want the fs row", got)
	}
	if got := m.rowAt(2, m.height-1); got != -1 {
		t.Fatalf("the version row resolved to row %d, want none", got)
	}
}

// An error batch never joins a file batch, and a mixed ChangeSet keeps its files.
func TestFeedKeepsErrorAndFileBatchesApart(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = spawnClock
	web := m.targets[0].spec

	m.recordFeed(rerun.ChangeSet{Error: errors.New("too many open files")}, spawnClock, web)
	m.recordFeed(oneFile("a.go"), spawnClock.Add(50*time.Millisecond), web)

	if len(m.feed) != 2 {
		t.Fatalf("batches = %d, want the error and the file kept apart", len(m.feed))
	}
	if m.feed[0].err == nil || len(m.feed[0].files) != 0 {
		t.Fatalf("error batch = %+v, want the error alone", m.feed[0])
	}
	file := m.feed[1]
	if file.err != nil || len(file.files) != 1 {
		t.Fatalf("file batch = %+v, want a.go without the error", file)
	}

	// A ChangeSet carrying both keeps its files in a batch of their own.
	mixed := oneFile("b.go")
	mixed.Error = errors.New("too many open files")
	m.recordFeed(mixed, spawnClock.Add(time.Second), web)

	if got := len(m.feed); got != 4 {
		t.Fatalf("batches = %d, want the mixed change split into a file and an error batch", got)
	}
	if m.feed[2].err != nil || len(m.feed[2].files) != 1 {
		t.Fatalf("mixed files batch = %+v, want b.go alone", m.feed[2])
	}
	if m.feed[3].err == nil {
		t.Fatalf("mixed error batch = %+v, want the error alone", m.feed[3])
	}
}
