package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The marker cells sit between the name and the status, so neither touches either.
func TestMarkerCellsSitBetweenNameAndStatus(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.setEdit(colNode{name: "api"}, []Column{{Name: "level", Expr: ".level"}})
	m.View()

	leftW, _ := m.layoutWidths()
	nameW := nameWidth(leftW)
	cells := func(y int) []rune { return []rune(ansi.Strip(m.leftLine(y, leftW))) }

	if got := string(cells(0)[2+nameW : 4+nameW]); got != "  " {
		t.Errorf("clean row marker = %q, want two blank cells", got)
	}
	if got := string(cells(1)[2+nameW : 4+nameW]); got != "* " {
		t.Errorf("dirty row marker = %q, want \"* \"", got)
	}
	for y := 0; y < 2; y++ {
		if got := string(cells(y)[4+nameW:]); !strings.HasPrefix(got, "0s") {
			t.Errorf("row %d status starts at %q, want the uptime at column %d", y, got, 4+nameW)
		}
	}
}

// Every state but running names itself in words, so color is never the only cue.
func TestEveryNonRunningStateNamesItself(t *testing.T) {
	m := newTestModel(t, target("web"))
	row := m.targets[0]
	for _, st := range []procState{stateOff, stateStarting, stateBackoff, stateExited, stateStopped, stateOnChange, stateManual} {
		row.state = st
		row.exitLabel = "exit 0"
		if got := m.status(row); strings.TrimSpace(got) == "" || !strings.ContainsAny(got, "abcdefghijklmnopqrstuvwxyz") {
			t.Errorf("state %v status = %q, want a word", st, got)
		}
	}
}

// The pane's top line names a muted cursor row in words.
func TestPaneTitleNamesMutedRow(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 0
	m.toggleMute(0)
	if line := paneOf(m)[0]; !strings.HasPrefix(line, "muted · ") {
		t.Errorf("title = %q, want it to start with muted", line)
	}
}

// Mute posts a footer notice naming the target.
func TestMuteNamesTheTargetInTheFooter(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.now = spawnClock
	for _, tc := range []struct {
		act  func()
		want string
	}{
		{func() { m.toggleMute(0) }, "muted web"},
		{func() { m.toggleMute(0) }, "unmuted web"},
	} {
		tc.act()
		if m.notice != tc.want || !m.noticeLive() {
			t.Errorf("notice = %q (live %v), want %q", m.notice, m.noticeLive(), tc.want)
		}
	}
}

// The short help fits a 30-row screen, and the full help fits it too.
func TestHelpFitsThirtyRows(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 30

	press(m, "?")
	short := ansi.Strip(m.View())
	if !strings.Contains(short, "ctrl+s") || strings.Contains(short, "backspace") {
		t.Errorf("short help should list the core keys only:\n%s", short)
	}
	assertBoxRows(t, short, 30, "short help")

	press(m, "?")
	full := ansi.Strip(m.View())
	for _, want := range []string{"ctrl+s", "backspace", "enter a f", "×", "rerun keys"} {
		if !strings.Contains(full, want) {
			t.Errorf("full help is missing %q", want)
		}
	}
	assertBoxRows(t, full, 30, "full help")
}

// assertBoxRows checks the help box's border rows span at most max rows.
func assertBoxRows(t *testing.T, view string, max int, what string) {
	t.Helper()
	top, bottom := -1, -1
	for i, line := range strings.Split(view, "\n") {
		if top < 0 && strings.Contains(line, "╭") {
			top = i
		}
		if strings.Contains(line, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom < top {
		t.Fatalf("%s: no help box in view", what)
	}
	if rows := bottom - top + 1; rows > max {
		t.Errorf("%s spans %d rows, want at most %d", what, rows, max)
	}
}

// The footer's help cue is the words `? help` at the default width.
func TestFooterCueReadsHelp(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 30
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	leftW, _ := m.layoutWidths()
	cue := strings.TrimRight(ansi.Cut(lines[len(lines)-1], 0, leftW), " ")
	if !strings.HasSuffix(cue, "· ? help") || strings.Contains(cue, "  ") {
		t.Errorf("footer cue = %q, want `· ? help` with single spaces", cue)
	}
}

// A narrow screen shrinks the left column to the names, down to 16; a wide one keeps 24.
func TestLeftColumnWidthFollowsTheScreen(t *testing.T) {
	m := newTestModel(t, target("a"))
	m.width, m.height = 80, 20
	if leftW, _ := m.layoutWidths(); leftW != 16 {
		t.Errorf("narrow leftW = %d, want 16", leftW)
	}
	m.width = 120
	if leftW, _ := m.layoutWidths(); leftW != 24 {
		t.Errorf("wide leftW = %d, want 24", leftW)
	}
}
