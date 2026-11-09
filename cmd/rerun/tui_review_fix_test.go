package main

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Stopping, starting, or rebooting ends the build in flight, so its ⚙ stops counting.
func TestControlsClearAnInFlightBuild(t *testing.T) {
	for name, act := range map[string]func(*tuiModel){
		"stop":   func(m *tuiModel) { m.stopTarget(0) },
		"start":  func(m *tuiModel) { m.startTarget(0) },
		"reboot": func(m *tuiModel) { m.rebootTarget(0) },
	} {
		m := newTestModel(t, target("api"))
		m.applyEvents([]event{{kind: evBuildStarted, target: "api", at: restartAt}})
		act(m)
		if m.targets[0].building {
			t.Errorf("%s left the build counting", name)
		}
	}
}

func TestBuildCancelledClearsBuilding(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evBuildStarted, target: "api", at: restartAt}})
	m.applyEvents([]event{{kind: evBuildCancelled, target: "api", at: restartAt}})
	if m.targets[0].building {
		t.Fatal("evBuildCancelled left the build counting")
	}
}

// A first build that fails leaves the row exited, not ticking as running.
func TestFailedFirstBuildShowsExitedNotRunning(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evBuildFailed, target: "api", err: errTest}})
	tg := m.targets[0]
	if tg.state != stateExited || m.status(tg) != "✗ build" || tg.glyph() != "✗" {
		t.Fatalf("state=%v status=%q glyph=%q, want exited build failed ✗", tg.state, m.status(tg), tg.glyph())
	}
	m.frame()
	if rows := m.paneLines(); len(rows) == 0 || !strings.Contains(strings.Join(rows, "\n"), "nothing running") {
		t.Fatalf("pane = %q, want the rule's nothing running row", rows)
	}
}

var errTest = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }

// A target with a health probe shows ● from its real evStarted, before any probe reports.
func TestStartedRunWithProbeShowsStarting(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].spec.Health = "http://localhost:1/health"
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt}})
	tg := m.targets[0]
	if tg.health != healthStarting || tg.glyph() != "●" {
		t.Fatalf("health=%q glyph=%q, want starting ●", tg.health, tg.glyph())
	}
	m.targets[0].spec.Health = ""
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt.Add(time.Second)}})
	if m.targets[0].health != "" {
		t.Fatalf("a target without a probe got health %q", m.targets[0].health)
	}
}

// A probe tagged with an earlier run never marks or completes the live run's rule.
func TestProbeFromEarlierRunIsDropped(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].spec.Health = "x"
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt}})
	second := restartAt.Add(time.Minute)
	m.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	m.targets[0].lastStart = second
	m.applyEvents([]event{{kind: evStarted, target: "api", at: second, restart: true}})

	m.applyEvents([]event{{kind: evHealth, target: "api", health: healthHealthy, at: restartAt.Add(2 * time.Second), runAt: restartAt, dur: 2 * time.Second}})
	if m.targets[0].health == healthHealthy || m.targets[0].lastRule.healthy != 0 {
		t.Fatalf("old run's probe changed the live run: health=%q healthy=%v", m.targets[0].health, m.targets[0].lastRule.healthy)
	}

	m.applyEvents([]event{{kind: evHealth, target: "api", health: healthHealthy, at: second.Add(time.Second), runAt: second, dur: time.Second}})
	if m.targets[0].health != healthHealthy || m.targets[0].lastRule.healthy != time.Second {
		t.Fatalf("live run's probe ignored: health=%q healthy=%v", m.targets[0].health, m.targets[0].lastRule.healthy)
	}
}

func TestFailedBuildClearsRestartBatch(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].restartBatch = 5
	m.applyEvents([]event{{kind: evBuildFailed, target: "api", err: errTest}})
	if m.targets[0].restartBatch != 0 {
		t.Fatalf("restartBatch = %d after a failed build, want 0", m.targets[0].restartBatch)
	}
}

// The healthy append reaches copy and search, not just the drawn rule.
func TestHealthyAppendRefreshesRawText(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	m.applyEvents([]event{{kind: evHealth, target: "api", health: healthHealthy, at: restartAt.Add(time.Second), runAt: restartAt, dur: 1800 * time.Millisecond}})
	m.frame()
	found := false
	for i, s := range m.visibleSep {
		if s.restart != nil {
			found = true
			if !strings.Contains(m.visibleRaw[i], "· healthy in 1s") {
				t.Fatalf("raw text = %q, want the healthy time", m.visibleRaw[i])
			}
		}
	}
	if !found {
		t.Fatal("no restart rule in the pane")
	}
}

// A reload that leaves no groups clears a selected group, not just a missing one.
func TestReloadWithoutGroupsClearsGroupScope(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), target("web"))
	m.applyScope(-1, ungrouped)
	m.rebuildRows(&configChange{targets: []Target{target("api"), target("web")}})
	if m.schemaSel != "" || m.hasGroups() {
		t.Fatalf("schemaSel = %q with groups=%v, want the scope cleared", m.schemaSel, m.hasGroups())
	}
}

// Group keys move the list only; from the pane they leave the scope alone.
func TestGroupKeysIgnoredWhenPaneHasFocus(t *testing.T) {
	m := groupedModel(t)
	m.cursor = 0
	m.liveSelect()
	press(m, "right")
	press(m, "]")
	if m.cursor != 0 || m.schemaSel != "backend" {
		t.Fatalf("] from the pane moved cursor to %d scope %q", m.cursor, m.schemaSel)
	}
}

func TestEscThatChangesNothingHintsAtChips(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	press(m, "esc")
	if !strings.HasPrefix(m.hint, "pills stay") {
		t.Fatalf("hint = %q, want the pills-stay hint", m.hint)
	}
}

func TestPaneBackspaceRemovesLastChipOfAnyKind(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "f", "delta")
	press(m, "right")
	press(m, "backspace")
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" {
		t.Fatalf("pills=%v, want the last pill removed first", m.pills)
	}
}

func TestBarHintNamesTheKeys(t *testing.T) {
	m := alphaModel(t)
	m.width = 130 // wide enough for the hint beside the pill status
	commitText(m, "/", "alpha")
	if got := paneOf(m)[0]; !strings.Contains(got, "/ add · ↑ pick") {
		t.Fatalf("bar = %q, want the key hint", got)
	}
}

// The cursor steps back while another region has focus.
func TestFocusVisibility(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 0
	m.applyScope(1, "") // solo api, so the cursor row is out of the pane's scope
	r, _ := m.cursorRow()
	if m.rowStyle(0, r).GetFaint() {
		t.Fatal("cursor row is faint with the list focused")
	}
	m.paneFocus = true
	if !m.rowStyle(0, r).GetFaint() {
		t.Fatal("cursor row is not faint with the pane focused")
	}
	m.paneFocus = false
}

func TestUnhealthyRunShowsStatusAndTriangle(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	tg := m.targets[0]
	tg.state = stateRunning
	tg.lastStart = restartAt.Add(-time.Minute)
	tg.health = healthUnhealthy
	if got := m.status(tg); got != "unhealthy" || tg.glyph() != "▲" {
		t.Fatalf("status=%q glyph=%q, want unhealthy ▲", got, tg.glyph())
	}
	if !strings.Contains(strings.Join(helpMouse, "\n"), "▲") {
		t.Fatal("help legend lacks ▲")
	}
}

func TestEmptyStateHints(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "nomatch")
	if got := m.emptyHint(); got != "no lines match the filters · backspace removes the last" {
		t.Fatalf("filtered hint = %q", got)
	}

	m = newTestModel(t, target("api"), target("web"))
	m.cursor = 0
	m.liveSelect()
	if got := m.emptyHint(); got != "api: no output yet" {
		t.Fatalf("solo hint = %q", got)
	}
	m.targets[0].building = true
	if got := m.emptyHint(); got != "api: building… output appears when the run starts" {
		t.Fatalf("building hint = %q", got)
	}
}

// Styles are dark until the terminal answers, then follow the reported background.
func TestStylesFollowTheReportedBackground(t *testing.T) {
	m := newTestModel(t, target("web"))
	if !m.isDark {
		t.Fatal("styles are light before the terminal reports its background")
	}
	if got := m.styles.cursor.GetBackground(); got != lipgloss.Color("237") {
		t.Fatalf("default cursor background = %v, want the dark palette 237", got)
	}
	if paint, ok := levelPaintOf("info", m.isDark); !ok || paint.fg != lipgloss.Color("46") {
		t.Fatalf("default INFO foreground = %v, want the dark palette 46", paint.fg)
	}

	for _, c := range []struct {
		name   string
		msg    color.Color
		dark   bool
		cursor color.Color
		info   color.Color
	}{
		{"light", color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, false, lipgloss.Color("252"), lipgloss.Color("28")},
		{"dark", color.RGBA{A: 0xff}, true, lipgloss.Color("237"), lipgloss.Color("46")},
	} {
		m.Update(tea.BackgroundColorMsg{Color: c.msg})
		if m.isDark != c.dark {
			t.Fatalf("%s background set isDark = %v", c.name, m.isDark)
		}
		if got := m.styles.cursor.GetBackground(); got != c.cursor {
			t.Errorf("%s cursor background = %v, want %v", c.name, got, c.cursor)
		}
		if paint, ok := levelPaintOf("info", m.isDark); !ok || paint.fg != c.info {
			t.Errorf("%s INFO foreground = %v, want %v", c.name, paint.fg, c.info)
		}
	}
}

// A bar too narrow for every chip elides the oldest behind …, never cutting
// one or the status.
func TestNarrowBarFoldsOldestChips(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "a1", "b1", "c1")
	for _, q := range []string{"alpha-long", "beta-long", "gamma-long"} {
		commitText(m, "/", q)
	}
	line := m.barLine(34)
	if !strings.HasPrefix(ansi.Strip(line), "… ") || !strings.Contains(line, "gamma-long ×]") {
		t.Fatalf("bar = %q, want the oldest elided behind … and the newest whole", ansi.Strip(line))
	}
	if strings.Contains(line, "alpha-long") {
		t.Fatalf("bar = %q, want alpha-long elided", ansi.Strip(line))
	}
	if displayWidth(line) != 34 {
		t.Fatalf("bar width = %d, want 34", displayWidth(line))
	}
}

// A focused pane with no scope draws every target, so nothing dims.
func TestFocusedPaneWithoutScopeDimsNothing(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.paneFocus = true
	if m.solo >= 0 || m.schemaSel != "" {
		t.Fatal("fixture must have no pane scope")
	}
	for _, key := range []string{"target:web", "target:api"} {
		if got := drawnRow(t, m, key); !strings.Contains(got, "\x1b[") || faintLine(got) {
			t.Fatalf("%s = %q, want full color with nothing dimmed", key, got)
		}
	}
}

// s on a focused group pane stops every running member, not the stale cursor row.
func TestStopInTheFocusedPaneStopsAGroupScope(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.applyScope(-1, "backend")
	m.paneFocus = true
	m.cursor = 3 // web, outside the scope
	for i := range m.targets {
		m.targets[i].state = stateRunning
	}

	press(m, "s")

	if m.targets[0].state != stateStopped || m.targets[1].state != stateStopped {
		t.Fatalf("backend states = %v, %v, want both stopped", m.targets[0].state, m.targets[1].state)
	}
	if m.targets[2].state != stateRunning {
		t.Fatalf("web state = %v, want running (outside the scope)", m.targets[2].state)
	}
}

// ctrl+l on a focused group pane blanks the group, not the stale cursor row.
func TestClearInTheFocusedPaneBlanksAGroupScope(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web"))
	m.applyScope(-1, "backend")
	addLines(m, "api", "api-1")
	addLines(m, "worker", "worker-1")
	addLines(m, "web", "web-1")
	m.frame()
	m.paneFocus = true
	m.cursor = 3 // web, outside the scope

	press(m, "ctrl+l")

	noMarkerText(t, m)
	blankTop(t, m)
}

func TestPinnedRuleShowsCue(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	addLines(m, "api", streamLines("api", 30)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	m.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	found := false
	for _, l := range paneOf(m) {
		if strings.Contains(l, "pinned") {
			found = true
		}
	}
	if !found {
		t.Fatal("no pinned cue under the held rule")
	}
}
