//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-cz/rerun"
)

type eventLog struct {
	mu  sync.Mutex
	evs []event
}

func (l *eventLog) emit(ev event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append(l.evs, ev)
}

func (l *eventLog) kind(k eventKind) []event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []event
	for _, ev := range l.evs {
		if ev.kind == k {
			out = append(out, ev)
		}
	}
	return out
}

func buildRunner(t *testing.T, build, run string) (*targetRunner, *eventLog, context.Context) {
	t.Helper()
	log := &eventLog{}
	r := newTargetRunner(Target{Name: "svc", Cmd: run, Build: build, Grace: 200 * time.Millisecond, Selected: true}, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		r.cancelBuild()
		r.killCurrent(context.Background())
		r.probes.Wait()
	})
	return r, log, ctx
}

func nextBuild(t *testing.T, r *targetRunner) buildResult {
	t.Helper()
	select {
	case res := <-r.buildResult():
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("build did not finish")
		return buildResult{}
	}
}

func changeOf(files ...string) rerun.ChangeSet {
	set := map[string]struct{}{}
	for _, f := range files {
		set[f] = struct{}{}
	}
	return rerun.ChangeSet{Files: set, FirstFile: files[0]}
}

// gatedBuild blocks until the returned release file exists.
func gatedBuild(t *testing.T) (script string, release func()) {
	gate := filepath.Join(t.TempDir(), "go")
	return "while [ ! -f " + gate + " ]; do sleep 0.02; done; echo built", func() {
		if err := os.WriteFile(gate, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildSwapsOnlyAfterBuildFinishes(t *testing.T) {
	script, release := gatedBuild(t)
	r, log, ctx := buildRunner(t, "true", "sleep 30")
	r.startBuild(ctx, nil)
	r.finishBuild(ctx, nextBuild(t, r))
	old := r.currentCmd()
	if old == nil || old.Exited() {
		t.Fatal("first start did not run after its build")
	}
	if got := log.kind(evStarted); len(got) != 1 || got[0].restart || got[0].buildDur <= 0 {
		t.Fatalf("first evStarted = %+v", got)
	}

	r.target.Build = script
	r.handleChange(ctx, changeOf("a.go"))
	time.Sleep(100 * time.Millisecond)
	if old.Exited() {
		t.Fatal("old run stopped before the build finished")
	}
	release()
	r.finishBuild(ctx, nextBuild(t, r))

	if !old.Exited() {
		t.Error("old run still up after the swap")
	}
	if cur := r.currentCmd(); cur == old || cur.Exited() {
		t.Fatal("new run not started")
	}
	started := log.kind(evStarted)
	if len(started) != 2 {
		t.Fatalf("evStarted count = %d", len(started))
	}
	if s := started[1]; !s.restart || s.prevUp <= 0 || s.buildDur <= 0 || !reflect.DeepEqual(s.files, []string{"a.go"}) {
		t.Errorf("swap evStarted = %+v", s)
	}
	if ex := log.kind(evExited); len(ex) != 1 || !ex[0].restart {
		t.Errorf("evExited = %+v", ex)
	}
	if len(log.kind(evBuildOK)) != 2 {
		t.Errorf("evBuildOK count = %d", len(log.kind(evBuildOK)))
	}
}

func TestBuildFailureKeepsOldRun(t *testing.T) {
	r, log, ctx := buildRunner(t, "true", "sleep 30")
	r.startBuild(ctx, nil)
	r.finishBuild(ctx, nextBuild(t, r))
	old := r.currentCmd()

	r.target.Build = "echo oops; echo bad >&2; exit 3"
	r.handleChange(ctx, changeOf("a.go"))
	r.finishBuild(ctx, nextBuild(t, r))

	if r.currentCmd() != old || old.Exited() {
		t.Fatal("failed build touched the old run")
	}
	failed := log.kind(evBuildFailed)
	if len(failed) != 1 {
		t.Fatalf("evBuildFailed count = %d", len(failed))
	}
	f := failed[0]
	if f.err == nil || !strings.Contains(f.err.Error(), "3") || !reflect.DeepEqual(f.lines, []string{"oops", "bad"}) || !reflect.DeepEqual(f.files, []string{"a.go"}) {
		t.Errorf("evBuildFailed = %+v", f)
	}
	if len(log.kind(evStarted)) != 1 {
		t.Error("a run started after a failed build")
	}
}

func TestBuildFailureKeepsOnlyTheLastLines(t *testing.T) {
	r, log, ctx := buildRunner(t, "i=1; while [ $i -le 250 ]; do echo line$i; i=$((i+1)); done; exit 1", "sleep 30")
	r.startBuild(ctx, nil)
	r.finishBuild(ctx, nextBuild(t, r))
	f := log.kind(evBuildFailed)[0]
	if len(f.lines) != buildTailLines || f.lines[0] != "line51" || f.lines[len(f.lines)-1] != "line250" {
		t.Errorf("tail = %d lines, first %q", len(f.lines), f.lines[0])
	}
}

func TestFirstStartBuildFailureStartsNothing(t *testing.T) {
	r, log, ctx := buildRunner(t, "exit 1", "sleep 30")
	r.startBuild(ctx, nil)
	r.finishBuild(ctx, nextBuild(t, r))
	if r.currentCmd() != nil {
		t.Fatal("run started despite a failed first build")
	}
	if len(log.kind(evBuildFailed)) != 1 || len(log.kind(evStarted)) != 0 {
		t.Error("expected one evBuildFailed and no evStarted")
	}

	r.target.Build = "true"
	r.handleChange(ctx, changeOf("a.go"))
	r.finishBuild(ctx, nextBuild(t, r))
	if r.currentCmd() == nil {
		t.Error("next change did not start the target")
	}
}

func TestChangeDuringBuildCancelsAndUnionsFiles(t *testing.T) {
	script, release := gatedBuild(t)
	r, log, ctx := buildRunner(t, script, "sleep 30")
	r.handleChange(ctx, changeOf("a.go"))
	first := r.build.cmd
	r.handleChange(ctx, changeOf("b.go"))

	if !first.Exited() {
		t.Error("first build still running after a new batch")
	}
	started := log.kind(evBuildStarted)
	if len(started) != 2 || !reflect.DeepEqual(started[1].files, []string{"a.go", "b.go"}) {
		t.Fatalf("evBuildStarted = %+v", started)
	}
	release()
	r.finishBuild(ctx, nextBuild(t, r))
	if got := log.kind(evStarted); len(got) != 1 || !reflect.DeepEqual(got[0].files, []string{"a.go", "b.go"}) {
		t.Errorf("evStarted = %+v", got)
	}
	if len(log.kind(evBuildFailed)) != 0 {
		t.Error("a cancelled build must not report a failure")
	}
}

func TestStopCancelsTheBuild(t *testing.T) {
	script, _ := gatedBuild(t)
	r, _, ctx := buildRunner(t, script, "sleep 30")
	r.handleChange(ctx, changeOf("a.go"))
	job := r.build
	r.handleControl(ctx, ctrlMsg{kind: ctrlStop})
	if r.build != nil || !job.cmd.Exited() {
		t.Error("stop left the build running")
	}
}

func TestNoBuildKeepsPlainRestart(t *testing.T) {
	r, log, ctx := buildRunner(t, "", "sleep 30")
	if err := r.start(ctx); err != nil {
		t.Fatal(err)
	}
	old := r.currentCmd()
	r.handleChange(ctx, changeOf("a.go"))
	if !old.Exited() || r.currentCmd() == old {
		t.Fatal("no-build change did not restart")
	}
	if len(log.kind(evBuildStarted)) != 0 {
		t.Error("build events without a build")
	}
	started := log.kind(evStarted)
	if s := started[1]; !s.restart || s.buildDur != 0 || !reflect.DeepEqual(s.files, []string{"a.go"}) {
		t.Errorf("evStarted = %+v", s)
	}
}

func TestPlainSinkRendersBuildAndHealthEvents(t *testing.T) {
	s := newPlainSink(nil)
	cases := []struct {
		ev   event
		want string
	}{
		{event{kind: evBuildStarted, target: "api", files: []string{"a.go", "b.go"}}, "[api] building (2 files)\n"},
		{event{kind: evBuildOK, target: "api", dur: 1500 * time.Millisecond}, "[api] build ok (1.5s)\n"},
		{event{kind: evBuildFailed, target: "api", dur: time.Second, err: os.ErrInvalid, lines: []string{"boom"}}, "[api] build failed after 1s: invalid argument\n[api] build: boom\n"},
		{event{kind: evHealth, target: "api", health: healthUnhealthy, dur: 2 * time.Second}, "[api] health: unhealthy (2s after start)\n"},
		{event{kind: evExited, target: "api", restart: true}, "[api] stopped for restart\n"},
	}
	for _, c := range cases {
		if got := s.render(c.ev); got != c.want {
			t.Errorf("render = %q, want %q", got, c.want)
		}
	}
}

func TestRunnerHealthStopsWithTheRun(t *testing.T) {
	fastHealth(t)
	r, log, ctx := buildRunner(t, "", "sleep 30")
	r.target.Health = "exit 0"
	if err := r.start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(log.kind(evHealth)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h := log.kind(evHealth); len(h) != 1 || h[0].health != healthHealthy {
		t.Fatalf("evHealth = %+v", h)
	}
	r.killCurrent(ctx)
	done := make(chan struct{})
	go func() { r.probes.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("health goroutine outlived the run")
	}
}

func TestCancelEmitsOnlyWhenNoBuildFollows(t *testing.T) {
	script, _ := gatedBuild(t)
	r, log, ctx := buildRunner(t, script, "sleep 30")
	r.handleChange(ctx, changeOf("a.go"))
	r.handleChange(ctx, changeOf("b.go"))
	if n := len(log.kind(evBuildCancelled)); n != 0 {
		t.Fatalf("rebuild emitted %d evBuildCancelled", n)
	}
	r.handleControl(ctx, ctrlMsg{kind: ctrlStop})
	if n := len(log.kind(evBuildCancelled)); n != 1 {
		t.Fatalf("stop emitted %d evBuildCancelled, want 1", n)
	}
	r.cancelBuild()
	if n := len(log.kind(evBuildCancelled)); n != 1 {
		t.Errorf("idle cancel emitted an event")
	}
}

func TestCancelBuildDoesNotHangOnAnEscapedChild(t *testing.T) {
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("perl not available")
	}
	old := buildCancelWait
	buildCancelWait = 200 * time.Millisecond
	t.Cleanup(func() { buildCancelWait = old })

	// The setsid child leaves the process group yet keeps the build's stdout open.
	script := perl + ` -e 'use POSIX; setsid(); exec "sleep","4"' & sleep 30`
	r, _, ctx := buildRunner(t, script, "sleep 30")
	r.startBuild(ctx, nil)
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() { r.cancelBuild(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelBuild blocked on a pipe held by an escaped child")
	}
}

func TestAbortedSpawnDoesNotLeakMetaIntoTheNextStart(t *testing.T) {
	r, log, ctx := buildRunner(t, "", "sleep 30")
	if err := r.start(ctx); err != nil {
		t.Fatal(err)
	}
	r.target.Cooldown = time.Hour
	dead, cancel := context.WithCancel(ctx)
	cancel()
	r.handleChange(dead, changeOf("a.go"))
	if len(r.meta.files) != 0 || r.meta.restart || r.meta.prevUp != 0 {
		t.Fatalf("meta survived an aborted restart: %+v", r.meta)
	}

	r.target.Cooldown = 0
	r.killCurrent(ctx)
	if err := r.start(ctx); err != nil {
		t.Fatal(err)
	}
	started := log.kind(evStarted)
	if last := started[len(started)-1]; last.restart || len(last.files) != 0 || last.prevUp != 0 {
		t.Errorf("stale meta on next start: %+v", last)
	}
}

func TestTailBufferCapsAnUnterminatedLine(t *testing.T) {
	b := &tailBuffer{n: 5}
	chunk := strings.Repeat("x", 10*1024)
	for i := 0; i < 20; i++ {
		_, _ = b.Write([]byte(chunk))
	}
	for _, l := range b.tail() {
		if len(l) > tailPartialCap {
			t.Fatalf("line of %d bytes exceeds the cap", len(l))
		}
	}
	if len(b.partial) > tailPartialCap {
		t.Errorf("partial = %d bytes", len(b.partial))
	}
}

func TestHealthEventsCarryTheRunStartAndFollowEvStarted(t *testing.T) {
	fastHealth(t)
	r, log, ctx := buildRunner(t, "", "sleep 30")
	r.target.Health = "exit 0"
	if err := r.start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(log.kind(evHealth)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.evs[0].kind != evStarted {
		t.Fatalf("first event = %v, want evStarted", log.evs[0].kind)
	}
	for _, ev := range log.evs {
		if ev.kind == evHealth && !ev.runAt.Equal(log.evs[0].at) {
			t.Errorf("runAt %v != evStarted.at %v", ev.runAt, log.evs[0].at)
		}
	}
}
