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
func runTargets(targets []Target, out io.Writer, configPath string, signals <-chan os.Signal) int {
	if out == nil {
		out = os.Stdout
	}
	signals, stopSignals := signalChannel(signals)
	defer stopSignals()

	group := newTargetGroup(targets, newPlainSink(out), configPath)
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
// front-ends share. A config reload can add, drop, and reorder runners, so the
// two lists are swapped under mu and read through a snapshot.
type targetGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	sink   sink

	mu      sync.Mutex
	targets []Target
	runners []*targetRunner
	closing bool

	// configPath is the rerun.yml this group reloads from; "" keeps the config
	// watch off, which is the ad-hoc path.
	configPath string
	// soloGuard keeps a target added while one stream is soloed from starting
	// itself: the user is focused on a single target, not on new rows.
	soloGuard atomic.Bool

	done   chan struct{}
	failed int32
	wg     sync.WaitGroup
}

func newTargetGroup(targets []Target, s sink, configPath string) *targetGroup {
	ctx, cancel := context.WithCancel(context.Background())
	g := &targetGroup{
		ctx:        ctx,
		cancel:     cancel,
		sink:       s,
		targets:    append([]Target(nil), targets...),
		configPath: configPath,
		done:       make(chan struct{}),
	}
	for _, target := range targets {
		g.runners = append(g.runners, newTargetRunner(target, s))
	}
	return g
}

func (g *targetGroup) start() {
	runners, _ := g.snapshot()
	for _, r := range runners {
		g.wg.Add(1)
		go g.runTarget(r)
	}
	go func() {
		g.wg.Wait()
		close(g.done)
	}()
	if g.configPath != "" {
		go g.watchConfig(g.configPath)
	}
}

// runTarget is one runner's goroutine: a failure to start takes the run's
// exit code down but never the other targets.
func (g *targetGroup) runTarget(r *targetRunner) {
	defer g.wg.Done()
	if err := r.run(g.ctx); err != nil {
		atomic.StoreInt32(&g.failed, 1)
		g.sink.emit(event{kind: evRunnerErr, target: r.target.Name, err: err})
	}
}

// snapshot returns the runners and their specs together, so a reload cannot
// swap one between the two.
func (g *targetGroup) snapshot() ([]*targetRunner, []Target) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*targetRunner(nil), g.runners...), append([]Target(nil), g.targets...)
}

// snapshotTargets is the spec list alone, which the config diff reads.
func (g *targetGroup) snapshotTargets() []Target {
	_, targets := g.snapshot()
	return targets
}

// setSoloGuard tells the group whether the TUI is soloed, which decides
// whether a newly added target starts by itself.
func (g *targetGroup) setSoloGuard(on bool) { g.soloGuard.Store(on) }

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
	// Refuse reloads first: no runner may be born while the group winds down.
	g.mu.Lock()
	g.closing = true
	runners := append([]*targetRunner(nil), g.runners...)
	g.mu.Unlock()

	for _, r := range runners {
		r.markStopping()
	}

	var forced int32
	stop := make(chan struct{})
	go func() {
		select {
		case <-signals:
			atomic.StoreInt32(&forced, 1)
			for _, r := range runners {
				r.force()
			}
		case <-stop:
		}
	}()

	var killWG sync.WaitGroup
	for _, r := range runners {
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
	// ctrlSpec swaps in a spec whose cmd changed: the process restarts unless
	// the user stopped the target.
	ctrlSpec
	// ctrlRewatch swaps in a spec whose watched set changed: only the file
	// watcher is rebuilt, the process is left alone.
	ctrlRewatch
)

type ctrlMsg struct {
	kind ctrlKind
	// target is the fresh spec a spec-changing control carries; nil leaves the
	// runner's own spec alone.
	target *Target
}

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
	// retire asks a runner whose target left the config to stop for good.
	retire chan struct{}
	// watchDone releases the watcher-closing goroutine when run returns.
	watchDone chan struct{}
	// watcher is the live file watcher, published so a shutdown can abort the
	// walk Watch starts even while the run goroutine is stuck in it.
	watcher atomic.Pointer[rerun.Watcher]

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
		target:    target,
		sink:      s,
		ctrlCh:    make(chan ctrlMsg, 16),
		finished:  make(chan struct{}),
		retire:    make(chan struct{}),
		watchDone: make(chan struct{}),
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
// reports false when the control never reached the runner: the queue is full,
// or the runner has returned. A full queue drops the command instead of
// blocking the caller, since a burst of key autorepeat asks for what the
// queued commands already do and stalling the TUI's event loop is worse.
func (r *targetRunner) control(kind ctrlKind) bool {
	return r.send(ctrlMsg{kind: kind})
}

// controlSpec hands the run goroutine a target whose spec changed. ctrlSpec
// restarts the process; ctrlRewatch only rebuilds the file watcher.
func (r *targetRunner) controlSpec(kind ctrlKind, target Target) bool {
	return r.send(ctrlMsg{kind: kind, target: &target})
}

func (r *targetRunner) send(msg ctrlMsg) bool {
	select {
	case r.ctrlCh <- msg:
		return true
	case <-r.finished:
		return false
	default:
		return false
	}
}

// retired reports whether the target left the config.
func (r *targetRunner) retired() bool {
	select {
	case <-r.retire:
		return true
	default:
		return false
	}
}

// newWatcher builds a watcher for the runner's current spec and publishes it,
// so a shutdown can abort the walk Watch is about to start. The caller closes
// it.
func (r *targetRunner) newWatcher() (*rerun.Watcher, error) {
	watcher, err := rerun.NewWatcher()
	if err != nil {
		return nil, err
	}
	watcher.RespectGitignore(!r.target.NoGitignore)
	r.watcher.Store(watcher)
	return watcher, nil
}

// startWatch registers the spec's paths on a fresh watcher and begins the
// walk. Each watcher takes one Watch call.
func (r *targetRunner) startWatch(watcher *rerun.Watcher) <-chan rerun.ChangeSet {
	on := r.target.On
	if len(on) == 0 {
		on = []string{"./"}
	}
	watcher.Add(on...)
	if len(r.target.Except) > 0 {
		watcher.Ignore(r.target.Except...)
	}
	return watcher.Watch(r.target.Delay)
}

func (r *targetRunner) run(ctx context.Context) error {
	defer close(r.finished)
	defer close(r.watchDone)

	watcher, err := r.newWatcher()
	if err != nil {
		return err
	}
	// The pointer, not the local, is what the closer below and every rewatch
	// publish, so this closes whatever is live when the runner returns.
	defer func() {
		if w := r.watcher.Swap(nil); w != nil {
			_ = w.Close()
		}
	}()

	// Close the watcher on shutdown: the initial walk below can take seconds on
	// a big tree and would otherwise hold the whole run back.
	go func() {
		select {
		case <-ctx.Done():
			if w := r.watcher.Load(); w != nil {
				_ = w.Close()
			}
		case <-r.watchDone:
		}
	}()

	changes := r.startWatch(watcher)

	// Start before the watch walk delivers: the walk can take seconds on a big
	// tree, and the command's output must not wait for it. A run cancelled
	// while the watcher was built starts nothing.
	if ctx.Err() == nil {
		if !r.userStopped() {
			if err := r.start(); err != nil {
				return err
			}
		}
		r.sink.emit(event{kind: evCleared, target: r.target.Name})
	}

	for {
		select {
		case <-ctx.Done():
			r.killCurrent(context.Background())
			return nil

		case <-r.retire:
			// A target dropped from the config stops for good.
			r.killCurrent(context.Background())
			return nil

		case changeSet, ok := <-changes:
			if !ok {
				r.killCurrent(context.Background())
				return nil
			}
			r.handleChange(ctx, changeSet)

		case c := <-r.ctrlCh:
			if c.kind == ctrlRewatch {
				if c.target != nil {
					r.target = *c.target
				}
				if w, ch, err := r.rewatch(); err != nil {
					r.sink.emit(event{kind: evWatcherErr, target: r.target.Name, err: err})
				} else {
					_ = watcher.Close()
					watcher, changes = w, ch
				}
				continue
			}
			r.handleControl(ctx, c)

		case <-r.exited:
			r.reportExit()
			r.exited = nil
		}
	}
}

// rewatch rebuilds the target's watcher from its current spec, leaving the
// running process alone.
func (r *targetRunner) rewatch() (*rerun.Watcher, <-chan rerun.ChangeSet, error) {
	watcher, err := r.newWatcher()
	if err != nil {
		return nil, nil, err
	}
	return watcher, r.startWatch(watcher), nil
}

// handleControl runs a front-end's start/stop/reboot request inside the run
// goroutine, so process state is never written from two goroutines.
func (r *targetRunner) handleControl(ctx context.Context, c ctrlMsg) {
	// A spec-changing control is written here, where the run goroutine is the
	// only reader of r.target.
	if c.target != nil {
		r.target = *c.target
	}

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
		r.restart(ctx)

	case ctrlSpec:
		// Its cmd changed. A target the user stopped only takes the new spec:
		// a config edit never surprises a stopped target into running.
		if r.userStopped() {
			return
		}
		r.restart(ctx)
	}
}

// restart bounces the current generation, respecting the cooldown.
func (r *targetRunner) restart(ctx context.Context) {
	r.killCurrent(ctx)
	r.waitCooldown(ctx)
	if ctx.Err() != nil || r.retired() {
		return
	}
	r.startReporting()
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

	r.sink.emit(event{
		kind:   evChanged,
		target: r.target.Name,
		count:  len(changeSet.Files),
		first:  changeSet.FirstFile,
		change: changeSet,
	})

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

	go r.readLines(pr, r.target.Name)
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

func (r *targetRunner) readLines(pr *os.File, name string) {
	defer pr.Close()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		r.sink.emit(event{kind: evOutput, target: name, line: scanner.Text()})
	}
	// A line past the cap ends the read, so say so instead of going quiet.
	if err := scanner.Err(); err != nil {
		r.sink.emit(event{kind: evOutput, target: name, line: fmt.Sprintf("rerun: reading output: %v", err)})
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
	case <-r.retire:
		// A target that left the config does not wait out the cooldown: its
		// run goroutine kills the process and returns.
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
