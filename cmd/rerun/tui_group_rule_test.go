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

func TestEventRowsLineUpWithLogLines(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run ./api"), cmdTarget("web", "npm start"))
	m.applyEvents([]event{
		{kind: evStarted, target: "api", at: restartAt},
		restartEvent("web", nil, 0, time.Minute),
	})
	addLines(m, "web", "hello")
	m.applyScope(-1, "")
	m.frame()
	started := dropStamp(ruleLine(m, "started"))
	restarted := dropStamp(ruleLine(m, "restarted"))
	if !strings.HasPrefix(started, "api") || !strings.HasPrefix(restarted, "web") {
		t.Fatalf("rows = %q, %q, want the name column first", started, restarted)
	}
	if strings.Index(started, "started") != strings.Index(restarted, "restarted") {
		t.Fatalf("verbs do not line up: %q, %q", started, restarted)
	}
	if strings.Contains(started, "go run") || strings.Contains(restarted, "npm start") {
		t.Fatalf("command shown while folded: %q, %q", started, restarted)
	}
}

func TestRestartDetailsShowOnlyWhenExpanded(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run ./api"))
	m.applyEvents([]event{restartEvent("api", []string{"/src/handler.go"}, 2*time.Second, time.Minute)})
	m.frame()
	if rows := strings.Join(m.paneLines(), "\n"); strings.Contains(rows, "handler.go") || strings.Contains(rows, "was up") {
		t.Fatalf("details shown while folded: %q", rows)
	}
	m.toggleExpand(m.paneCursorRow())
	rows := strings.Join(m.paneLines(), "\n")
	for _, want := range []string{"files: /src/handler.go", "built 2s", "was up 1m00s", "$ go run ./api"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("expansion lacks %q: %q", want, rows)
		}
	}
}

func TestRestartDetailsOmitWhatIsMissing(t *testing.T) {
	rr := &restartRule{name: "api", at: restartAt, cmd: "go run ./api"}
	if got := rr.details(); len(got) != 1 || got[0] != "$ go run ./api" {
		t.Errorf("details = %q, want only the command", got)
	}
}

func TestMultiLineCommandStaysOnOneRuleLine(t *testing.T) {
	m := newTestModel(t, cmdTarget("api", "go run\n  ./api"), cmdTarget("web", "npm start"))
	m.applyEvents([]event{restartEvent("api", nil, 0, time.Minute)})
	m.toggleExpand(m.paneCursorRow())
	m.frame()
	if got := ruleLine(m, "$ go run"); !strings.Contains(got, "$ go run ./api") {
		t.Errorf("detail = %q, want the command flattened to one line", got)
	}
}

func TestConciseTextPerEventKind(t *testing.T) {
	cases := []struct {
		name string
		got  sepLine
		want string
		tone sepTone
	}{
		{"started", startedRule("api", "go run ./api"), "started", toneNone},
		{"restarted", restartedRule(&restartRule{name: "api", files: []string{"a.go"}, cmd: "x"}), "restarted", toneNone},
		{"building", (&buildRec{name: "api", members: []string{"api"}}).sep(), "building", toneNone},
		{"built", (&buildRec{phase: buildPassed, dur: 3 * time.Second}).sep(), "built 3s", toneNone},
		{"build failed", (&buildRec{phase: buildBroke, reason: "exit 1"}).sep(), "build failed · exit 1", toneFail},
		{"replaced", (&buildRec{phase: buildDropped, reason: "replaced"}).sep(), "build replaced", toneNone},
	}
	for _, c := range cases {
		if c.got.text() != c.want || c.got.tone != c.tone {
			t.Errorf("%s: text %q tone %v, want %q tone %v", c.name, c.got.text(), c.got.tone, c.want, c.tone)
		}
	}
}
