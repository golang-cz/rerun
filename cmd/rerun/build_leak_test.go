//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// repoWithIgnoredBin mimics omsx: .gitignore at the repo root, watch root below it.
func repoWithIgnoredBin(t *testing.T) (repo, watched string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	watched = filepath.Join(repo, "apps", "api")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(watched, "bin"), filepath.Join(watched, "cmd")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(watched, "cmd", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, watched
}

func runWatchedBuild(t *testing.T, watched, build string, settle time.Duration) *recordingSink {
	t.Helper()
	sink := &recordingSink{}
	target := Target{Name: "svc", Cmd: "sleep 30", Build: build, On: []string{watched},
		Delay: 50 * time.Millisecond, Grace: 200 * time.Millisecond, Selected: true}
	r := newTargetRunner(target, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.run(ctx) }()
	time.Sleep(settle)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
	return sink
}

func TestBuildOutputInGitignoredBinDoesNotRebuild(t *testing.T) {
	_, watched := repoWithIgnoredBin(t)
	build := fmt.Sprintf("echo x > %s/bin/svc; sleep 0.1", watched)
	sink := runWatchedBuild(t, watched, build, 2*time.Second)
	if n := len(countKind(sink, evBuildStarted)); n != 1 {
		t.Fatalf("builds started = %d, want 1 (output in ignored bin/ must not retrigger)", n)
	}
}

// A build or run that rewrites its own non-ignored watched file loops until the
// breaker pauses it: bounded starts, a loop event naming the file.
func TestSelfTriggeringBuildPausesAfterBoundedBuilds(t *testing.T) {
	for name, build := range map[string]string{
		"writes at the end":   "sleep 0.1; echo x >> %s/gen.txt",
		"writes mid-build":    "echo x >> %s/gen.txt; sleep 0.2",
		"writes at the start": "echo x >> %s/gen.txt",
	} {
		t.Run(name, func(t *testing.T) {
			_, watched := repoWithIgnoredBin(t)
			sink := runWatchedBuild(t, watched, fmt.Sprintf(build, watched), 3*time.Second)
			if n := len(countKind(sink, evBuildStarted)); n > loopCycles+2 {
				t.Fatalf("builds started = %d, want a bounded count (<= %d)", n, loopCycles+2)
			}
			loops := countKind(sink, evLoop)
			if len(loops) == 0 || !strings.Contains(loops[0].line, "gen.txt") {
				t.Fatalf("loop events = %+v, want one naming gen.txt", loops)
			}
		})
	}
}

func TestSelfTriggeringRunPausesAfterBoundedStarts(t *testing.T) {
	_, watched := repoWithIgnoredBin(t)
	sink := &recordingSink{}
	target := Target{Name: "svc", Cmd: fmt.Sprintf("echo x >> %s/gen.txt; sleep 30", watched), On: []string{watched},
		Delay: 50 * time.Millisecond, Grace: 200 * time.Millisecond, Selected: true}
	r := newTargetRunner(target, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.run(ctx) }()
	defer func() { cancel(); <-done }()

	waitForEvent(t, sink, evLoop, "svc", 10*time.Second)
	if n := len(countKind(sink, evStarted)); n > loopCycles+2 {
		t.Fatalf("starts = %d, want a bounded count (<= %d)", n, loopCycles+2)
	}
	if cmd := r.currentCmd(); cmd == nil || cmd.Exited() {
		t.Fatal("the last good run was not kept")
	}
}

// A paused target keeps its last good run, and a change to another file resumes it.
func TestPausedTargetResumesOnAnotherFile(t *testing.T) {
	_, watched := repoWithIgnoredBin(t)
	build := fmt.Sprintf("sleep 0.1; echo x >> %s/gen.txt", watched)
	sink := &recordingSink{}
	target := Target{Name: "svc", Cmd: "sleep 30", Build: build, On: []string{watched},
		Delay: 50 * time.Millisecond, Grace: 200 * time.Millisecond, Selected: true}
	r := newTargetRunner(target, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.run(ctx) }()
	defer func() { cancel(); <-done }()

	waitForEvent(t, sink, evLoop, "svc", 15*time.Second)
	// The pause stops new builds, so the count is fixed once the loop event is out.
	paused := len(countKind(sink, evBuildStarted))

	edit := filepath.Join(watched, "cmd", "main.go")
	deadline := time.Now().Add(15 * time.Second)
	for len(countKind(sink, evBuildStarted)) == paused {
		if time.Now().After(deadline) {
			t.Fatal("a change to another file did not resume the target")
		}
		if err := os.WriteFile(edit, []byte(time.Now().String()), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Human saves must never read as a loop, at any cadence: each one builds.
func TestHumanSaveCadencesNeverPause(t *testing.T) {
	saves := loopCycles + 2
	cases := []struct {
		name  string
		build string
		save  func(t *testing.T, r *targetRunner, ctx context.Context, file string)
		want  int
	}{
		{"fast build, save after it finishes", "sleep 0.1", afterFinish, saves},
		{"slow build, save after it finishes", "sleep 0.5", afterFinish, saves},
		{"format on save rewrites the file again", "sleep 0.1", formatOnSave, saves * 2},
		{"autosave aborts the in-flight build", "sleep 0.6", whileBuilding, saves},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, log, ctx := buildRunner(t, tc.build, "sleep 30")
			file := filepath.Join(t.TempDir(), "main.go")
			for i := 0; i < saves; i++ {
				tc.save(t, r, ctx, file)
			}
			if got := len(log.kind(evBuildStarted)); got != tc.want {
				t.Errorf("builds started = %d, want %d: a save was dropped", got, tc.want)
			}
			if got := len(log.kind(evLoop)); got != 0 {
				t.Errorf("loop events = %d, want 0", got)
			}
		})
	}
}

// humanGap is the pause a person leaves between actions, well past loopSkew.
const humanGap = 350 * time.Millisecond

func saveFile(t *testing.T, file string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(time.Now().String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func afterFinish(t *testing.T, r *targetRunner, ctx context.Context, file string) {
	saveFile(t, file)
	r.handleChange(ctx, changeOf(file))
	r.finishBuild(ctx, nextBuild(t, r))
	time.Sleep(humanGap)
}

func formatOnSave(t *testing.T, r *targetRunner, ctx context.Context, file string) {
	saveFile(t, file)
	r.handleChange(ctx, changeOf(file))
	time.Sleep(90 * time.Millisecond)
	saveFile(t, file)
	r.handleChange(ctx, changeOf(file))
	r.finishBuild(ctx, nextBuild(t, r))
	time.Sleep(humanGap)
}

func whileBuilding(t *testing.T, r *targetRunner, ctx context.Context, file string) {
	saveFile(t, file)
	r.handleChange(ctx, changeOf(file))
	time.Sleep(humanGap)
}

// treeBuild spawns a grandchild per build, like make -> go build -> compile, and records its pid.
func treeBuild(t *testing.T) (script, pidfile string) {
	t.Helper()
	pidfile = filepath.Join(t.TempDir(), "pids")
	return "sleep 60 & echo $! >> " + pidfile + "; wait", pidfile
}

func recordedPids(t *testing.T, pidfile string) []int {
	t.Helper()
	data, _ := os.ReadFile(pidfile)
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, pid := range pids {
			if alive(pid) {
				n++
			}
		}
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var live []int
	for _, pid := range pids {
		if alive(pid) {
			live = append(live, pid)
		}
	}
	t.Fatalf("build descendants still alive: %v", live)
}

func waitPids(t *testing.T, pidfile string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pids := recordedPids(t, pidfile); len(pids) >= n {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("only %d of %d builds spawned a grandchild", len(recordedPids(t, pidfile)), n)
	return nil
}

func TestRapidChangesLeaveOneBuildTree(t *testing.T) {
	script, pidfile := treeBuild(t)
	r, _, ctx := buildRunner(t, script, "sleep 30")
	const changes = 8
	for i := 0; i < changes; i++ {
		r.handleChange(ctx, changeOf(fmt.Sprintf("f%d.go", i)))
		pids := waitPids(t, pidfile, i+1)
		// Only the newest build's tree may be alive while it runs.
		waitDead(t, pids[:len(pids)-1])
	}
	pids := recordedPids(t, pidfile)
	if !alive(pids[len(pids)-1]) {
		t.Fatal("the newest build was killed")
	}
	r.cancelBuild()
	waitDead(t, pids)
}

func TestStopAndQuitKillTheBuildTree(t *testing.T) {
	script, pidfile := treeBuild(t)
	r, _, ctx := buildRunner(t, script, "sleep 30")
	r.handleChange(ctx, changeOf("a.go"))
	pids := waitPids(t, pidfile, 1)
	r.handleControl(ctx, ctrlMsg{kind: ctrlStop})
	waitDead(t, pids)

	sink := &recordingSink{}
	target := Target{Name: "svc", Cmd: "sleep 30", Build: script, Grace: 200 * time.Millisecond, Selected: true}
	q := newTargetRunner(target, sink)
	qctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = q.run(qctx) }()
	pids = waitPids(t, pidfile, 2)
	cancel()
	<-done
	waitDead(t, pids)
}

// A run that exits at once must stay down: only a change or the user restarts it.
func TestCrashingRunDoesNotRebuild(t *testing.T) {
	_, watched := repoWithIgnoredBin(t)
	sink := &recordingSink{}
	target := Target{Name: "svc", Cmd: "exit 127", Build: "true", On: []string{watched},
		Delay: 50 * time.Millisecond, Grace: 200 * time.Millisecond, Selected: true}
	r := newTargetRunner(target, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.run(ctx) }()
	time.Sleep(3 * time.Second)
	cancel()
	<-done
	if n := len(countKind(sink, evBuildStarted)); n != 1 {
		t.Fatalf("builds = %d, want 1", n)
	}
	if n := len(countKind(sink, evStarted)); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	if len(countKind(sink, evExited)) == 0 {
		t.Fatal("the crash was not reported")
	}
}
