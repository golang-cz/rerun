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

	want := []string{"▶ web started 12:01:02 · sleep 1", "listening", "✓ web exit 0 · 4s"}
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
	if last.tone != toneFail || !strings.Contains(last.label, "exit 1") {
		t.Fatalf("exit rule = %+v, want a red exit 1 rule", last)
	}
}

func TestRunRuleFillsThePaneOnResize(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{{kind: evStarted, target: "web", at: time.Now()}})

	ruleWidth := func() int {
		for _, line := range paneOf(m) {
			if plain := ansi.Strip(line); strings.Contains(plain, "▶ web started") {
				return displayWidth(strings.TrimRight(plain, " "))
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
	if got := ansi.Strip(paneOf(m)[m.titleHeight()+1]); !strings.HasPrefix(got, "── ▶ web started") {
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
