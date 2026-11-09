package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/golang-cz/rerun"
)

// The marker cells sit between the name and the status, so neither touches either.
func TestMarkerCellsSitBetweenNameAndStatus(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.targets[0].state = stateRunning
	m.targets[1].state = stateRunning
	m.setEdit(colNode{name: "api"}, []Column{{Name: "level", Expr: ".level"}})
	m.frame()

	leftW, _ := m.layoutWidths()
	nameW := m.nameWidth(leftW)
	cells := func(y int) []rune { return []rune(ansi.Strip(m.leftLine(y+1, leftW))) }

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

// The help box is the shared modal size; the short help fits it whole.
func TestHelpFitsThirtyRows(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 30
	_, wantH := m.modalSize()

	press(m, "?")
	short := ansi.Strip(m.View().Content)
	if !strings.Contains(short, "ctrl+s") || strings.Contains(short, "backspace") {
		t.Errorf("short help should list the core keys only:\n%s", short)
	}
	assertBoxRows(t, short, wantH, "short help")
	if strings.Contains(short, "↓ more") {
		t.Errorf("short help scrolls, but it fits:\n%s", short)
	}

	press(m, "?")
	full := ansi.Strip(m.View().Content)
	for _, want := range []string{"ctrl+s", "backspace", "enter → a f", "×", "rerun keys"} {
		if !strings.Contains(full, want) {
			t.Errorf("full help is missing %q", want)
		}
	}
	assertBoxRows(t, full, wantH, "full help")
	if !strings.Contains(full, "↓ more") {
		t.Errorf("full help does not show more rows below:\n%s", full)
	}

	// Paging to the end reaches the last legend row, and the footer never leaves.
	for range 3 {
		press(m, "pgdown")
	}
	end := ansi.Strip(m.View().Content)
	for _, want := range []string{"· off", "esc close"} {
		if !strings.Contains(end, want) {
			t.Errorf("scrolled full help is missing %q:\n%s", want, end)
		}
	}
	if strings.Contains(end, "↓ more") {
		t.Errorf("full help still says more rows below at the end:\n%s", end)
	}
	assertBoxRows(t, end, wantH, "scrolled full help")
}

// On an 80-column screen no full-help row is cut: every row fits the box and
// ends without the ellipsis character.
func TestFullHelpRowsFitEightyColumns(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 80, 30
	m.help, m.helpAll = true, true

	outerW, _ := m.modalSize()
	innerW := m.modalInnerW(outerW)
	for _, line := range strings.Split(ansi.Strip(m.helpView()), "\n") {
		if strings.Contains(line, "…") {
			t.Fatalf("full help row is cut at 80 columns: %q", line)
		}
	}
	for _, kv := range helpKeys {
		if got := displayWidth(padRight(kv[0], 12) + " " + kv[1]); got > innerW {
			t.Fatalf("help row %q is %d cells, want at most the inner width %d", kv[0], got, innerW)
		}
	}
}

// Help is an overlay: the live frame, its log lines included, stays visible around the box.
func TestHelpOverlaysTheLiveFrame(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 120, 40
	m.applyEvents(streamEvents("web", "logline", 20).events)
	press(m, "?")

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "rerun keys") {
		t.Errorf("help overlay is missing its header:\n%s", view)
	}
	if !strings.Contains(view, "? m all keys  esc close") {
		t.Errorf("help overlay is missing its footer:\n%s", view)
	}

	lines := strings.Split(view, "\n")
	top, bottom := -1, -1
	for i, line := range lines {
		if top < 0 && strings.Contains(line, "╭") {
			top = i
		}
		if strings.Contains(line, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom < top {
		t.Fatalf("no help box in view:\n%s", view)
	}
	outside := strings.Join(append(append([]string{}, lines[:top]...), lines[bottom+1:]...), "\n")
	if !strings.Contains(outside, "logline-") {
		t.Fatalf("the frame around the help box shows no log line:\n%s", view)
	}
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
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
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

func TestEllipsizeMiddle(t *testing.T) {
	tests := []struct {
		name, in, want string
		w              int
	}{
		{name: "fits", in: "web", w: 5, want: "web"},
		{name: "exact fit", in: "web", w: 3, want: "web"},
		{name: "cut keeps both ends", in: "test-integration-postgres", w: 20, want: "test-integ…-postgres"},
		{name: "even split", in: "abcdefgh", w: 4, want: "ab…h"},
		{name: "one cell", in: "abcdefgh", w: 1, want: "…"},
		{name: "wide runes", in: "日本語テスト", w: 7, want: "日 … ト"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ellipsizeMiddle(tt.in, tt.w)
			if got != tt.want {
				t.Fatalf("ellipsizeMiddle(%q, %d) = %q, want %q", tt.in, tt.w, got, tt.want)
			}
			if w := displayWidth(got); w > tt.w {
				t.Fatalf("width = %d, want at most %d", w, tt.w)
			}
		})
	}
}

// A Makefile target starts manual, but the status word would repeat on every row.
func TestMakeTargetStatusIsEmptyAndYamlManualShowsIt(t *testing.T) {
	mk := Target{Name: "build", Cmd: "make build", Kind: makeKind, Start: startManual, Selected: true, Grace: rerun.DefaultGrace}
	mm := newTestModel(t, mk)
	if got := mm.status(mm.targets[0]); got != "" {
		t.Errorf("make target status = %q, want empty", got)
	}
	if strings.Contains(ansi.Strip(mm.View().Content), "manual") {
		t.Errorf("make target view shows manual, want no status word")
	}

	ym := newTestModel(t, Target{Name: "job", Cmd: "true", Start: startManual, Selected: true, Grace: rerun.DefaultGrace})
	if got := ym.status(ym.targets[0]); got != "manual" {
		t.Errorf("yaml manual status = %q, want manual", got)
	}
	if !strings.Contains(ansi.Strip(ym.View().Content), "manual") {
		t.Errorf("yaml manual view lacks the manual status word")
	}
}

// A long name is capped at maxNameW and cut from the middle, so the status stays put.
func TestLongTargetNameIsCapped(t *testing.T) {
	m := newTestModel(t, target("services-payments-ledger-reconciler-worker-nightly-backfill"))
	m.width, m.height = 120, 20
	m.frame()

	leftW, _ := m.layoutWidths()
	if want := maxNameW + statusW + 4; leftW != want {
		t.Fatalf("leftW = %d, want %d", leftW, want)
	}
	row := ansi.Strip(m.leftLine(1, leftW))
	if !strings.Contains(row, "services-") || !strings.Contains(row, "backfill") || !strings.Contains(row, "…") {
		t.Fatalf("row = %q, want the name kept at both ends with an ellipsis", row)
	}
	if strings.Contains(row, "nightly-backfill") {
		t.Fatalf("row = %q, want the middle of the name cut", row)
	}
}

// An auto target is gray and starting from the first frame, not green and running.
func TestFreshAutoTargetStartsGray(t *testing.T) {
	m := newTestModel(t, target("web"))
	row := m.targets[0]
	if row.state != stateStarting {
		t.Fatalf("state = %v, want starting", row.state)
	}
	if got := m.status(row); got != "starting" {
		t.Errorf("status = %q, want starting", got)
	}
	if got := stateColor(row); got != colorGray {
		t.Errorf("color = %q, want gray %q", got, colorGray)
	}
	if got := row.glyph(); got != "●" {
		t.Errorf("glyph = %q, want ●", got)
	}
}
