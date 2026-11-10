package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
)

// foldGroup is a target in the `ops` group that folds when idle.
func foldGroup(name string) Target {
	tg := target(name)
	tg.Schema = "ops"
	tg.AutoFold = true
	return tg
}

// foldFixture is an `ops` group of build, serve and lint, with build running and the rest never run.
func foldFixture(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, foldGroup("build"), foldGroup("serve"), foldGroup("lint"))
	m.now = spawnClock
	m.targets[0].state = stateRunning
	m.targets[0].runStarted = true
	m.targets[0].lastStart = spawnClock.Add(-time.Minute)
	m.targets[1].state = stateManual
	m.targets[2].state = stateManual
	m.frame()
	return m
}

// listedNames is the target names the list draws, in order, skipping headers and (+) rows.
func listedNames(m *tuiModel) []string {
	var out []string
	for _, r := range m.listRows() {
		if !r.header && !r.more {
			out = append(out, m.targets[r.target].spec.Name)
		}
	}
	return out
}

// rowAtKey is the list index of the row named by key, or fails the test.
func rowAtKey(t *testing.T, m *tuiModel, key string) int {
	t.Helper()
	i := m.indexOfKey(key)
	if i < 0 {
		t.Fatalf("no listed row %q; rows = %v", key, nestedRows(m))
	}
	return i
}

func TestFoldRule(t *testing.T) {
	old := spawnClock.Add(-foldWindow - time.Minute)
	recent := spawnClock.Add(-time.Minute)
	cases := []struct {
		name      string
		state     procState
		code      int
		lastStart time.Time
		exitedAt  time.Time
		ran       bool
		buildFail bool
		listed    bool
	}{
		{"started recently", stateRunning, -1, recent, time.Time{}, true, false, true},
		{"running steadily for over 10 minutes", stateRunning, -1, old, time.Time{}, true, false, false},
		{"starting", stateStarting, -1, recent, time.Time{}, false, false, true},
		{"backoff", stateBackoff, -1, old, time.Time{}, true, false, true},
		{"failed long ago", stateExited, 1, old, old, true, false, true},
		{"build failed", stateExited, -1, old, old, true, false, true},
		{"build failed flag", stateRunning, -1, old, time.Time{}, true, true, true},
		{"exit 0 recently", stateExited, 0, old, recent, true, false, true},
		{"exit 0 over 10 minutes ago", stateExited, 0, old, old, true, false, false},
		{"stopped recently", stateStopped, -1, recent, time.Time{}, true, false, true},
		{"never ran: manual", stateManual, -1, recent, time.Time{}, false, false, false},
		{"never ran: off", stateOff, -1, recent, time.Time{}, false, false, false},
		{"never ran: on change", stateOnChange, -1, recent, time.Time{}, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, foldGroup("build"), target("other"))
			m.now = spawnClock
			tg := m.targets[0]
			tg.state, tg.exitCode, tg.lastStart, tg.exitedAt, tg.runStarted, tg.buildFailed = tc.state, tc.code, tc.lastStart, tc.exitedAt, tc.ran, tc.buildFail
			if got := slices.Contains(listedNames(m), "build"); got != tc.listed {
				t.Fatalf("listed = %v, want %v; rows = %v", got, tc.listed, nestedRows(m))
			}
		})
	}
}

func TestFoldNeverFoldsTheSoloTarget(t *testing.T) {
	m := foldFixture(t)
	m.solo = 1
	if got, want := listedNames(m), []string{"build", "serve"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo serve: listed %v, want %v", got, want)
	}
}

func TestGroupsWithoutFoldListEveryMember(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateStopped
	if got := listedNames(m); !reflect.DeepEqual(got, []string{"web"}) {
		t.Fatalf("listed %v, want web", got)
	}
}

func TestMoreRowCountsAndExpandsFoldedMembers(t *testing.T) {
	m := foldFixture(t)
	if got, want := nestedRows(m), []string{"▾ops", " build", " (+) 2 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	m.cursor = rowAtKey(t, m, "more:ops")
	press(m, "right")
	if got, want := nestedRows(m), []string{"▾ops", " build", " serve", " lint"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after →: rows = %v, want %v", got, want)
	}
	press(m, "left")
	if got, want := nestedRows(m), []string{"▾ops", " build", " (+) 2 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after ← on the header: rows = %v, want %v", got, want)
	}
}

func TestNoMoreRowWhenNothingFolds(t *testing.T) {
	m := foldFixture(t)
	m.targets[1].state, m.targets[1].runStarted, m.targets[1].lastStart = stateRunning, true, spawnClock
	m.targets[2].state, m.targets[2].runStarted, m.targets[2].lastStart = stateRunning, true, spawnClock
	for _, row := range nestedRows(m) {
		if strings.Contains(row, "(+)") {
			t.Fatalf("rows = %v, want no (+) row", nestedRows(m))
		}
	}
}

func TestHeaderGlyphFollowsVisibleMembers(t *testing.T) {
	m := foldFixture(t)
	if got := nestedRows(m)[0]; got != "▾ops" {
		t.Fatalf("with build visible: header = %q, want ▾ops", got)
	}
	m.targets[0].lastStart = spawnClock.Add(-foldWindow - time.Minute)
	if got, want := nestedRows(m), []string{"▸ops", " (+) 3 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all folded: rows = %v, want %v", got, want)
	}
}

func TestMoreRowEnterExpandsWithoutFocusingThePane(t *testing.T) {
	m := foldFixture(t)
	m.cursor = rowAtKey(t, m, "more:ops")
	press(m, "enter")
	if got := len(listedNames(m)); got != 3 {
		t.Fatalf("enter on (+): listed %d targets, want 3; rows = %v", got, nestedRows(m))
	}
	if m.paneFocus {
		t.Fatal("enter on the (+) row focused the pane")
	}
}

func TestMoreRowClickExpandsAndArrowClickFolds(t *testing.T) {
	m := foldFixture(t)
	click(m, 2, rowAtKey(t, m, "more:ops")+1)
	if got := len(listedNames(m)); got != 3 {
		t.Fatalf("click on (+) listed %d targets, want 3", got)
	}
	m.handleMouse(mouse(0, 1, false)) // the header's arrow cell
	if got := len(listedNames(m)); got != 1 {
		t.Fatalf("arrow click listed %d targets, want 1", got)
	}
}

func TestFoldingNeverMutes(t *testing.T) {
	m := foldFixture(t)
	m.toggleFold(0)
	m.toggleFold(0)
	for _, tg := range m.targets {
		if tg.muted {
			t.Fatalf("%s muted after folding; want folding to leave mute alone", tg.spec.Name)
		}
	}
}

func TestHIsUnbound(t *testing.T) {
	m := foldFixture(t)
	press(m, "h")
	if got := len(listedNames(m)); got != 1 {
		t.Fatalf("h listed %d targets, want the fold untouched", got)
	}
}

func TestToggleKeepsUserStateAcrossReload(t *testing.T) {
	m := foldFixture(t)
	m.cursor = rowAtKey(t, m, "more:ops")
	press(m, "enter")
	specs := make([]Target, len(m.targets))
	for i, tg := range m.targets {
		specs[i] = tg.spec
	}
	m.rebuildRows(&configChange{targets: specs})
	if got := len(listedNames(m)); got != 3 {
		t.Fatalf("after reload listed %d targets, want the user's expansion kept", got)
	}
}

func TestFoldedHeaderRendersAsTwoRows(t *testing.T) {
	m := foldFixture(t)
	m.targets[0].lastStart = spawnClock.Add(-foldWindow - time.Minute)
	if got, want := nestedRows(m), []string{"▸ops", " (+) 3 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folded rows = %v, want %v", got, want)
	}
	leftW, _ := m.layoutWidths()
	if line := strings.TrimSpace(ansi.Strip(m.leftLine(2, leftW))); line != "(+) 3 more" {
		t.Fatalf("more row = %q", line)
	}
}

func TestCursorStaysOnItsRowWhenRowsAboveFold(t *testing.T) {
	m := newTestModel(t, foldGroup("x"), foldGroup("y"), foldGroup("z"))
	m.now = spawnClock
	for _, tg := range m.targets {
		tg.state, tg.runStarted, tg.lastStart = stateRunning, true, spawnClock
	}
	m.frame()
	m.cursor = rowAtKey(t, m, "target:z")
	idx, key := m.cursor, m.cursorKey()
	m.targets[0].lastStart = spawnClock.Add(-foldWindow - time.Minute)
	m.targets[0].state = stateStopped
	m.targets[0].exitedAt = m.targets[0].lastStart
	m.keepCursor(idx, key)
	if got := m.cursorKey(); got != "target:z" {
		t.Fatalf("cursor on %q, want target:z; rows = %v", got, nestedRows(m))
	}
}

func TestCursorMovesToNearestRowWhenItsRowFolds(t *testing.T) {
	m := foldFixture(t)
	m.cursor = rowAtKey(t, m, "target:build")
	m.targets[0].lastStart = spawnClock.Add(-foldWindow - time.Minute)
	m.frame()
	if m.cursor < 0 || m.cursor >= m.listLen() {
		t.Fatalf("cursor %d outside %d rows", m.cursor, m.listLen())
	}
}

func TestFinderJumpExpandsTheFoldedBlock(t *testing.T) {
	m := foldFixture(t)
	idx := -1
	for i, r := range m.allRows() {
		if !r.header && m.targets[r.target].spec.Name == "serve" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("finder does not list the folded serve target")
	}
	m.finderJump(idx)
	if got := m.cursorKey(); got != "target:serve" {
		t.Fatalf("finder jump cursor on %q, want target:serve", got)
	}
}

func TestMoreRowFitsEightyColumnsInEveryListMode(t *testing.T) {
	m := foldFixture(t)
	m.width, m.height = 80, 30
	m.frame()
	press(m, "right")
	for _, times := range []bool{false, true} {
		m.showTimes = times
		leftW, _ := m.layoutWidths()
		for y := range m.listLen() {
			if got := displayWidth(ansi.Strip(m.leftLine(y, leftW))); got > leftW {
				t.Fatalf("times %v row %d is %d cells, wider than %d", times, y, got, leftW)
			}
		}
	}
}

// Every value of the right column ends flush at the list's right edge, before the padding cell.
func TestRightColumnValuesShareOneRightEdge(t *testing.T) {
	a, b, c := target("run"), target("done"), target("man")
	for _, tg := range []*Target{&a, &b, &c} {
		tg.Schema = "ops"
	}
	m := newTestModel(t, a, b, c)
	m.showTimes = true
	m.now = spawnClock
	m.targets[0].state = stateRunning
	m.targets[1].state, m.targets[1].exitCode, m.targets[1].exitLabel = stateExited, 0, "exit 0"
	m.targets[2].state = stateManual
	changeAt(m, "run", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock.Add(4 * time.Second)
	m.frame()
	leftW, _ := m.layoutWidths()
	for y, want := range []string{"idle", "1/3", "4s", "exit 0"} {
		line := strings.TrimRight(ansi.Strip(m.leftLine(y, leftW)), " ")
		if y == 0 {
			want = "4s"
		}
		if displayWidth(ansi.Strip(m.leftLine(y, leftW))) != leftW || !strings.HasSuffix(strings.TrimRight(ansi.Strip(m.leftLine(y, leftW)), " "), want) {
			t.Fatalf("row %d = %q, want %q flush at column %d", y, line, want, leftW)
		}
	}
}
