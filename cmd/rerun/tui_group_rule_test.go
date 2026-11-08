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

func TestRulesNameTheCommandOnlyInGroupView(t *testing.T) {
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
	if got := ruleLine(m, "↻ web restarted"); !strings.Contains(got, "· $ npm start") {
		t.Errorf("group restart rule = %q, want the command last", got)
	}

	m.applyScope(0, "")
	m.frame()
	if got := ruleLine(m, "▶ api started"); got == "" || strings.Contains(got, "$ go run") {
		t.Errorf("single-target start rule = %q, want no command", got)
	}
}

func TestRestartRuleCommandIsCutFirst(t *testing.T) {
	rr := &restartRule{name: "api", at: restartAt, built: 2 * time.Second, cmd: "go run ./api"}
	full := "↻ api restarted 15:04:05 · built 2s"
	if got := rr.label(1<<16, true); got != full+" · $ go run ./api" {
		t.Errorf("grouped label = %q", got)
	}
	if got := rr.label(displayWidth(full)+2, true); got != full {
		t.Errorf("narrow grouped label = %q, want the command dropped", got)
	}
	if got := rr.label(1<<16, false); got != full {
		t.Errorf("label = %q, want no command outside group view", got)
	}
}

func TestRestartRuleWithoutBuildOmitsIt(t *testing.T) {
	rr := &restartRule{name: "api", at: restartAt, cmd: "go run ./api"}
	if got := rr.label(1<<16, false); got != "↻ api restarted 15:04:05" {
		t.Errorf("label = %q, want no build segment", got)
	}
	if got := rr.label(1<<16, true); got != "↻ api restarted 15:04:05 · $ go run ./api" {
		t.Errorf("grouped label = %q", got)
	}
}

func TestMultiLineCommandStaysOnOneRuleLine(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run\n  ./api"), cmdTarget("web", "npm start"))
	m.applyEvents([]event{restartEvent("api", nil, 0, time.Minute)})
	m.applyScope(-1, "")
	m.frame()
	if got := ruleLine(m, "↻ api restarted"); !strings.Contains(got, "· $ go run ./api") {
		t.Errorf("rule = %q, want the command flattened to one line", got)
	}
}

func TestInterleavedFollowsTheStreamScope(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run ./api"), cmdTarget("web", "npm start"))
	m.applyScope(-1, "")
	if !m.interleaved() {
		t.Error("all-targets view is not interleaved")
	}
	m.applyScope(-1, "backend")
	if !m.interleaved() {
		t.Error("schema selection is not interleaved")
	}
	m.applyScope(0, "")
	if m.interleaved() {
		t.Error("single-target view is interleaved")
	}
}
