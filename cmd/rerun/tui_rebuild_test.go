package main

import (
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
	if c := buildCell(tg, m.now); c.text != "✓" || c.color != runGreen {
		t.Fatalf("cell 9s after a good build = %+v, want a green ✓", c)
	}
	m.now = buildAt.Add(time.Second + 10*time.Second + time.Millisecond)
	if c := buildCell(tg, m.now); c.text != " " {
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
	if c := buildCell(tg, m.now); c.text != "✗" {
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
	if got := hintRow(m); got != "idle" {
		t.Fatalf("hint 11s after the batch = %q, want idle", got)
	}
}

// A file-triggered build flashes only its gear, for one second.
func TestBuildFlashReversesOnlyTheGear(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	m.applyEvents([]event{buildStart("api", buildAt, "a.go")})
	if c := buildCell(tg, buildAt.Add(500*time.Millisecond)); !c.reverse || c.text != "⚙" {
		t.Fatalf("gear 500ms into a file-triggered build = %+v, want a reversed ⚙", c)
	}
	if c := buildCell(tg, buildAt.Add(1500*time.Millisecond)); c.reverse || c.text != "⚙" {
		t.Fatalf("gear 1.5s into a file-triggered build = %+v, want a plain ⚙", c)
	}
}

// A manual build, with no file behind it, does not flash its gear.
func TestManualBuildDoesNotFlash(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	m.applyEvents([]event{buildStart("api", buildAt)})
	if c := buildCell(m.targets[0], buildAt.Add(100*time.Millisecond)); c.reverse {
		t.Fatalf("gear of a manual build = %+v, want no flash", c)
	}
}

// A restart flashes the state glyph alone; the name and the rest of the row stay plain.
func TestRestartFlashIsTheGlyphOnly(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 1 // keep the cursor off the flashing row, so its own reverse cannot mix in
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})

	m.now = spawnClock.Add(500 * time.Millisecond)
	m.frame()
	leftW, _ := m.layoutWidths()
	line := m.leftLine(1, leftW) // web is the first row under the fs hint
	if got := reversedRun(line); got != "●" {
		t.Fatalf("reversed run while the restart flashes = %q, want the state glyph alone", got)
	}
	m.now = spawnClock.Add(1500 * time.Millisecond)
	if got := reversedRun(m.leftLine(1, leftW)); got != "" {
		t.Fatalf("reversed run 1.5s after the restart = %q, want none", got)
	}
}

// A core loop event shows the red ∞, the red loop hint and the modal warning.
func TestCoreLoopEventShowsLoopState(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	m.applyEvent(event{kind: evLoop, target: "api", line: "a.go", files: []string{"a.go"}})

	if c := buildCell(tg, m.now); c.text != "∞" || c.color != runRed {
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
			if c := buildCell(tg, m.now); c.text == "∞" {
				t.Fatalf("build cell after resume = %+v, want no ∞", c)
			}
			if got := ansi.Strip(m.feedHintCell(30)); strings.Contains(got, "loop:") {
				t.Fatalf("hint after resume = %q, want no loop line", got)
			}
		})
	}
}
