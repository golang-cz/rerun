package main

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Consume exactly one complete OSC frame, with no other bytes in the stream.
func takeStatus(t *testing.T, out *bytes.Buffer, topic, state string) map[string]string {
	t.Helper()
	frame := out.String()
	out.Reset()
	body, start := strings.CutPrefix(frame, "\x1b]7501;")
	body, end := strings.CutSuffix(body, "\x1b\\")
	if !start || !end || strings.ContainsRune(body, '\x1b') || len(frame) > 4096 {
		t.Fatalf("invalid frame: %q", frame)
	}
	fields := map[string]string{}
	for field := range strings.SplitSeq(body, ":") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			t.Fatalf("invalid field %q", field)
		}
		fields[key] = value
	}
	if !regexp.MustCompile(`^rerun/[A-Za-z0-9_.+-]{1,32}/`+topic+`$`).MatchString(fields["id"]) || fields["state"] != state {
		t.Fatalf("unexpected report: %q", fields)
	}
	if state != "clear" && fields["app"] != "rerun" {
		t.Fatalf("missing app: %q", fields)
	}
	return fields
}

func TestProgramStatusBuildAndHealth(t *testing.T) {
	var out bytes.Buffer
	m := newTUI([]Target{{Name: "api", Build: "build", Health: "ping", Selected: true}})
	m.programStatus = &programStatus{out: &out}
	now := m.now
	emit := func(ev event) { ev.target = "api"; m.applyEvent(ev) }
	emit(event{kind: evStarted, at: now})
	emit(event{kind: evHealth, health: healthUnhealthy, runAt: now.Add(-time.Second)})
	emit(event{kind: evOutput, line: "build failed"})
	m.now = now.Add(time.Second)
	m.rebootTarget(0)
	emit(event{kind: evBuildStarted})
	build := takeStatus(t, &out, "build", "working")["id"]
	emit(event{kind: evBuildFailed})
	takeStatus(t, &out, "build", "error")
	emit(event{kind: evHealth, health: healthUnhealthy, runAt: now})
	health := takeStatus(t, &out, "health", "error")["id"]
	emit(event{kind: evHealth, health: healthUnhealthy, runAt: now})
	emit(event{kind: evBuildStarted})
	takeStatus(t, &out, "build", "working")
	emit(event{kind: evBuildOK})
	if got := takeStatus(t, &out, "build", "clear")["id"]; got != build {
		t.Fatalf("cleared %q instead of %q", got, build)
	}
	emit(event{kind: evHealth, health: healthHealthy, runAt: now})
	if got := takeStatus(t, &out, "health", "clear")["id"]; got != health {
		t.Fatalf("cleared %q instead of %q", got, health)
	}
}

func TestProgramStatusCleanup(t *testing.T) {
	var out bytes.Buffer
	m := newTUI([]Target{{Name: "a"}, {Name: "b"}})
	m.programStatus = &programStatus{out: &out}
	emit := func(kind eventKind, target string) { m.applyEvent(event{kind: kind, target: target}) }
	emit(evBuildStarted, "a")
	emit(evBuildStarted, "b")
	out.Reset()
	emit(evBuildCancelled, "a")
	a := takeStatus(t, &out, "build", "clear")["id"]
	i := m.targetIndex("b")
	m.stopTarget(i)
	emit(evBuildFailed, "b") // queued before the runner processes the stop
	takeStatus(t, &out, "build", "error")
	r := m.targets[i].runner
	r.handleControl(t.Context(), nextCtrl(t, r))
	m.applyEvent(<-m.events)
	b := takeStatus(t, &out, "build", "clear")["id"]
	if a == b {
		t.Fatal("targets share a record")
	}
	emit(evBuildFailed, "a")
	out.Reset()
	m.rebuildRows(&configChange{})
	takeStatus(t, &out, "build", "clear")
	emit(evBuildFailed, "a") // retired target
	m.programStatus.close()
	if out.Len() != 0 {
		t.Fatalf("unexpected report after removal: %q", out.String())
	}
}

func TestProgramStatusReloadDuringReplacementBuild(t *testing.T) {
	var out bytes.Buffer
	spec := Target{Name: "api", Build: "old", Start: startManual}
	m := newTUI([]Target{spec})
	m.programStatus = &programStatus{out: &out}
	spec.Build = "replacement"
	m.applyEvent(event{kind: evBuildStarted, target: "api"})
	takeStatus(t, &out, "build", "working")
	m.rebuildRows(&configChange{targets: []Target{spec}, runners: m.group.runners})
	if out.Len() != 0 {
		t.Fatalf("reconciliation cleared an ongoing build: %q", out.String())
	}

	spec.Build = ""
	r := m.targets[0].runner
	r.handleControl(t.Context(), ctrlMsg{kind: ctrlSpec, target: &spec})
	m.applyEvent(<-m.events)
	takeStatus(t, &out, "build", "clear")
}

func TestProgramStatusDoesNotInferComposeServiceHealth(t *testing.T) {
	var out bytes.Buffer
	m := newTUI([]Target{{Name: "db", Kind: composeKind}})
	m.programStatus = &programStatus{out: &out}
	m.applyEvent(event{kind: evStarted, target: "db", at: m.now})
	m.applyEvent(event{kind: evHealth, target: "db", runAt: m.now, health: healthUnhealthy})
	if out.Len() != 0 {
		t.Fatalf("reported container health as service health: %q", out.String())
	}
}

func TestProgramStatusWireLimitsAndShutdown(t *testing.T) {
	var out bytes.Buffer
	s := &programStatus{out: &out}
	target := "a/\x1b:\x07\x80" + strings.Repeat("界", 100)
	s.set(target, "build", "error", "Build failed")
	fields := takeStatus(t, &out, "build", "error")
	title, err := base64.StdEncoding.DecodeString(fields["title"])
	if err != nil || len(title) > 192 || !utf8.Valid(title) || !strings.HasPrefix(string(title), "a/:") || printable(string(title), false) != string(title) {
		t.Fatalf("invalid title: %q (%v)", title, err)
	}
	if fields["msg"] != base64.StdEncoding.EncodeToString([]byte("Build failed")) {
		t.Fatalf("bad message %q", fields["msg"])
	}
	s.close()
	s.close()
	if got := out.String(); got != "\x1b]7501;id=rerun:state=clear\x1b\\" {
		t.Fatalf("unexpected shutdown output: %q", got)
	}
}
