package main

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
)

// rbTrigger is a file-triggered build's change batch and its build start, at one instant.
func rbTrigger(m *tuiModel, name string, at time.Time, files ...string) {
	m.now = at
	ops := map[string]fsnotify.Op{}
	for _, f := range files {
		ops[f] = fsnotify.Write
	}
	m.applyEvents([]event{
		{kind: evChanged, target: name, count: len(ops), at: at, change: changeBatch(ops), restarts: true},
		buildStart(name, at, files...),
	})
}

// rbBuildDone is a passed build finishing at one instant.
func rbBuildDone(m *tuiModel, name string, at time.Time) {
	m.now = at
	m.applyEvents([]event{buildOK(name, time.Second)})
}

// reversedRun is the text drawn in reverse video in one rendered line.
func reversedRun(line string) string {
	var out strings.Builder
	rev := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j >= len(line) {
				break
			}
			rev = sgrReverse(line[i+2:j], rev)
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if rev {
			out.WriteRune(r)
		}
		i += size
	}
	return out.String()
}

// sgrReverse applies one SGR parameter list to the reverse-video state.
func sgrReverse(params string, rev bool) bool {
	if params == "" {
		return false
	}
	parts := strings.Split(params, ";")
	for k := 0; k < len(parts); k++ {
		switch parts[k] {
		case "", "0", "27":
			rev = false
		case "7":
			rev = true
		case "38", "48":
			// Colour arguments carry their own numbers, which must not read as flags.
			if k+1 < len(parts) && parts[k+1] == "5" {
				k += 2
			} else if k+1 < len(parts) && parts[k+1] == "2" {
				k += 4
			}
		}
	}
	return rev
}

// The ✓ shows for recentWindow after a good build, then the cell is blank.
func TestBuildCheckShowsForTenSeconds(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	tg := m.targets[0]
	rbTrigger(m, "api", buildAt, "a.go")
	rbBuildDone(m, "api", buildAt.Add(time.Second))

	m.now = buildAt.Add(time.Second + 9*time.Second)
	if c := buildCell(tg, m.now, 0); c.text != "✓" || c.color != runGreen {
		t.Fatalf("cell 9s after a good build = %+v, want a green ✓", c)
	}
	m.now = buildAt.Add(time.Second + 10*time.Second + time.Millisecond)
	if c := buildCell(tg, m.now, 0); c.text != " " {
		t.Fatalf("cell 10s after a good build = %+v, want blank", c)
	}
}

// A failed build keeps its red ✗ however long it has been.
func TestBuildFailureStaysUntilNextBuild(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	tg := m.targets[0]
	m.now = buildAt
	m.applyEvents([]event{buildStart("api", buildAt), buildFail("api", time.Second)})
	m.now = buildAt.Add(time.Minute)
	if c := buildCell(tg, m.now, 0); c.text != "✗" {
		t.Fatalf("cell a minute after a failed build = %+v, want the red ✗", c)
	}
}

// The fs hint names the newest batch for recentWindow, then reads idle.
func TestFsHintGoesIdleAfterTenSeconds(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})

	m.now = spawnClock.Add(9 * time.Second)
	if got := hintRow(m); !strings.Contains(got, "a.go") {
		t.Fatalf("hint 9s after the batch = %q, want a.go", got)
	}
	m.now = spawnClock.Add(11 * time.Second)
	if got := hintRow(m); got != "fs idle" {
		t.Fatalf("hint 11s after the batch = %q, want fs idle", got)
	}
}

// tinted is the foreground color of a target's name at one instant.
func tinted(tg *tuiTarget, now time.Time) string { return tg.nameColor(now) }

// Every build start tints the name orange for 1 s and shows the spinner while it builds.
func TestBuildStartTintsNameOrangeThenPlain(t *testing.T) {
	for name, files := range map[string][]string{"file-triggered": {"a.go"}, "manual": nil} {
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t, buildTarget("api"))
			m.now = buildAt
			tg := m.targets[0]
			m.applyEvents([]event{buildStart("api", buildAt, files...)})
			if got := tinted(tg, buildAt.Add(500*time.Millisecond)); got != runYellow {
				t.Fatalf("name 0.5s into a build = %q, want orange", got)
			}
			if got := tinted(tg, buildAt.Add(3*time.Second)); got != "" {
				t.Fatalf("name 3s into a build = %q, want plain", got)
			}
			c := buildCell(tg, buildAt.Add(3*time.Second), 0)
			if !slices.Contains(spinnerFrames, c.text) || c.color != runYellow {
				t.Fatalf("cell while building = %+v, want an orange spinner frame", c)
			}
			if buildCell(tg, buildAt.Add(4*time.Second), 1).text == c.text {
				t.Fatal("spinner did not advance with the clock")
			}
		})
	}
}

// A passed build leaves the name plain and shows a green ✓ for 10 s.
func TestBuildOKNamePlainAndCheckFor10s(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	tg := m.targets[0]
	m.applyEvents([]event{buildStart("api", buildAt)})
	rbBuildDone(m, "api", buildAt.Add(time.Second))
	at := buildAt.Add(time.Second)
	if got := tinted(tg, at); got != "" {
		t.Fatalf("name after OK = %q, want plain", got)
	}
	if c := buildCell(tg, at.Add(9*time.Second), 0); c.text != "✓" || c.color != runGreen {
		t.Fatalf("cell 9s after OK = %+v, want a green ✓", c)
	}
	if c := buildCell(tg, at.Add(11*time.Second), 0); c.text != " " {
		t.Fatalf("cell 11s after OK = %+v, want blank", c)
	}
}

// A failed build keeps the name red until the next build; red beats orange and green.
func TestFailedBuildNameRedBeatsOtherTints(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	m.applyEvents([]event{buildStart("api", buildAt), buildFail("api", time.Second)})
	tg.restartedAt = buildAt
	if got := tinted(tg, buildAt.Add(500*time.Millisecond)); got != runRed {
		t.Fatalf("name right after a failure = %q, want red over green", got)
	}
	if got := tinted(tg, buildAt.Add(time.Hour)); got != runRed {
		t.Fatalf("name an hour later = %q, want red", got)
	}
	m.applyEvents([]event{buildStart("api", buildAt.Add(time.Hour))})
	if got := tinted(tg, buildAt.Add(time.Hour+500*time.Millisecond)); got != runYellow {
		t.Fatalf("name in the next build = %q, want orange", got)
	}
}

// A loop paints the name red, beating a fresh restart's green.
func TestLoopNameRed(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	tg.restartedAt = buildAt
	m.applyEvent(event{kind: evLoop, target: "api", line: "a.go", files: []string{"a.go"}})
	if got := tinted(tg, buildAt); got != runRed {
		t.Fatalf("name while looping = %q, want red", got)
	}
}

// A file restart tints the name green for 1 s; the glyph and build cell stay as they were.
func TestRestartTintsNameGreen(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	web := m.targets[0]
	if got := tinted(web, spawnClock.Add(500*time.Millisecond)); got != runGreen {
		t.Fatalf("name 0.5s after a restart = %q, want green", got)
	}
	if got := tinted(web, spawnClock.Add(3*time.Second)); got != "" {
		t.Fatalf("name 3s after a restart = %q, want plain", got)
	}
}

// Only the cursor and selections draw in reverse: no list row flashes.
func TestNoListRowIsReversedOffTheCursor(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), buildTarget("web"))
	m.cursor = 1
	m.now = buildAt
	changeAt(m, "api", buildAt, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.applyEvents([]event{buildStart("api", buildAt, "a.go")})
	m.now = buildAt.Add(500 * time.Millisecond)
	m.frame()
	leftW, _ := m.layoutWidths()
	for y := 1; y <= 2; y++ {
		if got := reversedRun(m.leftLine(y, leftW)); got != "" {
			t.Fatalf("row %d reversed run = %q, want none", y, got)
		}
	}
}

// A core loop event shows the red ∞, the red loop hint and the modal warning.
func TestCoreLoopEventShowsLoopState(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	m.applyEvent(event{kind: evLoop, target: "api", line: "a.go", files: []string{"a.go"}})

	if c := buildCell(tg, m.now, 0); c.text != "∞" || c.color != runRed {
		t.Fatalf("build cell while the core loop pauses = %+v, want a red ∞", c)
	}
	if got := ansi.Strip(m.feedHintCell(30)); !strings.Contains(got, "loop: a.go") {
		t.Fatalf("hint while looping = %q, want loop: a.go", got)
	}
	m.eventsModal.open = true
	if got := ansi.Strip(m.eventsView()); !strings.Contains(got, "api: rebuild loop, a.go changes after every build, paused") {
		t.Fatalf("modal while looping lacks the warning:\n%s", got)
	}
}

// A held change keeps the loop on; a start after the pause ends it.
func TestCoreResumeClearsLoopState(t *testing.T) {
	resumes := map[string]event{
		"run starts":   {kind: evStarted, target: "api", at: buildAt},
		"build starts": buildStart("api", buildAt, "a.go"),
	}
	for name, resume := range resumes {
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t, buildTarget("api"))
			m.now = buildAt
			tg := m.targets[0]
			m.applyEvents([]event{{kind: evLoop, target: "api", line: "a.go", files: []string{"a.go"}}})
			m.applyEvents([]event{{kind: evChanged, target: "api", count: 1, at: buildAt, change: oneFile("a.go"), restarts: false}})
			if !tg.looping() {
				t.Fatal("a held change cleared the loop, want it kept")
			}
			m.applyEvents([]event{resume})
			if tg.looping() {
				t.Fatal("loop still on after the target resumed")
			}
			if c := buildCell(tg, m.now, 0); c.text == "∞" {
				t.Fatalf("build cell after resume = %+v, want no ∞", c)
			}
			if got := ansi.Strip(m.feedHintCell(30)); strings.Contains(got, "loop:") {
				t.Fatalf("hint after resume = %q, want no loop line", got)
			}
		})
	}
}

// The fast spinner tick starts with a build, advances the frame, and stops with the last build.
func TestSpinTickRunsOnlyWhileBuilding(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	if cmd := m.startSpin(); cmd != nil || m.spinning {
		t.Fatal("spin tick started with no build")
	}
	if cmd := m.applyEvents([]event{buildStart("api", buildAt)}); cmd == nil || !m.spinning {
		t.Fatalf("build start did not start the spin tick (spinning = %v)", m.spinning)
	}
	if cmd := m.applyEvents([]event{buildStart("api", buildAt)}); cmd != nil {
		t.Fatal("a second batch started a second spin tick")
	}
	_, next := m.Update(spinTickMsg{})
	if m.spin != 1 || next == nil {
		t.Fatalf("spin = %d, next = %v, want the frame advanced and the tick kept", m.spin, next)
	}
	m.applyEvents([]event{buildOK("api", time.Second)})
	if _, next = m.Update(spinTickMsg{}); next != nil || m.spinning || m.spin != 1 {
		t.Fatalf("tick after the last build: next = %v, spinning = %v, spin = %d, want it stopped", next, m.spinning, m.spin)
	}
}
