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

// buttons is the stream button of every row, in row order.
func buttons(m *tuiModel) []string {
	out := make([]string, len(m.targets))
	for i := range m.targets {
		out[i] = m.button(i)
	}
	return out
}

func segText(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}

func TestUptimeFormat(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{3 * time.Second, "3s"},
		{9 * time.Second, "9s"},
		{48 * time.Second, "0m48s"},
		{75 * time.Second, "1m15s"},
		{30 * time.Minute, "30m00s"},
		{90 * time.Minute, "1h30m"},
		{150 * time.Minute, "2h30m"},
		{23 * time.Hour, "23h00m"},
		{99 * time.Hour, "4d03h"},
		{-time.Second, "0s"},
	}
	for _, tt := range tests {
		if got := uptime(tt.in); got != tt.want {
			t.Errorf("uptime(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStreamButtonMuteToggle(t *testing.T) {
	m := newTestModel(t)
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", " ", " "}) {
		t.Fatalf("buttons = %q, want blank", got)
	}

	m.cursor = 1
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

	m.cursor = 2
	press(m, "s")
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", "S", " "}) {
		t.Fatalf("solo on row 2: buttons = %q, want other rows blank", got)
	}
	if !m.targets[0].muted {
		t.Error("solo cleared a mute state it must keep inside")
	}

	// Clearing solo restores the hidden Ms.
	press(m, "s")
	if got := buttons(m); !reflect.DeepEqual(got, []string{"M", " ", " "}) {
		t.Fatalf("after clearing solo: buttons = %q, want row 1 M again", got)
	}
}

func TestStreamButtonSoloUnmutesAndIsExclusive(t *testing.T) {
	m := newTestModel(t)
	m.toggleMute(0)

	m.cursor = 1
	press(m, "s")
	if got := buttons(m); !reflect.DeepEqual(got, []string{"S", " ", " "}) {
		t.Fatalf("buttons = %q, want S on row 1", got)
	}
	if m.targets[0].muted {
		t.Error("s must unmute the row it solos")
	}

	m.cursor = 2
	press(m, "s")
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", "S", " "}) {
		t.Fatalf("buttons = %q, want only one S", got)
	}
}

func TestStreamButtonMuteOnSoloRowIsNoOp(t *testing.T) {
	m := newTestModel(t)
	m.toggleSolo(1)

	m.cursor = 2
	press(m, "m")
	if got := buttons(m); !reflect.DeepEqual(got, []string{" ", "S", " "}) {
		t.Fatalf("buttons = %q, m on the solo row must be a no-op", got)
	}
	if m.targets[1].muted {
		t.Error("m muted the solo row")
	}
}

func TestRollupCounts(t *testing.T) {
	m := newTestModel(t, target("a"), target("b"), target("c"), target("d"), target("e"))
	m.targets[0].state, m.targets[1].state, m.targets[2].state = stateRunning, stateRunning, stateRunning
	m.targets[3].state, m.targets[3].exitCode = stateExited, 0
	m.targets[4].state, m.targets[4].exitCode = stateExited, 1

	if got, want := segText(m.rollupSegs()), "3● 1✓ 1✗"; got != want {
		t.Fatalf("rollup = %q, want %q", got, want)
	}

	// Stopped and off rows are not counted.
	m.targets[0].state = stateStopped
	m.targets[1].state = stateOff
	if got, want := segText(m.rollupSegs()), "1● 1✓ 1✗"; got != want {
		t.Fatalf("rollup = %q, want %q", got, want)
	}
}

func TestUnseenBadgeOnMutedOutput(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.toggleMute(1)

	m.applyEvent(event{kind: evOutput, target: "api", line: "hidden"})
	if !m.targets[1].unseen {
		t.Fatal("muted output did not raise the badge")
	}
	if !strings.Contains(m.View(), "✱") {
		t.Error("the ✱ badge is not on screen")
	}

	// Unmuting the row clears it.
	m.toggleMute(1)
	if m.targets[1].unseen {
		t.Error("unmuting did not clear the badge")
	}
}

func TestNoBadgeForVisibleOutput(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvent(event{kind: evOutput, target: "api", line: "visible"})
	if m.targets[1].unseen {
		t.Fatal("output already in the pane must not raise the badge")
	}
}

func TestUnseenExitBadgeClearsOnUnmute(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvent(event{kind: evExited, target: "web", status: "status 1", code: 1, err: nil})
	if !m.targets[0].unseen {
		t.Fatal("an exit did not raise the badge")
	}

	m.toggleMute(0)
	if !m.targets[0].unseen {
		t.Error("muting must not clear the badge")
	}
	m.toggleMute(0)
	if m.targets[0].unseen {
		t.Error("unmuting did not clear the badge")
	}
}

// While one row is soloed, every other row's output is out of the pane and
// earns the badge; esc brings the lines back and drops it.
func TestUnseenBadgeOnSoloHiddenOutput(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("worker"))
	m.toggleSolo(0)

	m.applyEvent(event{kind: evOutput, target: "api", line: "hidden by solo"})
	if !m.targets[1].unseen {
		t.Fatal("output hidden by another row's solo did not raise the badge")
	}
	if !strings.Contains(m.View(), "✱") {
		t.Error("the ✱ badge is not on screen")
	}

	// The soloed row's own output is in the pane: no badge.
	m.applyEvent(event{kind: evOutput, target: "web", line: "visible"})
	if m.targets[0].unseen {
		t.Error("the soloed row's visible output raised the badge")
	}

	press(m, "esc")
	if m.solo != -1 {
		t.Fatal("esc did not clear the solo")
	}
	if m.targets[1].unseen {
		t.Error("clearing the solo did not clear the hidden row's badge")
	}
}

// Switching the solo from A to B returns A's lines to the pane, so its badge
// goes; B stays badged until its own lines show.
func TestUnseenBadgeClearsOnSoloSwitch(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.toggleSolo(0)
	m.applyEvent(event{kind: evOutput, target: "api", line: "hidden"})
	if !m.targets[1].unseen {
		t.Fatal("solo-hidden output did not raise the badge")
	}

	m.toggleSolo(1)
	if m.targets[0].unseen {
		t.Error("web's badge survived the solo switching away from API")
	}
	if m.targets[1].unseen {
		t.Error("the newly soloed row kept a badge")
	}
}

func TestHelpLegendLine(t *testing.T) {
	m := newTestModel(t)
	m.help = true
	view := m.View()
	for _, want := range []string{"● run", "○ stopped", "✓ ok", "✗ exit", "· off", "✱ unseen"} {
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

	m.toggleSolo(2)
	if got, want := m.paneLines(), []string{"worker  k1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo pane = %q, want %q", got, want)
	}

	// Clearing solo brings the muted row back as muted, not streaming.
	m.toggleSolo(2)
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

	m.toggleSolo(0)
	if got, want := m.paneLines(), []string{"web  hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo pane = %q, want %q", got, want)
	}

	m.clearSolo()
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

	m.cursor = 1
	_, rightW := m.layoutWidths()
	lines := paneOf(m)

	if !strings.HasPrefix(lines[0], "0s · 0 files · vite dev") {
		t.Fatalf("title = %q, want the cursor row's uptime, files, and command", lines[0])
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
	if line := paneOf(m)[0]; !strings.HasPrefix(line, "0s · 3 files · vite dev") {
		t.Fatalf("title after a change = %q, want the file count", line)
	}

	// The title follows the cursor row.
	m.cursor = 2
	if line := paneOf(m)[0]; !strings.Contains(line, "· sleep 1") {
		t.Fatalf("title after moving = %q, want the api row's command", line)
	}
}

func TestRightPaneTitleLinksTheURL(t *testing.T) {
	web := target("frontend")
	web.Cmd = "vite dev"
	web.URL = "http://localhost:5173"
	m := newTestModel(t, web, target("api"))
	m.cursor = 1

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
	m.cursor = 1

	_, rightW := m.layoutWidths()
	line := paneOf(m)[0]
	if got := displayWidth(line); got != rightW {
		t.Fatalf("title width = %d, want %d: %q", got, rightW, line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("title wrapped: %q", line)
	}
}

func TestNoTitleOnAllRowAndPaneUsesFullHeight(t *testing.T) {
	web := target("frontend")
	web.Cmd = "vite dev"
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
		t.Fatalf("ALL row viewport height = %d, want the full %d", m.vp.Height, m.height)
	}
	if strings.Contains(lines[0], "$ vite dev") {
		t.Fatalf("ALL row still shows a title: %q", lines[0])
	}
}

// The left column has no command footer now, so its bottom list line is a
// row; only the version line under it is inert.
func TestMouseClicksTheBottomRowWithoutAFooter(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
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

// The left column is nameW + the fixed glyph, badge, tree cell, separators,
// status, and button.
func TestLeftColumnIsNameWidthPlusFixedColumns(t *testing.T) {
	m := newTestModel(t, target("omsx"), target("backoffice-webapp"))
	leftW, _ := m.layoutWidths()

	wantName := len("backoffice-webapp")
	if got := nameWidth(leftW); got != wantName {
		t.Fatalf("name width = %d, want %d", got, wantName)
	}
	if leftW != wantName+17 {
		t.Fatalf("leftW = %d, want %d", leftW, wantName+17)
	}
	if statusW != 11 {
		t.Fatalf("statusW = %d, want 11", statusW)
	}
	// The tree cell sits between the name's badge and the status column.
	if got := treeX(leftW); got != wantName+3 {
		t.Fatalf("tree cell x = %d, want %d", got, wantName+3)
	}
}

func TestSoloKeysAndClear(t *testing.T) {
	m := newTestModel(t)
	m.cursor = 2

	press(m, "s")
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after s", m.solo)
	}
	press(m, "s")
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared after a second s", m.solo)
	}

	press(m, "enter")
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after enter", m.solo)
	}
	press(m, "enter")
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared after a second enter", m.solo)
	}
}

func TestEnterOnAllRowClearsSolo(t *testing.T) {
	m := newTestModel(t)
	m.toggleSolo(2)
	m.cursor = 0
	press(m, "enter")
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared", m.solo)
	}
}

func TestAllRowMuteSoloShowsHint(t *testing.T) {
	for _, key := range []string{"m", "s"} {
		t.Run(key, func(t *testing.T) {
			m := newTestModel(t)
			m.cursor = 0
			press(m, key)
			if view := m.View(); !strings.Contains(view, allRowHint) {
				t.Fatalf("the ALL-row hint is not in the view:\n%s", view)
			}

			m.now = m.now.Add(hintDuration + time.Second)
			if view := m.View(); strings.Contains(view, allRowHint) {
				t.Fatalf("the hint outlived its window:\n%s", view)
			}
		})
	}
}

func TestNewLinesMarkerBeatsAllRowHint(t *testing.T) {
	m := newTestModel(t)
	m.cursor = 0
	press(m, "m") // arms the ALL-row hint
	m.follow = false
	m.pending = 2

	view := m.View()
	if !strings.Contains(view, "↓ 2 new") {
		t.Fatalf("the new-lines marker is not in the view:\n%s", view)
	}
	if strings.Contains(view, allRowHint) {
		t.Fatalf("the ALL-row hint took the marker's line:\n%s", view)
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
	m.cursor = 1

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

func TestSpaceOnAllRowStopsAndStartsEverything(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 0

	press(m, " ")
	for i, row := range m.targets {
		if row.state != stateStopped {
			t.Errorf("target %d state = %v, want stopped", i, row.state)
		}
	}

	press(m, " ")
	for i, row := range m.targets {
		if row.state != stateRunning {
			t.Errorf("target %d state = %v, want running", i, row.state)
		}
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
	m.cursor = 1
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

	m.cursor = 2 // the api row, named off by the yml and not on the command line
	press(m, " ")
	if m.targets[1].state != stateRunning {
		t.Fatalf("state after space = %v, want running", m.targets[1].state)
	}
}

func TestRebootOnAllRowTouchesSelectedTargetsOnly(t *testing.T) {
	off := target("job")
	off.Selected = false
	m := newTestModel(t, off, target("web"))
	m.cursor = 0

	press(m, "r")
	if m.targets[0].state != stateOff {
		t.Errorf("unselected target state = %v, want off", m.targets[0].state)
	}
	if m.targets[1].state != stateRunning {
		t.Errorf("selected target state = %v, want running", m.targets[1].state)
	}
}

func TestHelpOverlayToggles(t *testing.T) {
	m := newTestModel(t)
	press(m, "?")
	if !m.help {
		t.Fatal("? did not open the help overlay")
	}
	view := m.View()
	for _, want := range []string{"mute", "solo", "shift-drag", "open the target's url"} {
		if !strings.Contains(view, want) {
			t.Errorf("help overlay is missing %q", want)
		}
	}
	press(m, "?")
	if m.help {
		t.Fatal("? did not close the help overlay")
	}
}

func TestQuitKeysReturnQuit(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		m := newTestModel(t)
		if _, cmd := m.Update(keyMsg(key)); cmd == nil {
			t.Errorf("%s did not return a quit command", key)
		}
	}

	m := newTestModel(t)
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
	press(m, "f")
	if got := bottomPaneLine(m); !strings.Contains(got, "frozen (f/G)") {
		t.Fatalf("bottom line = %q, want the frozen chip", got)
	}
}

// Soloing a frozen pane appends the solo chip after the frozen one.
func TestSoloChipAppendsToFrozen(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.toggleSolo(0)
	press(m, "f")
	if got := bottomPaneLine(m); !strings.Contains(got, "frozen (f/G) · solo web") {
		t.Fatalf("bottom line = %q, want frozen then solo", got)
	}
}

// An open bar owns the bottom line, so the state chip stays hidden.
func TestChipHiddenWhileInputOpen(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 20).events)
	m.View()
	press(m, "f")
	press(m, "/")
	if got := bottomPaneLine(m); strings.Contains(got, "frozen") || strings.Contains(got, "solo") {
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

	web := lines[1]
	for _, want := range []string{"●", "web", "0m48s", "M"} {
		if !strings.Contains(web, want) {
			t.Errorf("web row %q is missing %q", web, want)
		}
	}
	if api := lines[2]; !strings.Contains(api, "✓") || !strings.Contains(api, "exit 0") {
		t.Errorf("api row = %q, want ✓ and exit 0", api)
	}
	if all := lines[0]; !strings.Contains(all, "1●") || !strings.Contains(all, "1✓") {
		t.Errorf("ALL row = %q, want the 1● 1✓ rollup", all)
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

	// The stream button cell is the last cell of the left column. It mutes
	// only: no solo, even though the click lands on a target row.
	btnX := m.layout.leftW - 1
	m.handleMouse(mouse(btnX, 1, false))
	if got := m.button(0); got != "M" {
		t.Fatalf("button after click = %q, want M", got)
	}
	if m.solo != -1 {
		t.Fatalf("solo = %d, want none after a button-cell click", m.solo)
	}
	if !m.targets[0].muted {
		t.Error("the button-cell click did not mute")
	}

	// A single click on a name cell moves the cursor there and solos it.
	m.handleMouse(mouse(2, 2, false))
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.cursor)
	}
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after one click", m.solo)
	}
	if !m.targets[0].muted {
		t.Error("solo dropped the hidden mute state")
	}

	// A second click on the soloed row clears solo. Slowing it past the
	// double-click window keeps it a single click; the pair case is below.
	slowClick(m)
	m.handleMouse(mouse(2, 2, false))
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared after the second single click", m.solo)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 still", m.cursor)
	}

	// Shift-click solos the row under the pointer.
	m.handleMouse(mouse(2, 1, true))
	if got := m.button(0); got != "S" {
		t.Fatalf("button after shift-click = %q, want S", got)
	}
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 after shift-click", m.solo)
	}

	// Clicking the ALL row moves the cursor back to it and clears solo.
	m.handleMouse(mouse(2, 0, false))
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared by the ALL-row click", m.solo)
	}
}

func TestMouseClickAllRowClearsSolo(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	m.handleMouse(mouse(2, 1, false))
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 after one click", m.solo)
	}

	m.handleMouse(mouse(2, 0, false))
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared by the ALL-row click", m.solo)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
}

func TestMouseDoubleClickStopsRunningRowAndKeepsSolo(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	// The pair's first click solos the row; the second stops it.
	m.handleMouse(mouse(2, 1, false))
	m.handleMouse(mouse(2, 1, false))

	if m.targets[0].state != stateStopped {
		t.Fatalf("state = %v, want stopped after a double-click", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStop {
		t.Fatalf("control = %v, want ctrlStop", got)
	}
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 (the first click's solo survives)", m.solo)
	}
	if m.cursor != 1 {
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

	m.handleMouse(mouse(2, 1, false))
	m.handleMouse(mouse(2, 1, false))

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

func TestMouseDoubleClickOnAllRowDoesNotToggleAll(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	m.handleMouse(mouse(2, 0, false))
	m.handleMouse(mouse(2, 0, false))

	for i, row := range m.targets {
		if row.state != stateRunning {
			t.Fatalf("target %d state = %v, want running (ALL is not a service)", i, row.state)
		}
		select {
		case c := <-row.runner.ctrlCh:
			t.Fatalf("target %d queued %v, want none on an ALL-row double-click", i, c.kind)
		default:
		}
	}
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared by the ALL-row click", m.solo)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
}

func TestMouseThirdClickIsAFreshSingle(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.View()

	// Two quick clicks stop the running row and leave it soloed.
	m.handleMouse(mouse(2, 1, false))
	m.handleMouse(mouse(2, 1, false))
	queuedCtrl(t, m.targets[0].runner) // drain the stop

	// The third click is a fresh single: it toggles solo, never stop/start.
	m.handleMouse(mouse(2, 1, false))
	if m.solo != -1 {
		t.Fatalf("solo = %d, want cleared by the third click's solo toggle", m.solo)
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

	m.handleMouse(motion(2, 1))
	if m.hover != 1 {
		t.Fatalf("hover = %d, want 1 after a motion over row 1", m.hover)
	}

	// A wiggle inside the same row must not alter the frame.
	before := m.View()
	m.handleMouse(motion(1, 1))
	if m.hover != 1 {
		t.Fatalf("hover = %d, want 1 still", m.hover)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 still", m.cursor)
	}
	if after := m.View(); after != before {
		t.Fatalf("the view changed on a same-row motion:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	m.handleMouse(motion(2, 2))
	if m.hover != 2 {
		t.Fatalf("hover = %d, want 2 after a motion onto another row", m.hover)
	}

	m.handleMouse(motion(m.layout.leftW+1, 2))
	if m.hover != -1 {
		t.Fatalf("hover = %d, want -1 outside the left column", m.hover)
	}
}

func TestEscClearsSolo(t *testing.T) {
	m := newTestModel(t)
	m.toggleSolo(1)
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
	m.cursor = 2
	press(m, "o")
	if len(opened) != 1 {
		t.Fatalf("o on a target without a url opened %q", opened)
	}
	m.cursor = 1
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
	if got := stateColor(m.targets[0]); got != colorRed {
		t.Errorf("color = %q, want red", got)
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
	if got := strings.Join(m.paneLines(), "\n"); !strings.Contains(got, "executable file not found") {
		t.Errorf("the stream is missing the runner error:\n%s", got)
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
		t.Fatalf("glyph = %q, want the running glyph", got)
	}
	if got := stateColor(row); got != colorYellow {
		t.Fatalf("color = %q, want the running color", got)
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

// The backoff event lands one pane line naming the delay and the restart.
func TestBackoffEventAddsPaneLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(event{kind: evBackoff, target: "web", delay: 4 * time.Second, ordinal: 5})

	if m.targets[0].state != stateBackoff {
		t.Fatalf("state = %v, want backoff", m.targets[0].state)
	}
	pane := strings.Join(m.paneLines(), "\n")
	if !strings.Contains(pane, "# backoff 4s (5th restart in 30s)") {
		t.Fatalf("pane is missing the backoff line:\n%s", pane)
	}
}

// The backoff line words its ordinal correctly.
func TestOrdinalWord(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd"} {
		if got := ordinalWord(n); got != want {
			t.Errorf("ordinalWord(%d) = %q, want %q", n, got, want)
		}
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
	if got := m.status(m.targets[0]); got != "0s" {
		t.Fatalf("status after started = %q, want 0s", got)
	}
}
