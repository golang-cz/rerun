package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/golang-cz/rerun"
)

// runTargets runs every target in parallel until a shutdown signal arrives.
//
// It returns 0 after a graceful shutdown and 130 when a second signal forces
// the kill. A nil signals channel installs the default handlers.
func runTargets(targets []Target, out io.Writer, signals <-chan os.Signal) int {
	if out == nil {
		out = os.Stdout
	}
	signals, stopSignals := signalChannel(signals)
	defer stopSignals()

	group := newTargetGroup(targets, newPlainSink(out))
	group.start()

	select {
	case <-group.done:
		return group.exitCode()
	case <-signals:
		return shutdownTargets(group, signals)
	}
}

// signalChannel returns the caller's channel, or a fresh one wired to the
// shutdown signals. The returned stop function releases it.
func signalChannel(signals <-chan os.Signal) (<-chan os.Signal, func()) {
	if signals != nil {
		return signals, func() {}
	}
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	return ch, func() { signal.Stop(ch) }
}

// targetGroup owns one runner per target and the process lifecycle both
// front-ends share.
type targetGroup struct {
	ctx     context.Context
	cancel  context.CancelFunc
	sink    sink
	runners []*targetRunner
	done    chan struct{}
	failed  int32
	wg      sync.WaitGroup
}

func newTargetGroup(targets []Target, s sink) *targetGroup {
	ctx, cancel := context.WithCancel(context.Background())
	g := &targetGroup{ctx: ctx, cancel: cancel, sink: s, done: make(chan struct{})}
	for _, target := range targets {
		g.runners = append(g.runners, newTargetRunner(target, s))
	}
	return g
}

func (g *targetGroup) start() {
	for _, r := range g.runners {
		g.wg.Add(1)
		go func(r *targetRunner) {
			defer g.wg.Done()
			if err := r.run(g.ctx); err != nil {
				atomic.StoreInt32(&g.failed, 1)
				g.sink.emit(event{kind: evRunnerErr, target: r.target.Name, err: err})
			}
		}(r)
	}
	go func() {
		g.wg.Wait()
		close(g.done)
	}()
}

// exitCode is the run's exit code once every runner has returned on its own.
func (g *targetGroup) exitCode() int {
	if atomic.LoadInt32(&g.failed) != 0 {
		return 1
	}
	return 0
}

// shutdownTargets stops every runner gracefully, force-killing on a second
// signal. It returns 130 when the kill was forced.
func shutdownTargets(g *targetGroup, signals <-chan os.Signal) int {
	for _, r := range g.runners {
		r.markStopping()
	}

	var forced int32
	stop := make(chan struct{})
	go func() {
		select {
		case <-signals:
			atomic.StoreInt32(&forced, 1)
			for _, r := range g.runners {
				r.force()
			}
		case <-stop:
		}
	}()

	var killWG sync.WaitGroup
	for _, r := range g.runners {
		killWG.Add(1)
		go func(r *targetRunner) {
			defer killWG.Done()
			r.stop()
		}(r)
	}
	killWG.Wait()

	g.cancel()
	<-g.done
	close(stop)

	if atomic.LoadInt32(&forced) != 0 {
		return 130
	}
	return 0
}

// ctrlKind is a front-end's request to the runner that owns the process.
type ctrlKind int

const (
	ctrlStop ctrlKind = iota
	ctrlStart
	ctrlReboot
)

type ctrlMsg struct{ kind ctrlKind }

// targetRunner owns one target's watcher and its current child process. Its
// run goroutine is the only writer of process state; every other goroutine
// sends ctrlMsg over ctrlCh and reads the event stream.
type targetRunner struct {
	target Target
	sink   sink
	// out receives the ad-hoc command's output directly. Nil for sinks that
	// render output themselves.
	out io.Writer

	ctrlCh   chan ctrlMsg
	finished chan struct{}

	mu        sync.Mutex
	cmd       rerun.Command
	lastStart time.Time
	stopping  int32
	// stopped is the user's stop: the target stays down until space or r.
	stopped int32

	// exited is closed when the current generation is reaped. Only the run
	// goroutine reads it, so no lock is needed.
	exited <-chan struct{}
}

func newTargetRunner(target Target, s sink) *targetRunner {
	r := &targetRunner{
		target:   target,
		sink:     s,
		ctrlCh:   make(chan ctrlMsg, 16),
		finished: make(chan struct{}),
	}
	if w, ok := s.(interface{ writer() io.Writer }); ok {
		r.out = w.writer()
	}
	// An unselected target starts off: it stays down until space or r.
	if !target.Selected {
		atomic.StoreInt32(&r.stopped, 1)
	}
	return r
}

// control asks the run goroutine to stop, start, or reboot the target. It
// reports false once the runner has returned. A full queue drops the command
// instead of blocking the caller: a burst of key autorepeat asks for what the
// queued commands already do, and stalling the TUI's event loop is worse.
func (r *targetRunner) control(kind ctrlKind) bool {
	select {
	case r.ctrlCh <- ctrlMsg{kind: kind}:
		return true
	case <-r.finished:
		return false
	default:
		return true
	}
}

func (r *targetRunner) run(ctx context.Context) error {
	defer close(r.finished)

	watcher, err := rerun.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	watcher.RespectGitignore(!r.target.NoGitignore)

	// Close the watcher on shutdown: the initial walk below can take seconds on
	// a big tree and would otherwise hold the whole run back.
	closeWatcher := make(chan struct{})
	defer close(closeWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = watcher.Close()
		case <-closeWatcher:
		}
	}()

	on := r.target.On
	if len(on) == 0 {
		on = []string{"./"}
	}
	watcher.Add(on...)
	if len(r.target.Except) > 0 {
		watcher.Ignore(r.target.Except...)
	}

	// Start before the watch walk: the walk can take seconds on a big tree,
	// and the command's output must not wait for it.
	if !r.userStopped() {
		if err := r.start(); err != nil {
			return err
		}
	}
	r.sink.emit(event{kind: evCleared, target: r.target.Name})

	changes := watcher.Watch(r.target.Delay)

	for {
		select {
		case <-ctx.Done():
			r.killCurrent(context.Background())
			return nil

		case changeSet, ok := <-changes:
			if !ok {
				r.killCurrent(context.Background())
				return nil
			}
			r.handleChange(ctx, changeSet)

		case c := <-r.ctrlCh:
			r.handleControl(ctx, c)

		case <-r.exited:
			r.reportExit()
			r.exited = nil
		}
	}
}

// handleControl runs a front-end's start/stop/reboot request inside the run
// goroutine, so process state is never written from two goroutines.
func (r *targetRunner) handleControl(ctx context.Context, c ctrlMsg) {
	switch c.kind {
	case ctrlStop:
		atomic.StoreInt32(&r.stopped, 1)
		r.killCurrent(ctx)

	case ctrlStart:
		atomic.StoreInt32(&r.stopped, 0)
		if cmd := r.currentCmd(); cmd != nil && !cmd.Exited() {
			return
		}
		r.startReporting()

	case ctrlReboot:
		atomic.StoreInt32(&r.stopped, 0)
		r.killCurrent(ctx)
		r.waitCooldown(ctx)
		if ctx.Err() != nil {
			return
		}
		r.startReporting()
	}
}

func (r *targetRunner) handleChange(ctx context.Context, changeSet rerun.ChangeSet) {
	if changeSet.Error != nil {
		r.sink.emit(event{kind: evWatcherErr, target: r.target.Name, err: changeSet.Error})
		return
	}

	// A target the user stopped waits for space or r, not for files.
	if r.userStopped() {
		return
	}

	r.killCurrent(ctx)

	r.waitCooldown(ctx)
	if ctx.Err() != nil {
		return
	}

	r.sink.emit(event{kind: evChanged, target: r.target.Name, count: len(changeSet.Files), first: changeSet.FirstFile})

	r.startReporting()
}

// startReporting starts a generation, reporting a spawn failure in the
// target's stream instead of taking the whole run down.
func (r *targetRunner) startReporting() {
	if err := r.start(); err != nil {
		r.sink.emit(event{kind: evRunnerErr, target: r.target.Name, err: err})
	}
}

// start spawns a new generation.
func (r *targetRunner) start() error {
	var (
		cmd rerun.Command
		pw  *os.File
		err error
	)
	if r.raw() {
		// The ad-hoc command is passed through raw: no pipe, no prefixing.
		cmd, err = rerun.StartCommand([]string{r.target.Cmd}, r.target.Grace, rerun.WithStdout(r.out))
	} else {
		cmd, pw, err = r.startPiped()
	}
	if err != nil {
		return err
	}

	exited := r.watchGeneration(cmd, pw)

	r.mu.Lock()
	r.cmd = cmd
	r.lastStart = time.Now()
	started := r.lastStart
	r.mu.Unlock()

	r.exited = exited
	r.sink.emit(event{kind: evStarted, target: r.target.Name, at: started})
	return nil
}

// startPiped spawns a generation whose output is reported line by line.
func (r *targetRunner) startPiped() (rerun.Command, *os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	cmd, err := rerun.StartCommand([]string{r.target.Cmd}, r.target.Grace,
		rerun.WithStdout(pw), rerun.WithStderr(pw))
	if err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, nil, err
	}

	go r.readLines(pr)
	return cmd, pw, nil
}

// watchGeneration closes the pipe writer once the generation is reaped, which
// unblocks the readLines side.
func (r *targetRunner) watchGeneration(cmd rerun.Command, pw *os.File) <-chan struct{} {
	exited := make(chan struct{})
	go func() {
		_ = cmd.WaitExit(context.Background())
		if pw != nil {
			_ = pw.Close()
		}
		close(exited)
	}()
	return exited
}

func (r *targetRunner) readLines(pr *os.File) {
	defer pr.Close()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		r.sink.emit(event{kind: evOutput, target: r.target.Name, line: scanner.Text()})
	}
	// A line past the cap ends the read, so say so instead of going quiet.
	if err := scanner.Err(); err != nil {
		r.sink.emit(event{kind: evOutput, target: r.target.Name, line: fmt.Sprintf("rerun: reading output: %v", err)})
	}
}

func (r *targetRunner) reportExit() {
	// A kill rerun performed is not the target's exit.
	if r.stoppingNow() || r.userStopped() {
		return
	}

	cmd := r.currentCmd()
	if cmd == nil {
		return
	}

	code, err := cmd.ExitState()
	r.sink.emit(event{
		kind:   evExited,
		target: r.target.Name,
		status: describeExit(code, err),
		code:   code,
		err:    err,
	})
}

// raw reports whether the target passes output through unprefixed. The ad-hoc
// command is the only nameless target.
func (r *targetRunner) raw() bool { return r.target.Name == "" }

// killCurrent stops the current generation, waiting the grace period.
func (r *targetRunner) killCurrent(ctx context.Context) {
	cmd := r.currentCmd()
	if cmd == nil || cmd.Exited() {
		return
	}
	_ = cmd.Kill()
	_ = cmd.WaitExit(ctx)
}

// waitCooldown holds a restart back until the cooldown since the last start.
func (r *targetRunner) waitCooldown(ctx context.Context) {
	if r.target.Cooldown <= 0 {
		return
	}

	r.mu.Lock()
	last := r.lastStart
	r.mu.Unlock()

	remaining := r.target.Cooldown - time.Since(last)
	if remaining <= 0 {
		return
	}

	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (r *targetRunner) userStopped() bool {
	return atomic.LoadInt32(&r.stopped) != 0
}

func (r *targetRunner) stoppingNow() bool {
	return atomic.LoadInt32(&r.stopping) != 0
}

func (r *targetRunner) markStopping() {
	atomic.StoreInt32(&r.stopping, 1)
}

func (r *targetRunner) stop() {
	cmd := r.currentCmd()
	if cmd == nil {
		return
	}
	_ = cmd.Kill()
}

func (r *targetRunner) force() {
	cmd := r.currentCmd()
	if cmd == nil {
		return
	}
	_ = cmd.KillForce()
}

func (r *targetRunner) currentCmd() rerun.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd
}

const (
	// clearScrollbackXterm clears the scrollback buffer in terminals
	// supporting the standard xterm sequence ESC[3J.
	clearScrollbackXterm = "\033[3J"

	// clearScreen moves the cursor to the home position (ESC[H)
	// and clears the visible screen (ESC[2J).
	clearScreen = "\033[H\033[2J"

	// clearScrollbackITerm is iTerm2's proprietary escape code that clears
	// the entire scrollback buffer. Required because iTerm2 ignores ESC[3J.
	clearScrollbackITerm = "\033]1337;ClearScrollback\a"

	// clearTerminal combines all sequences above to thoroughly clear both
	// the visible screen and full scrollback history across major terminals.
	clearTerminal = clearScrollbackXterm + clearScreen + clearScrollbackITerm

	greenColor = "\033[32m"
	resetColor = "\033[0m"
)
