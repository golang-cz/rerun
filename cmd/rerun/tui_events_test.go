package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
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

// rowText is a modal body line without its box border and padding.
func rowText(line string) string { return strings.Trim(line, "│ ") }

// changeWithError is a change batch that also carries the watcher's error.
func changeWithError(ops map[string]fsnotify.Op, err error) rerun.ChangeSet {
	cs := changeBatch(ops)
	cs.Error = err
	return cs
}

// watchedTarget is a target with an on: watchlist, for per-file service matching.
func watchedTarget(name string, on ...string) Target {
	t := target(name)
	t.On = on
	return t
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
	if outerW != modalWidth(m.width) {
		t.Fatalf("modal width = %d, want the finder's %d", outerW, modalWidth(m.width))
	}
	// One batch fits on one row: the box hugs its content, floored at 8 rows.
	if outerH != 8 {
		t.Fatalf("modal height = %d, want the 8-row floor for one batch", outerH)
	}

	lines := eventsLines(m)
	if !strings.Contains(lines[1], "fs events") || !strings.Contains(lines[1], "last 1m · 1 batch") {
		t.Fatalf("header = %q, want the title and the window count", lines[1])
	}
	if got := eventsFooterLine(m); !strings.Contains(got, "j k move · → expand · c copy all · esc close") {
		t.Fatalf("footer = %q, want the expand key line on a collapsed cursor", got)
	}
	if strings.Contains(eventsFooterLine(m), "← collapse") {
		t.Fatalf("footer = %q, want no ← collapse while the cursor batch is collapsed", eventsFooterLine(m))
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

// A collapsed row is the time, the first file with +N, and the restarted
// services right-aligned behind one middle dot — never op counts or a dot leader.
func TestEventsModalCollapsedRowLayout(t *testing.T) {
	m := newTestModel(t, watchedTarget("omsx-api"), watchedTarget("files"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	for _, name := range []string{"omsx-api", "files"} {
		changeAt(m, name, spawnClock, map[string]fsnotify.Op{
			"apps/api/data/schema.svg": fsnotify.Write,
			"apps/api/data/zz.svg":     fsnotify.Write,
		})
	}

	press(m, "e")
	row := rowText(eventsBody(m)[0])
	want := "14:32:07  apps/api/data/schema.svg +1"
	if !strings.HasPrefix(row, want) {
		t.Fatalf("row = %q, want it to start with %q", row, want)
	}
	if !strings.HasSuffix(row, "· ↻ omsx-api, files") {
		t.Fatalf("row = %q, want the services right-aligned behind one · with the ↻ mark", row)
	}
	if strings.Contains(row, "→") || strings.Contains(row, "~1") || strings.Contains(row, "none") {
		t.Fatalf("row = %q, want no arrow, op counts or none", row)
	}
	between := row[len(want) : len(row)-len("· ↻ omsx-api, files")]
	if strings.Contains(between, "·") {
		t.Fatalf("row = %q, want a single middle dot, never a dot leader", row)
	}
}

// A batch that restarted nothing ends after its path.
func TestEventsModalRowWithoutServicesEndsAfterThePath(t *testing.T) {
	m := newTestModel(t, Target{Name: "web", Cmd: "true", Selected: false, Grace: target("web").Grace})
	m.width, m.height = 100, 30
	m.now = spawnClock
	m.applyEvent(event{kind: evChanged, target: "web", count: 1, first: "a.go", at: spawnClock, change: oneFile("a.go")})

	press(m, "e")
	row := rowText(eventsBody(m)[0])
	if want := "14:32:07  a.go"; row != want {
		t.Fatalf("row = %q, want exactly %q", row, want)
	}
}

// Services that do not fit shorten to `a, b +N more`; the path takes a middle
// ellipsis when the row is still too long.
func TestEventsModalRowShortensServicesAndPath(t *testing.T) {
	names := []string{"alpha-service", "bravo-service", "charlie-service", "delta-service"}
	defs := make([]Target, 0, len(names))
	for _, name := range names {
		defs = append(defs, watchedTarget(name))
	}
	m := newTestModel(t, defs...)
	m.width, m.height = 80, 24
	m.now = spawnClock
	for _, name := range names {
		changeAt(m, name, spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	}

	press(m, "e")
	row := rowText(eventsBody(m)[0])
	if !strings.Contains(row, "+") || !strings.HasSuffix(row, "more") {
		t.Fatalf("row = %q, want the services shortened to `a, b +N more`", row)
	}
	if strings.Contains(row, "delta-service") && !strings.Contains(row, "+1 more") {
		t.Fatalf("row = %q, want cut names counted, never dropped silently", row)
	}

	long := newTestModel(t, watchedTarget("web"))
	long.width, long.height = 80, 24
	long.now = spawnClock
	changeAt(long, "web", spawnClock, map[string]fsnotify.Op{
		"apps/api/some/deeply/nested/directory/structure/schema.svg": fsnotify.Write,
	})
	press(long, "e")
	row = rowText(eventsBody(long)[0])
	if !strings.Contains(row, "…") || !strings.Contains(row, "schema.svg") {
		t.Fatalf("row = %q, want the path cut with a middle ellipsis", row)
	}
	outerW, _ := long.eventsSize()
	if got := displayWidth(eventsBody(long)[0]); got > outerW {
		t.Fatalf("row = %d cells, want within the %d-cell modal", got, outerW)
	}
}

// A watcher error is one red line above the batches, never a row of its own.
func TestEventsModalErrorIsOneRedLineAboveTheBatches(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")),
	})

	press(m, "e")
	body := eventsBody(m)
	if len(body) == 0 || !strings.HasPrefix(rowText(body[0]), "⚠ watch failed: too many open files") {
		t.Fatalf("body = %q, want the error line on top", body)
	}
	rows := 0
	for _, line := range body[1:] {
		if strings.Contains(line, "a.go") {
			rows++
		}
		if strings.Contains(line, "too many open files") {
			t.Fatalf("the error repeats on row %d:\n%s", rows, strings.Join(body, "\n"))
		}
	}
	if rows != 1 {
		t.Fatalf("body = %q, want the file batch as the one row under the error", body)
	}

	red := lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed)).Render("x")
	for _, line := range strings.Split(m.eventsView(), "\n") {
		if strings.Contains(line, "watch failed") {
			if !strings.Contains(line, red[:strings.Index(red, "x")]) {
				t.Fatalf("error line = %q, want red paint", ansi.Strip(line))
			}
			return
		}
	}
	t.Fatal("no watch failed line drawn")
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

// Every expanded file row names the services whose watch globs that file matched.
func TestEventsModalExpandedRowsNamePerFileServices(t *testing.T) {
	m := newTestModel(t, watchedTarget("web", "./"), watchedTarget("api", "api/"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"api/x.go": fsnotify.Write, "web/y.go": fsnotify.Write})
	changeAt(m, "api", spawnClock, map[string]fsnotify.Op{"api/x.go": fsnotify.Write})

	press(m, "e", "right")
	body := eventsBody(m)
	var xRow, yRow string
	for _, line := range body {
		switch {
		case strings.Contains(line, "~ api/x.go"):
			xRow = rowText(line)
		case strings.Contains(line, "~ web/y.go"):
			yRow = rowText(line)
		}
	}
	if !strings.HasSuffix(xRow, "· ↻ web, api") {
		t.Fatalf("x.go row = %q, want both watchers named", xRow)
	}
	if !strings.HasSuffix(yRow, "· ↻ web") {
		t.Fatalf("y.go row = %q, want only web named", yRow)
	}
}

// Per-file services are recorded when the change lands, never re-inferred
// from watch globs: a file web's globs would match but web never reported does
// not name web. (The watcher's on:/except: filtering stays in the watcher, one
// ChangeSet per target.)
func TestEventsModalPerFileServicesAreRecordedNotInferred(t *testing.T) {
	m := newTestModel(t, watchedTarget("web", "./"), watchedTarget("logger", "./"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	changeAt(m, "logger", spawnClock, map[string]fsnotify.Op{"debug.log": fsnotify.Write})

	press(m, "e", "right")
	body := eventsBody(m)
	for _, line := range body {
		if strings.Contains(line, "debug.log") && strings.Contains(line, "web") {
			t.Fatalf("debug.log row = %q, want only logger: services are recorded, not re-matched", rowText(line))
		}
		if strings.Contains(line, "~ debug.log") && !strings.HasSuffix(rowText(line), "· ↻ logger") {
			t.Fatalf("debug.log row = %q, want logger named", rowText(line))
		}
		if strings.Contains(line, "~ a.go") && !strings.HasSuffix(rowText(line), "· ↻ web") {
			t.Fatalf("a.go row = %q, want web named", rowText(line))
		}
	}
}

// At 80 columns an expanded file row ellipsizes a long path from its middle,
// and services that do not fit continue on lines aligned under the row's ·,
// clamped so every service name fits whole.
func TestEventsModalExpandedRowEllipsizesPathAndClampsServices(t *testing.T) {
	names := []string{"svc-one", "svc-two", "svc-three"}
	defs := make([]Target, 0, len(names))
	for _, name := range names {
		defs = append(defs, watchedTarget(name, "./"))
	}
	m := newTestModel(t, defs...)
	m.width, m.height = 80, 24
	m.now = spawnClock
	long := "apps/api/some/deeply/nested/directory/structure/that/keeps/going/schema.svg"
	if displayWidth(long) <= 64 {
		t.Fatalf("the test path is %d cells, want > 64", displayWidth(long))
	}
	for _, name := range names {
		changeAt(m, name, spawnClock, map[string]fsnotify.Op{long: fsnotify.Write})
	}

	press(m, "e", "right")
	outerW, _ := m.eventsSize()
	innerW := m.modalInnerW(outerW)
	body := eventsBody(m)
	joined := strings.Join(body, "\n")
	var fileRow string
	var conts []string
	for _, line := range body {
		switch {
		case strings.Contains(line, "~ ") && strings.Contains(line, "svg"):
			fileRow = line
		case strings.Contains(line, "svc-") && !strings.Contains(line, "svg"):
			conts = append(conts, line)
		}
	}
	if fileRow == "" {
		t.Fatalf("no expanded file row:\n%s", joined)
	}
	row := rowText(fileRow)
	if !strings.Contains(row, "…") || !strings.HasSuffix(row, "· ↻ svc-one") {
		t.Fatalf("file row = %q, want the path middle-ellipsized with the first service kept", row)
	}
	if got := displayWidth(row); got > innerW {
		t.Fatalf("file row = %d cells, want within the %d-cell modal: %q", got, innerW, row)
	}
	for _, name := range names {
		if !strings.Contains(joined, name) {
			t.Fatalf("services cut %s:\n%s", name, joined)
		}
	}
	if len(conts) == 0 {
		t.Fatalf("no continuation lines:\n%s", joined)
	}
	dot := displayWidth(fileRow[:strings.Index(fileRow, "·")]) // cells: the … shifts byte offsets
	for _, c := range conts {
		if got := displayWidth(c[:strings.Index(c, "svc-")]); got != dot {
			t.Fatalf("continuation = %q starts at cell %d, want aligned under the · at %d", c, got, dot)
		}
		if got := displayWidth(rowText(c)); got > innerW {
			t.Fatalf("continuation = %d cells, want within the %d-cell modal: %q", got, innerW, c)
		}
	}
}

// y copies the error text even when no file batch is visible.
func TestEventsModalCopyWithOnlyAnError(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: rerun.ChangeSet{Error: errors.New("too many open files")},
	})

	press(m, "e")
	if got := strings.Join(eventsBody(m), "\n"); !strings.Contains(got, "watch failed") {
		t.Fatalf("body = %q, want the error line", got)
	}
	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatal("y on an error-only modal returned no clipboard command")
	}
	text, _ := m.eventsCopyText()
	if got := fmt.Sprint(cmd()); got != text {
		t.Fatalf("clipboard payload = %q, want the copy text %q", got, text)
	}
}

// c in the fs events modal sends the batches' text to the clipboard as one payload.
func TestEventsCopySendsItsTextToTheClipboard(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	press(m, "e")

	text, _ := m.eventsCopyText()
	if text == "" {
		t.Fatal("setup: the modal has nothing to copy")
	}
	if got := clipboardText(t, press(m, "c")); got != text {
		t.Fatalf("clipboard = %q, want %q", got, text)
	}
}

// A service whose current state is exited gets a ✗ after its name.
func TestEventsModalPerFileServicesMarkExited(t *testing.T) {
	m := newTestModel(t, watchedTarget("web", "./"), watchedTarget("api", "./"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	changeAt(m, "api", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.targets[1].state = stateExited

	press(m, "e", "right")
	for _, line := range eventsBody(m) {
		if strings.Contains(line, "~ a.go") {
			if got := rowText(line); !strings.HasSuffix(got, "· ↻ web, api ✗") {
				t.Fatalf("a.go row = %q, want api marked ✗", got)
			}
			return
		}
	}
	t.Fatal("the expanded list has no a.go row")
}

// Services that do not fit wrap onto continuation lines aligned under the ·, never cut.
func TestEventsModalPerFileServicesWrap(t *testing.T) {
	names := []string{"service-one", "service-two", "service-three", "service-four", "service-five", "service-six"}
	defs := make([]Target, 0, len(names))
	for _, name := range names {
		defs = append(defs, watchedTarget(name, "./"))
	}
	m := newTestModel(t, defs...)
	m.width, m.height = 80, 24
	m.now = spawnClock
	for _, name := range names {
		changeAt(m, name, spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	}

	press(m, "e", "right")
	body := eventsBody(m)
	joined := strings.Join(body, "\n")
	for _, name := range names {
		if !strings.Contains(joined, name) {
			t.Fatalf("services cut %s:\n%s", name, joined)
		}
	}
	var fileRow string
	var conts []string
	for _, line := range body {
		switch {
		case strings.Contains(line, "~ a.go"):
			fileRow = line
		case strings.Contains(line, "service-") && !strings.Contains(line, "a.go"):
			conts = append(conts, line)
		}
	}
	if len(conts) == 0 {
		t.Fatalf("no continuation lines:\n%s", joined)
	}
	dot := strings.Index(fileRow, "·")
	if dot < 0 {
		t.Fatalf("file row = %q, want its · marker", fileRow)
	}
	for _, c := range conts {
		if got := strings.Index(c, "service-"); got != dot {
			t.Fatalf("continuation = %q starts at %d, want aligned under the · at %d", c, got, dot)
		}
	}
}

// Consecutive batches over the same file set show as one row with the newest
// time and every restarted service.
func TestEventsModalMergesSameFileBatches(t *testing.T) {
	m := newTestModel(t, watchedTarget("web"), watchedTarget("api"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{".schema-diagram-1.svg": fsnotify.Create})
	changeAt(m, "api", spawnClock.Add(time.Second), map[string]fsnotify.Op{".schema-diagram-1.svg": fsnotify.Write})
	changeAt(m, "web", spawnClock.Add(2*time.Second), map[string]fsnotify.Op{"other.go": fsnotify.Write})

	press(m, "e")
	body := eventsBody(m)
	rows := 0
	for _, line := range body {
		if rowText(line) != "" {
			rows++
		}
	}
	if rows != 2 {
		t.Fatalf("body = %d rows, want the merged pair and other.go:\n%s", rows, strings.Join(body, "\n"))
	}
	merged := rowText(body[0])
	if !strings.Contains(merged, "other.go") {
		t.Fatalf("first row = %q, want the newest batch", merged)
	}
	merged = rowText(body[1])
	if !strings.Contains(merged, spawnClock.Add(time.Second).Local().Format("15:04:05")) {
		t.Fatalf("merged row = %q, want the newest of the merged times", merged)
	}
	if !strings.HasSuffix(merged, "· ↻ api, web") {
		t.Fatalf("merged row = %q, want both batches' services", merged)
	}
}

// y copies every visible batch expanded, with per-file services, and the footer
// confirms for two seconds.
func TestEventsModalCopyAll(t *testing.T) {
	m := newTestModel(t, watchedTarget("web", "./"), watchedTarget("api", "api/"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"api/x.go": fsnotify.Write, "web/y.go": fsnotify.Write})
	changeAt(m, "api", spawnClock, map[string]fsnotify.Op{"api/x.go": fsnotify.Write})
	changeAt(m, "web", spawnClock.Add(time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})

	press(m, "e")
	if _, cmd := m.Update(keyMsg("c")); cmd == nil {
		t.Fatal("y returned no clipboard command")
	}
	if got := eventsFooterLine(m); !strings.Contains(got, "copied 2 batches") {
		t.Fatalf("footer = %q, want the copy confirmation", got)
	}
	m.now = spawnClock.Add(3 * time.Second)
	if got := eventsFooterLine(m); !strings.Contains(got, "c copy all") {
		t.Fatalf("footer after 3s = %q, want the key line back", got)
	}

	text, n := m.eventsCopyText()
	if n != 2 {
		t.Fatalf("copied %d batches, want 2", n)
	}
	want := []string{
		"14:32:08  b.go  · web",
		"~ b.go  · web",
		"14:32:07  api/x.go +1  · web, api",
		"~ api/x.go  · web, api",
		"~ web/y.go  · web",
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("copy = %d lines, want %d:\n%s", len(lines), len(want), text)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("copy line %d = %q, want %q\nfull text:\n%s", i, lines[i], w, text)
		}
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
	rows := m.eventsRows(batches, dim, 76)
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
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "fs events ·") && strings.Contains(view, "last 1m") {
		t.Fatalf("the modal is still drawn after esc:\n%s", view)
	}
}

// The help overlay names the e key.
func TestHelpNamesFsEvents(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	press(m, "?")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "fs events (last minute)") {
		t.Fatalf("short help does not name e:\n%s", view)
	}
	press(m, "?")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "fs events (last minute)") {
		t.Fatalf("full help does not name e:\n%s", view)
	}
}

// A watcher error rides a change batch into the feed: the modal shows its text
// on the red line, and no target restarts for it.
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
}

// Clicks and wheels under the e modal reach nothing: the frame stays as it was.
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
		changeAt(m, "web", spawnClock.Add(time.Duration(i)*time.Second), map[string]fsnotify.Op{fmt.Sprintf("f%02d.go", i): fsnotify.Write})
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

// The modal hugs its content: header and footer plus the batch rows, floored
// at 8 rows, capped at the finder's height. The footer offers ← collapse only
// while the cursor batch is expanded.
func TestEventsModalHeightFitsContent(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write, "apps/b.go": fsnotify.Write})
	m.now = spawnClock

	press(m, "e")
	if _, h := m.eventsSize(); h != 8 {
		t.Fatalf("height = %d, want the 8-row floor for one collapsed batch", h)
	}

	press(m, "right") // expand: a dir row and two file rows join the batch row
	if _, h := m.eventsSize(); h != 2+3+4 {
		t.Fatalf("height = %d expanded, want frame+chrome+4 content rows", h)
	}
	if got := eventsFooterLine(m); !strings.Contains(got, "← collapse") || strings.Contains(got, "→ expand") {
		t.Fatalf("footer = %q, want ← collapse only while expanded", got)
	}
	press(m, "left")
	if got := eventsFooterLine(m); !strings.Contains(got, "→ expand") || strings.Contains(got, "← collapse") {
		t.Fatalf("footer = %q, want → expand only while collapsed", got)
	}

	m2 := newTestModel(t, target("web"))
	m2.width, m2.height = 100, 30
	for i := range 30 {
		changeAt(m2, "web", spawnClock.Add(time.Duration(i)*time.Second), map[string]fsnotify.Op{fmt.Sprintf("f%02d.go", i): fsnotify.Write})
	}
	m2.now = spawnClock.Add(30 * time.Second)
	press(m2, "e")
	if _, h := m2.eventsSize(); h != 30*4/5 {
		t.Fatalf("height = %d for 30 batches, want the finder cap %d", h, 30*4/5)
	}
}
