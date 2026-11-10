package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestStartAndExitWriteRunRules(t *testing.T) {
	m := newTestModel(t, target("web"))
	start := time.Date(2026, 11, 4, 12, 1, 2, 0, time.Local)
	m.applyEvents([]event{
		{kind: evStarted, target: "web", at: start},
		{kind: evOutput, target: "web", line: "listening"},
		{kind: evExited, target: "web", code: 0, at: start.Add(4200 * time.Millisecond)},
	})

	want := []string{"web started", "listening", "web exited"}
	var got []string
	for i, line := range m.visibleRaw {
		if m.visibleSep[i].isSep() || line == "listening" {
			got = append(got, line)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stream = %q, want %q", got, want)
	}
	if last := m.visibleSep[len(m.visibleSep)-1]; last.tone != toneOK {
		t.Fatalf("exit 0 tone = %v, want ok", last.tone)
	}
}

func TestFailedExitRuleIsRed(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{
		{kind: evStarted, target: "web", at: time.Now()},
		{kind: evExited, target: "web", code: 1},
	})
	last := m.visibleSep[len(m.visibleSep)-1]
	if last.tone != toneFail || last.fact != "exit 1" {
		t.Fatalf("exit rule = %+v, want a red exit 1 rule", last)
	}
}

func TestRunRuleFillsThePaneOnResize(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{{kind: evStarted, target: "web", at: time.Now()}})

	ruleWidth := func() int {
		for _, line := range paneOf(m) {
			if plain := ansi.Strip(line); strings.Contains(plain, " started") {
				return displayWidth(strings.TrimRight(dropStamp(plain), " "))
			}
		}
		t.Fatal("no run rule on screen")
		return 0
	}
	if got, want := ruleWidth(), m.paneWidth(); got != want {
		t.Fatalf("rule width = %d, want the %d-cell pane", got, want)
	}

	m.Update(tea.WindowSizeMsg{Width: 140, Height: 20})
	if got, want := ruleWidth(), m.paneWidth(); got != want {
		t.Fatalf("rule width after resize = %d, want %d", got, want)
	}
}

func TestRunRuleIsNotATableRow(t *testing.T) {
	m := newTestModel(t, tableTarget("web"))
	addLines(m, "web", jsonGet)
	m.applyEvents([]event{{kind: evStarted, target: "web", at: time.Now()}})
	m.frame()

	if !m.tableOn() {
		t.Fatal("expected table mode")
	}
	if got := dropStamp(ansi.Strip(paneOf(m)[m.titleHeight()+1])); !strings.HasPrefix(got, "web ") || !strings.Contains(got, " started") {
		t.Fatalf("rule row = %q, want the run rule unchanged at the left edge", got)
	}
}

func TestRunRuleRespectsTheScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyScope(1, "")
	m.applyEvents([]event{{kind: evStarted, target: "web", at: time.Now()}})

	for _, line := range m.visibleRaw {
		if strings.Contains(line, "web started") {
			t.Fatalf("web's rule shows while api is soloed: %q", line)
		}
	}
	m.applyEvents([]event{{kind: evStarted, target: "api", at: time.Now()}})
	if !strings.Contains(m.visibleRaw[len(m.visibleRaw)-1], "api started") {
		t.Fatalf("api's rule missing from the soloed stream: %q", m.visibleRaw)
	}
}

// stampModel is a model whose clock starts at 12:00:00 and reads through the returned pointer.
func stampModel(t *testing.T, targets ...Target) (*tuiModel, *time.Time) {
	t.Helper()
	m := newTestModel(t, targets...)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local)
	m.startAt, m.now = now, now
	m.clock = func() time.Time { return now }
	m.width, m.height = 120, 30
	return m, &now
}

// paneRowsText is the right column's rows, styling stripped.
func paneRowsText(m *tuiModel) []string {
	var out []string
	for _, l := range strings.Split(m.frame(), "\n") {
		l = ansi.Strip(l)
		out = append(out, string([]rune(l)[m.layout.leftW+1:]))
	}
	return out
}

// Lines of one batch share one clock read and one stamp.
func TestLinesFromOneBatchShareAStamp(t *testing.T) {
	m, now := stampModel(t, target("web"))
	reads := 0
	m.clock = func() time.Time { reads++; return *now }
	*now = now.Add(5 * time.Second)
	m.applyEvents([]event{
		{kind: evOutput, target: "web", line: "one"},
		{kind: evOutput, target: "web", line: "two"},
		{kind: evOutput, target: "web", line: "three"},
	})
	if reads != 1 {
		t.Fatalf("clock reads for one batch = %d, want 1", reads)
	}
	if got := m.visibleAt; len(got) != 3 || got[0] != 5 || got[1] != 5 || got[2] != 5 {
		t.Fatalf("stamps = %v, want [5 5 5]", got)
	}
	*now = now.Add(2 * time.Second)
	m.applyEvents([]event{{kind: evOutput, target: "web", line: "four"}})
	if got := m.visibleAt[3]; got != 7 {
		t.Fatalf("next batch stamp = %d, want 7", got)
	}
}

// Every pane line, service output and rerun's own rules alike, shows its time in front, whatever t says.
func TestPaneLinesAlwaysShowTime(t *testing.T) {
	m, now := stampModel(t, buildTarget("web"))
	*now = now.Add(5 * time.Second)
	m.applyEvents([]event{
		{kind: evStarted, target: "web", at: *now},
		{kind: evOutput, target: "web", line: "hello"},
		buildStart("web", *now),
		buildOK("web", time.Second),
		buildStart("web", *now),
		buildFail("web", time.Second),
		{kind: evStarted, target: "web", at: *now, restart: true},
		{kind: evExited, target: "web", code: 1, at: *now},
	})
	for _, times := range []bool{false, true} {
		m.showTimes = times
		m.vpDirty = true
		n := 0
		for _, l := range paneRowsText(m) {
			if strings.Contains(l, "hello") || strings.Contains(l, "started") || strings.Contains(l, "restarted") || strings.Contains(l, "built") || strings.Contains(l, "build failed") || strings.Contains(l, "exit 1") {
				n++
				if !strings.HasPrefix(l, "12:00:05 ") {
					t.Fatalf("times=%v: row %q, want the stamp in front", times, l)
				}
			}
		}
		if n < 6 {
			t.Fatalf("times=%v: found %d rule and output rows, want 6", times, n)
		}
	}
}

// The time is no part of a line's text, so copy and search never see it.
func TestStampIsNoPartOfCopyOrSearch(t *testing.T) {
	m, _ := stampModel(t, target("web"))
	m.showTimes = true
	m.applyEvents([]event{{kind: evOutput, target: "web", line: "hello"}})
	m.frame()
	for _, l := range append(m.visible, m.visibleRaw...) {
		if strings.Contains(l, "12:00:00") {
			t.Fatalf("line text %q holds the stamp", l)
		}
	}
	if rows := m.paneLines(); strings.Contains(strings.Join(rows, "\n"), "12:00:00") {
		t.Fatalf("pane lines %q hold the stamp", rows)
	}
}

// An expansion row under a line stays blank, so the time shows once per line.
func TestExpansionRowsDoNotRepeatTheStamp(t *testing.T) {
	m, _ := stampModel(t, target("web"))
	m.showTimes = true
	m.applyEvents([]event{{kind: evOutput, target: "web", line: `{"level":"info","msg":"hi","a":1,"b":2}`}})
	m.frame()
	m.expanded[m.visibleSeq[0]] = true
	m.dispDirty, m.vpDirty = true, true
	rows := paneRowsText(m)
	stamped := 0
	for _, l := range rows {
		if strings.Contains(l, "12:00:00") {
			stamped++
		}
	}
	if m.paneLen() < 2 || stamped != 1 {
		t.Fatalf("paneLen = %d, stamped rows = %d, want an expansion and one stamp:\n%s", m.paneLen(), stamped, strings.Join(rows, "\n"))
	}
}

// Ingesting a line stays cheap with the stamp.
func BenchmarkAddLine(b *testing.B) {
	m := newTestModel(b, target("web"))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.addLine(0, "a log line that is typical in length")
	}
}
