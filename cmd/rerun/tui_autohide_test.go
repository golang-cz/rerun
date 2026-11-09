package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// autoGroup is a target in the `ops` group that auto-hides when idle.
func autoGroup(name string) Target {
	tg := target(name)
	tg.Schema = "ops"
	tg.AutoHide = true
	return tg
}

// autoHideFixture is an `ops` group of build, serve and lint, with build running and the rest idle.
func autoHideFixture(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, autoGroup("build"), autoGroup("serve"), autoGroup("lint"))
	m.now = spawnClock
	m.targets[0].state = stateRunning
	m.targets[1].state = stateStopped
	m.targets[2].state = stateStopped
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

func TestAutoHideRule(t *testing.T) {
	old := spawnClock.Add(-autoHideGrace - time.Minute)
	recent := spawnClock.Add(-time.Minute)
	cases := []struct {
		name     string
		state    procState
		code     int
		exitedAt time.Time
		listed   bool
	}{
		{"running", stateRunning, -1, time.Time{}, true},
		{"starting", stateStarting, -1, time.Time{}, true},
		{"backoff", stateBackoff, -1, time.Time{}, true},
		{"failed", stateExited, 1, old, true},
		{"build failed", stateExited, -1, old, true},
		{"exit 0 recently", stateExited, 0, recent, true},
		{"exit 0 for over 10 minutes", stateExited, 0, old, false},
		{"stopped", stateStopped, -1, time.Time{}, false},
		{"off", stateOff, -1, time.Time{}, false},
		{"manual", stateManual, -1, time.Time{}, false},
		{"on change", stateOnChange, -1, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, autoGroup("build"), target("other"))
			m.now = spawnClock
			m.targets[0].state, m.targets[0].exitCode, m.targets[0].exitedAt = tc.state, tc.code, tc.exitedAt
			if got := slices.Contains(listedNames(m), "build"); got != tc.listed {
				t.Fatalf("listed = %v, want %v; rows = %v", got, tc.listed, nestedRows(m))
			}
		})
	}
}

func TestAutoHideNeverHidesTheSoloTarget(t *testing.T) {
	m := autoHideFixture(t)
	m.solo = 1
	if got, want := listedNames(m), []string{"build", "serve"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo serve: listed %v, want %v", got, want)
	}
}

func TestNonAutoHideTargetsAreAlwaysListed(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.targets[0].state = stateStopped
	if got := listedNames(m); !reflect.DeepEqual(got, []string{"web"}) {
		t.Fatalf("listed %v, want web", got)
	}
}

func TestMoreRowCountsAndRevealsHiddenMembers(t *testing.T) {
	m := autoHideFixture(t)
	if got, want := nestedRows(m), []string{"▾ops", " build", " (+) 2 hidden"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	m.cursor = rowAtKey(t, m, "more:ops")
	press(m, "right")
	if got, want := nestedRows(m), []string{"▾ops", " build", " serve", " lint", " (−) hide 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after →: rows = %v, want %v", got, want)
	}
	if !dimListed(m, "serve") {
		t.Fatalf("revealed serve should draw dim; rows = %v", nestedRows(m))
	}
	m.cursor = rowAtKey(t, m, "more:ops")
	press(m, "left")
	if got, want := nestedRows(m), []string{"▾ops", " build", " (+) 2 hidden"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after ← on the (−) row: rows = %v, want %v", got, want)
	}
}

func TestMoreRowEnterTogglesWithoutFocusingThePane(t *testing.T) {
	m := autoHideFixture(t)
	m.cursor = rowAtKey(t, m, "more:ops")
	press(m, "enter")
	if got, want := nestedRows(m), []string{"▾ops", " build", " serve", " lint", " (−) hide 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enter on (+): rows = %v, want %v", got, want)
	}
	if m.paneFocus {
		t.Fatal("enter on the (+) row focused the pane")
	}
	press(m, "enter")
	if got := len(listedNames(m)); got != 1 {
		t.Fatalf("enter on (−) listed %d targets, want 1", got)
	}
}

func TestMoreRowClickToggles(t *testing.T) {
	m := autoHideFixture(t)
	y := rowAtKey(t, m, "more:ops")
	click(m, 2, y)
	if got := len(listedNames(m)); got != 3 {
		t.Fatalf("click on (+) listed %d targets, want 3", got)
	}
	click(m, 2, rowAtKey(t, m, "more:ops"))
	if got := len(listedNames(m)); got != 1 {
		t.Fatalf("click on (−) listed %d targets, want 1", got)
	}
}

func TestHTogglesTheCursorsGroupFromAnyRow(t *testing.T) {
	m := autoHideFixture(t)
	press(m, "down", "h")
	if got := len(listedNames(m)); got != 3 {
		t.Fatalf("h on a target row listed %d, want 3", got)
	}
	press(m, "up", "h")
	if got := len(listedNames(m)); got != 1 {
		t.Fatalf("h on the header listed %d, want 1", got)
	}
}

// h is the list's key: with the pane focused it must not reveal the cursor's group.
func TestHDoesNothingInThePane(t *testing.T) {
	m := autoHideFixture(t)
	m.focusPane()
	press(m, "h")
	if m.revealed["ops"] {
		t.Fatal("h in the focused pane revealed the group")
	}
}

func TestHDoesNothingWithoutHiddenMembers(t *testing.T) {
	m := autoHideFixture(t)
	m.targets[1].state, m.targets[2].state = stateRunning, stateRunning
	press(m, "h")
	if m.revealed["ops"] {
		t.Fatal("h revealed a group with no hidden members")
	}
}

func TestFoldedHeaderCountsHiddenMembers(t *testing.T) {
	m := autoHideFixture(t)
	m.toggleCollapse(0)
	if got, want := nestedRows(m), []string{"▸ops"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folded rows = %v, want %v", got, want)
	}
	leftW, _ := m.layoutWidths()
	if line := ansi.Strip(m.leftLine(0, leftW)); !strings.Contains(line, "· 2 hidden") || strings.Contains(line, "hidd ") {
		t.Fatalf("folded header = %q, want the hidden count", line)
	}
}

func TestCursorStaysOnItsRowWhenRowsAboveHide(t *testing.T) {
	m := newTestModel(t, autoGroup("x"), autoGroup("y"), autoGroup("z"))
	m.now = spawnClock
	for i := range m.targets {
		m.targets[i].state = stateRunning
	}
	m.frame()
	m.cursor = rowAtKey(t, m, "target:z")
	m.Update(eventsMsg{events: []event{{kind: evStopped, target: "x"}}})
	if got := m.cursorKey(); got != "target:z" {
		t.Fatalf("cursor on %q, want target:z; rows = %v", got, nestedRows(m))
	}
}

func TestCursorMovesToNearestRowWhenItsRowHides(t *testing.T) {
	m := autoHideFixture(t)
	m.targets[0].state = stateStopped
	m.targets[1].state = stateRunning
	m.frame()
	m.cursor = rowAtKey(t, m, "target:serve")
	m.Update(eventsMsg{events: []event{{kind: evStopped, target: "serve"}}})
	if m.cursor < 0 || m.cursor >= m.listLen() {
		t.Fatalf("cursor %d outside %d rows", m.cursor, m.listLen())
	}
}

func TestFinderFindsHiddenTargetAndRunningItShowsIt(t *testing.T) {
	m := autoHideFixture(t)
	idx := -1
	for i, r := range m.allRows() {
		if !r.header && m.targets[r.target].spec.Name == "serve" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("finder does not list the hidden serve target")
	}
	m.finderJump(idx)
	if got := m.cursorKey(); got != "target:serve" {
		t.Fatalf("finder jump cursor on %q, want target:serve", got)
	}
}

func TestMoreRowFitsEightyColumnsInEveryListMode(t *testing.T) {
	m := autoHideFixture(t)
	m.width, m.height = 80, 30
	m.frame()
	press(m, "right")
	for _, mode := range []leftMode{leftFull, leftNames} {
		m.leftMode = mode
		leftW, _ := m.layoutWidths()
		for y := range m.listLen() {
			if got := displayWidth(ansi.Strip(m.leftLine(y, leftW))); got > leftW {
				t.Fatalf("mode %d row %d is %d cells, wider than %d", mode, y, got, leftW)
			}
		}
	}
}

// dimListed reports whether the list draws the named target as a dimmed, revealed row.
func dimListed(m *tuiModel, name string) bool {
	for _, r := range m.listRows() {
		if !r.header && !r.more && m.targets[r.target].spec.Name == name {
			return r.dim
		}
	}
	return false
}

// autoImport is an import's target that auto-hides when idle, in the sub-group sub.
func autoImport(imp, name, sub string) Target {
	tg := importTarget(imp, name, sub)
	tg.Schema = sub
	tg.AutoHide = true
	return tg
}

// h on an import header toggles the hidden members of all its sub-groups together.
func TestHOnImportHeaderTogglesEverySubGroup(t *testing.T) {
	m := newTestModel(t, autoImport("omsx", "api", "omsx/backend"), autoImport("omsx", "web", "omsx/webapps"), target("lone"))
	m.now = spawnClock
	m.targets[0].state = stateStopped
	m.targets[1].state = stateStopped
	m.frame()
	toggle := func() {
		for i, r := range m.listRows() {
			if r.header && r.schema == importKeyPrefix+"omsx" {
				m.cursor = i
			}
		}
		press(m, "h")
	}
	shown := func() []string { return listedNames(m) }

	toggle()
	if got := shown(); !slices.Contains(got, "omsx/api") || !slices.Contains(got, "omsx/web") {
		t.Fatalf("h on the import header: listed %v, want both sub-groups revealed", got)
	}
	toggle()
	if got := shown(); slices.Contains(got, "omsx/api") || slices.Contains(got, "omsx/web") {
		t.Fatalf("h again: listed %v, want both sub-groups hidden", got)
	}

	m.setRevealed("omsx/backend", true)
	toggle()
	if got := shown(); !slices.Contains(got, "omsx/web") {
		t.Fatalf("h with one sub-group still hiding: listed %v, want all revealed", got)
	}
	toggle()
	if got := shown(); slices.Contains(got, "omsx/api") || slices.Contains(got, "omsx/web") {
		t.Fatalf("h with all revealed: listed %v, want all hidden", got)
	}
}
