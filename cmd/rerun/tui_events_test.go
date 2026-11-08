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

// eventsLines is the fs events modal as drawn, styling stripped.
func eventsLines(m *tuiModel) []string { return strings.Split(ansi.Strip(m.eventsView()), "\n") }

// eventsBody is the modal's body rows: everything between the header's rule and the footer.
func eventsBody(m *tuiModel) []string {
	lines := eventsLines(m)
	if len(lines) < 5 {
		return nil
	}
	return lines[3 : len(lines)-2]
}

// eventsFooterLine is the modal's last inner row.
func eventsFooterLine(m *tuiModel) string {
	lines := eventsLines(m)
	return lines[len(lines)-2]
}

// changeWithError is a change batch that also carries the watcher's error.
func changeWithError(ops map[string]fsnotify.Op, err error) rerun.ChangeSet {
	cs := changeBatch(ops)
	cs.Error = err
	return cs
}

// e opens the fs events modal; the old w key no longer does.
func TestEventsKeyOpensAndWDoesNot(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24

	press(m, "w")
	if m.eventsModal.open {
		t.Fatal("w still opens the fs events modal")
	}
	press(m, "e")
	if !m.eventsModal.open {
		t.Fatal("e did not open the fs events modal")
	}
}

// e opens the modal at the finder's size, with its title, window and key line.
func TestEventsModalOpensWithHeaderAndFooter(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock

	press(m, "e")
	if !m.eventsModal.open {
		t.Fatal("e did not open the fs events modal")
	}
	outerW, outerH := m.eventsSize()
	if outerW != modalWidth(m.width) || outerH != m.height*4/5 {
		t.Fatalf("modal size = %dx%d, want the finder's %dx%d", outerW, outerH, modalWidth(m.width), m.height*4/5)
	}

	lines := eventsLines(m)
	if !strings.Contains(lines[1], "fs events") || !strings.Contains(lines[1], "last 1m · 1 batch") {
		t.Fatalf("header = %q, want the title and the window count", lines[1])
	}
	if got := eventsFooterLine(m); !strings.Contains(got, "j k move · → files · ← back · esc close") {
		t.Fatalf("footer = %q, want the key line", got)
	}
}

// The body lists one row per batch, newest first, each with its absolute local time.
func TestEventsModalRowsNewestFirstWithAbsoluteTime(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	changeAt(m, "web", spawnClock.Add(time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(2 * time.Second)

	press(m, "e")
	body := eventsBody(m)
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
func TestEventsModalErrorBatchShowsErrorText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")),
	})

	press(m, "e")
	body := eventsBody(m)
	if len(body) == 0 || !strings.Contains(body[0], "too many open files") {
		t.Fatalf("body = %q, want the watcher's error text", body)
	}
}

// → expands the cursor batch's files under its row and ← collapses them.
func TestEventsModalRightExpandsLeftCollapses(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write, "apps/b.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "e")

	if got := strings.Join(eventsBody(m), "\n"); strings.Contains(got, "b.go") {
		t.Fatalf("files listed before →:\n%s", got)
	}
	press(m, "right")
	joined := strings.Join(eventsBody(m), "\n")
	if !strings.Contains(joined, "apps/") || !strings.Contains(joined, "b.go") {
		t.Fatalf("→ did not list the batch's files grouped by dir:\n%s", joined)
	}
	press(m, "left")
	if got := strings.Join(eventsBody(m), "\n"); strings.Contains(got, "b.go") {
		t.Fatalf("← did not collapse the file list:\n%s", got)
	}
}

// A batch that arrives while the modal is open takes the top row without moving
// the cursor off its own batch.
func TestEventsModalKeepsCursorWhenANewBatchArrives(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "e")
	first := m.eventsModal.cursor

	changeAt(m, "web", spawnClock.Add(time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(time.Second)
	if m.eventsModal.cursor != first {
		t.Fatalf("cursor = %d, want it still on batch %d", m.eventsModal.cursor, first)
	}

	batches, dim := m.feedVisible()
	rows := m.eventsRows(batches, dim)
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
func TestEventsModalEscCloses(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "e")
	press(m, "esc")
	if m.eventsModal.open {
		t.Fatal("esc did not close the fs events modal")
	}
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "fs events") {
		t.Fatalf("the modal is still drawn after esc:\n%s", view)
	}
}

// The help overlay names the w key.
func TestHelpNamesFsEvents(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	press(m, "?")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "fs events (last minute)") {
		t.Fatalf("short help does not name w:\n%s", view)
	}
	press(m, "?")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "fs events (last minute)") {
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

	press(m, "e")
	if body := eventsBody(m); len(body) == 0 || !strings.Contains(body[0], "too many open files") {
		t.Fatalf("e modal body = %q, want the watcher's error text", body)
	}
	for _, line := range drawerOf(m) {
		if strings.Contains(line, "too many open files") {
			return
		}
	}
	t.Fatalf("drawer = %q, want the watcher's error text", drawerOf(m))
}

// Clicks and wheels under the w modal reach nothing: the frame stays as it was.
func TestEventsModalBlocksMouse(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "e")

	before, cursor, offset, follow := m.eventsModal, m.cursor, m.vp.YOffset(), m.follow
	m.handleMouse(mouse(2, 2, false))
	m.handleMouse(wheelDown(2, 1))
	m.handleMouse(wheelUp(2, 1))

	if m.eventsModal != before || m.cursor != cursor || m.vp.YOffset() != offset || m.follow != follow {
		t.Fatalf("mouse reached the frame under the modal: events=%+v cursor=%d offset=%d follow=%v", m.eventsModal, m.cursor, m.vp.YOffset(), m.follow)
	}
}

// An empty feed shows a faint placeholder instead of a blank body.
func TestEventsModalEmptyFeedPlaceholder(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	press(m, "e")

	if body := strings.Join(eventsBody(m), "\n"); !strings.Contains(body, "no fs events in the last minute") {
		t.Fatalf("empty feed body = %q, want the placeholder", body)
	}
}

// Drawing the modal twice is identical and moves no state; the handlers clamp top.
func TestEventsViewStaysPure(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	for i := range 30 {
		changeAt(m, "web", spawnClock.Add(time.Duration(i)*time.Second), map[string]fsnotify.Op{"a.go": fsnotify.Write})
	}
	m.now = spawnClock.Add(30 * time.Second)
	press(m, "e")
	for range 30 {
		press(m, "j")
	}

	top := m.eventsModal.top
	if top == 0 {
		t.Fatal("the cursor did not scroll the modal, so the purity check is vacuous")
	}
	first := m.eventsView()
	if m.eventsModal.top != top {
		t.Fatalf("eventsView moved top from %d to %d", top, m.eventsModal.top)
	}
	if second := m.eventsView(); second != first {
		t.Fatal("two renders of the same state differ")
	}
	if m.eventsModal.top != top {
		t.Fatalf("the second render moved top from %d to %d", top, m.eventsModal.top)
	}
}
