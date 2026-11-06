package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/golang-cz/rerun"
)

func target(name string) Target {
	return Target{Name: name, Cmd: "sleep 1", Delay: defaultDelay, Grace: rerun.DefaultGrace, Selected: true}
}

func newTestModel(t *testing.T, targets ...Target) *tuiModel {
	t.Helper()
	if len(targets) == 0 {
		targets = []Target{target("web"), target("api"), target("worker")}
	}
	m := newTUI(targets)
	m.width, m.height = 100, 20
	return m
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func press(m *tuiModel, keys ...string) {
	for _, k := range keys {
		m.Update(keyMsg(k))
	}
}

func mouse(x, y int, shift bool) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Shift: shift, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

func motion(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion}
}

// slowClick expires the double-click window, so the next click reads as a
// fresh single click.
func slowClick(m *tuiModel) {
	m.lastClickAt = m.lastClickAt.Add(-doubleClickWindow)
}

// fadeNotice expires the info notice, so the bottom line shows the state chips.
func fadeNotice(m *tuiModel) { m.noticeUntil = time.Time{} }

// buttons is each row's stream state in row order: S scoped, M muted, blank otherwise.
// A scope hides every other row's mute state.
func buttons(m *tuiModel) []string {
	out := make([]string, len(m.targets))
	for i := range m.targets {
		switch {
		case m.solo == i:
			out[i] = "S"
		case m.solo >= 0:
			out[i] = " "
		case m.targets[i].muted:
			out[i] = "M"
		default:
			out[i] = " "
		}
	}
	return out
}

func TestStreamButtonMuteToggle(t *testing.T) {
	m := newTestModel(t)
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", " ", " "}) {
		t.Fatalf("buttons = %q, want blank", got)
	}

	m.cursor = 0
	press(m, "m")
	if got := buttons(m); !reflect.DeepEqual(got, []string{"M", " ", " "}) {
		t.Fatalf("after m: buttons = %q, want M on row 1", got)
	}
	if !m.targets[0].muted {
		t.Error("row 1 is not muted")
	}

	press(m, "m")
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", " ", " "}) {
		t.Fatalf("after second m: buttons = %q, want blank", got)
	}
	if m.targets[0].muted {
		t.Error("row 1 is still muted")
	}
}

func TestStreamButtonSoloHidesOtherMuteStates(t *testing.T) {
	m := newTestModel(t)
	m.toggleMute(0)

	m.cursor = 1
	m.liveSelect()
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", "S", " "}) {
		t.Fatalf("solo on row 2: buttons = %q, want other rows blank", got)
	}
	if !m.targets[0].muted {
		t.Error("solo cleared a mute state it must keep inside")
	}

	// Clearing solo restores the hidden Ms.
	press(m, "esc")
	if got := buttons(m); !reflect.DeepEqual(got, []string{"M", " ", " "}) {
		t.Fatalf("after clearing solo: buttons = %q, want row 1 M again", got)
	}
}

func TestStreamButtonMuteOnSoloRowIsNoOp(t *testing.T) {
	m := newTestModel(t)
	m.applyScope(1, "")

	m.cursor = 1
	press(m, "m")
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", "S", " "}) {
		t.Fatalf("buttons = %q, m on the solo row must be a no-op", got)
	}
	if m.targets[1].muted {
		t.Error("m muted the solo row")
	}
}

func TestHelpLegendLine(t *testing.T) {
	m := newTestModel(t)
	m.help, m.helpAll = true, true
	view := m.View()
	for _, want := range []string{"● run", "● starting", "○ stopped", "✓ ok", "✗ exit", "· off"} {
		if !strings.Contains(view, want) {
			t.Errorf("help is missing the legend %q", want)
		}
	}
}

func TestMergedStreamDropsMutedAndNonSolo(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("worker"))
	for _, ev := range []event{
		{kind: evOutput, target: "web", line: "w1"},
		{kind: evOutput, target: "api", line: "a1"},
		{kind: evOutput, target: "worker", line: "k1"},
	} {
		m.applyEvent(ev)
	}

	// Names pad to the longest of web/api/worker, so every message starts at
	// the same column.
	if got, want := m.paneLines(), []string{"web     w1", "api     a1", "worker  k1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %q, want %q", got, want)
	}
	if a, b := strings.Index(m.paneLines()[0], "w1"), strings.Index(m.paneLines()[1], "a1"); a != b {
		t.Fatalf("message columns differ: %d vs %d", a, b)
	}

	m.toggleMute(1)
	if got, want := m.paneLines(), []string{"web     w1", "worker  k1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged after mute = %q, want %q", got, want)
	}

	m.applyScope(2, "")
	if got, want := m.paneLines(), []string{"worker  k1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo pane = %q, want %q", got, want)
	}

	// Clearing the scope brings the muted row back as muted, not streaming.
	m.applyScope(-1, "")
	if got, want := m.paneLines(), []string{"web     w1", "worker  k1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged after solo cleared = %q, want %q", got, want)
	}
	if !m.targets[1].muted {
		t.Error("solo dropped the muted state it had to keep inside")
	}
}

// The pane prefix is the bare name padded to the longest target, then a fixed
// two-space gap. No brackets: JSON logs make punctuation ambiguous.
func TestMergedStreamPadsPrefixesToAConstantColumn(t *testing.T) {
	m := newTestModel(t, target("omsx"), target("backoffice-webapp"))
	m.applyEvent(event{kind: evOutput, target: "omsx", line: "GET /v1/ping"})
	m.applyEvent(event{kind: evOutput, target: "backoffice-webapp", line: "GET /v1/ping"})

	if got, want := m.prefixW, len("backoffice-webapp"); got != want {
		t.Fatalf("prefixW = %d, want %d", got, want)
	}
	lines := m.paneLines()
	if got, want := lines[0], "omsx"+strings.Repeat(" ", 15)+"GET /v1/ping"; got != want {
		t.Fatalf("omsx line = %q, want %q", got, want)
	}
	if got, want := lines[1], "backoffice-webapp  GET /v1/ping"; got != want {
		t.Fatalf("backoffice line = %q, want %q", got, want)
	}
	if a, b := strings.Index(lines[0], "GET"), strings.Index(lines[1], "GET"); a != b {
		t.Fatalf("message columns differ: %d vs %d", a, b)
	}
	for _, line := range lines {
		if strings.ContainsAny(line, "[]|") || strings.Contains(line, ":") {
			t.Fatalf("prefix still uses punctuation: %q", line)
		}
	}
}

func TestMergedStreamKeepsArrivalOrder(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	for _, ev := range []event{
		{kind: evOutput, target: "web", line: "w1"},
		{kind: evOutput, target: "api", line: "a1"},
		{kind: evOutput, target: "web", line: "w2"},
	} {
		m.applyEvent(ev)
	}

	want := []string{"web  w1", "api  a1", "web  w2"}
	if got := m.paneLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %q, want %q", got, want)
	}

	// A mute rebuild must not reorder the stream by target.
	m.toggleMute(1)
	m.toggleMute(1)
	if got := m.paneLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged after rebuild = %q, want %q", got, want)
	}
}

func TestScrollDownResumesFollowingAtTheBottom(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i := 0; i < 60; i++ {
		m.applyEvent(event{kind: evOutput, target: "web", line: "line"})
	}
	m.View()

	m.scrollUp(5)
	if m.follow {
		t.Fatal("scrolling up did not freeze the tail")
	}
	m.scrollDown(5)
	if !m.follow || m.pending != 0 {
		t.Fatalf("scrolling back to the bottom did not resume: follow=%v pending=%d", m.follow, m.pending)
	}
}

func TestSoloPaneKeepsPrefix(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvent(event{kind: evOutput, target: "web", line: "hello"})
	m.applyEvent(event{kind: evOutput, target: "api", line: "world"})

	m.applyScope(0, "")
	if got, want := m.paneLines(), []string{"web  hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo pane = %q, want %q", got, want)
	}

	m.clearScope()
	if got, want := m.paneLines(), []string{"web  hello", "api  world"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged pane after un-solo = %q, want %q", got, want)
	}
}

// The right pane's first line is the cursor row's header: its status, the size
// of its last change batch, and its command. The left column already prints
// the name.
func TestRightPaneTitleShowsCursorRowCommand(t *testing.T) {
	web := target("frontend")
	web.Cmd = "vite dev"
	m := newTestModel(t, web, target("api"))

	m.cursor = 0
	_, rightW := m.layoutWidths()
	lines := paneOf(m)

	if !strings.HasPrefix(lines[0], "running · since ") || !strings.Contains(lines[0], "· no files · vite dev") {
		t.Fatalf("title = %q, want the cursor row's run start, files, and command", lines[0])
	}
	if got := displayWidth(lines[0]); got != rightW {
		t.Fatalf("title width = %d, want %d", got, rightW)
	}
	if m.vp.Height != m.height-1 {
		t.Fatalf("viewport height = %d, want %d with a title row", m.vp.Height, m.height-1)
	}

	// The file count is the row's retained change batch.
	m.applyEvent(event{
		kind:   evChanged,
		target: "frontend",
		count:  3,
		first:  "src/a.go",
		change: rerun.ChangeSet{Files: map[string]struct{}{"src/a.go": {}, "src/b.go": {}, "src/c.go": {}}},
	})
	if line := paneOf(m)[0]; !strings.HasPrefix(line, "running · since ") || !strings.Contains(line, "· 3 files · vite dev") {
		t.Fatalf("title after a change = %q, want the file count", line)
	}

	// The title follows the cursor row.
	m.cursor = 1
	if line := paneOf(m)[0]; !strings.Contains(line, "· sleep 1") {
		t.Fatalf("title after moving = %q, want the api row's command", line)
	}
}

// The pane header replaces the cursor row's uptime with the row's state
// whenever it is not running; an exited row leads with its exit glyph.
func TestRightPaneTitleShowsRowState(t *testing.T) {
	web := target("frontend")
	web.Cmd = "make run"
	tests := []struct {
		name  string
		state procState
		setup func(m *tuiModel)
		want  string
	}{
		{name: "running", state: stateRunning, setup: func(m *tuiModel) {
			m.now = m.targets[0].lastStart.Add(12 * time.Second)
		}, want: "running · since "},
		{name: "exited ok", state: stateExited, setup: func(m *tuiModel) {
			m.targets[0].exitCode = 0
			m.targets[0].exitLabel = "exit 0"
		}, want: "✓ exit 0 · no files · make run"},
		{name: "exited failure", state: stateExited, setup: func(m *tuiModel) {
			m.targets[0].exitCode = -1
			m.targets[0].exitLabel = "exit segv"
		}, want: "✗ exit segv · no files · make run"},
		{name: "backoff", state: stateBackoff, setup: func(m *tuiModel) {
			m.targets[0].backoffUntil = m.now.Add(4 * time.Second)
		}, want: "backoff 4s · no files · make run"},
		{name: "stopped", state: stateStopped, want: "stopped · no files · make run"},
		{name: "off", state: stateOff, want: "off · no files · make run"},
		{name: "starting", state: stateStarting, want: "starting · no files · make run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t, web, target("api"))
			m.cursor = 0
			m.targets[0].state = tt.state
			if tt.setup != nil {
				tt.setup(m)
			}
			got := paneOf(m)[0]
			if !strings.HasPrefix(got, tt.want) {
				t.Fatalf("title = %q, want prefix %q", got, tt.want)
			}
			if tt.state == stateRunning && !strings.Contains(got, "· no files · make run") {
				t.Fatalf("title = %q, want files and command after the run start", got)
			}
		})
	}
}

func TestRightPaneTitleLinksTheURL(t *testing.T) {
	web := target("frontend")
	web.Cmd = "vite dev"
	web.URL = "http://localhost:5173"
	m := newTestModel(t, web, target("api"))
	m.cursor = 0

	_, rightW := m.layoutWidths()
	line := paneOf(m)[0]
	if !strings.Contains(line, osc8(web.URL, web.URL)) {
		t.Fatalf("title = %q, want the linked url", line)
	}
	if got := displayWidth(line); got != rightW {
		t.Fatalf("title width = %d, want %d", got, rightW)
	}
}

func TestRightPaneTitleTruncatesToWidth(t *testing.T) {
	web := target("frontend")
	web.Cmd = "a-very-long-command-that-will-not-fit"
	web.URL = "http://localhost:5173/very/long/path"
	m := newTestModel(t, web, target("api"))
	m.width = 30
	m.cursor = 0

	_, rightW := m.layoutWidths()
	line := paneOf(m)[0]
	if got := displayWidth(line); got != rightW {
		t.Fatalf("title width = %d, want %d: %q", got, rightW, line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("title wrapped: %q", line)
	}
}

func TestNoTitleOnSchemaRowAndPaneUsesFullHeight(t *testing.T) {
	web := target("frontend")
	web.Cmd = "vite dev"
	web.Schema = "front"
	m := newTestModel(t, web, target("api"))
	m.applyEvent(event{kind: evOutput, target: "frontend", line: "hello"})

	m.cursor = 1
	m.View()
	if m.vp.Height != m.height-1 {
		t.Fatalf("setup: viewport height = %d, want %d", m.vp.Height, m.height-1)
	}

	m.cursor = 0
	lines := paneOf(m)
	if m.vp.Height != m.height {
		t.Fatalf("schema row viewport height = %d, want the full %d", m.vp.Height, m.height)
	}
	if strings.Contains(lines[0], "$ vite dev") {
		t.Fatalf("schema row still shows a title: %q", lines[0])
	}
}

// The left column has no command footer now, so its bottom list line is a
// row; only the version line under it is inert.
func TestMouseClicksTheBottomRowWithoutAFooter(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("worker"))
	m.height = 4
	m.View()

	if row := m.rowAt(2, m.height-2); row != 2 {
		t.Fatalf("rowAt at the bottom = %d, want 2", row)
	}
	m.handleMouse(mouse(2, m.height-2, false))
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 after a click on the bottom row", m.cursor)
	}
}

// The left column is nameW + the fixed glyph, space, two-cell marker, and status.
func TestLeftColumnIsNameWidthPlusFixedColumns(t *testing.T) {
	m := newTestModel(t, target("omsx"), target("backoffice-webapp"))
	leftW, _ := m.layoutWidths()

	wantName := len("backoffice-webapp")
	if got := nameWidth(leftW); got != wantName {
		t.Fatalf("name width = %d, want %d", got, wantName)
	}
	if leftW != wantName+15 {
		t.Fatalf("leftW = %d, want %d", leftW, wantName+15)
	}
	if statusW != 11 {
		t.Fatalf("statusW = %d, want 11", statusW)
	}
}

func TestSoloKeysAndClear(t *testing.T) {
	m := newTestModel(t)
	m.cursor = 1

	m.liveSelect()
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after s", m.solo)
	}
	press(m, "esc")
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared by esc", m.solo)
	}

	press(m, "down")
	if m.solo != 2 {
		t.Fatalf("solo = %d, want 2 after moving the cursor onto it", m.solo)
	}
	press(m, "esc")
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared by esc", m.solo)
	}
}

func TestEnterOnListRowFocusesPane(t *testing.T) {
	m := newTestModel(t)
	m.applyScope(2, "")
	m.cursor = 2
	press(m, "enter")
	if !m.paneFocus {
		t.Fatal("enter on a list row did not focus the log pane")
	}
	if m.solo != 2 {
		t.Fatalf("solo = %d, want the scope kept by enter", m.solo)
	}
}

func TestNewLinesMarkerBeatsAllRowHint(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "c") // arms the table hint, which has no columns to show
	m.follow = false
	m.pending = 2

	view := m.View()
	if !strings.Contains(view, "↓ 2 new") {
		t.Fatalf("the new-lines marker is not in the view:\n%s", view)
	}
	if strings.Contains(view, tableHint) {
		t.Fatalf("the table hint took the marker's line:\n%s", view)
	}
}

// queuedCtrl pops the control message a runner's loop would have taken.
func queuedCtrl(t *testing.T, r *targetRunner) ctrlKind {
	t.Helper()
	select {
	case c := <-r.ctrlCh:
		return c.kind
	default:
		t.Fatal("the runner was not asked to change state")
		return 0
	}
}

func TestSpaceStopsAndStartsTheHighlightedTarget(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 0

	press(m, " ")
	if m.targets[0].state != stateStopped {
		t.Fatalf("state = %v, want stopped", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStop {
		t.Fatalf("control = %v, want ctrlStop", got)
	}

	press(m, " ")
	if m.targets[0].state != stateRunning {
		t.Fatalf("state = %v, want running", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStart {
		t.Fatalf("control = %v, want ctrlStart", got)
	}
}

func TestUnselectedTargetStartsOff(t *testing.T) {
	off := target("job")
	off.Selected = false
	m := newTestModel(t, off, target("web"))
	m.now = m.targets[0].lastStart.Add(3 * time.Second)

	if m.targets[0].state != stateOff {
		t.Fatalf("state = %v, want off", m.targets[0].state)
	}
	if got := m.status(m.targets[0]); got != "off" {
		t.Fatalf("status = %q, want off", got)
	}
	if got := m.targets[0].glyph(); got != "·" {
		t.Fatalf("glyph = %q, want ·", got)
	}

	// Space starts it by hand.
	m.cursor = 0
	press(m, " ")
	if m.targets[0].state != stateRunning {
		t.Fatalf("state after space = %v, want running", m.targets[0].state)
	}
}

// The named-target flow lists every rerun.yml target and keeps the unnamed
// ones startable with space.
func TestNamedRunListsEveryTargetAndStartsOffOnDemand(t *testing.T) {
	cfg := configFromYAML(t, threeTargetsYAML)
	m := newTestModel(t, resolveTargets(cfg, Args{Targets: []string{"frontend"}})...)

	if len(m.targets) != 3 {
		t.Fatalf("got %d rows, want 3", len(m.targets))
	}
	if m.targets[0].state != stateRunning {
		t.Errorf("named target state = %v, want running", m.targets[0].state)
	}
	for i := 1; i < len(m.targets); i++ {
		if m.targets[i].state != stateOff {
			t.Errorf("unnamed target %d state = %v, want off", i, m.targets[i].state)
		}
	}

	m.cursor = 1 // the api row, named off by the yml and not on the command line
	press(m, " ")
	if m.targets[1].state != stateRunning {
		t.Fatalf("state after space = %v, want running", m.targets[1].state)
	}
}

func TestHelpOverlayToggles(t *testing.T) {
	m := newTestModel(t)
	press(m, "?")
	if !m.help || m.helpAll {
		t.Fatal("? did not open the short help overlay")
	}
	if view := m.View(); !strings.Contains(view, "move between rows") || strings.Contains(view, "shift-drag") {
		t.Errorf("short help should list the core keys only:\n%s", view)
	}
	press(m, "?")
	view := m.View()
	for _, want := range []string{"mute", "scope", "shift-drag", "open the target's url"} {
		if !strings.Contains(view, want) {
			t.Errorf("full help overlay is missing %q", want)
		}
	}
	press(m, "?")
	if m.help {
		t.Fatal("? did not close the full help overlay")
	}
}

func TestQuitKeysReturnQuit(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd != nil {
		t.Error("ctrl+c on the main screen returned a quit command instead of asking")
	}
	if !m.confirmQuit {
		t.Error("ctrl+c on the main screen did not arm the confirm modal")
	}

	m = newTestModel(t)
	m.help = true
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd == nil {
		t.Error("ctrl+c in the help overlay did not return a quit command")
	}
}

// Inside help, q closes the overlay instead of quitting.
func TestQClosesHelpWithoutQuitting(t *testing.T) {
	m := newTestModel(t)
	press(m, "?")
	if !m.help {
		t.Fatal("? did not open the help overlay")
	}
	if _, cmd := m.Update(keyMsg("q")); cmd != nil {
		t.Fatal("q in the help overlay returned a quit command")
	}
	if m.help {
		t.Fatal("q did not close the help overlay")
	}
}

// A frozen pane names its state even with nothing pending.
func TestFrozenChipShowsWithoutPending(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 5).events)
	m.View()
	m.pauseTail()
	if got := bottomPaneLine(m); !strings.Contains(got, "scrollback · esc live") {
		t.Fatalf("bottom line = %q, want the paused chip", got)
	}
}

// A scoped target adds nothing to the bottom line: the list selection shows it.
func TestScopeAddsNoBottomLineChip(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyScope(0, "")
	fadeNotice(m)
	m.pauseTail()
	if got := bottomPaneLine(m); !strings.Contains(got, "scrollback · esc live") || strings.Contains(got, "web") {
		t.Fatalf("bottom line = %q, want the paused chip and no scope", got)
	}
}

// An open bar owns the bottom line, so the state chip stays hidden.
func TestChipHiddenWhileInputOpen(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 20).events)
	m.View()
	m.pauseTail()
	press(m, "/")
	if got := bottomPaneLine(m); strings.Contains(got, "scrollback") || strings.Contains(got, "solo") {
		t.Fatalf("bottom line = %q, want no chip while the bar is open", got)
	}
}

func TestViewShowsGlyphStatusAndButton(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.now = m.targets[0].lastStart.Add(48 * time.Second)
	m.targets[1].state = stateExited
	m.targets[1].exitCode = 0
	m.targets[1].exitLabel = "exit 0"
	m.toggleMute(0)

	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Fatalf("view has %d lines, want %d", len(lines), m.height)
	}

	for _, line := range lines {
		if got := lipgloss.Width(line); got != m.width {
			t.Fatalf("line width = %d, want %d: %q", got, m.width, line)
		}
	}

	web := lines[0]
	for _, want := range []string{"●", "web", "48s"} {
		if !strings.Contains(web, want) {
			t.Errorf("web row %q is missing %q", web, want)
		}
	}
	if title := lines[0]; !strings.Contains(title, "muted") {
		t.Errorf("pane title %q does not name the muted row", title)
	}
	if api := lines[1]; !strings.Contains(api, "✓") || !strings.Contains(api, "exit 0") {
		t.Errorf("api row = %q, want ✓ and exit 0", api)
	}
}

func TestViewFitsNarrowTerminals(t *testing.T) {
	for _, width := range []int{20, 40} {
		m := newTestModel(t, target("a-very-long-target-name"), target("api"))
		m.width, m.height = width, 8
		for _, line := range strings.Split(m.View(), "\n") {
			if got := lipgloss.Width(line); got != width {
				t.Fatalf("width %d: line width = %d: %q", width, got, line)
			}
		}
	}
}

func TestScrollFreezesTailAndMarkerResumesIt(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i := 0; i < 40; i++ {
		m.applyEvent(event{kind: evOutput, target: "web", line: "line"})
	}
	m.View()

	m.scrollUp(10)
	if m.follow {
		t.Fatal("scrolling up did not freeze the tail")
	}

	m.applyEvent(event{kind: evOutput, target: "web", line: "new"})
	if m.pending != 1 {
		t.Fatalf("pending = %d, want 1", m.pending)
	}
	if view := m.View(); !strings.Contains(view, "↓ 1 new") {
		t.Fatalf("the marker is not in the view:\n%s", view)
	}

	marker := m.layout.marker
	if marker.w == 0 {
		t.Fatal("the marker was not recorded for hit testing")
	}
	m.handleMouse(mouse(marker.x, marker.y, false))
	if !m.follow || m.pending != 0 {
		t.Fatalf("clicking the marker did not resume following: follow=%v pending=%d", m.follow, m.pending)
	}
}

func TestMouseClicksMuteAndSolo(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	// The right edge is the status column now; a click there never mutes.
	m.toggleMute(0)
	m.handleMouse(mouse(m.layout.leftW-1, 1, false))
	if m.targets[1].muted {
		t.Fatal("a right-edge click muted the row")
	}
	slowClick(m)
	m.clearScope()

	// A single click on a name cell moves the cursor there and solos it.
	m.handleMouse(mouse(2, 1, false))
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 2", m.cursor)
	}
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after one click", m.solo)
	}
	if !m.targets[0].muted {
		t.Error("solo dropped the hidden mute state")
	}

	slowClick(m)
	m.handleMouse(mouse(2, 1, false))
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 kept after the second single click", m.solo)
	}
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 2 still", m.cursor)
	}

	// Shift-click is a plain click: it scopes the row and leaves its mute alone.
	m.handleMouse(mouse(2, 0, true))
	if m.solo != 0 || m.cursor != 0 {
		t.Fatalf("solo = %d cursor = %d after shift-click, want row 1 scoped", m.solo, m.cursor)
	}
	if !m.targets[0].muted {
		t.Error("shift-click unmuted the row")
	}

}

func TestMouseDoubleClickStopsRunningRowAndKeepsSolo(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	// The pair's first click solos the row; the second stops it.
	m.handleMouse(mouse(2, 0, false))
	m.handleMouse(mouse(2, 0, false))

	if m.targets[0].state != stateStopped {
		t.Fatalf("state = %v, want stopped after a double-click", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStop {
		t.Fatalf("control = %v, want ctrlStop", got)
	}
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 (the first click's solo survives)", m.solo)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	if m.lastClick != -1 {
		t.Fatalf("lastClick = %d, want -1 after a double-click", m.lastClick)
	}
}

func TestMouseDoubleClickStartsAnOffRow(t *testing.T) {
	web := target("web")
	web.Selected = false
	m := newTestModel(t, web, target("api"))
	m.View()

	if m.targets[0].state != stateOff {
		t.Fatalf("setup: state = %v, want off", m.targets[0].state)
	}

	m.handleMouse(mouse(2, 0, false))
	m.handleMouse(mouse(2, 0, false))

	if m.targets[0].state != stateRunning {
		t.Fatalf("state = %v, want running after a double-click on an off row", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStart {
		t.Fatalf("control = %v, want ctrlStart", got)
	}
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0", m.solo)
	}
}

func TestMouseThirdClickIsAFreshSingle(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	// Two quick clicks stop the running row and leave it soloed.
	m.handleMouse(mouse(2, 0, false))
	m.handleMouse(mouse(2, 0, false))
	queuedCtrl(t, m.targets[0].runner) // drain the stop

	m.handleMouse(mouse(2, 0, false))
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 kept by the third click", m.solo)
	}
	if m.targets[0].state != stateStopped {
		t.Fatalf("state = %v, want stopped still (no start on the third click)", m.targets[0].state)
	}
	select {
	case c := <-m.targets[0].runner.ctrlCh:
		t.Fatalf("third click queued %v, want none", c.kind)
	default:
	}
}

func TestMouseMotionSameRowLeavesModelAlone(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	m.handleMouse(motion(2, 0))
	if m.hover != 0 {
		t.Fatalf("hover = %d, want 1 after a motion over row 1", m.hover)
	}

	// A wiggle inside the same row must not alter the frame.
	before := m.View()
	m.handleMouse(motion(1, 0))
	if m.hover != 0 {
		t.Fatalf("hover = %d, want 1 still", m.hover)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 still", m.cursor)
	}
	if after := m.View(); after != before {
		t.Fatalf("the view changed on a same-row motion:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	m.handleMouse(motion(2, 1))
	if m.hover != 1 {
		t.Fatalf("hover = %d, want 2 after a motion onto another row", m.hover)
	}

	m.handleMouse(motion(m.layout.leftW+1, 1))
	if m.hover != -1 {
		t.Fatalf("hover = %d, want -1 outside the left column", m.hover)
	}
}

func TestEscClearsSolo(t *testing.T) {
	m := newTestModel(t)
	m.applyScope(1, "")
	press(m, "esc")
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared", m.solo)
	}
}

func TestOpenKeyAndAutoOpen(t *testing.T) {
	var opened []string
	old := openURL
	openURL = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	defer func() { openURL = old }()

	web := target("web")
	web.URL = "http://localhost:5173"
	web.Open = true
	m := newTestModel(t, web, target("api"))

	m.applyEvent(event{kind: evStarted, target: "web"})
	m.applyEvent(event{kind: evStarted, target: "web"}) // a restart must not re-open
	if len(opened) != 1 || opened[0] != web.URL {
		t.Fatalf("opened = %q, want the url exactly once", opened)
	}

	// o opens the highlighted row's url, and does nothing without one.
	m.cursor = 1
	press(m, "o")
	if len(opened) != 1 {
		t.Fatalf("o on a target without a url opened %q", opened)
	}
	m.cursor = 0
	press(m, "o")
	if len(opened) != 2 {
		t.Fatalf("o did not open the url, opened = %q", opened)
	}
}

func TestRowStatusText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = m.targets[0].lastStart.Add(65 * time.Second)
	if got := m.status(m.targets[0]); got != "1m05s" {
		t.Errorf("running status = %q, want 1m05s", got)
	}

	m.targets[0].state = stateExited
	m.targets[0].exitCode = -1
	m.targets[0].exitLabel = "exit segv"
	if got := m.status(m.targets[0]); got != "exit segv" {
		t.Errorf("exited status = %q, want exit segv", got)
	}
	if got := m.targets[0].glyph(); got != "✗" {
		t.Errorf("glyph = %q, want ✗", got)
	}
	if got := stateColor(m.targets[0]); got != runRed {
		t.Errorf("color = %q, want red", got)
	}
}

// A row that has not run yet shows its start mode's own status word.
func TestRowStatusTextForStartModes(t *testing.T) {
	onchange := target("api")
	onchange.Start = startOnChange
	manual := target("job")
	manual.Start = startManual
	m := newTestModel(t, onchange, manual)

	if got := m.targets[0].state; got != stateOnChange {
		t.Fatalf("state = %v, want onchange", got)
	}
	if got := m.status(m.targets[0]); got != "waiting" {
		t.Errorf("status = %q, want waiting", got)
	}
	if got := m.status(m.targets[1]); got != "manual" {
		t.Errorf("status = %q, want manual", got)
	}
	if got := m.targets[0].glyph(); got != "·" {
		t.Errorf("glyph = %q, want ·", got)
	}
	frame := strings.Split(m.View(), "\n")
	if got := frame[0]; !strings.Contains(got, "waiting") {
		t.Errorf("row 1 = %q, want the waiting status", got)
	}
	if got := frame[1]; !strings.Contains(got, "manual") {
		t.Errorf("row 2 = %q, want the manual status", got)
	}

	// The first run takes the row to the ordinary running state.
	m.applyEvents([]event{{kind: evStarted, target: "api", at: m.now}})
	if got := m.status(m.targets[0]); got != "0.1s" {
		t.Errorf("status after the first run = %q, want 0.1s", got)
	}
}

// r starts a target whatever its start mode: an onchange target and a
// manual one both come up on the control.
func TestControlStartBootsEveryStartMode(t *testing.T) {
	for _, mode := range []struct {
		name string
		mode startMode
	}{
		{name: "onchange", mode: startOnChange},
		{name: "manual", mode: startManual},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, kind := range []ctrlKind{ctrlStart, ctrlReboot} {
				sink := &recordingSink{}
				r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Start: mode.mode, Grace: rerun.DefaultGrace}, sink)

				r.handleControl(context.Background(), ctrlMsg{kind: kind})

				var started bool
				for _, ev := range sink.all() {
					started = started || ev.kind == evStarted
				}
				if !started {
					t.Fatalf("ctrl %v did not start a %s target", kind, mode.name)
				}
			}
		})
	}
}

func TestAppendCappedKeepsNewestLines(t *testing.T) {
	var lines []string
	for i := 0; i < lineCap+5; i++ {
		lines = appendCapped(lines, "line")
	}
	if len(lines) != lineCap {
		t.Fatalf("len = %d, want %d", len(lines), lineCap)
	}
}

// recordingSink captures what a runner reports without a terminal.
type recordingSink struct {
	mu     sync.Mutex
	events []event
}

func (s *recordingSink) emit(ev event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *recordingSink) all() []event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event(nil), s.events...)
}

func change() rerun.ChangeSet {
	return rerun.ChangeSet{Files: map[string]struct{}{"a.go": {}}, FirstFile: "a.go"}
}

// A target the user stopped must not restart on a file change. The batch is
// still reported, so the row can say which files no spawn used; a restart would
// report evStarted too.
func TestStoppedTargetDoesNotRestartOnChange(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target Target
		stop   bool
	}{
		{name: "stopped by space", target: Target{Name: "t", Cmd: "true", Selected: true}, stop: true},
		{name: "selected false", target: Target{Name: "t", Cmd: "true", Selected: false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sink := &recordingSink{}
			r := newTargetRunner(tt.target, sink)
			if tt.stop {
				atomic.StoreInt32(&r.stopped, 1)
			}

			r.handleChange(context.Background(), change())

			if got, want := eventKinds(sink), []eventKind{evChanged}; !reflect.DeepEqual(got, want) {
				t.Fatalf("a stopped target reported %v, want %v and no start", got, want)
			}
		})
	}
}

// The contrast case: a running target does report the change and restarts.
func TestRunningTargetRestartsOnChange(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)

	r.handleChange(context.Background(), change())

	var kinds []eventKind
	for _, ev := range sink.all() {
		kinds = append(kinds, ev.kind)
	}
	if want := []eventKind{evChanged, evStarted}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
}

// Control messages are how the TUI starts a target that boots off.
func TestControlStartBootsAnOffTarget(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: false, Grace: rerun.DefaultGrace}, sink)
	if !r.userStopped() {
		t.Fatal("a selected:false target did not boot off")
	}

	r.handleControl(context.Background(), ctrlMsg{kind: ctrlStart})
	if r.userStopped() {
		t.Error("ctrlStart did not clear the stop")
	}

	var started bool
	for _, ev := range sink.all() {
		started = started || ev.kind == evStarted
	}
	if !started {
		t.Fatal("ctrlStart did not start a generation")
	}
}

func eventKinds(s *recordingSink) []eventKind {
	var kinds []eventKind
	for _, ev := range s.all() {
		kinds = append(kinds, ev.kind)
	}
	return kinds
}

// A target that exits by itself must not restart itself: the runner reports the
// exit, stays idle, and starts a generation again only on the next change.
func TestSelfExitedTargetWaitsForChange(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)

	if err := r.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	<-r.exited
	r.reportExit()

	// Phase 1: idle after the self-exit. Nothing else was started.
	if got, want := eventKinds(sink), []eventKind{evStarted, evExited}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the self-exit events = %v, want %v and no restart", got, want)
	}

	m := newTestModel(t, target("t"))
	m.applyEvents(sink.all())
	if m.targets[0].state != stateExited {
		t.Fatalf("state while waiting = %v, want exited", m.targets[0].state)
	}
	if got := m.status(m.targets[0]); got != "exit 0" {
		t.Fatalf("status while waiting = %q, want exit 0", got)
	}

	// Phase 2: the next change brings it back.
	seen := len(sink.all())
	r.handleChange(context.Background(), change())

	if got, want := eventKinds(sink), []eventKind{evStarted, evExited, evChanged, evStarted}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the change events = %v, want %v", got, want)
	}
	m.applyEvents(sink.all()[seen:])
	if m.targets[0].state != stateRunning {
		t.Fatalf("state after the change = %v, want running", m.targets[0].state)
	}
	if got := m.targets[0].glyph(); got != "●" {
		t.Errorf("glyph after the change = %q, want ●", got)
	}
}

// c and / are reserved for Phase B, so pressing one must change nothing.
// A runner that could not start leaves nothing running: the row must not keep
// the running glyph and a ticking uptime.
func TestRunnerErrorLeavesRunningState(t *testing.T) {
	m := newTestModel(t, target("web"))
	if m.targets[0].state != stateRunning {
		t.Fatalf("state = %v, want running before the error", m.targets[0].state)
	}

	m.applyEvent(event{kind: evRunnerErr, target: "web", err: errors.New("exec: \"nope\": executable file not found")})

	if m.targets[0].state != stateExited {
		t.Fatalf("state = %v, want exited after a runner error", m.targets[0].state)
	}
	if got := m.targets[0].glyph(); got != "✗" {
		t.Errorf("glyph = %q, want ✗", got)
	}
	if got := m.status(m.targets[0]); got != "exit error" {
		t.Errorf("status = %q, want exit error", got)
	}
	if !strings.Contains(m.notice, "executable file not found") || !m.noticeErr {
		t.Errorf("the footer is missing the runner error: %q", m.notice)
	}
}

// A single line past the scanner's cap ends the read, so the reader must say so
// instead of going quiet.
func TestReadLinesReportsScannerError(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true"}, sink)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer pw.Close()
		_, _ = pw.Write(bytes.Repeat([]byte("x"), 2*1024*1024))
	}()

	r.readLines(pr, "t", nil)

	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("events = %d, want the one scanner error line", len(got))
	}
	if got[0].kind != evOutput || got[0].target != "t" || !strings.Contains(got[0].line, "token too long") {
		t.Fatalf("event = %+v, want the t output line naming the scanner error", got[0])
	}
}

// An overflowing row is cut to the column, never wrapped into extra lines.
func TestRenderSegsTruncatesInsteadOfWrapping(t *testing.T) {
	got := renderSegs([]seg{{text: "aaaaaaaaaa"}, {text: "bbbbbbbbbb"}}, lipgloss.NewStyle(), 12)
	if n := strings.Count(got, "\n"); n != 0 {
		t.Fatalf("renderSegs wrapped into %d extra lines: %q", n, got)
	}
	if w := displayWidth(got); w != 12 {
		t.Fatalf("width = %d, want 12: %q", w, got)
	}
}

// A row queued behind the start stagger shows `starting` and keeps the running
// glyph while its uptime stays frozen.
func TestRowStatusStartingFreezesUptime(t *testing.T) {
	m := newTestModel(t, target("web"))
	row := m.targets[0]
	row.state = stateStarting
	row.lastStart = m.now.Add(-5 * time.Second)

	if got := m.status(row); got != "starting" {
		t.Fatalf("status = %q, want starting", got)
	}
	if got := row.glyph(); got != "●" {
		t.Fatalf("glyph = %q, want the starting glyph", got)
	}
	if got := stateColor(row); got != colorGray {
		t.Fatalf("color = %q, want the starting color", got)
	}
}

// A backing-off row counts the wait down in its status column.
func TestRowStatusBackoffCountsDown(t *testing.T) {
	m := newTestModel(t, target("web"))
	row := m.targets[0]
	row.state = stateBackoff
	row.exitCode = 1
	row.backoffUntil = m.now.Add(4 * time.Second)

	if got := m.status(row); got != "backoff 4s" {
		t.Fatalf("status = %q, want backoff 4s", got)
	}
	m.now = m.now.Add(2 * time.Second)
	if got := m.status(row); got != "backoff 2s" {
		t.Fatalf("status after 2s = %q, want backoff 2s", got)
	}
	m.now = m.now.Add(3 * time.Second)
	if got := m.status(row); got != "backoff 0s" {
		t.Fatalf("status after the wait = %q, want backoff 0s", got)
	}
	if got := row.glyph(); got != "✗" {
		t.Fatalf("glyph = %q, want the failed exit glyph", got)
	}
}

// The backoff event sets the row's status and writes no pane line: rerun's own
// chatter stays out of the stream.
func TestBackoffEventAddsNoPaneLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(event{kind: evBackoff, target: "web", delay: 4 * time.Second, ordinal: 5})

	if m.targets[0].state != stateBackoff {
		t.Fatalf("state = %v, want backoff", m.targets[0].state)
	}
	if pane := strings.Join(m.paneLines(), "\n"); strings.Contains(pane, "backoff") {
		t.Fatalf("the backoff event wrote a pane line:\n%s", pane)
	}
}

func TestExitEventAddsOnlyTheRunRule(t *testing.T) {
	m := newTestModel(t, target("web"))
	before := m.paneLen()
	m.applyEvent(event{kind: evExited, target: "web", status: "exit 1", code: 1})

	if m.targets[0].state != stateExited || m.targets[0].exitLabel == "" {
		t.Fatalf("exit state = %v, label %q, want the exited state and a label", m.targets[0].state, m.targets[0].exitLabel)
	}
	if got := m.paneLen(); got != before+1 {
		t.Fatalf("pane lines after an exit = %d, want %d", got, before+1)
	}
	if !strings.Contains(m.visibleRaw[len(m.visibleRaw)-1], "■ web exited 1") {
		t.Fatalf("last pane line = %q, want the exit rule", m.visibleRaw[len(m.visibleRaw)-1])
	}
}

// The status column is wide enough for the longest backoff text.
func TestStatusWFitsBackoff(t *testing.T) {
	if got := displayWidth("backoff 10s"); got > statusW {
		t.Fatalf("`backoff 10s` is %d cells, statusW is %d", got, statusW)
	}
}

// The starting event flips a running row to starting; the started event ends
// the backoff and resumes the uptime.
func TestStartingAndStartedEventsMoveTheRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(event{kind: evExited, target: "web", status: "exit 1", code: 1})
	m.applyEvent(event{kind: evBackoff, target: "web", delay: 2 * time.Second, ordinal: 3})
	m.applyEvent(event{kind: evStarting, target: "web"})

	if m.targets[0].state != stateStarting {
		t.Fatalf("state after starting = %v, want starting", m.targets[0].state)
	}
	if got := m.status(m.targets[0]); got != "starting" {
		t.Fatalf("status after starting = %q, want starting", got)
	}

	m.now = m.now.Add(3 * time.Second)
	spawn := m.now
	m.applyEvent(event{kind: evStarted, target: "web", at: spawn})
	if m.targets[0].state != stateRunning {
		t.Fatalf("state after started = %v, want running", m.targets[0].state)
	}
	if got := m.status(m.targets[0]); got != "0.1s" {
		t.Fatalf("status after started = %q, want 0.1s", got)
	}
}
