package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	buildTailLines = 200
	// tailPartialCap bounds a never-terminated line, so a noisy build cannot grow memory.
	tailPartialCap = 64 * 1024
)

// buildCancelWait bounds the wait on a killed build: a child that escaped the
// process group can hold the output pipe open and block the reaper.
var buildCancelWait = 3 * time.Second

// buildResult is one runner's view of a finished shared build.
type buildResult struct {
	err     error
	code    int
	lines   []string
	dur     time.Duration
	files   map[string]struct{}
	started time.Time
	ended   time.Time
}

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
	r.builds.start(r, files)
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

// abortBuild leaves the shared build quietly; the process dies with its last member.
func (r *targetRunner) abortBuild() bool {
	job := r.build
	if job == nil {
		return false
	}
	r.build = nil
	r.builds.leave(job)
	return true
}

func (r *targetRunner) finishBuild(ctx context.Context, res buildResult) {
	if r.build == nil {
		return
	}
	r.build = nil
	r.lastBuildStart, r.lastBuildEnd = res.started, res.ended
	files := sortedFiles(res.files)
	if res.err != nil {
		r.sink.emit(event{kind: evBuildFailed, target: r.target.Name, files: files, dur: res.dur, code: res.code, lines: res.lines, err: res.err})
		return
	}
	r.sink.emit(event{kind: evBuildOK, target: r.target.Name, dur: res.dur})
	if r.userStopped() {
		return
	}

	r.noteReplace(res.files)
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
