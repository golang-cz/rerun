package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-cz/rerun"
)

const (
	buildTailLines = 200
	// tailPartialCap bounds a never-terminated line, so a noisy build cannot grow memory.
	tailPartialCap = 64 * 1024
)

// buildCancelWait bounds the wait on a killed build: a child that escaped the
// process group can hold the output pipe open and block the reaper.
var buildCancelWait = 3 * time.Second

type buildJob struct {
	files     map[string]struct{}
	started   time.Time
	ended     atomic.Int64 // unix nanos at exit, set before done is sent
	cmd       rerun.Command
	done      chan buildResult
	cancelled atomic.Bool
}

type buildResult struct {
	err   error
	code  int
	lines []string
	dur   time.Duration
}

var errBuildCancelled = errors.New("build cancelled")

type tailBuffer struct {
	mu      sync.Mutex
	n       int
	lines   []string
	partial string
	// onLine sees each line as it completes, under mu, so the lines keep their order across writers.
	onLine func(string)
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	parts := strings.Split(b.partial+string(p), "\n")
	b.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		b.add(strings.TrimSuffix(l, "\r"))
	}
	if len(b.partial) > tailPartialCap {
		b.add(b.partial[:tailPartialCap])
		b.partial = ""
	}
	b.trim()
	return len(p), nil
}

func (b *tailBuffer) flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.partial != "" {
		b.add(b.partial)
		b.partial = ""
	}
	b.trim()
}

func (b *tailBuffer) add(line string) {
	b.lines = append(b.lines, line)
	if b.onLine != nil {
		b.onLine(line)
	}
}

func (b *tailBuffer) trim() {
	if over := len(b.lines) - b.n; over > 0 {
		b.lines = slices.Clone(b.lines[over:])
	}
}

func (b *tailBuffer) tail() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := slices.Clone(b.lines)
	if b.partial != "" {
		out = append(out, b.partial)
		if len(out) > b.n {
			out = out[len(out)-b.n:]
		}
	}
	return out
}

func (r *targetRunner) launch(ctx context.Context) {
	if r.target.Build != "" {
		r.startBuild(nil)
		return
	}
	r.spawn(ctx)
}

// A nil channel when idle makes the run loop's select skip this case.
func (r *targetRunner) buildResult() <-chan buildResult {
	if r.build == nil {
		return nil
	}
	return r.build.done
}

func (r *targetRunner) startBuild(files map[string]struct{}) {
	union := make(map[string]struct{}, len(files))
	if r.build != nil {
		for f := range r.build.files {
			union[f] = struct{}{}
		}
		r.abortBuild()
	}
	for f := range files {
		union[f] = struct{}{}
	}

	sorted := sortedFiles(union)

	started := time.Now()
	r.sink.emit(event{kind: evBuildStarted, target: r.target.Name, files: sorted, at: started})

	buf := &tailBuffer{n: buildTailLines, onLine: func(line string) {
		r.sink.emit(event{kind: evBuildOutput, target: r.target.Name, line: line, buildAt: started})
	}}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		r.sink.emit(event{kind: evBuildFailed, target: r.target.Name, files: sorted, err: err})
		return
	}
	defer devNull.Close()
	cmd, err := rerun.StartCommand([]string{r.target.Build}, r.target.Grace,
		rerun.WithDir(r.target.Dir), rerun.WithStdin(devNull), rerun.WithStdout(buf), rerun.WithStderr(buf))
	if err != nil {
		r.sink.emit(event{kind: evBuildFailed, target: r.target.Name, files: sorted, err: err})
		return
	}

	job := &buildJob{files: union, started: started, cmd: cmd, done: make(chan buildResult, 1)}
	go func() {
		_ = cmd.WaitExit(context.Background())
		job.ended.Store(time.Now().UnixNano())
		buf.flush()
		code, werr := cmd.ExitState()
		res := buildResult{dur: time.Since(started), code: code, lines: buf.tail()}
		switch {
		case job.cancelled.Load():
			res.err = errBuildCancelled
		case code != 0 || werr != nil:
			res.err = describeBuildErr(code, werr)
		}
		job.done <- res
	}()
	r.build = job
}

func describeBuildErr(code int, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("exit status %d", code)
}

// cancelBuild drops the build for good, so the UI can clear its building state.
func (r *targetRunner) cancelBuild() {
	if r.abortBuild() {
		r.sink.emit(event{kind: evBuildCancelled, target: r.target.Name, at: time.Now()})
	}
}

// abortBuild kills the build quietly, for a rebuild that replaces it.
func (r *targetRunner) abortBuild() bool {
	job := r.build
	if job == nil {
		return false
	}
	r.build = nil
	job.cancelled.Store(true)
	// KillForce itself waits for the reap, so it must not run on the loop.
	go func() { _ = job.cmd.KillForce() }()
	select {
	case <-job.done:
	case <-time.After(buildCancelWait):
	}
	return true
}

func (r *targetRunner) finishBuild(ctx context.Context, res buildResult) {
	job := r.build
	r.build = nil
	if job == nil || errors.Is(res.err, errBuildCancelled) {
		return
	}
	r.lastBuildStart, r.lastBuildEnd = job.started, time.Unix(0, job.ended.Load())
	files := sortedFiles(job.files)
	if res.err != nil {
		r.sink.emit(event{kind: evBuildFailed, target: r.target.Name, files: files, dur: res.dur, code: res.code, lines: res.lines, err: res.err})
		return
	}
	r.sink.emit(event{kind: evBuildOK, target: r.target.Name, dur: res.dur})
	if r.userStopped() {
		return
	}

	r.noteReplace(job.files)
	r.meta.buildDur = res.dur
	if r.meta.restart {
		r.sink.emit(event{kind: evExited, target: r.target.Name, status: "stopped for restart", restart: true, at: time.Now()})
	}
	r.killCurrent(ctx)
	r.waitCooldown(ctx)
	if ctx.Err() != nil || r.retired() {
		r.meta = startMeta{}
		return
	}
	r.spawn(ctx)
}

func (r *targetRunner) noteReplace(files map[string]struct{}) {
	r.meta.files = sortedFiles(files)
	r.meta.restart, r.meta.prevUp, r.meta.buildDur = false, 0, 0
	if !r.be.Running() {
		return
	}
	r.mu.Lock()
	r.meta.prevUp = time.Since(r.lastStart)
	r.mu.Unlock()
	r.meta.restart = true
}

func sortedFiles(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}
