//go:build !windows

package main

import (
	"context"
	"errors"
	"github.com/golang-cz/rerun"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// countedBuild appends a line to counter on every run, then exits with code once gate exists.
func countedBuild(t *testing.T, code int) (script, counter, gate string) {
	dir := t.TempDir()
	counter, gate = filepath.Join(dir, "runs"), filepath.Join(dir, "gate")
	script = "echo run >> " + counter + "; while [ ! -f " + gate + " ]; do sleep 0.02; done; exit " + itoa(code)
	return script, counter, gate
}

func itoa(n int) string {
	return string(rune('0' + n))
}

func releaseGate(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runCount(counter string) int {
	b, err := os.ReadFile(counter)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func waitForRuns(t *testing.T, counter string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runCount(counter) < want {
		if time.Now().After(deadline) {
			t.Fatalf("build runs = %d, want %d", runCount(counter), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// oldFile writes a file older than any build started in the test, so a trigger on it joins rather than reruns.
func oldFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

// sharedRunner is a runner in dir that shares share's builds with every other runner given the same share.
func sharedRunner(t *testing.T, share *buildShare, name, dir, build string) (*targetRunner, *eventLog, context.Context) {
	t.Helper()
	log := &eventLog{}
	r := newTargetRunner(Target{Name: name, Dir: dir, Cmd: "sleep 30", Build: build, Grace: 200 * time.Millisecond, Selected: true}, log)
	r.builds = share
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		r.cancelBuild()
		r.killCurrent(context.Background())
		r.probes.Wait()
	})
	return r, log, ctx
}

func TestSharedBuildRunsOnceForEveryTriggeredTarget(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)
	b, bLog, _ := sharedRunner(t, share, "b", dir, script)

	src := oldFile(t, dir, "a.go")
	a.handleChange(ctx, changeOf(src))
	b.handleChange(ctx, changeOf(src))
	releaseGate(t, gate)
	ra, rb := nextBuild(t, a), nextBuild(t, b)
	a.finishBuild(ctx, ra)
	b.finishBuild(ctx, rb)

	if n := runCount(counter); n != 1 {
		t.Fatalf("build ran %d times, want 1", n)
	}
	for name, log := range map[string]*eventLog{"a": aLog, "b": bLog} {
		if got := len(log.kind(evBuildOK)); got != 1 {
			t.Errorf("%s: evBuildOK = %d, want 1", name, got)
		}
		if got := len(log.kind(evStarted)); got != 1 {
			t.Errorf("%s: evStarted = %d, want 1 (restart)", name, got)
		}
	}
}

func TestSharedBuildStartupJoinsFreshRun(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)
	b, bLog, _ := sharedRunner(t, share, "b", dir, script)

	a.startBuild(nil)
	waitForRuns(t, counter, 1)
	b.startBuild(nil)
	releaseGate(t, gate)
	a.finishBuild(ctx, nextBuild(t, a))
	b.finishBuild(ctx, nextBuild(t, b))

	if n := runCount(counter); n != 1 {
		t.Fatalf("build ran %d times, want 1", n)
	}
	for name, log := range map[string]*eventLog{"a": aLog, "b": bLog} {
		if got := len(log.kind(evBuildStarted)); got != 1 {
			t.Errorf("%s: evBuildStarted = %d, want 1", name, got)
		}
	}
}

func TestSharedBuildRestartsOnlyTheTriggeredTargets(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)
	_, bLog, _ := sharedRunner(t, share, "b", dir, script)

	a.handleChange(ctx, changeOf(oldFile(t, dir, "a.go")))
	releaseGate(t, gate)
	a.finishBuild(ctx, nextBuild(t, a))

	if n := runCount(counter); n != 1 {
		t.Fatalf("build ran %d times, want 1", n)
	}
	if got := len(aLog.kind(evStarted)); got != 1 {
		t.Errorf("a: evStarted = %d, want 1", got)
	}
	if got := len(bLog.kind(evBuildStarted)) + len(bLog.kind(evStarted)); got != 0 {
		t.Errorf("untriggered b saw %d build or start events, want 0", got)
	}
}

func TestSharedBuildFailureKeepsEveryTriggeredOldRun(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 1)
	releaseGate(t, gate)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)
	b, bLog, _ := sharedRunner(t, share, "b", dir, script)

	src := oldFile(t, dir, "a.go")
	a.handleChange(ctx, changeOf(src))
	b.handleChange(ctx, changeOf(src))
	ra, rb := nextBuild(t, a), nextBuild(t, b)
	a.finishBuild(ctx, ra)
	b.finishBuild(ctx, rb)

	if n := runCount(counter); n != 1 {
		t.Fatalf("build ran %d times, want 1", n)
	}
	for name, log := range map[string]*eventLog{"a": aLog, "b": bLog} {
		if got := len(log.kind(evBuildFailed)); got != 1 {
			t.Errorf("%s: evBuildFailed = %d, want 1", name, got)
		}
		if got := len(log.kind(evStarted)); got != 0 {
			t.Errorf("%s: evStarted = %d, want 0 (old run kept)", name, got)
		}
	}
}

func TestSharedBuildSameCommandInOtherDirRunsTwice(t *testing.T) {
	share := newBuildShare()
	script, counter, gate := countedBuild(t, 0)
	a, _, ctxA := sharedRunner(t, share, "a", t.TempDir(), script)
	b, _, ctxB := sharedRunner(t, share, "b", t.TempDir(), script)

	a.handleChange(ctxA, changeOf(oldFile(t, a.target.Dir, "a.go")))
	b.handleChange(ctxB, changeOf(oldFile(t, b.target.Dir, "b.go")))
	waitForRuns(t, counter, 2)
	releaseGate(t, gate)
	a.finishBuild(ctxA, nextBuild(t, a))
	b.finishBuild(ctxB, nextBuild(t, b))

	if n := runCount(counter); n != 2 {
		t.Fatalf("build ran %d times, want 2", n)
	}
}

func TestSharedBuildLateTriggerReRunsWithEveryFile(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)
	b, bLog, _ := sharedRunner(t, share, "b", dir, script)

	a.handleChange(ctx, changeOf(oldFile(t, dir, "a.go")))
	waitForRuns(t, counter, 1)
	// late.go does not exist yet, so its change cannot predate the running build.
	b.handleChange(ctx, changeOf(filepath.Join(dir, "late.go")))
	waitForRuns(t, counter, 2)
	releaseGate(t, gate)
	a.finishBuild(ctx, nextBuild(t, a))
	b.finishBuild(ctx, nextBuild(t, b))

	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "late.go")}
	for name, log := range map[string]*eventLog{"a": aLog, "b": bLog} {
		started := log.kind(evBuildStarted)
		if len(started) == 0 || !reflect.DeepEqual(started[len(started)-1].files, sortedStrings(want)) {
			t.Errorf("%s: last evBuildStarted = %+v, want files %v", name, started, want)
		}
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// A build killed by its last member must be gone before a trigger starts the next one.
func TestSharedBuildWaitsForKilledProcessBeforeRelaunch(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	pids := filepath.Join(t.TempDir(), "pids")
	// Ignores SIGTERM, so the kill has to wait out the grace period.
	script := "trap '' TERM; echo $$ >> " + pids + "; exec sleep 30"
	a, _, ctxA := sharedRunner(t, share, "a", dir, script)
	b, _, ctxB := sharedRunner(t, share, "b", dir, script)

	a.handleChange(ctxA, changeOf(oldFile(t, dir, "a.go")))
	first := waitForBuildPids(t, pids, 1)[0]
	// The kill waits out the 200ms grace; b triggers inside that window.
	done := make(chan struct{})
	go func() { a.cancelBuild(); close(done) }()
	time.Sleep(50 * time.Millisecond)
	b.handleChange(ctxB, changeOf(oldFile(t, dir, "b.go")))
	<-done

	if err := syscall.Kill(first, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("first build pid %d still alive when the second build started", first)
	}
}

func waitForBuildPids(t *testing.T, path string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		var out []int
		for _, f := range strings.Fields(string(b)) {
			pid, err := strconv.Atoi(f)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, pid)
		}
		if len(out) >= n {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("pids = %v, want %d", out, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stuckCommand is a process whose kill never reaps: KillForce returns, and the run never exits.
type stuckCommand struct{ rerun.Command }

func (stuckCommand) KillForce() error { return nil }

// A killed build that outlives its kill wait tells its members before the replacement starts.
func TestSharedBuildWarnsWhenKilledBuildOutlivesTheWait(t *testing.T) {
	old := buildCancelWait
	buildCancelWait = 50 * time.Millisecond
	t.Cleanup(func() { buildCancelWait = old })

	share := newBuildShare()
	dir := t.TempDir()
	b, logB, ctxB := sharedRunner(t, share, "b", dir, "sleep 30")
	logA := &eventLog{}
	key := buildKeyOf(b.target)
	stuck := &sharedBuild{key: key, share: share, cmd: stuckCommand{}, buf: &tailBuffer{}, started: time.Now(), exited: make(chan struct{}), files: map[string]struct{}{}, cancelled: true}
	stuck.members = []*buildJob{{name: "a", sink: logA, run: stuck, done: make(chan buildResult, 1)}}
	share.open[key] = stuck

	b.handleChange(ctxB, changeOf(oldFile(t, dir, "b.go")))

	const want = "old build still exiting · starting new one"
	for name, lg := range map[string]*eventLog{"a": logA, "b": logB} {
		found := false
		for _, ev := range lg.kind(evNotice) {
			found = found || ev.line == want
		}
		if !found {
			t.Errorf("member %s did not hear that the killed build was still exiting", name)
		}
	}
}

// The run keeps going when its owner leaves, so the next member owns its events.
func TestSharedBuildOwnerLeavingHandsRunToNextMember(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)
	b, bLog, _ := sharedRunner(t, share, "b", dir, script)

	src := oldFile(t, dir, "a.go")
	a.handleChange(ctx, changeOf(src))
	b.handleChange(ctx, changeOf(src))
	waitForRuns(t, counter, 1)
	a.cancelBuild()

	share.mu.Lock()
	owner := share.open[buildKeyOf(a.target)].owner
	share.mu.Unlock()
	if owner != "b" {
		t.Fatalf("run owner = %q, want b", owner)
	}
	releaseGate(t, gate)
	b.finishBuild(ctx, nextBuild(t, b))
	if got := len(bLog.kind(evBuildOK)); got != 1 {
		t.Errorf("b: evBuildOK = %d, want 1", got)
	}
	if got := len(aLog.kind(evBuildOK)); got != 0 {
		t.Errorf("a: evBuildOK = %d, want 0 after it left", got)
	}
	if n := runCount(counter); n != 1 {
		t.Fatalf("build ran %d times, want 1", n)
	}
}

// A startup trigger that finds a run already ended joins it; relaunching would drop the run's result.
func TestSharedBuildStartupJoinsRunThatAlreadyEnded(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, _, ctx := sharedRunner(t, share, "a", dir, script)
	b, bLog, _ := sharedRunner(t, share, "b", dir, script)

	a.startBuild(nil)
	waitForRuns(t, counter, 1)
	// The process has exited, but its reap has not reached the share yet.
	share.mu.Lock()
	share.open[buildKeyOf(a.target)].ended.Store(time.Now().UnixNano())
	share.mu.Unlock()
	b.startBuild(nil)
	releaseGate(t, gate)
	a.finishBuild(ctx, nextBuild(t, a))
	b.finishBuild(ctx, nextBuild(t, b))

	if n := runCount(counter); n != 1 {
		t.Fatalf("build ran %d times, want 1", n)
	}
	if got := len(bLog.kind(evBuildOK)); got != 1 {
		t.Errorf("b: evBuildOK = %d, want 1", got)
	}
}

// restartBuild replaces a running shared build, so the next run starts from scratch.
func TestSharedBuildRestartReplacesRunningBuild(t *testing.T) {
	share := newBuildShare()
	dir := t.TempDir()
	script, counter, gate := countedBuild(t, 0)
	a, aLog, ctx := sharedRunner(t, share, "a", dir, script)

	a.startBuild(nil)
	waitForRuns(t, counter, 1)
	a.restartBuild()
	waitForRuns(t, counter, 2)
	releaseGate(t, gate)
	a.finishBuild(ctx, nextBuild(t, a))

	if got := len(aLog.kind(evBuildStarted)); got != 2 {
		t.Errorf("a: evBuildStarted = %d, want 2 (launch and restart)", got)
	}
}
