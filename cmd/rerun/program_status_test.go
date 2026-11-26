package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golang-cz/rerun"
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
	if !regexp.MustCompile(`^rerun/[A-Za-z0-9_.+-]{1,32}/[A-Za-z0-9_.+-]{1,32}/`+topic+`$`).MatchString(fields["id"]) || fields["state"] != state {
		t.Fatalf("unexpected report: %q", fields)
	}
	if state != "clear" && fields["app"] != "rerun" {
		t.Fatalf("missing app: %q", fields)
	}
	return fields
}

// statusModel is a TUI that reports program status to out. apply feeds an
// event the way Update does, syncing the report after it.
func statusModel(t *testing.T, targets ...Target) (m *tuiModel, out *bytes.Buffer, apply func(event)) {
	t.Helper()
	out = &bytes.Buffer{}
	m = newTestModel(t, targets...)
	m.programStatus = newProgramStatus(out)
	apply = func(ev event) {
		m.applyEvent(ev)
		m.programStatus.sync(m.targets)
	}
	return m, out, apply
}

func TestProgramStatusBuildAndHealth(t *testing.T) {
	m, out, apply := statusModel(t, Target{Name: "api", Build: "build", Health: "ping", Selected: true})
	now := m.now
	emit := func(ev event) { ev.target = "api"; apply(ev) }
	emit(event{kind: evStarted, at: now})
	emit(event{kind: evHealth, health: healthUnhealthy, runAt: now.Add(-time.Second)})
	emit(event{kind: evOutput, line: "build failed"})
	m.now = now.Add(time.Second)
	m.rebootTarget(0)
	emit(event{kind: evBuildStarted})
	build := takeStatus(t, out, "build", "working")["id"]
	emit(event{kind: evBuildFailed})
	takeStatus(t, out, "build", "error")
	// The failed rebuild left the old run up, so its probes still count.
	emit(event{kind: evHealth, health: healthUnhealthy, runAt: now})
	health := takeStatus(t, out, "health", "error")["id"]
	emit(event{kind: evHealth, health: healthUnhealthy, runAt: now})
	emit(event{kind: evBuildStarted})
	takeStatus(t, out, "build", "working")
	emit(event{kind: evBuildOK})
	if got := takeStatus(t, out, "build", "clear")["id"]; got != build {
		t.Fatalf("cleared %q instead of %q", got, build)
	}
	emit(event{kind: evHealth, health: healthHealthy, runAt: now})
	if got := takeStatus(t, out, "health", "clear")["id"]; got != health {
		t.Fatalf("cleared %q instead of %q", got, health)
	}
}

func TestProgramStatusCleanup(t *testing.T) {
	m, out, apply := statusModel(t, Target{Name: "a"}, Target{Name: "b"})
	emit := func(kind eventKind, target string) { apply(event{kind: kind, target: target}) }
	emit(evBuildStarted, "a")
	emit(evBuildStarted, "b")
	out.Reset()
	emit(evBuildCancelled, "a")
	a := takeStatus(t, out, "build", "clear")["id"]
	i := m.targetIndex("b")
	m.stopTarget(i)
	m.programStatus.sync(m.targets)
	b := takeStatus(t, out, "build", "clear")["id"]
	if a == b {
		t.Fatal("targets share a record")
	}
	emit(evBuildFailed, "b") // queued before the runner processes the stop
	if out.Len() != 0 || m.targets[i].state != stateStopped || m.targets[i].buildFailed {
		t.Fatalf("a cancelled build's result reached a stopped row: %q, state %v", out.String(), m.targets[i].state)
	}
	emit(evBuildFailed, "a")
	out.Reset()
	m.rebuildRows(&configChange{})
	m.programStatus.sync(m.targets)
	takeStatus(t, out, "build", "clear")
	emit(evBuildFailed, "a") // retired target
	m.programStatus.close()
	if out.Len() != 0 {
		t.Fatalf("unexpected report after removal: %q", out.String())
	}
}

func TestProgramStatusStartBeforeStopAck(t *testing.T) {
	m, out, apply := statusModel(t, target("api"))
	r := m.targets[0].runner
	apply(event{kind: evStarted, target: "api", at: m.now})
	press(m, "s")
	stop := nextCtrl(t, r)
	m.now = m.now.Add(time.Minute)
	press(m, " ")
	if got := queuedCtrl(t, r); got != ctrlStart {
		t.Fatalf("control = %v, want start", got)
	}
	if !m.targets[0].lastStart.Equal(m.now) {
		t.Fatal("the stopped run's uptime carried into the next one")
	}
	apply(event{kind: evBuildFailed, target: "api"}) // queued before the stop was processed
	apply(event{kind: evRunnerErr, target: "api", err: fmt.Errorf("old spawn failed")})
	r.handleControl(t.Context(), stop)
	apply(<-m.events)
	apply(event{kind: evBuildStarted, target: "api"})
	takeStatus(t, out, "build", "working")
	press(m, "s")
	if got := queuedCtrl(t, r); got != ctrlStop {
		t.Fatalf("control = %v, want stop during replacement build", got)
	}
	takeStatus(t, out, "build", "clear")
}

func TestProgramStatusRejectedStopKeepsBuild(t *testing.T) {
	m, out, apply := statusModel(t, target("api"))
	apply(event{kind: evBuildStarted, target: "api"})
	takeStatus(t, out, "build", "working")
	for m.targets[0].runner.control(ctrlReboot) {
	}
	m.stopTarget(0)
	m.programStatus.sync(m.targets)
	if out.Len() != 0 || !m.targets[0].building {
		t.Fatal("a rejected stop cleared the build")
	}
}

func TestProgramStatusRetryAfterSpawnFailure(t *testing.T) {
	m, out, apply := statusModel(t, target("api"))
	apply(event{kind: evStarted, target: "api", at: m.now})
	apply(event{kind: evHealth, target: "api", runAt: m.now, health: healthUnhealthy})
	takeStatus(t, out, "health", "error")
	apply(event{kind: evRunnerErr, target: "api", err: fmt.Errorf("spawn failed")})
	takeStatus(t, out, "health", "clear")
	m.rebootTarget(0)
	m.programStatus.sync(m.targets)
	if out.Len() != 0 {
		t.Fatal("retry reported the previous process's health")
	}
}

func TestProgramStatusReloadDropsBuild(t *testing.T) {
	spec := Target{Name: "api", Build: "old", Start: startManual}
	m, out, apply := statusModel(t, spec)
	spec.Build = "replacement"
	apply(event{kind: evBuildStarted, target: "api"})
	takeStatus(t, out, "build", "working")
	m.rebuildRows(&configChange{targets: []Target{spec}, runners: m.group.runners})
	m.programStatus.sync(m.targets)
	if out.Len() != 0 {
		t.Fatalf("reconciliation cleared an ongoing build: %q", out.String())
	}

	spec.Build = ""
	r := m.targets[0].runner
	r.build = &buildJob{} // the replacement build still in flight
	r.handleControl(t.Context(), ctrlMsg{kind: ctrlSpec, target: &spec})
	apply(<-m.events)
	takeStatus(t, out, "build", "clear")
}

func TestProgramStatusReloadDropsFailedBuild(t *testing.T) {
	spec := Target{Name: "api", Build: "old", Start: startManual}
	m, out, apply := statusModel(t, spec)
	apply(event{kind: evBuildFailed, target: "api"})
	takeStatus(t, out, "build", "error")
	spec.Build = ""
	m.targets[0].runner.handleControl(t.Context(), ctrlMsg{kind: ctrlSpec, target: &spec})
	select {
	case ev := <-m.events:
		apply(ev)
	default:
		t.Fatal("removing the build step did not retire its failed result")
	}
	takeStatus(t, out, "build", "clear")
}

func TestProgramStatusDoesNotInferComposeServiceHealth(t *testing.T) {
	m, out, apply := statusModel(t, Target{Name: "db", Kind: composeKind})
	apply(event{kind: evStarted, target: "db", at: m.now})
	apply(event{kind: evHealth, target: "db", runAt: m.now, health: healthUnhealthy})
	if out.Len() != 0 {
		t.Fatalf("reported container health as service health: %q", out.String())
	}
}

func TestProgramStatusWireLimitsAndShutdown(t *testing.T) {
	var out bytes.Buffer
	s := newProgramStatus(&out)
	name := "a/\x1b:\x07\x80" + strings.Repeat("界", 100)
	row := &tuiTarget{spec: Target{Name: name}, state: stateExited, buildFailed: true}
	s.sync([]*tuiTarget{row})
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
	if got, want := out.String(), "\x1b]7501;id=rerun/"+s.run+":state=clear\x1b\\"; got != want {
		t.Fatalf("shutdown output %q, want %q", got, want)
	}
}

func TestPendingStopDrainDropsTargetEvents(t *testing.T) {
	m := newTestModel(t, target("api"))
	row := m.targets[0]
	m.applyEvent(event{kind: evStarted, target: "api", at: m.now})
	press(m, "s")
	if row.pendingStops != 1 {
		t.Fatalf("pendingStops = %d, want 1", row.pendingStops)
	}
	lines := len(row.lines)
	m.applyEvent(event{kind: evChanged, target: "api", change: rerun.ChangeSet{FirstFile: "a.go", Files: map[string]struct{}{"a.go": {}}}, restarts: true})
	m.applyEvent(event{kind: evLoop, target: "api", line: "a.go", files: []string{"a.go"}})
	m.applyEvent(event{kind: evRunnerErr, target: "api", err: errors.New("old spawn failed")})
	m.applyEvent(event{kind: evOutput, target: "api", line: "late line"})
	if got := row.lastChange; got.FirstFile != "" || len(got.Files) != 0 {
		t.Fatalf("a change landed on the stopped row: %+v", got)
	}
	if row.restartBatch != 0 || row.loopFile != "" || len(row.lines) != lines {
		t.Fatalf("a stopped run's event landed: restart %d, loop %q, lines %d", row.restartBatch, row.loopFile, len(row.lines)-lines)
	}
	if row.state != stateStopped || row.exitLabel != "" {
		t.Fatalf("state = %v %q, want stopped", row.state, row.exitLabel)
	}
}

func TestStopAckNeverGoesNegative(t *testing.T) {
	m := newTestModel(t, target("api"))
	row := m.targets[0]
	m.applyEvent(event{kind: evStarted, target: "api", at: m.now})
	press(m, "s")
	if row.pendingStops != 1 {
		t.Fatalf("pendingStops = %d, want 1", row.pendingStops)
	}
	m.applyEvent(event{kind: evStopAck, target: "api"})
	m.applyEvent(event{kind: evStopAck, target: "api"}) // duplicate, or a late one
	if row.pendingStops != 0 {
		t.Fatalf("pendingStops = %d, want 0", row.pendingStops)
	}
}

func TestProgramStatusRunNamespace(t *testing.T) {
	var first, second bytes.Buffer
	one, two := newProgramStatus(&first), newProgramStatus(&second)
	if one.run == two.run {
		t.Fatalf("both runs use the namespace %q", one.run)
	}
	rows := []*tuiTarget{{spec: Target{Name: "api"}, state: stateExited, buildFailed: true}}
	one.sync(rows)
	two.sync(rows)
	if id := takeStatus(t, &first, "build", "error")["id"]; !strings.HasPrefix(id, "rerun/"+one.run+"/") {
		t.Fatalf("id %q is outside this run's subtree", id)
	}
	twoID := takeStatus(t, &second, "build", "error")["id"]
	one.close()
	if got, want := first.String(), "\x1b]7501;id=rerun/"+one.run+":state=clear\x1b\\"; got != want {
		t.Fatalf("quit wrote %q, want %q", got, want)
	}
	if strings.Contains(first.String(), two.run) {
		t.Fatalf("quit cleared another run's records: %q", first.String())
	}
	if second.String() != "" {
		t.Fatalf("another run's quit touched these records: %q", second.String())
	}
	two.close()
	if second.String() != "\x1b]7501;id=rerun/"+two.run+":state=clear\x1b\\" {
		t.Fatalf("unexpected shutdown output: %q", second.String())
	}
	if !strings.Contains(twoID, "/"+two.run+"/") {
		t.Fatalf("id %q does not name its run", twoID)
	}
}

func TestLockedStdoutKeepsWritesWhole(t *testing.T) {
	var buf bytes.Buffer
	w := &lockedStdout{out: &buf}
	const writers, chunks, size = 8, 25, 512
	var group sync.WaitGroup
	for i := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			line := strings.Repeat(strconv.Itoa(i), size) + "\n"
			for range chunks {
				_, _ = w.Write([]byte(line))
			}
		}()
	}
	group.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != writers*chunks {
		t.Fatalf("got %d lines, want %d", len(lines), writers*chunks)
	}
	for _, line := range lines {
		if len(line) != size || strings.Count(line, line[:1]) != size {
			t.Fatalf("a write was split: %q", line)
		}
	}
}

func TestProgramStatusOptOut(t *testing.T) {
	t.Setenv("RERUN_PROGRAM_STATUS", "0")
	if programStatusEnabled() {
		t.Fatal(`RERUN_PROGRAM_STATUS=0 still reports`)
	}
	for _, value := range []string{"", "1", "false"} {
		t.Setenv("RERUN_PROGRAM_STATUS", value)
		if !programStatusEnabled() {
			t.Fatalf("%q disabled the reports", value)
		}
	}
}

func TestStopTargetClearsHealth(t *testing.T) {
	m := newTestModel(t, target("api"))
	row := m.targets[0]
	row.state, row.runStarted, row.health = stateRunning, true, healthUnhealthy
	m.stopTarget(0)
	if row.health != "" {
		t.Fatalf("a stopped row kept health %q", row.health)
	}
}
