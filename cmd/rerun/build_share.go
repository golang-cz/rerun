package main

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-cz/rerun"
)

// buildKey is the share key: one build command in one directory is one build.
type buildKey struct{ dir, cmd string }

func buildKeyOf(t Target) buildKey {
	dir := t.Dir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return buildKey{dir: dir, cmd: t.Build}
}

// buildShare runs one process per key; every runner that triggers a key joins its run.
// Lock order: buildShare.mu, then a run's tail lock. A run's members are guarded by both.
// Events are emitted after buildShare.mu is released, so a slow front-end never stalls other targets.
type buildShare struct {
	mu   sync.Mutex
	open map[buildKey]*sharedBuild
}

func newBuildShare() *buildShare {
	return &buildShare{open: make(map[buildKey]*sharedBuild)}
}

// sharedBuild is one process run for a key.
type sharedBuild struct {
	key     buildKey
	share   *buildShare
	cmd     rerun.Command
	buf     *tailBuffer
	started time.Time
	ended   atomic.Int64 // unix nanos at exit, 0 while running
	exited  chan struct{}

	// files, cancelled, replacing, finished: guarded by share.mu.
	// A cancelled run stays in share.open until its process is reaped, so a trigger waits for it.
	files     map[string]struct{}
	cancelled bool
	replacing bool // a trigger is waiting for this run's process to exit, then relaunches
	finished  bool
	// members: guarded by share.mu and buf.mu.
	members []*buildJob
}

// buildJob is one runner's seat in a sharedBuild.
type buildJob struct {
	name string
	sink sink
	run  *sharedBuild // guarded by share.mu
	done chan buildResult
}

// emission is an event held back until share.mu is released.
type emission struct {
	sink sink
	ev   event
}

// start joins or begins the shared build for r's key. Runner goroutine only.
func (s *buildShare) start(r *targetRunner, files map[string]struct{}) {
	job := r.build
	if job == nil {
		job = &buildJob{name: r.target.Name, sink: r.sink, done: make(chan buildResult, 1)}
		r.build = job
	}
	key := buildKeyOf(r.target)

	s.mu.Lock()
	cur := s.open[key]
	late := false
	if cur != nil && !cur.cancelled && cur.ended.Load() == 0 && files != nil {
		// The stat runs unlocked, so re-check the run once the lock is back.
		started := cur.started
		s.mu.Unlock()
		late = lateAt(started, files)
		s.mu.Lock()
		if s.open[key] != cur {
			s.mu.Unlock()
			s.start(r, files)
			return
		}
	}
	union := copySet(files)
	if job.run != nil {
		maps.Copy(union, job.run.files)
	}
	drainResult(job.done)

	var run *sharedBuild
	var out []emission
	switch {
	case cur == nil:
		run, out = s.launchLocked(key, r.target.Grace, []*buildJob{job}, union)
	case cur.replacing:
		// The run that is waiting to relaunch picks this trigger up.
		cur.adoptLocked(job, union)
	case cur.cancelled || files == nil || late || cur.ended.Load() != 0:
		// The process may have read the files already, or is going away: rerun once with everyone.
		cur.cancelled, cur.replacing = true, true
		s.mu.Unlock()
		stuck := cur.kill()
		s.mu.Lock()
		cur.adoptLocked(job, union)
		if stuck {
			out = cur.stuckNotices()
		}
		var launched []emission
		run, launched = s.launchLocked(key, r.target.Grace, cur.members, cur.files)
		out = append(out, launched...)
	default:
		run, out = cur.join(job, union)
	}

	if run == nil {
		s.mu.Unlock()
		emitAll(out)
		return
	}
	// Taking the tail lock before releasing the share lock keeps the events ahead of any output line.
	s.mu.Unlock()
	emitAll(out)
	run.buf.mu.Unlock()
}

func emitAll(out []emission) {
	for _, e := range out {
		e.sink.emit(e.ev)
	}
}

// lateAt reports whether a trigger on files came after the process started. Caller holds no lock.
func lateAt(started time.Time, files map[string]struct{}) bool {
	for f := range files {
		info, err := os.Stat(f)
		if err != nil || !info.ModTime().Before(started) {
			return true
		}
	}
	return false
}

// join adds a trigger that the running process will already see. Caller holds share.mu.
// A non-nil run comes back with its tail lock held; the caller releases it after emitting.
func (run *sharedBuild) join(job *buildJob, files map[string]struct{}) (*sharedBuild, []emission) {
	run.buf.mu.Lock()
	maps.Copy(run.files, files)
	if slices.Contains(run.members, job) {
		run.buf.mu.Unlock()
		return nil, nil
	}
	job.run = run
	run.members = append(run.members, job)
	out := []emission{{job.sink, event{kind: evBuildStarted, target: job.name, files: sortedFiles(run.files), at: run.started}}}
	// Replay only completed lines: the partial one arrives whole when it completes.
	for _, line := range run.buf.lines {
		out = append(out, emission{job.sink, event{kind: evBuildOutput, target: job.name, line: line, buildAt: run.started}})
	}
	return run, out
}

// adoptLocked adds job to a run that is being replaced. Caller holds share.mu.
func (run *sharedBuild) adoptLocked(job *buildJob, files map[string]struct{}) {
	run.buf.mu.Lock()
	defer run.buf.mu.Unlock()
	maps.Copy(run.files, files)
	if !slices.Contains(run.members, job) {
		run.members = append(run.members, job)
	}
	job.run = run
}

// launchLocked starts the process for members. Caller holds share.mu.
// A non-nil run comes back with its tail lock held; the caller releases it after emitting.
func (s *buildShare) launchLocked(key buildKey, grace time.Duration, members []*buildJob, files map[string]struct{}) (*sharedBuild, []emission) {
	run := &sharedBuild{
		key:     key,
		share:   s,
		files:   copySet(files),
		started: time.Now(),
		exited:  make(chan struct{}),
	}
	run.buf = &tailBuffer{n: buildTailLines, onLine: run.fanOut}
	run.buf.mu.Lock()
	run.members = slices.Clone(members)
	for _, m := range run.members {
		m.run = run
	}
	s.open[key] = run

	sorted := sortedFiles(run.files)
	var out []emission
	for _, m := range run.members {
		out = append(out, emission{m.sink, event{kind: evBuildStarted, target: m.name, files: sorted, at: run.started}})
	}

	devNull, err := os.Open(os.DevNull)
	if err == nil {
		defer devNull.Close()
		run.cmd, err = rerun.StartCommand([]string{key.cmd}, grace,
			rerun.WithDir(key.dir), rerun.WithStdin(devNull), rerun.WithStdout(run.buf), rerun.WithStderr(run.buf))
	}
	if err != nil {
		s.failLocked(run, err)
		run.buf.mu.Unlock()
		return nil, out
	}
	go run.wait()
	return run, out
}

// fanOut sends one output line to every member. Runs under the tail lock.
func (run *sharedBuild) fanOut(line string) {
	for _, m := range run.members {
		m.sink.emit(event{kind: evBuildOutput, target: m.name, line: line, buildAt: run.started})
	}
}

func (run *sharedBuild) wait() {
	_ = run.cmd.WaitExit(context.Background())
	run.ended.Store(time.Now().UnixNano())
	run.buf.flush()
	code, werr := run.cmd.ExitState()
	res := buildResult{
		dur:     time.Since(run.started),
		code:    code,
		lines:   run.buf.tail(),
		started: run.started,
		ended:   time.Unix(0, run.ended.Load()),
	}
	if code != 0 || werr != nil {
		res.err = describeBuildErr(code, werr)
	}
	// Close before finish: a replacing run waits on this without holding the share lock.
	close(run.exited)
	run.share.finish(run, res)
}

// finish delivers the result to every member of a run that was not replaced or killed.
func (s *buildShare) finish(run *sharedBuild, res buildResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.cancelled {
		return
	}
	run.finished = true
	if s.open[run.key] == run {
		delete(s.open, run.key)
	}
	res.files = copySet(run.files)
	run.buf.mu.Lock()
	members := run.members
	run.members = nil
	run.buf.mu.Unlock()
	for _, m := range members {
		deliver(m.done, res)
	}
}

// failLocked reports a run that never started. Caller holds share.mu and the run's tail lock.
func (s *buildShare) failLocked(run *sharedBuild, err error) {
	run.finished = true
	if s.open[run.key] == run {
		delete(s.open, run.key)
	}
	res := buildResult{err: err, files: copySet(run.files), started: run.started, ended: time.Now()}
	for _, m := range run.members {
		deliver(m.done, res)
	}
	run.members = nil
}

// leave detaches a runner. The process dies only when its last member leaves.
func (s *buildShare) leave(job *buildJob) {
	s.mu.Lock()
	run := job.run
	if run == nil {
		s.mu.Unlock()
		return
	}
	run.buf.mu.Lock()
	run.members = slices.DeleteFunc(run.members, func(m *buildJob) bool { return m == job })
	empty := len(run.members) == 0
	run.buf.mu.Unlock()
	if run.finished || run.cancelled || !empty {
		s.mu.Unlock()
		return
	}
	// The run stays open until its process is reaped, so a trigger waits instead of starting a second process.
	run.cancelled = true
	s.mu.Unlock()
	run.kill()
}

// kill stops the process and waits, bounded, for the reap; it reports whether the wait ran out.
func (run *sharedBuild) kill() (stuck bool) {
	// KillForce itself waits for the reap, so it must not run on the caller.
	go func() { _ = run.cmd.KillForce() }()
	select {
	case <-run.exited:
		return false
	case <-time.After(buildCancelWait):
		return true
	}
}

// stuckNotices tells every member the killed build is still exiting, before its replacement launches. Caller holds share.mu.
func (run *sharedBuild) stuckNotices() []emission {
	out := make([]emission, 0, len(run.members))
	for _, m := range run.members {
		out = append(out, emission{sink: m.sink, ev: event{kind: evNotice, target: m.name, line: "old build still exiting · starting new one"}})
	}
	return out
}

// window reports when job's run started and ended (0 while running).
func (s *buildShare) window(job *buildJob) (started time.Time, ended int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.run == nil {
		return time.Time{}, 0
	}
	return job.run.started, job.run.ended.Load()
}

// deliver replaces a result the job never read; the send never blocks because the caller holds the share lock.
func deliver(ch chan buildResult, res buildResult) {
	drainResult(ch)
	ch <- res
}

func drainResult(ch chan buildResult) {
	select {
	case <-ch:
	default:
	}
}

func copySet(set map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(set))
	maps.Copy(out, set)
	return out
}
