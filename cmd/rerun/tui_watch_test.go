package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// watchLines is the what-changed modal as drawn, styling stripped.
func watchLines(m *tuiModel) []string { return strings.Split(ansi.Strip(m.watchView()), "\n") }

// watchBody is the modal's body rows: everything between the header's rule and the footer.
func watchBody(m *tuiModel) []string {
	lines := watchLines(m)
	if len(lines) < 5 {
		return nil
	}
	return lines[3 : len(lines)-2]
}

// watchFooterLine is the modal's last inner row.
func watchFooterLine(m *tuiModel) string {
	lines := watchLines(m)
	return lines[len(lines)-2]
}

// changeWithError is a change batch that also carries the watcher's error.
func changeWithError(ops map[string]fsnotify.Op, err error) rerun.ChangeSet {
	cs := changeBatch(ops)
	cs.Error = err
	return cs
}

// w opens the modal at the finder's size, with its title, window and key line.
func TestWatchModalOpensWithHeaderAndFooter(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock

	press(m, "w")
	if !m.watch.open {
		t.Fatal("w did not open the what-changed modal")
	}
	outerW, outerH := m.watchSize()
	if outerW != modalWidth(m.width) || outerH != m.height*4/5 {
		t.Fatalf("modal size = %dx%d, want the finder's %dx%d", outerW, outerH, modalWidth(m.width), m.height*4/5)
	}

	lines := watchLines(m)
	if !strings.Contains(lines[1], "what changed") || !strings.Contains(lines[1], "last 1m · 1 batch") {
		t.Fatalf("header = %q, want the title and the window count", lines[1])
	}
	if got := watchFooterLine(m); !strings.Contains(got, "j k move · → files · ← back · esc close") {
		t.Fatalf("footer = %q, want the key line", got)
	}
}

// The body lists one row per batch, newest first, each with its absolute local time.
func TestWatchModalRowsNewestFirstWithAbsoluteTime(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	changeAt(m, "web", spawnClock.Add(time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(2 * time.Second)

	press(m, "w")
	body := watchBody(m)
	if len(body) < 2 {
		t.Fatalf("body = %d rows, want one per batch:\n%s", len(body), strings.Join(body, "\n"))
	}
	newest, older := spawnClock.Add(time.Second).Local(), spawnClock.Local()
	if !strings.Contains(body[0], newest.Format("15:04:05")) || !strings.Contains(body[0], "b.go") {
		t.Fatalf("first row = %q, want the newest batch with its absolute time", body[0])
	}
	if !strings.Contains(body[1], older.Format("15:04:05")) || !strings.Contains(body[1], "a.go") {
		t.Fatalf("second row = %q, want the older batch with its absolute time", body[1])
	}
}

// A batch whose ChangeSet carried an error shows the error text, not its files.
func TestWatchModalErrorBatchShowsErrorText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")),
	})

	press(m, "w")
	body := watchBody(m)
	if len(body) == 0 || !strings.Contains(body[0], "too many open files") {
		t.Fatalf("body = %q, want the watcher's error text", body)
	}
}

// → expands the cursor batch's files under its row and ← collapses them.
func TestWatchModalRightExpandsLeftCollapses(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write, "apps/b.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "w")

	if got := strings.Join(watchBody(m), "\n"); strings.Contains(got, "b.go") {
		t.Fatalf("files listed before →:\n%s", got)
	}
	press(m, "right")
	joined := strings.Join(watchBody(m), "\n")
	if !strings.Contains(joined, "apps/") || !strings.Contains(joined, "b.go") {
		t.Fatalf("→ did not list the batch's files grouped by dir:\n%s", joined)
	}
	press(m, "left")
	if got := strings.Join(watchBody(m), "\n"); strings.Contains(got, "b.go") {
		t.Fatalf("← did not collapse the file list:\n%s", got)
	}
}

// A batch that arrives while the modal is open takes the top row without moving
// the cursor off its own batch.
func TestWatchModalKeepsCursorWhenANewBatchArrives(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "w")
	first := m.watch.cursor

	changeAt(m, "web", spawnClock.Add(time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(time.Second)
	if m.watch.cursor != first {
		t.Fatalf("cursor = %d, want it still on batch %d", m.watch.cursor, first)
	}

	batches, dim := m.feedVisible()
	rows := m.watchRows(batches, dim)
	if rows[0].batch.id != m.feed[len(m.feed)-1].id || rows[0].batch.id == first {
		t.Fatalf("top row batch = %d, want the new batch above the cursor's", rows[0].batch.id)
	}
	for _, r := range rows {
		if r.batch != nil && r.batch.id == first && !r.sel {
			t.Fatal("the cursor's batch lost its selected row")
		}
	}
}

// esc closes the modal, and the live frame is back.
func TestWatchModalEscCloses(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "w")
	press(m, "esc")
	if m.watch.open {
		t.Fatal("esc did not close the what-changed modal")
	}
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "what changed") {
		t.Fatalf("the modal is still drawn after esc:\n%s", view)
	}
}

// The help overlay names the w key.
func TestHelpNamesWhatChanged(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	press(m, "?")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "what changed (last minute)") {
		t.Fatalf("short help does not name w:\n%s", view)
	}
	press(m, "?")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "what changed (last minute)") {
		t.Fatalf("full help does not name w:\n%s", view)
	}
}

// A watcher error rides a change batch into the feed: the modal and the drawer
// show its text, and no target restarts for it.
func TestWatcherErrorReachesTheFeedWithoutRestart(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "web", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
	r.handleChange(context.Background(), changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")))

	if got := eventKinds(sink); len(got) != 1 || got[0] != evChanged {
		t.Fatalf("watcher error reported %v, want one evChanged and no restart", got)
	}

	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	m.applyEvents(sink.all())
	if n := len(m.feed); n != 2 {
		t.Fatalf("feed = %d batches, want the files and the error kept apart", n)
	}
	if m.feed[0].err != nil || len(m.feed[0].files) != 1 {
		t.Fatalf("file batch = %+v, want the file with no error", m.feed[0])
	}
	if m.feed[1].err == nil {
		t.Fatalf("feed = %+v, want the error batch as the newest", m.feed)
	}
	if got := m.feed[0].targets; len(got) != 0 {
		t.Fatalf("feed targets = %v, want none triggered", got)
	}

	press(m, "w")
	if body := watchBody(m); len(body) == 0 || !strings.Contains(body[0], "too many open files") {
		t.Fatalf("w modal body = %q, want the watcher's error text", body)
	}
	for _, line := range drawerOf(m) {
		if strings.Contains(line, "too many open files") {
			return
		}
	}
	t.Fatalf("drawer = %q, want the watcher's error text", drawerOf(m))
}

// Clicks and wheels under the w modal reach nothing: the frame stays as it was.
func TestWatchModalBlocksMouse(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "w")

	watch, cursor, offset, follow := m.watch, m.cursor, m.vp.YOffset(), m.follow
	m.handleMouse(mouse(2, 2, false))
	m.handleMouse(wheelDown(2, 1))
	m.handleMouse(wheelUp(2, 1))

	if m.watch != watch || m.cursor != cursor || m.vp.YOffset() != offset || m.follow != follow {
		t.Fatalf("mouse reached the frame under the modal: watch=%+v cursor=%d offset=%d follow=%v", m.watch, m.cursor, m.vp.YOffset(), m.follow)
	}
}

// An empty feed shows a faint placeholder instead of a blank body.
func TestWatchModalEmptyFeedPlaceholder(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	press(m, "w")

	if body := strings.Join(watchBody(m), "\n"); !strings.Contains(body, "no changes in the last minute") {
		t.Fatalf("empty feed body = %q, want the placeholder", body)
	}
}

// Drawing the modal twice is identical and moves no state; the handlers clamp top.
func TestWatchViewStaysPure(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	for i := range 30 {
		changeAt(m, "web", spawnClock.Add(time.Duration(i)*time.Second), map[string]fsnotify.Op{"a.go": fsnotify.Write})
	}
	m.now = spawnClock.Add(30 * time.Second)
	press(m, "w")
	for range 30 {
		press(m, "j")
	}

	top := m.watch.top
	if top == 0 {
		t.Fatal("the cursor did not scroll the modal, so the purity check is vacuous")
	}
	first := m.watchView()
	if m.watch.top != top {
		t.Fatalf("watchView moved top from %d to %d", top, m.watch.top)
	}
	if second := m.watchView(); second != first {
		t.Fatal("two renders of the same state differ")
	}
	if m.watch.top != top {
		t.Fatalf("the second render moved top from %d to %d", top, m.watch.top)
	}
}
