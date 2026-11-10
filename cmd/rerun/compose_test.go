package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeDockerScript replays canned docker output. State lives in FAKE_DOCKER_DIR:
//   - events.N.txt is the Nth events stream; a stream with no file blocks until killed.
//   - ps.N.json replaces ps.json once N events streams have ended.
//   - logs.<id>.K.txt is the Kth log attach for a container. A file that exists
//     makes the attach exit after printing it, so a test can end and re-attach it.
//     logs.<id>.txt is printed by any attach with no K file, which then blocks.
//   - restart.fail makes `restart` exit non-zero.
//   - pids collects the PID of every events and logs child, for the leak check.
const fakeDockerScript = `#!/bin/sh
D="$FAKE_DOCKER_DIR"
echo "$*" >> "$D/calls.log"
sub=""
for a in "$@"; do
  case "$a" in
    config|ps|up|stop|restart|events|logs|inspect) sub="$a"; break;;
  esac
done
for last; do :; done
case "$sub" in
  config)
    case "$*" in
      *"--profile debug"*) cat "$D/config.debug.json";;
      *) cat "$D/config.json";;
    esac;;
  ps)
    n=$(cat "$D/events.count" 2>/dev/null || echo 0)
    if [ -f "$D/ps.$n.json" ]; then cat "$D/ps.$n.json"; else cat "$D/ps.json"; fi;;
  up|stop)
    echo "$sub $last" >> "$D/actions.log";;
  restart)
    echo "$sub $last" >> "$D/actions.log"
    if [ -f "$D/restart.fail" ]; then exit 1; fi;;
  events)
    echo $$ >> "$D/pids"
    n=$(cat "$D/events.count" 2>/dev/null || echo 0)
    n=$((n+1))
    echo $n > "$D/events.count"
    if [ -f "$D/events.$n.await" ]; then
      while [ ! -f "$D/$(cat "$D/events.$n.await")" ]; do sleep 0.01; done
      sleep 0.2
    fi
    if [ -f "$D/events.$n.txt" ]; then cat "$D/events.$n.txt"; exit 0; fi
    exec sleep 1000;;
  logs)
    echo $$ >> "$D/pids"
    k=$(cat "$D/logs.$last.count" 2>/dev/null || echo 0)
    k=$((k+1))
    echo $k > "$D/logs.$last.count"
    if [ -f "$D/logs.$last.$k.txt" ]; then cat "$D/logs.$last.$k.txt"; touch "$D/logs.$last.$k.done"; exit 0; fi
    if [ -f "$D/logs.$last.txt" ]; then cat "$D/logs.$last.txt"; fi
    exec sleep 1000;;
  inspect)
    if [ -f "$D/inspect.$last.txt" ]; then cat "$D/inspect.$last.txt"; else exit 1; fi;;
  *) exit 2;;
esac
`

// fakeDocker puts a fake `docker` first on PATH and returns its state directory.
func fakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_DIR", dir)
	return dir
}

func writeFake(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFake(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// fastRetries shrinks the reconnect pacing so the tests run in milliseconds.
func fastRetries(t *testing.T) {
	t.Helper()
	oldMin, oldMax, oldStable := composeRetryMin, composeRetryMax, composeStableRun
	composeRetryMin, composeRetryMax, composeStableRun = time.Millisecond, 5*time.Millisecond, 0
	t.Cleanup(func() { composeRetryMin, composeRetryMax, composeStableRun = oldMin, oldMax, oldStable })
}

// eventTick is the event clock: each dockerEventLine advances it one second.
var eventTick int64

// dockerEventLine is one `docker events` line for container id of the test service.
func dockerEventLine(action, svc, id string, attrs map[string]string) string {
	eventTick++
	attributes := map[string]string{"com.docker.compose.service": svc, "name": "devbox-" + svc + "-1"}
	for k, v := range attrs {
		attributes[k] = v
	}
	b, _ := json.Marshal(map[string]any{
		"status": action,
		"Action": action,
		"Actor":  map[string]any{"ID": id, "Attributes": attributes},
		"time":   1793000000 + eventTick,
		// timeNano keeps the event time exact, so runAt compares equal across events.
		"timeNano": (1793000000 + eventTick) * 1_000_000_000,
	})
	return string(b) + "\n"
}

// psLine is one `docker compose ps --format json` entry.
func psLine(id, state string, exitCode int, health string) string {
	return fmt.Sprintf(`{"ID":%q,"Name":"devbox-svc-1","State":%q,"ExitCode":%d,"Health":%q}`, id, state, exitCode, health)
}

func composeFixture(t *testing.T, service string) (string, Target) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	writeFake(t, dir, "docker-compose.yml", "services: {}\n")
	target := Target{Name: service, Kind: composeKind, Selected: true, Start: startManual, Dir: dir, File: file,
		Compose: &composeTarget{File: file, Project: "devbox", Service: service}}
	return dir, target
}

// waitEvents polls the sink until the events satisfy want, or fails the test.
func waitEvents(t *testing.T, sink *recordingSink, want func([]event) bool) []event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := sink.all(); want(got) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for events; got %s", describeEvents(sink.all()))
	return nil
}

func describeEvents(evs []event) string {
	var parts []string
	for _, ev := range evs {
		parts = append(parts, fmt.Sprintf("%d(%s restart=%v code=%d health=%s line=%q)", ev.kind, ev.status, ev.restart, ev.code, ev.health, ev.line))
	}
	return strings.Join(parts, " ")
}

// lifecycle returns the events that change a container's state, without rerun's own notices.
func lifecycle(evs []event) []event {
	var out []event
	for _, ev := range evs {
		switch ev.kind {
		case evStarted, evExited, evStopped, evHealth:
			out = append(out, ev)
		}
	}
	return out
}

// serveEvents runs the backend's Serve loop until the test ends.
func serveEvents(t *testing.T, b *composeBackend, sink *recordingSink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Serve(ctx, sink.emit)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// wantKinds fails unless the lifecycle events have exactly these kinds, in order.
func wantKinds(t *testing.T, got []event, want ...eventKind) {
	t.Helper()
	var kinds []eventKind
	for _, ev := range got {
		kinds = append(kinds, ev.kind)
	}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("kinds = %v, want %v; events %s", kinds, want, describeEvents(got))
	}
}

// waitLifecycle waits until the lifecycle events reach n.
func waitLifecycle(t *testing.T, sink *recordingSink, n int) []event {
	t.Helper()
	return lifecycle(waitEvents(t, sink, func(evs []event) bool { return len(lifecycle(evs)) >= n }))
}

func TestComposeCrashStopOOM(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	writeFake(t, dir, "events.1.txt",
		dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "1"})+
			dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("kill", "postgres", "cid-a", map[string]string{"signal": "15"})+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "0"})+
			dockerEventLine("stop", "postgres", "cid-a", nil)+
			dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("oom", "postgres", "cid-a", nil)+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "137"}))

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 6)
	wantKinds(t, got[:6], evStarted, evExited, evStarted, evStopped, evStarted, evExited)
	if got[1].code != 1 || got[1].status != "exited 1" {
		t.Errorf("crash = %+v, want exited 1", got[1])
	}
	if !got[2].restart {
		t.Errorf("start after a crash must read as a restart: %+v", got[2])
	}
	if got[4].restart {
		t.Errorf("start after a stop must not read as a restart: %+v", got[4])
	}
	if got[5].status != "oom" || got[5].code != 137 {
		t.Errorf("oom = %+v, want status oom code 137", got[5])
	}
}

func TestComposeStopOrderThenCrash(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	// The real `docker stop` order is kill 15, die, stop. A later crash is still a crash.
	writeFake(t, dir, "events.1.txt",
		dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("kill", "postgres", "cid-a", map[string]string{"signal": "15"})+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "0"})+
			dockerEventLine("stop", "postgres", "cid-a", nil)+
			dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "1"}))

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 4)
	wantKinds(t, got[:4], evStarted, evStopped, evStarted, evExited)
	if got[3].code != 1 {
		t.Errorf("crash after a stop = %+v, want exited 1", got[3])
	}
}

func TestComposeDockerKillIsACrash(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	// `docker kill` sends signal 9 and no stop, so it reads as a crash.
	writeFake(t, dir, "events.1.txt",
		dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("kill", "postgres", "cid-a", map[string]string{"signal": "9"})+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "137"}))

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 2)
	wantKinds(t, got[:2], evStarted, evExited)
	if got[1].status != "exited 137" {
		t.Errorf("docker kill = %+v, want exited 137", got[1])
	}
}

func TestComposeHUPSetsNoIntent(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	// A HUP is not a stop: the die that follows is a crash.
	writeFake(t, dir, "events.1.txt",
		dockerEventLine("start", "postgres", "cid-a", nil)+
			dockerEventLine("kill", "postgres", "cid-a", map[string]string{"signal": "1"})+
			dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "1"}))

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 2)
	wantKinds(t, got[:2], evStarted, evExited)
}

func TestComposeRestartOrder(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	// docker restart: kill, die, stop, start, restart.
	writeFake(t, dir, "events.1.txt",
		dockerEventLine("start", "api", "cid-a", nil)+
			dockerEventLine("kill", "api", "cid-a", map[string]string{"signal": "15"})+
			dockerEventLine("die", "api", "cid-a", map[string]string{"exitCode": "0"})+
			dockerEventLine("stop", "api", "cid-a", nil)+
			dockerEventLine("start", "api", "cid-a", nil)+
			dockerEventLine("restart", "api", "cid-a", nil))

	_, target := composeFixture(t, "api")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 3)
	wantKinds(t, got[:3], evStarted, evStopped, evStarted)
}

func TestComposeHealthFollowsTheRun(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	writeFake(t, dir, "events.1.txt",
		dockerEventLine("start", "api", "cid-a", nil)+
			dockerEventLine("health_status: healthy", "api", "cid-a", nil)+
			dockerEventLine("health_status: starting", "api", "cid-a", nil)+
			dockerEventLine("health_status: unhealthy", "api", "cid-a", nil))

	_, target := composeFixture(t, "api")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 3)
	wantKinds(t, got[:3], evStarted, evHealth, evHealth)
	if got[1].health != healthHealthy || !got[1].runAt.Equal(got[0].at) {
		t.Errorf("healthy = %+v, want evHealth for the run started at %v", got[1], got[0].at)
	}
	if got[2].health != healthUnhealthy {
		t.Errorf("unhealthy = %+v; starting is not shown", got[2])
	}
}

func TestComposeCrashLoopShowsRestarts(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	var stream string
	for i := 0; i < 3; i++ {
		stream += dockerEventLine("start", "worker", "cid-a", nil)
		stream += dockerEventLine("die", "worker", "cid-a", map[string]string{"exitCode": "2"})
	}
	writeFake(t, dir, "events.1.txt", stream)

	_, target := composeFixture(t, "worker")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 6)
	for i := 0; i < 6; i += 2 {
		if got[i].kind != evStarted || got[i+1].kind != evExited {
			t.Fatalf("loop step %d = %s, want started then exited", i/2, describeEvents(got))
		}
		if wantRestart := i > 0; got[i].restart != wantRestart {
			t.Errorf("loop step %d restart = %v, want %v", i/2, got[i].restart, wantRestart)
		}
	}
}

func TestComposeReconnectReconciles(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "[]")
	// The stream ends after one start. While rerun was disconnected the container died.
	writeFake(t, dir, "events.1.txt", dockerEventLine("start", "db", "cid-db", nil))
	writeFake(t, dir, "ps.1.json", "["+psLine("cid-db", "exited", 1, "")+"]")

	_, target := composeFixture(t, "db")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitEvents(t, sink, func(evs []event) bool { return len(lifecycle(evs)) >= 2 })
	var unreachable, reconnected bool
	for _, ev := range got {
		if ev.kind == evNotice && strings.Contains(ev.line, "docker unreachable (") {
			unreachable = true
		}
		if ev.kind == evNotice && strings.HasSuffix(ev.line, "docker reconnected") {
			reconnected = true
		}
	}
	wantKinds(t, lifecycle(got)[:2], evStarted, evExited)
	if lifecycle(got)[1].code != 1 {
		t.Errorf("reconciled exit = %+v, want code 1", lifecycle(got)[1])
	}
	if !unreachable || !reconnected {
		t.Errorf("events = %s, want the unreachable notice then the reconnected notice", describeEvents(got))
	}
}

func TestComposeRecreatedContainerIsANewRun(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "["+psLine("cid-old", "running", 0, "")+"]")
	writeFake(t, dir, "inspect.cid-old.txt", "2026-11-06T09:00:00Z\n")
	// The old container is gone after the reconnect, replaced by a new ID.
	writeFake(t, dir, "events.1.txt", "")
	writeFake(t, dir, "ps.1.json", "["+psLine("cid-new", "running", 0, "")+"]")
	writeFake(t, dir, "inspect.cid-new.txt", "2026-11-06T09:05:00Z\n")

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 3)
	wantKinds(t, got[:3], evStarted, evExited, evStarted)
	if !got[2].at.Equal(time.Date(2026, 11, 6, 9, 5, 0, 0, time.UTC)) {
		t.Errorf("new run started at %v, want the new container's StartedAt", got[2].at)
	}
}

func TestComposeEventInReconcileGapIsApplied(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "["+psLine("cid-a", "running", 0, "")+"]")
	writeFake(t, dir, "inspect.cid-a.txt", "2026-11-06T09:00:00Z\n")
	// The die happened after the reconcile snapshot. The events stream must still carry it.
	writeFake(t, dir, "events.1.txt", dockerEventLine("die", "postgres", "cid-a", map[string]string{"exitCode": "3"}))

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	got := waitLifecycle(t, sink, 2)
	wantKinds(t, got[:2], evStarted, evExited)
	if got[1].code != 3 {
		t.Errorf("gap die = %+v, want exited 3", got[1])
	}
	if calls := readFake(t, dir, "calls.log"); !strings.Contains(calls, "events --format {{json .}} --since ") {
		t.Errorf("events stream must start from the reconcile time, calls: %s", calls)
	}
}

func TestComposeLogsReattachWithoutRepeats(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "["+psLine("cid-a", "running", 0, "")+"]")
	writeFake(t, dir, "inspect.cid-a.txt", "2026-11-06T09:00:00Z\n")
	// The first attach ends after two lines. The re-attach repeats the boundary line, then adds one.
	writeFake(t, dir, "logs.cid-a.1.txt", "2026-11-06T09:00:01Z a\n2026-11-06T09:00:02Z b\n")
	writeFake(t, dir, "logs.cid-a.2.txt", "2026-11-06T09:00:02Z b\n2026-11-06T09:00:03Z c\n")
	writeFake(t, dir, "events.1.txt", "")
	// The events stream waits for the first attach to finish, so the reconnect sees its tail done.
	writeFake(t, dir, "events.1.await", "logs.cid-a.1.done")

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	serveEvents(t, newComposeBackend(target, sink), sink)

	waitEvents(t, sink, func(evs []event) bool {
		n := 0
		for _, ev := range evs {
			if ev.kind == evOutput {
				n++
			}
		}
		return n >= 3
	})
	time.Sleep(20 * time.Millisecond) // give a repeated line the chance to show up
	var lines []string
	for _, ev := range sink.all() {
		if ev.kind == evOutput {
			lines = append(lines, ev.line)
		}
	}
	if fmt.Sprint(lines) != "[a b c]" {
		t.Errorf("log lines = %v, want [a b c] with the boundary line once", lines)
	}
	if calls := readFake(t, dir, "calls.log"); !strings.Contains(calls, "--since 2026-11-06T09:00:02Z") {
		t.Errorf("re-attach must resume from the last line, calls: %s", calls)
	}
}

// A log line past the scanner's cap ends the read, so the attach must report it
// instead of going quiet mid-stream.
func TestComposeLogLinePastTheCapIsReported(t *testing.T) {
	dir := fakeDocker(t)
	writeFake(t, dir, "logs.cid-a.1.txt", strings.Repeat("x", 2*1024*1024))

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	b := newComposeBackend(target, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.tailLogs(ctx, "cid-a", []string{"logs", "-f", "--timestamps", "cid-a"}, sink.emit)
	}()

	got := waitEvents(t, sink, func(evs []event) bool { return len(evs) > 0 })
	if ev := got[0]; ev.kind != evOutput || ev.target != "postgres" || !strings.Contains(ev.line, "token too long") {
		t.Fatalf("event = %+v, want a postgres output line naming the scanner error", ev)
	}
	// The blocked docker write keeps Wait from returning on its own.
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("tailLogs did not return after the context was cancelled")
	}
}

func TestComposeAlreadyRunningAtStartup(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "["+psLine("cid-pg", "running", 0, "healthy")+"]")
	writeFake(t, dir, "inspect.cid-pg.txt", "2026-11-06T09:00:00.5Z\n")
	writeFake(t, dir, "logs.cid-pg.txt", "2026-11-06T09:00:01.000000001Z ready to accept connections\n")

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	b := newComposeBackend(target, sink)
	serveEvents(t, b, sink)

	got := waitEvents(t, sink, func(evs []event) bool {
		for _, ev := range evs {
			if ev.kind == evOutput {
				return true
			}
		}
		return false
	})
	if got[0].kind != evStarted {
		t.Fatalf("first event = %s, want the running container shown as started", describeEvents(got))
	}
	want := time.Date(2026, 11, 6, 9, 0, 0, 500_000_000, time.UTC)
	if !got[0].at.Equal(want) {
		t.Errorf("started at %v, want the container's StartedAt %v", got[0].at, want)
	}
	if !b.Running() {
		t.Error("Running() = false for a container that is up")
	}
	var line string
	for _, ev := range got {
		if ev.kind == evOutput {
			line = ev.line
		}
	}
	if line != "ready to accept connections" {
		t.Errorf("log line = %q, want the timestamp stripped", line)
	}
	if calls := readFake(t, dir, "calls.log"); !strings.Contains(calls, "--tail") {
		t.Errorf("first log attach should tail, calls: %s", calls)
	}
}

func TestComposeServiceActionsIssueTheRightCommands(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	r := newTargetRunner(target, sink)

	r.handleControl(context.Background(), ctrlMsg{kind: ctrlStart})
	r.handleControl(context.Background(), ctrlMsg{kind: ctrlStop})
	r.handleControl(context.Background(), ctrlMsg{kind: ctrlReboot})

	if got, want := readFake(t, dir, "actions.log"), "up postgres\nstop postgres\nrestart postgres\n"; got != want {
		t.Fatalf("actions = %q, want %q", got, want)
	}
	if calls := readFake(t, dir, "calls.log"); !strings.Contains(calls, "-f "+target.File+" --profile * up -d postgres") {
		t.Errorf("up was not run against the compose file with every profile: %s", calls)
	}
}

func TestComposeRestartFallsBackToUpWhenNoContainer(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "restart.fail", "")
	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	r := newTargetRunner(target, sink)

	r.handleControl(context.Background(), ctrlMsg{kind: ctrlReboot})

	if got, want := readFake(t, dir, "actions.log"), "restart postgres\nup postgres\n"; got != want {
		t.Fatalf("actions = %q, want restart then up", got)
	}
}

func TestComposeShutdownLeavesContainersRunning(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	_, target := composeFixture(t, "postgres")
	r := newTargetRunner(target, &recordingSink{})

	r.be.Release(context.Background())
	r.stop()
	r.force()

	if got := readFake(t, dir, "actions.log"); got != "" {
		t.Fatalf("shutdown ran %q, want no stop of the containers", got)
	}
}

func TestComposeServeLeavesNoChildren(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "ps.json", "["+psLine("cid-a", "running", 0, "")+"]")
	writeFake(t, dir, "inspect.cid-a.txt", "2026-11-06T09:00:00Z\n")

	_, target := composeFixture(t, "postgres")
	sink := &recordingSink{}
	b := newComposeBackend(target, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Serve(ctx, sink.emit)
	}()
	waitLifecycle(t, sink, 1)
	waitPIDs(t, dir, 2) // the events stream and the log tail

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context ended")
	}
	for _, pid := range readPIDs(t, dir) {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("child pid %d is still alive after Serve returned (err %v)", pid, err)
		}
	}
}

func readPIDs(t *testing.T, dir string) []int {
	t.Helper()
	var pids []int
	for _, f := range strings.Fields(readFake(t, dir, "pids")) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func waitPIDs(t *testing.T, dir string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(readPIDs(t, dir)) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fewer than %d child processes started", n)
}

func TestResolveComposeFiltersServicesAndProfiles(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"postgres":{},"redis":{}}}`)
	writeFake(t, dir, "config.debug.json", `{"name":"devbox","services":{"postgres":{},"redis":{},"adminer":{}}}`)
	file := filepath.Join(dir, "docker-compose.yml")

	project, names, _, err := resolveCompose(file, nil, []string{"redis"})
	if err != nil {
		t.Fatal(err)
	}
	if project != "devbox" || fmt.Sprint(names) != "[redis]" {
		t.Fatalf("resolve = %q %v, want devbox [redis]", project, names)
	}

	_, names, _, err = resolveCompose(file, []string{"debug"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(names) != "[adminer postgres redis]" {
		t.Fatalf("debug profile = %v, want all three services", names)
	}

	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"postgres":{},"redis":{}}}`)
	if _, names, _, err = resolveCompose(file, nil, nil); err != nil || fmt.Sprint(names) != "[postgres redis]" {
		t.Fatalf("no profile = %v, %v, want the services compose lists", names, err)
	}
	if calls := readFake(t, dir, "calls.log"); !strings.Contains(calls, "--profile *") {
		t.Errorf("no profiles should name every profile with `*`, calls: %s", calls)
	}
	if calls := readFake(t, dir, "calls.log"); !strings.Contains(calls, "--profile debug") {
		t.Errorf("profile was not passed to compose: %s", calls)
	}

	if _, _, _, err := resolveCompose(file, nil, []string{"nope"}); err == nil || !strings.Contains(err.Error(), `unknown service "nope"`) {
		t.Errorf("unknown service err = %v, want it named", err)
	}
}

func TestComposeImportBuildsTargets(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"postgres":{},"redis":{}}}`)
	composeYML := "services:\n  postgres:\n    image: postgres\n  redis:\n    image: redis\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\n    services: [postgres]\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetNames(cfg); fmt.Sprint(got) != "[devbox/postgres]" {
		t.Fatalf("targets = %v, want only devbox/postgres after the services filter", got)
	}
	tgt := cfg.Targets[0]
	if tgt.Kind != composeKind || tgt.Compose == nil || tgt.Compose.Service != "postgres" || tgt.Compose.Project != "devbox" {
		t.Errorf("target = %+v, want a compose target for devbox's postgres", tgt)
	}
	if tgt.Import != "devbox" || tgt.Start != startManual || tgt.Dir != filepath.Dir(tgt.File) {
		t.Errorf("target import/start/dir = %q %v %q, want devbox, manual, the file's dir", tgt.Import, tgt.Start, tgt.Dir)
	}
}

func TestComposeImportRejectsMisuse(t *testing.T) {
	fastRetries(t)
	fakeDocker(t)
	composeYML := "services:\n  postgres:\n    image: postgres\n"
	for _, tt := range []struct {
		name, root, want string
	}{
		{"groups on a compose import", "imports:\n  - path: ../devbox/docker-compose.yml\n    groups: [x]\n", "groups applies to rerun files"},
		{"services on a rerun import", "imports:\n  - path: ../bpn/rerun.yml\n    services: [api]\n", "apply to compose files"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newImportFixture(t, tt.root, map[string]string{
				"devbox/docker-compose.yml": composeYML,
				"bpn/rerun.yml":             "targets:\n  api:\n    run: echo\n",
			})
			_, err := ParseConfig(f.root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestComposeImportWithoutDocker(t *testing.T) {
	composeYML := "services:\n  postgres:\n    image: postgres\n"
	t.Setenv("PATH", t.TempDir()) // no docker on PATH

	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\n", map[string]string{"devbox/docker-compose.yml": composeYML})
	if _, err := ParseConfig(f.root); err == nil || !strings.Contains(err.Error(), "docker not found on PATH") {
		t.Fatalf("required import err = %v, want docker not found", err)
	}

	f = newImportFixture(t, "targets:\n  api:\n    run: echo\n\nimports:\n  - path: ../devbox/docker-compose.yml\n    optional: true\n", map[string]string{"devbox/docker-compose.yml": composeYML})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("optional import err = %v, want a skip", err)
	}
	if got := targetNames(cfg); fmt.Sprint(got) != "[api]" || len(cfg.Notices) == 0 {
		t.Fatalf("optional import: targets %v notices %v, want a skip with a notice", got, cfg.Notices)
	}
}

func TestComposeImportWithoutProfilesKeepsGatedServices(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"postgres":{},"adminer":{"profiles":["debug"]}}}`)
	composeYML := "services:\n  postgres:\n    image: postgres\n  adminer:\n    image: adminer\n    profiles: [debug]\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetNames(cfg); fmt.Sprint(got) != "[devbox/adminer devbox/postgres]" {
		t.Fatalf("targets = %v, want both services without a profiles filter", got)
	}
	if got := cfg.Notices; len(got) != 0 {
		t.Errorf("notices = %v, want none for a non-empty import", got)
	}
}

func TestComposeImportWithNoServicesSaysSo(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{}}`)
	composeYML := "services: {}\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\ntargets:\n  api:\n    run: echo hi\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})

	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetNames(cfg); fmt.Sprint(got) != "[api]" {
		t.Fatalf("targets = %v, want none", targetNames(cfg))
	}
	found := false
	for _, n := range cfg.Notices {
		if strings.Contains(n, "no services") && strings.Contains(n, "devbox/docker-compose.yml") {
			found = true
		}
	}
	if !found {
		t.Errorf("notices = %v, want one naming the empty compose file", cfg.Notices)
	}
}
