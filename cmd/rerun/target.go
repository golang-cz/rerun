package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/golang-cz/rerun"
)

// Timing knobs, package vars so tests can shrink them to milliseconds.
var (
	// staggerLimit is how many targets may be starting at the same time.
	staggerLimit = 4
	// staggerSlotTimeout bounds how long a quiet target may hold a start slot.
	staggerSlotTimeout = 10 * time.Second

	// backoffWindow is the span the restarts that trigger a backoff land in,
	// and the age past which a run stops counting as a crash.
	backoffWindow = 30 * time.Second
	// backoffDelays is the wait before the 3rd, 4th, ... restart in a window.
	backoffDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second}

	// loopCycles is how many consecutive self-written re-triggers must share a
	// file before rerun calls it a loop and pauses the target.
	loopCycles = 6
	// loopSkew is how close to a build or run boundary a file's mtime must sit to
	// count as that process's own write; a human cannot save this fast.
	loopSkew = 150 * time.Millisecond
	// loopQuiet is the gap without a held change that ends a pause, and
	// loopMaxPause the longest a pause lasts, so a save is never held for good.
	loopQuiet    = 5 * time.Second
	loopMaxPause = 30 * time.Second
)

// errStartAborted reports a start cancelled before it spawned, which is not a
// spawn failure the front-end should print.
var errStartAborted = errors.New("start aborted")

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
	// Ad-hoc children inherit stdin, so they can leave the tty in raw mode.
	tty := saveTTY()
	defer tty.restore()

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

// startGate bounds how many targets may be starting at once, so a config with
// many targets does not launch them all in the same instant. Callers queue in
// rerun.yml order and are served in it.
type startGate struct {
	limit int

	mu      sync.Mutex
	active  int
	waiters []*gateWaiter
}

type gateWaiter struct {
	order   int // the runner's config index, the queue's sort key
	granted bool
	grant   chan struct{}
}

func newStartGate(limit int) *startGate {
	if limit < 1 {
		limit = 1
	}
	return &startGate{limit: limit}
}

// acquire takes a starting slot, calling onQueue once when the wait begins.
// ctrl is the control that aborted the wait, or nil; ok is false when ctx,
// retire, or ctrl aborted it.
func (g *startGate) acquire(ctx context.Context, retire <-chan struct{}, ctrl <-chan ctrlMsg, order int, onQueue func()) (release func(), abort *ctrlMsg, ok bool) {
	g.mu.Lock()
	// A free slot still waits for a lower-order waiter already queued. The
	// initial burst has no waiters, so its order stays goroutine order.
	if !g.overtakenLocked(order) && g.active < g.limit {
		g.active++
		g.mu.Unlock()
		return g.slot(), nil, true
	}
	w := &gateWaiter{order: order, grant: make(chan struct{})}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()

	if onQueue != nil {
		onQueue()
	}

	select {
	case <-w.grant:
		return g.slot(), nil, true
	case <-ctx.Done():
	case <-retire:
	case c := <-ctrl:
		abort = &c
	}

	// The grant raced the abort: whoever loses hands the slot back.
	g.mu.Lock()
	if w.granted {
		g.releaseLocked()
	} else {
		g.removeLocked(w)
	}
	g.mu.Unlock()
	return nil, abort, false
}

// overtakenLocked reports whether a lower-order waiter is already queued.
func (g *startGate) overtakenLocked(order int) bool {
	for _, w := range g.waiters {
		if w.order < order {
			return true
		}
	}
	return false
}

// slot returns the release for one starting slot. The first call wins.
func (g *startGate) slot() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.releaseLocked()
			g.mu.Unlock()
		})
	}
}

// releaseLocked frees one slot, handing it to the lowest-index waiter.
func (g *startGate) releaseLocked() {
	if len(g.waiters) == 0 {
		g.active--
		return
	}
	i := 0
	for j := 1; j < len(g.waiters); j++ {
		if g.waiters[j].order < g.waiters[i].order {
			i = j
		}
	}
	w := g.waiters[i]
	g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
	w.granted = true
	close(w.grant)
}

// removeLocked drops a waiter that was not granted. It reports whether the
// waiter was still queued.
func (g *startGate) removeLocked(w *gateWaiter) bool {
	for i, x := range g.waiters {
		if x == w {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			return true
		}
	}
	return false
}

// startSlot is the gate slot one generation holds while it starts. The first
// of its first output line, its exit, or the timeout frees it.
type startSlot struct {
	release func()
	once    sync.Once
	timer   *time.Timer
}

func newStartSlot(release func()) *startSlot {
	s := &startSlot{release: release}
	s.timer = time.AfterFunc(staggerSlotTimeout, s.free)
	return s
}

func (s *startSlot) free() {
	s.once.Do(func() {
		s.timer.Stop()
		s.release()
	})
}

// targetGroup owns one runner per target and the process lifecycle both
// front-ends share. A config reload can add, drop, and reorder runners, so the
// two lists are swapped under mu and read through a snapshot.
type targetGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	sink   sink
	// gate staggers the targets' starts; every runner holds it while spawning.
	gate *startGate

	mu      sync.Mutex
	targets []Target
	runners []*targetRunner
	// queries is the config's global saved-query set as last emitted to the
	// front-end, so a queries-only edit still fires a reconcile.
	queries map[string]string
	// missing is the optional imports whose file is absent at the last load.
	missing []missingImport
	closing bool

	// configPath is the rerun.yml this group reloads from; "" keeps the config
	// watch off, which is the ad-hoc path.
	configPath string
	// soloGuard keeps a target added while one stream is soloed from starting
	// itself: the user is focused on a single target, not on new rows.
	soloGuard atomic.Bool

	// builds shares one build process among the targets that run the same command in the same directory.
	builds *buildShare

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
		gate:       newStartGate(staggerLimit),
		targets:    append([]Target(nil), targets...),
		configPath: configPath,
		builds:     newBuildShare(),
		done:       make(chan struct{}),
	}
	for i, target := range targets {
		r := newTargetRunner(target, s)
		r.gate = g.gate
		r.builds = g.builds
		r.order.Store(int64(i))
		g.runners = append(g.runners, r)
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
	// ctrlSpec swaps in a spec whose cmd changed: a running generation bounces
	// so the new cmd applies, and an auto target restarts even if it exited.
	// Nothing wakes a target with no generation of its own.
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
	// be runs the target: the process, or the compose service.
	be backend

	ctrlCh   chan ctrlMsg
	finished chan struct{}
	// retire asks a runner whose target left the config to stop for good.
	retire chan struct{}
	// watchDone releases the watcher-closing goroutine when run returns.
	watchDone chan struct{}
	// stash is a control drainSpecs pulled out of the queue behind a spec swap,
	// waiting for the run loop to handle it in order. Run goroutine only.
	stash *ctrlMsg
	// watcher is the live file watcher, published so a shutdown can abort the
	// walk Watch starts even while the run goroutine is stuck in it.
	watcher atomic.Pointer[rerun.Watcher]

	// gate and order are the runner's place in the group's start stagger, which
	// a config reload can reorder. A nil gate means no stagger, which is how
	// the unit tests build a runner.
	gate  *startGate
	order atomic.Int64
	// selfExited, lastRun, and restarts track the crash loop the backoff
	// guards against. Run goroutine only.
	selfExited bool
	lastRun    time.Duration
	restarts   []time.Time

	// fastCycles holds the self-written files of the recent re-triggers, oldest
	// first; loopPaused is the set that tripped the breaker. The build times
	// anchor the mtime test. Run goroutine only.
	fastCycles                   [][]string
	loopPaused                   map[string]struct{}
	loopPausedAt, loopHeldAt     time.Time
	lastBuildStart, lastBuildEnd time.Time

	mu        sync.Mutex
	lastStart time.Time
	stopping  int32
	// stopped is the user's stop: the target stays down until space or r.
	stopped int32

	// exited is closed when the current generation is reaped. Only the run
	// goroutine reads it, so no lock is needed.
	exited <-chan struct{}

	// builds is the group's build share, set before the runner starts. builds and build are the runner's view of it.
	builds *buildShare
	// build is this runner's seat in the shared build in flight, nil when none. Run goroutine only.
	build *buildJob
	// meta is what the next evStarted reports. Run goroutine only.
	meta startMeta
	// probes counts the health goroutines, so run can wait them out on return.
	probes sync.WaitGroup
}

// startMeta is what a spawn reports about why it happened.
type startMeta struct {
	files    []string
	buildDur time.Duration
	prevUp   time.Duration
	restart  bool
}

func newTargetRunner(target Target, s sink) *targetRunner {
	r := &targetRunner{
		target:    target,
		sink:      s,
		ctrlCh:    make(chan ctrlMsg, 16),
		finished:  make(chan struct{}),
		retire:    make(chan struct{}),
		watchDone: make(chan struct{}),
		builds:    newBuildShare(),
	}
	r.be = newBackend(target, s, &r.probes)
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
	// A finished runner reports the miss first: ctrlCh can still be writable,
	// and a random select would otherwise claim a send that nobody reads.
	select {
	case <-r.finished:
		return false
	default:
	}
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
	watcher.Add(inDir(r.target.Dir, on)...)
	if len(r.target.Except) > 0 {
		watcher.Ignore(exceptPaths(r.target.Dir, r.target.Except)...)
	}
	return watcher.Watch(r.target.Delay)
}

// inDir resolves relative paths against the defining file's dir, so on: and
// except: mean the same from any working directory. An empty dir keeps them as written.
func inDir(dir string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if dir == "" || filepath.IsAbs(p) {
			out[i] = p
		} else {
			out[i] = filepath.Join(dir, p)
		}
	}
	return out
}

// exceptPaths resolves except: entries. A glob with no "/" stays as written, since
// the watcher matches it against any trailing path segments.
func exceptPaths(dir string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if strings.ContainsAny(p, "*?[") && !strings.Contains(p, "/") {
			out[i] = p
			continue
		}
		out[i] = inDir(dir, []string{p})[0]
	}
	return out
}

func (r *targetRunner) run(ctx context.Context) error {
	defer close(r.finished)
	defer close(r.watchDone)
	defer r.probes.Wait()
	// The backend's own goroutines (compose events) end with the run, before the wait above.
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	r.probes.Add(1)
	go func() {
		defer r.probes.Done()
		r.be.Serve(serveCtx, r.sink.emit)
	}()

	watcher, changes, err := r.rewatch()
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

	handleCtrl := func(c ctrlMsg) {
		if c.kind == ctrlRewatch {
			wasAuto := r.target.Start == startAuto
			if c.target != nil {
				r.target = *c.target
			}
			if w, ch, err := r.rewatch(); err != nil {
				r.sink.emit(event{kind: evWatcherErr, target: r.target.Name, err: err})
			} else {
				if watcher != nil {
					_ = watcher.Close()
				}
				watcher, changes = w, ch
			}
			// A target switched to auto that never ran starts now.
			if r.target.Start == startAuto && !wasAuto && !r.userStopped() && r.currentCmd() == nil {
				r.launch(ctx)
			}
			return
		}
		r.handleControl(ctx, c)
	}

	// Start before the watch walk delivers: the walk can take seconds on a big
	// tree, and the command's output must not wait for it. A run cancelled
	// while the watcher was built starts nothing. onchange and manual keep the
	// first generation back, for a change and for r.
	if ctx.Err() == nil {
		if !r.userStopped() && r.target.Start == startAuto {
			if r.target.Build != "" {
				r.startBuild(nil)
			} else if err := r.start(ctx); err != nil && !errors.Is(err, errStartAborted) {
				return err
			}
		}
		r.sink.emit(event{kind: evCleared, target: r.target.Name})
	}

	for {
		if r.stash != nil {
			c := *r.stash
			r.stash = nil
			handleCtrl(c)
			continue
		}
		select {
		case <-ctx.Done():
			r.cancelBuild()
			r.be.Release(context.Background())
			return nil

		case <-r.retire:
			// A target dropped from the config stops for good.
			r.cancelBuild()
			r.be.Release(context.Background())
			return nil

		case res := <-r.buildResult():
			r.finishBuild(ctx, res)

		case changeSet, ok := <-changes:
			if !ok {
				r.cancelBuild()
				r.be.Release(context.Background())
				return nil
			}
			r.handleChange(ctx, changeSet)

		case c := <-r.ctrlCh:
			handleCtrl(c)

		case <-r.exited:
			r.reportExit()
			r.exited = nil
		}
	}
}

// rewatch builds the target's watcher from its current spec, leaving the
// running process alone. A manual target watches nothing: no watcher, no walk.
func (r *targetRunner) rewatch() (*rerun.Watcher, <-chan rerun.ChangeSet, error) {
	if r.target.Start == startManual {
		return nil, nil, nil
	}
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
		r.forgetCrashLoop()
		r.cancelBuild()
		r.killCurrent(ctx)

	case ctrlStart:
		atomic.StoreInt32(&r.stopped, 0)
		r.forgetCrashLoop()
		r.clearLoop()
		if r.be.Running() {
			return
		}
		if r.build != nil {
			return
		}
		r.launch(ctx)

	case ctrlReboot:
		atomic.StoreInt32(&r.stopped, 0)
		r.forgetCrashLoop()
		r.clearLoop()
		r.restart(ctx)

	case ctrlSpec:
		// A burst of queued reloads collapses into one bounce: only the newest
		// spec needs its own restart.
		r.drainSpecs()
		r.clearLoop()
		// A target the user stopped only takes the new spec: a config edit
		// never surprises a stopped target into running, and a manual target
		// only ever starts on r. An onchange target waits for its
		// change just the same.
		running := r.be.Running()
		if !r.userStopped() && (r.target.Start == startAuto || running) {
			r.restart(ctx)
		}
	}
}

// drainSpecs consumes queued spec swaps and keeps the newest. The first
// control of another kind waits in stash for the run loop, so a rewatch or a
// stop queued behind the burst is not lost.
func (r *targetRunner) drainSpecs() {
	for {
		select {
		case c := <-r.ctrlCh:
			if c.kind != ctrlSpec {
				r.stash = &c
				return
			}
			if c.target != nil {
				r.target = *c.target
			}
		default:
			return
		}
	}
}

// restart bounces the current generation, respecting the cooldown.
func (r *targetRunner) restart(ctx context.Context) {
	if rs, ok := r.be.(restarter); ok {
		rs.Restart(ctx)
		return
	}
	if r.target.Build != "" {
		r.restartBuild()
		return
	}
	r.cancelBuild()
	r.noteReplace(nil)
	r.killCurrent(ctx)
	r.waitCooldown(ctx)
	if ctx.Err() != nil || r.retired() {
		r.meta = startMeta{}
		return
	}
	r.spawn(ctx)
}

func (r *targetRunner) handleChange(ctx context.Context, changeSet rerun.ChangeSet) {
	// The error rides the change batch into the feed; no target triggers for it.
	if changeSet.Error != nil {
		r.reportChange(changeSet, false)
		return
	}

	// A target the user stopped waits for space or r, not for files. The batch
	// is still reported, so the row can say which files it did not use.
	if r.userStopped() {
		r.reportChange(changeSet, false)
		return
	}

	if r.loopHeld(changeSet) {
		r.reportChange(changeSet, false)
		return
	}

	if r.target.Build != "" {
		r.reportChange(changeSet, true)
		r.startBuild(changeSet.Files)
		return
	}

	r.noteReplace(changeSet.Files)
	r.killCurrent(ctx)

	r.waitCooldown(ctx)
	if ctx.Err() != nil || r.retired() {
		r.meta = startMeta{}
		return
	}

	r.reportChange(changeSet, true)
	r.spawn(ctx)
}

// Holding the change keeps the last good process up instead of feeding the loop.
func (r *targetRunner) loopHeld(changeSet rerun.ChangeSet) bool {
	if r.loopPaused != nil {
		now := time.Now()
		expired := now.Sub(r.loopHeldAt) >= loopQuiet || now.Sub(r.loopPausedAt) >= loopMaxPause
		inSet := true
		for f := range changeSet.Files {
			if _, ok := r.loopPaused[f]; !ok {
				inSet = false
				break
			}
		}
		if !inSet || expired {
			r.clearLoop()
		} else {
			r.loopHeldAt = now
			files := sortedFiles(changeSet.Files)
			r.sink.emit(event{kind: evLoop, target: r.target.Name, line: strings.Join(files, ", "), files: files})
			return true
		}
	}

	paths := r.trackFastCycle(changeSet)
	if len(paths) == 0 {
		return false
	}
	r.loopPaused = make(map[string]struct{}, len(paths))
	for _, p := range paths {
		r.loopPaused[p] = struct{}{}
	}
	r.loopPausedAt, r.loopHeldAt = time.Now(), time.Now()
	r.sink.emit(event{kind: evLoop, target: r.target.Name, line: strings.Join(paths, ", "), files: paths})
	return true
}

func (r *targetRunner) clearLoop() {
	r.loopPaused = nil
	r.fastCycles = r.fastCycles[:0]
}

// A cycle without a self-written file resets the streak, so one human save breaks it.
func (r *targetRunner) trackFastCycle(changeSet rerun.ChangeSet) []string {
	paths := r.selfWritten(changeSet)
	if len(paths) == 0 {
		r.fastCycles = r.fastCycles[:0]
		return nil
	}
	r.fastCycles = append(r.fastCycles, paths)
	if len(r.fastCycles) > loopCycles {
		r.fastCycles = r.fastCycles[len(r.fastCycles)-loopCycles:]
	}
	if len(r.fastCycles) < loopCycles {
		return nil
	}

	var shared []string
	for _, path := range r.fastCycles[0] {
		inAll := true
		for _, set := range r.fastCycles[1:] {
			if !slices.Contains(set, path) {
				inAll = false
				break
			}
		}
		if inAll {
			shared = append(shared, path)
		}
	}
	return shared
}

// selfWritten lists the changed files whose mtime hugs a boundary of this
// target's own build or run. fsnotify names no writer, so timing is the only tell.
func (r *targetRunner) selfWritten(changeSet rerun.ChangeSet) []string {
	r.mu.Lock()
	starts := []time.Time{r.lastStart, r.lastBuildStart}
	r.mu.Unlock()
	ends := []time.Time{r.lastBuildEnd}
	if r.build != nil {
		started, ended := r.builds.window(r.build)
		starts = append(starts, started)
		if ended != 0 {
			ends = append(ends, time.Unix(0, ended))
		}
	}

	const maxStats = 64
	var out []string
	for i, path := range sortedFiles(changeSet.Files) {
		if i == maxStats {
			break
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		m := info.ModTime()
		if nearAfter(m, starts) || nearBefore(m, ends) {
			out = append(out, path)
		}
	}
	return out
}

// The small lead absorbs the gap between fork and the start timestamp.
func nearAfter(m time.Time, starts []time.Time) bool {
	for _, b := range starts {
		if d := m.Sub(b); !b.IsZero() && d >= -50*time.Millisecond && d < loopSkew {
			return true
		}
	}
	return false
}

// Builds write their output last, just before they exit.
func nearBefore(m time.Time, ends []time.Time) bool {
	for _, b := range ends {
		if d := m.Sub(b); !b.IsZero() && d > -loopSkew && d <= 50*time.Millisecond {
			return true
		}
	}
	return false
}

// reportChange tells the front-end which files changed, and whether a spawn
// follows.
func (r *targetRunner) reportChange(changeSet rerun.ChangeSet, restarts bool) {
	r.sink.emit(event{
		kind:     evChanged,
		target:   r.target.Name,
		count:    len(changeSet.Files),
		first:    changeSet.FirstFile,
		change:   changeSet,
		restarts: restarts,
		at:       time.Now(),
	})
}

// spawn starts a generation, waiting out the restart backoff first. A spawn
// failure is reported in the target's own stream instead of taking the whole
// run down.
func (r *targetRunner) spawn(ctx context.Context) {
	if !r.waitBackoff(ctx) {
		r.meta = startMeta{}
		return
	}
	if err := r.start(ctx); err != nil && !errors.Is(err, errStartAborted) {
		r.sink.emit(event{kind: evRunnerErr, target: r.target.Name, err: err})
	}
}

// start spawns a new generation, holding a stagger slot until it is under way.
func (r *targetRunner) start(ctx context.Context) error {
	var slot *startSlot
	if r.gate != nil {
		if r.stash != nil {
			r.meta = startMeta{}
			return errStartAborted // the run loop drains the stash first
		}
		release, abort, ok := r.gate.acquire(ctx, r.retire, r.ctrlCh, int(r.order.Load()), func() {
			// Queued, not spawned yet: the row shows `starting` until evStarted.
			r.sink.emit(event{kind: evStarting, target: r.target.Name})
		})
		if !ok {
			if abort != nil {
				r.stash = abort
			}
			r.meta = startMeta{}
			return errStartAborted
		}
		slot = newStartSlot(release)
	}

	exited, err := r.be.Start(ctx, r.target, slot, r.announce)
	if err != nil {
		r.meta = startMeta{}
		return err
	}
	r.exited = exited
	if _, ok := r.be.(*processBackend); !ok {
		// Without an announce, a backend's own start event reports the run, so the runner resets its state here.
		r.mu.Lock()
		r.lastStart = time.Now()
		r.mu.Unlock()
		r.meta = startMeta{}
	}
	return nil
}

// announce reports a generation that just came up, as the backend starts its health probe.
func (r *targetRunner) announce(started time.Time) {
	r.mu.Lock()
	r.lastStart = started
	r.mu.Unlock()
	r.selfExited = false

	meta := r.meta
	r.meta = startMeta{}
	r.sink.emit(event{
		kind: evStarted, target: r.target.Name, at: started,
		files: meta.files, buildDur: meta.buildDur, prevUp: meta.prevUp, restart: meta.restart,
	})
}

func (r *targetRunner) reportExit() {
	// A kill rerun performed is not the target's exit.
	if r.stoppingNow() || r.userStopped() {
		return
	}

	code, err, ok := r.be.ExitState()
	if !ok {
		return
	}

	r.mu.Lock()
	r.lastRun = time.Since(r.lastStart)
	r.mu.Unlock()
	r.selfExited = true

	r.sink.emit(event{
		kind:   evExited,
		target: r.target.Name,
		status: describeExit(code, err),
		code:   code,
		err:    err,
		at:     time.Now(),
	})
}

// forgetCrashLoop drops the restart window: the user took over, so the next
// start must not be delayed by an earlier crash loop.
func (r *targetRunner) forgetCrashLoop() {
	r.selfExited = false
	r.restarts = r.restarts[:0]
}

// waitBackoff holds a restart back while the target keeps dying young. It
// reports false when the wait was aborted, not completed.
func (r *targetRunner) waitBackoff(ctx context.Context) bool {
	if !r.selfExited {
		return true
	}
	if r.lastRun >= backoffWindow {
		// A run that outlived the window is no longer a crash loop, so its
		// restart neither counts nor needs a delay.
		r.restarts = r.restarts[:0]
		return true
	}

	cut := time.Now().Add(-backoffWindow)
	kept := r.restarts[:0]
	for _, at := range r.restarts {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	r.restarts = append(kept, time.Now())

	d := backoffDelay(len(r.restarts))
	if d <= 0 {
		return true
	}
	r.sink.emit(event{kind: evBackoff, target: r.target.Name, delay: d, ordinal: len(r.restarts)})
	if !r.waitDelay(ctx, d) {
		// The next spawn pays the same delay again, so one crash never counts
		// as two ordinals.
		r.restarts = r.restarts[:len(r.restarts)-1]
		return false
	}
	return true
}

// backoffDelay is the wait before the ordinal-th restart in the window.
func backoffDelay(ordinal int) time.Duration {
	if ordinal < 3 {
		return 0
	}
	return backoffDelays[min(ordinal-3, len(backoffDelays)-1)]
}

// waitDelay sleeps out d, aborting on ctx, retire, or a queued stop. A control
// already stashed for the run loop wins over the delay.
func (r *targetRunner) waitDelay(ctx context.Context, d time.Duration) bool {
	if r.stash != nil {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-r.retire:
		return false
	case c := <-r.ctrlCh:
		r.stash = &c
		return false
	}
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

// killCurrent stops the current generation, waiting the grace period.
func (r *targetRunner) killCurrent(ctx context.Context) {
	r.be.Stop(ctx)
}

func (r *targetRunner) stop() {
	r.be.Kill(false)
}

func (r *targetRunner) force() {
	r.be.Kill(true)
}

// currentCmd is the live process handle, or nil when the target is not a process.
func (r *targetRunner) currentCmd() rerun.Command {
	if p, ok := r.be.(*processBackend); ok {
		return p.current()
	}
	return nil
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
