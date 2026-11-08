package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// feedWidth is a column wide enough for a whole feed line, so a test reads text
// instead of truncation.
const feedWidth = 60

// changeAt feeds one change batch at a fixed clock, so the drawer ages it the
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

// drawerAt is the fs drawer as drawn at width: the header line, then the body.
// The blank divider and the version line sit below it.
func drawerAt(m *tuiModel, width int) []string {
	m.frame()
	n := len(m.layout.drawer)
	out := make([]string, 0, n)
	for y := m.height - 2 - n; y < m.height-2; y++ {
		out = append(out, strings.TrimRight(ansi.Strip(m.leftLine(y, width)), " "))
	}
	return out
}

func drawerOf(m *tuiModel) []string { return drawerAt(m, feedWidth) }

// feedBatchY is the screen row of the drawer's body line n, counting from 0.
func feedBatchY(m *tuiModel, n int) int {
	m.frame()
	return m.height - 2 - len(m.layout.drawer) + 1 + n
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

// Batches older than a minute drop off; with none left the newest stays, dimmed.
func TestFeedExpiresAfterAMinuteAndKeepsTheLatestDimmed(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock.Add(-2*time.Minute), map[string]fsnotify.Op{"old.go": fsnotify.Write})
	changeAt(m, "web", spawnClock.Add(-30*time.Second), map[string]fsnotify.Op{"new.go": fsnotify.Write})
	m.now = spawnClock

	batches, dim := m.feedVisible()
	if dim || len(batches) != 1 || batches[0].files["new.go"] == 0 {
		t.Fatalf("visible = %d batches (dim %v), want only new.go undimmed", len(batches), dim)
	}

	m.now = spawnClock.Add(5 * time.Minute)
	rows := m.drawerRows()
	if len(rows) != 2 || rows[1].kind != drawerBatch || !rows[1].dim {
		t.Fatalf("drawer = %+v, want the header and the dimmed latest batch", rows)
	}
	if got := drawerOf(m); !strings.Contains(got[1], "new.go") {
		t.Fatalf("dimmed batch line = %q, want new.go", got[1])
	}
}

// A line reads the batch age, the first file with +N for the rest, and the targets it restarted.
func TestFeedLineNamesFileCountAndTargets(t *testing.T) {
	m := newTestModel(t, target("omsx-api"), target("backoffice"))
	m.now = spawnClock
	changeAt(m, "omsx-api", spawnClock, map[string]fsnotify.Op{
		"apps/api/rpc/users.go": fsnotify.Write,
		"apps/api/rpc/x.go":     fsnotify.Write,
		"apps/api/rpc/y.go":     fsnotify.Write,
	})
	changeAt(m, "backoffice", spawnClock, map[string]fsnotify.Op{"apps/api/rpc/users.go": fsnotify.Write})

	got := drawerOf(m)
	if want := "fs · last 1m"; !strings.HasPrefix(got[0], want) {
		t.Fatalf("header = %q, want the %q title", got[0], want)
	}
	if want := "   0s  apps/api/rpc/users.go +2  → omsx-api, backoffice"; got[1] != want {
		t.Fatalf("line = %q, want %q", got[1], want)
	}
}

// A narrow column cuts the target list with an ellipsis and keeps the first name.
func TestFeedLineTruncatesTargetsWithAnEllipsis(t *testing.T) {
	m := newTestModel(t, target("omsx-api"), target("backoffice"), target("worker-one"), target("auth-api"))
	m.now = spawnClock
	for _, name := range []string{"omsx-api", "backoffice", "worker-one", "auth-api"} {
		changeAt(m, name, spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	}

	got := drawerAt(m, 30)
	if want := "   0s  a.go  → omsx-api, back…"; got[1] != want {
		t.Fatalf("line = %q, want %q", got[1], want)
	}
	if line := m.leftLine(m.height-2-len(m.layout.drawer)+1, 30); displayWidth(line) != 30 {
		t.Fatalf("truncated line = %d cells, want 30", displayWidth(line))
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
	press(m, "tab", "enter")

	got := drawerOf(m)
	if !strings.HasPrefix(got[0], "fs · 14:32:07 · 3 files") {
		t.Fatalf("header = %q, want the batch's file count", got[0])
	}
	want := []string{"pkg/api/", "  + a.go", "  ~ b.go", "− solo/one.go"}
	if !reflect.DeepEqual(got[1:], want) {
		t.Fatalf("file list = %q, want %q", got[1:], want)
	}

	press(m, "esc")
	if got := drawerOf(m)[0]; !strings.HasPrefix(got, "fs · last 1m") {
		t.Fatalf("header after esc = %q, want the feed back", got)
	}
	press(m, "esc")
	if m.drawerFocus {
		t.Fatalf("second esc left the drawer focused")
	}
}

// A click on a batch line focuses the drawer and lists its files.
func TestFeedClickOpensTheBatchFiles(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})

	m.handleMouse(mouse(2, feedBatchY(m, 0), false))
	if !m.drawerFocus || m.feedOpen == 0 {
		t.Fatalf("click focus = %v, open = %d; want the drawer focused with the batch open", m.drawerFocus, m.feedOpen)
	}
	if got := drawerOf(m)[0]; !strings.HasPrefix(got, "fs · 14:32:07 · 1 file ") {
		t.Fatalf("header = %q, want the batch's file list", got)
	}

	m.handleMouse(mouse(2, 0, false))
	if m.drawerFocus {
		t.Fatalf("a list click left the drawer focused")
	}
}

// Arrow keys move the feed cursor once the drawer is focused, and the window
// follows the cursor. Unfocused, they move the target cursor as before.
func TestFeedScrollFollowsTheCursor(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.now = spawnClock.Add(10 * time.Second)
	for i := 0; i < 9; i++ {
		changeAt(m, "web", spawnClock.Add(time.Duration(i)*time.Second), map[string]fsnotify.Op{
			fmt.Sprintf("f%d.go", i): fsnotify.Write,
		})
	}
	m.now = spawnClock.Add(10 * time.Second)

	press(m, "j")
	if m.cursor != 1 {
		t.Fatalf("unfocused j moved the target cursor to %d, want 1", m.cursor)
	}
	press(m, "tab")
	press(m, "j", "j", "j", "j", "j", "j", "j", "j")
	if got := drawerOf(m); len(got) != feedRows+1 || !strings.Contains(got[len(got)-1], "f0.go") {
		t.Fatalf("drawer after 8 downs = %q, want the oldest batch on the last line", got)
	}
	press(m, "k", "k", "k", "k", "k", "k", "k", "k")
	if got := drawerOf(m); !strings.Contains(got[1], "f8.go") {
		t.Fatalf("drawer after 8 ups = %q, want the newest batch on the first line", got)
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

// t hides the drawer and drops its open list and focus; t again shows the feed.
func TestFeedDrawerTogglesWithT(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	press(m, "tab", "enter", "t")

	m.frame()
	if got := len(m.layout.drawer); got != 0 {
		t.Fatalf("drawer rows after t = %d, want 0", got)
	}
	if m.feedOpen != 0 || m.drawerFocus {
		t.Fatalf("hiding left the list open (%d) or the drawer focused (%v)", m.feedOpen, m.drawerFocus)
	}
	press(m, "t")
	m.frame()
	if got := len(m.layout.drawer); got == 0 {
		t.Fatalf("drawer rows after the second t = 0, want the feed back")
	}
}

// The drawer stays hidden until a change arrives.
func TestFeedDrawerStaysHiddenUntilSomethingChanged(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	if got := len(m.layout.drawer); got != 0 {
		t.Fatalf("drawer rows before any change = %d, want 0", got)
	}
}

// A stopped target's changes still feed the drawer, but it restarted nothing,
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

// The list keeps its cursor on screen under a tall drawer.
func TestListScrollKeepsTheCursorVisibleUnderATallDrawer(t *testing.T) {
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

// Every left line is exactly leftW cells wide, the file list included, and a
// narrow column truncates rather than wraps.
func TestEveryLeftLineIsExactlyLeftWide(t *testing.T) {
	for _, width := range []int{24, 30, 100} {
		m := newTestModel(t, target("web"), target("backoffice-webapp"))
		m.width = width
		m.cursor = 2
		changeAt(m, "web", spawnClock, map[string]fsnotify.Op{
			"apps/api/deep/types.gen.go": fsnotify.Write,
			"apps/root.go":               fsnotify.Write,
		})
		press(m, "tab", "enter")
		m.frame()

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
func TestVersionLineOwnsTheBottomRowUnderTheDrawer(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.height = 14
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"apps/a.go": fsnotify.Write})
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

// A long path cuts from its left, so the file name and its +N survive a narrow column.
func TestFeedPathCutsFromTheLeftKeepingTheFileName(t *testing.T) {
	m := newTestModel(t, target("omsx-api"))
	m.now = spawnClock
	changeAt(m, "omsx-api", spawnClock, map[string]fsnotify.Op{
		"apps/api/rpc/handlers/users.go": fsnotify.Write,
		"apps/api/x.go":                  fsnotify.Write,
	})

	line := drawerAt(m, 32)[1]
	if !strings.HasPrefix(line, "   0s  …") || !strings.HasSuffix(line, "users.go +1  → omsx-api") {
		t.Fatalf("line = %q, want the path cut from its left with the file name kept", line)
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

	if got := drawerOf(m)[1]; got != "   0s  pkg/api/ +2  → web" {
		t.Fatalf("batch line = %q, want the directory with a trailing slash", got)
	}
	press(m, "tab", "enter")
	want := []string{"pkg/api/", "  + a.go", "  ~ b.go"}
	if got := drawerOf(m)[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("file list = %q, want %q", got, want)
	}
}

// Tab gives the drawer the cursor: its batch row lights up and the list cursor dims.
func TestDrawerFocusHighlightsItsCursorAndDimsTheList(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock.Add(2 * restartFlash) // past the restart flash, which reverses the row
	m.cursor = 0
	m.frame()
	leftW, _ := m.layoutWidths()
	// The adaptive cursor background as v2 renders it: palette 237 in truecolor.
	const cursorBg = "\x1b[48;2;58;58;58m"
	if !strings.Contains(m.leftLine(0, leftW), cursorBg) {
		t.Fatal("the list cursor is not highlighted before the drawer takes focus")
	}

	press(m, "tab")
	m.frame()
	batchY := m.height - 2 - len(m.layout.drawer) + 1
	if !strings.Contains(m.leftLine(batchY, leftW), cursorBg) {
		t.Fatal("the drawer's cursor batch is not highlighted while the drawer has focus")
	}
	if strings.Contains(m.leftLine(0, leftW), cursorBg) {
		t.Fatal("the list cursor is still highlighted while the drawer has focus")
	}
}

// Each focus of the drawer starts its cursor on the newest batch.
func TestDrawerCursorResetsToNewestOnFocus(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	later := spawnClock.Add(2 * time.Second)
	changeAt(m, "web", later, map[string]fsnotify.Op{"b.go": fsnotify.Write})
	newest := m.feed[len(m.feed)-1].id

	press(m, "tab", "j")
	if m.feedCursor == newest {
		t.Fatal("j did not move the drawer cursor off the newest batch")
	}
	press(m, "esc", "tab")
	if m.feedCursor != newest {
		t.Fatalf("drawer cursor = %d after refocus, want the newest batch %d", m.feedCursor, newest)
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

	// The file's drawer row keeps its plain paint, not the error's red.
	rows := feedBatchRows([]feedBatch{file}, false, file.id, m.now)
	red := lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed)).Render("x")
	if got := batchLine(rows[0], feedWidth, m.styles.base); strings.Contains(got, red[:strings.Index(red, "x")]) {
		t.Fatalf("file batch row = %q, want no error paint", ansi.Strip(got))
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
