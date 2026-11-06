package main

import (
	"strings"
	"testing"
	"time"
)

var restartAt = time.Date(2026, 11, 5, 15, 4, 5, 0, time.Local)

func restartEvent(name string, files []string, buildDur, prevUp time.Duration) event {
	return event{kind: evStarted, target: name, at: restartAt, restart: true,
		files: files, buildDur: buildDur, prevUp: prevUp}
}

func rawRules(m *tuiModel) []string {
	var out []string
	for _, l := range rawPane(m) {
		if strings.Contains(l, "↻") || strings.Contains(l, "■") || strings.Contains(l, "✓ ") || strings.Contains(l, "✗ ") || strings.Contains(l, "▶") {
			out = append(out, l)
		}
	}
	return out
}

func TestRestartRuleTextAndOmissions(t *testing.T) {
	full := &restartRule{name: "api", at: restartAt, files: []string{"/src/handler.go", "/src/a.go", "/src/b.go"},
		built: 6 * time.Second, upFor: 3*time.Minute + 12*time.Second}
	cases := []struct {
		name string
		rr   *restartRule
		want string
	}{
		{"all segments", full, "↻ api restarted 15:04:05 · handler.go +2 · built 6s · was up 3m12s"},
		{"no files", &restartRule{name: "api", at: restartAt, built: 6 * time.Second, upFor: 3*time.Minute + 12*time.Second},
			"↻ api restarted 15:04:05 · built 6s · was up 3m12s"},
		{"no build", &restartRule{name: "api", at: restartAt, files: []string{"a.go"}, upFor: 3*time.Minute + 12*time.Second},
			"↻ api restarted 15:04:05 · a.go · was up 3m12s"},
		{"no previous run", &restartRule{name: "api", at: restartAt, files: []string{"a.go"}, built: 6 * time.Second},
			"↻ api restarted 15:04:05 · a.go · built 6s"},
		{"one file", &restartRule{name: "api", at: restartAt, files: []string{"/x/only.go"}},
			"↻ api restarted 15:04:05 · only.go"},
	}
	for _, c := range cases {
		if got := c.rr.label(200, false); got != c.want {
			t.Errorf("%s: label = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRestartRuleTruncatesRestLastAndKeepsNameAndTime(t *testing.T) {
	full := &restartRule{name: "api", at: restartAt, files: []string{"/src/handler.go", "/src/a.go"},
		built: 6 * time.Second, upFor: 3*time.Minute + 12*time.Second}
	headFiles := "↻ api restarted 15:04:05 · handler.go +1"
	if got := full.label(displayWidth(headFiles), false); got != headFiles {
		t.Errorf("label at name+time+files width = %q, want %q (the rest dropped first)", got, headFiles)
	}
	if got := full.label(displayWidth(headFiles)-1, false); got != "↻ api restarted 15:04:05" {
		t.Errorf("one cell narrower = %q, want the files segment dropped as a unit", got)
	}
	if got := full.label(10, false); !strings.HasPrefix(got, "↻ api") {
		t.Errorf("narrowest label = %q, want the name kept", got)
	}
}

func TestHealthyAppendsToItsRestartRule(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt}})
	if m.targets[0].lastRule != nil {
		t.Fatal("a first start must not own a restart rule")
	}

	m.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	m.applyEvents([]event{{kind: evHealth, target: "api", health: healthUnhealthy, at: restartAt.Add(time.Second), runAt: restartAt, dur: time.Second}})
	if m.targets[0].lastRule.healthy != 0 {
		t.Fatal("an unhealthy probe must not append")
	}

	m.applyEvents([]event{{kind: evHealth, target: "api", health: healthHealthy, at: restartAt.Add(1800 * time.Millisecond), runAt: restartAt, dur: 1800 * time.Millisecond}})
	if got := m.targets[0].lastRule.label(200, false); !strings.HasSuffix(got, " · healthy in 1.8s") {
		t.Fatalf("label = %q, want healthy in 1.8s appended", got)
	}

	m.applyEvents([]event{{kind: evHealth, target: "api", health: healthHealthy, at: restartAt.Add(-time.Minute), runAt: restartAt.Add(-time.Minute), dur: 9 * time.Second}})
	if m.targets[0].lastRule.healthy != 1800*time.Millisecond {
		t.Fatal("a probe from before the rule's run changed its healthy time")
	}
}

func TestRestartDrawsOneRuleAndNoExitRule(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt}})
	m.applyEvents([]event{{kind: evExited, target: "api", restart: true, code: -1, status: "stopped for restart"}})
	m.applyEvents([]event{restartEvent("api", []string{"a.go"}, 0, 2*time.Second)})
	m.View()

	rules := rawRules(m)
	if len(rules) != 2 || !strings.HasPrefix(rules[0], "▶ api started") || !strings.HasPrefix(rules[1], "↻ api restarted") {
		t.Fatalf("rules = %q, want the first-start rule then one restart rule and no exit rule", rules)
	}
	if strings.Contains(strings.Join(rules, "\n"), "■") {
		t.Fatalf("a restart exit drew a ■ rule: %q", rules)
	}
	if strings.Contains(rules[1], "built") {
		t.Fatalf("a no-build restart names a build: %q", rules[1])
	}
}

func TestNonRestartExitKeepsExitRule(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt}})
	m.applyEvents([]event{{kind: evExited, target: "api", code: 0, status: "exited 0", at: restartAt.Add(time.Second)}})
	m.View()
	if rules := rawRules(m); len(rules) != 2 || !strings.HasPrefix(rules[1], "✓ api exited 0") {
		t.Fatalf("rules = %q, want the ■ exit rule after the start", rules)
	}
}

func TestFirstStartRuleNamesItsBuild(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evStarted, target: "api", at: restartAt, buildDur: 6 * time.Second}})
	m.View()
	if rules := rawRules(m); len(rules) != 1 || !strings.HasPrefix(rules[0], "▶ api started 15:04:05") || strings.Contains(rules[0], "built") {
		t.Fatalf("rules = %q, want the first start without its build", rules)
	}
}

func TestBuildFailedRuleLeavesOnlyTheHint(t *testing.T) {
	cases := []struct {
		name  string
		state procState
		note  string
	}{
		{"old run still up", stateRunning, "still running the old build"},
		{"nothing running", stateOff, "nothing running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t, target("api"))
			m.targets[0].state = c.state
			m.targets[0].runStarted = c.state == stateRunning
			m.applyEvents([]event{{kind: evBuildFailed, target: "api", files: []string{"/x/a.go", "/x/b.go"},
				lines: []string{"boom \x1b[31mred", "second"}}})
			m.View()

			want := "✗ api build failed · a.go +1 more"
			if got := buildRuleTexts(m); len(got) != 1 || got[0] != want {
				t.Fatalf("rule = %q, want %q", got, want)
			}
			if rows := strings.Join(m.paneLines(), "\n"); !strings.Contains(rows, c.note) {
				t.Fatalf("pane = %q, want the note %q under the rule", rows, c.note)
			}
		})
	}
}

func TestListMarksPerState(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	tg := m.targets[0]

	tg.state = stateRunning
	if tg.glyph() != "●" {
		t.Fatalf("running without a probe: glyph = %q, want today's ●", tg.glyph())
	}
	if got := stateColor(tg); got != runGreen {
		t.Fatalf("running without a probe: color = %q, want %q", got, runGreen)
	}

	cases := []struct {
		health healthState
		glyph  string
		color  string
	}{
		{healthStarting, "●", colorGray},
		{healthHealthy, "●", runGreen},
		{healthUnhealthy, "▲", runRed},
	}
	for _, c := range cases {
		tg.health = c.health
		if tg.glyph() != c.glyph || stateColor(tg) != c.color {
			t.Errorf("%s: glyph %q color %q, want %q %q", c.health, tg.glyph(), stateColor(tg), c.glyph, c.color)
		}
	}

	tg.health = healthHealthy
	tg.state = stateExited
	tg.exitCode = 1
	if tg.glyph() != "■" || stateColor(tg) != runRed {
		t.Errorf("exited with a probe: glyph %q color %q, want ■ red", tg.glyph(), stateColor(tg))
	}
}

func TestBuildingStatusCountsElapsed(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.now = restartAt
	tg := m.targets[0]
	tg.state = stateRunning
	tg.building = true
	tg.buildStart = restartAt.Add(-4 * time.Second)
	if got := m.status(tg); got != "⚙ 4s" {
		t.Fatalf("status = %q, want ⚙ 4s", got)
	}
	m.now = m.now.Add(2 * time.Second)
	if got := m.status(tg); got != "⚙ 6s" {
		t.Fatalf("after 2s status = %q, want ⚙ 6s", got)
	}
}

func TestBuildLifecycleClearsBuilding(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.applyEvents([]event{{kind: evBuildStarted, target: "api", at: restartAt}})
	if !m.targets[0].building {
		t.Fatal("evBuildStarted did not mark the build in flight")
	}
	m.applyEvents([]event{{kind: evBuildOK, target: "api", dur: time.Second}})
	if m.targets[0].building {
		t.Fatal("evBuildOK left the build in flight")
	}
}

// The pane holds a solo target's restart rule at the top, while the tail is followed.
func TestRestartPinsRuleAtTopOnlyWhenSoloedAndFollowing(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	addLines(m, "api", streamLines("api", 30)...)
	m.View()
	m.cursor = 0
	m.liveSelect()

	m.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	m.View()
	row, ok := m.ruleRow(m.pinSeq)
	if !ok || m.vp.YOffset != row {
		t.Fatalf("pinned=%v row=%d YOffset=%d, want the rule at the top", ok, row, m.vp.YOffset)
	}

	addLines(m, "api", "after-1", "after-2", "after-3")
	m.View()
	if m.vp.YOffset != row {
		t.Fatalf("YOffset = %d after 3 lines, want the rule still on top at %d", m.vp.YOffset, row)
	}

	addLines(m, "api", streamLines("more", 40)...)
	m.View()
	if m.pinSeq != 0 || m.vp.YOffset != m.paneLen()-m.vp.Height {
		t.Fatalf("pin=%d YOffset=%d, want the tail again once output fills the pane", m.pinSeq, m.vp.YOffset)
	}
}

func TestRestartDoesNotJumpWhenScrolledUpOrGrouped(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	addLines(m, "api", streamLines("api", 30)...)
	m.View()
	m.cursor = 0
	m.liveSelect()
	m.pauseTail() // freezes the tail, as a scroll does
	m.scrollUp(5)
	m.View()
	before := m.vp.YOffset

	m.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	m.View()
	if m.pinSeq != 0 || m.vp.YOffset != before {
		t.Fatalf("scrolled up: pin=%d YOffset=%d, want no jump from %d", m.pinSeq, m.vp.YOffset, before)
	}

	grouped := newTestModel(t, schemaTarget("api", "backend"), target("web"))
	addLines(grouped, "api", streamLines("api", 30)...)
	grouped.View()
	grouped.applyScope(-1, "backend")
	grouped.applyEvents([]event{restartEvent("api", nil, 0, 0)})
	grouped.View()
	if grouped.pinSeq != 0 {
		t.Fatalf("group scope pinned the rule (pin=%d), want no jump", grouped.pinSeq)
	}
}

func streamLines(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, prefix+"-line")
	}
	return out
}
