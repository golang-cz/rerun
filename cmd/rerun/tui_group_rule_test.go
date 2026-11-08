package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func cmdTarget(name, cmd string) Target {
	t := target(name)
	t.Cmd = cmd
	return t
}

func ruleLine(m *tuiModel, marker string) string {
	for _, l := range paneOf(m) {
		if s := ansi.Strip(l); strings.Contains(s, marker) {
			return s
		}
	}
	return ""
}

func TestRulesNameTheCommand(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run ./api"), cmdTarget("web", "npm start"))
	m.applyEvents([]event{
		{kind: evStarted, target: "api", at: restartAt, buildDur: 2 * time.Second},
		restartEvent("web", nil, 0, time.Minute),
	})

	m.applyScope(-1, "")
	m.frame()
	if got := ruleLine(m, "▶ api started"); !strings.Contains(got, "· go run ./api") {
		t.Errorf("group start rule = %q, want the command named", got)
	}
	if got := ruleLine(m, "↻ web restarted"); !strings.Contains(got, "· npm start") {
		t.Errorf("group restart rule = %q, want the command last", got)
	}

	m.applyScope(0, "")
	m.frame()
	if got := ruleLine(m, "▶ api started"); !strings.Contains(got, "· go run ./api") {
		t.Errorf("single-target start rule = %q, want the command named", got)
	}
}

func TestRestartRuleLabelCutsMetaBeforeCommand(t *testing.T) {
	rr := &restartRule{name: "api", at: restartAt, built: 2 * time.Second, cmd: "go run ./api"}
	full := "↻ api restarted 15:04:05 · built 2s"
	if got := rr.label(1 << 16); got != full+" · go run ./api" {
		t.Errorf("label = %q", got)
	}
	short := "↻ api restarted 15:04:05"
	if got := rr.label(displayWidth(short) + 2); got != short+" · go run ./api" {
		t.Errorf("narrow label = %q, want the build segment cut and the command kept", got)
	}
}

func TestRestartRuleWithoutBuildOmitsIt(t *testing.T) {
	rr := &restartRule{name: "api", at: restartAt, cmd: "go run ./api"}
	if got := rr.label(1 << 16); got != "↻ api restarted 15:04:05 · go run ./api" {
		t.Errorf("label = %q, want no build segment", got)
	}
}

func TestLongRestartCommandExpandsToFullText(t *testing.T) {
	long := "go run ./cmd/api " + strings.Repeat("--flag ", 30) + "end"
	m := newTestModel(t, cmdTarget("api", long))
	m.applyEvents([]event{restartEvent("api", nil, 0, time.Minute)})
	m.frame()
	row := m.paneCursorRow()
	if !m.expandable(row) {
		t.Fatal("a cut-short restart command is not expandable")
	}
	m.toggleExpand(row)
	if rows := strings.Join(m.paneLines(), "\n"); !strings.Contains(rows, "--flag end") {
		t.Fatalf("expansion lacks the full command: %q", rows)
	}
}

func TestMultiLineCommandStaysOnOneRuleLine(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run\n  ./api"), cmdTarget("web", "npm start"))
	m.applyEvents([]event{restartEvent("api", nil, 0, time.Minute)})
	m.applyScope(-1, "")
	m.frame()
	if got := ruleLine(m, "↻ api restarted"); !strings.Contains(got, "· go run ./api") {
		t.Errorf("rule = %q, want the command flattened to one line", got)
	}
}
