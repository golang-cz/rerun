//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/golang-cz/rerun"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startRunner boots runTargets from a rerun.yml fixture in a temp cwd. The
// returned stop function shuts the runner down once and returns its exit code.
func startRunner(t *testing.T, yml string) (*safeBuffer, func() int, string) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ConfigFileName), yml)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	out := &safeBuffer{}
	signals := make(chan os.Signal, 2)
	code := make(chan int, 1)
	go func() { code <- runTargets(cfg.Targets, out, cfg.Path, signals) }()

	var once sync.Once
	exitCode := -1
	stop := func() int {
		once.Do(func() {
			select {
			case signals <- syscall.SIGTERM:
			default:
			}
			select {
			case exitCode = <-code:
			case <-time.After(5 * time.Second):
				t.Error("runner did not stop after SIGTERM")
			}
		})
		return exitCode
	}
	t.Cleanup(func() { stop() })

	return out, stop, dir
}

func waitForOutput(t *testing.T, out *safeBuffer, sub string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output:\n%s", sub, out.String())
}

func waitForCount(t *testing.T, path string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := -1
	for time.Now().Before(deadline) {
		last = countLines(path)
		if last == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lines in %s, got %d", want, path, last)
}

// waitForStableCount fails as soon as path grows past want, and only passes
// once the whole window has elapsed without a new line. A negative assertion
// needs a window; polling makes a regression fail at once instead of late.
func waitForStableCount(t *testing.T, path string, want int, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if got := countLines(path); got != want {
			t.Fatalf("%s has %d lines within %v, want %d", path, got, window, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func TestRunTargetsParallelPrefixesOutput(t *testing.T) {
	out, stop, dir := startRunner(t, `a:
  on: src
  cmd: echo a > a.out && echo a-finished
b:
  on: src
  cmd: echo b > b.out && echo b-finished
`)

	waitForOutput(t, out, "[a] a-finished", 3*time.Second)
	waitForOutput(t, out, "[b] b-finished", 3*time.Second)
	waitForOutput(t, out, "[a] exited (status 0)", 3*time.Second)
	waitForOutput(t, out, "[b] exited (status 0)", 3*time.Second)

	if got := readFile(t, filepath.Join(dir, "a.out")); got != "a\n" {
		t.Errorf("a.out = %q, want %q", got, "a\n")
	}
	if got := readFile(t, filepath.Join(dir, "b.out")); got != "b\n" {
		t.Errorf("b.out = %q, want %q", got, "b\n")
	}

	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

func TestRunTargetsSelfExitDoesNotRestart(t *testing.T) {
	out, stop, dir := startRunner(t, `t:
  on: src
  cmd: echo tick >> count.txt
`)

	count := filepath.Join(dir, "count.txt")
	waitForOutput(t, out, "[t] exited (status 0)", 3*time.Second)
	waitForCount(t, count, 1, 2*time.Second)

	// A self-exiting target must stay down until something changes.
	waitForStableCount(t, count, 1, 300*time.Millisecond)

	writeFile(t, filepath.Join(dir, "src", "change.txt"), "x")
	waitForCount(t, count, 2, 3*time.Second)

	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

func TestRunTargetsReportsSignalExit(t *testing.T) {
	out, stop, _ := startRunner(t, `t:
  on: src
  cmd: kill -TERM $$
`)

	waitForOutput(t, out, "[t] exited (signal: terminated)", 3*time.Second)

	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

func TestRunTargetsCooldown(t *testing.T) {
	_, stop, dir := startRunner(t, `t:
  on: src
  cmd: echo tick >> count.txt; sleep 5
  delay: 20ms
  cooldown: 900ms
  grace: 200ms
`)

	count := filepath.Join(dir, "count.txt")
	waitForCount(t, count, 1, 3*time.Second)

	writeFile(t, filepath.Join(dir, "src", "a.txt"), "a")
	waitForCount(t, count, 2, 3*time.Second)

	// These changes must wait out the cooldown before restarting.
	writeFile(t, filepath.Join(dir, "src", "b.txt"), "b")
	writeFile(t, filepath.Join(dir, "src", "c.txt"), "c")
	writeFile(t, filepath.Join(dir, "src", "d.txt"), "d")

	waitForStableCount(t, count, 2, 500*time.Millisecond)

	waitForCount(t, count, 3, 3*time.Second)

	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// lineWith is the first output line containing sub.
func lineWith(out *safeBuffer, sub string) (string, bool) {
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, sub) {
			return line, true
		}
	}
	return "", false
}

// saveUntilLine saves a config and waits for the line it should produce. The
// save is repeated: like writeUntilChange, a fresh watch can miss the very
// first event, and a repeated identical save is a no-op for the targets.
func saveUntilLine(t *testing.T, out *safeBuffer, path, yml, sub string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		writeFile(t, path, yml)
		wait := 300 * time.Millisecond
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		time.Sleep(wait)
		if line, ok := lineWith(out, sub); ok {
			return line
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a line with %q; output:\n%s", sub, out.String())
		}
	}
}

// The config file is watched: saving it reconciles the running targets and
// reports the delta as one banner line, and a config rerun cannot parse leaves
// every running target alone.
func TestRunTargetsReloadsTheConfig(t *testing.T) {
	out, stop, dir := startRunner(t, `web:
  on: src
  cmd: echo web-up && sleep 30

api:
  on: src
  cmd: echo api-up && sleep 30
`)

	waitForOutput(t, out, "[web] web-up", 3*time.Second)
	waitForOutput(t, out, "[api] api-up", 3*time.Second)

	// One save: web's cmd changes and worker appears; api is untouched.
	reloaded := `web:
  on: src
  cmd: echo web-v2 && sleep 30

api:
  on: src
  cmd: echo api-up && sleep 30

worker:
  on: src
  cmd: echo worker-up && sleep 30
`
	banner := saveUntilLine(t, out, filepath.Join(dir, ConfigFileName), reloaded, "config reloaded", 5*time.Second)
	if !strings.Contains(banner, "~web(cmd)") || !strings.Contains(banner, "+worker") {
		t.Fatalf("banner = %q, want the cmd restart and the added target", banner)
	}
	waitForOutput(t, out, "[web] web-v2", 5*time.Second)
	waitForOutput(t, out, "[worker] worker-up", 5*time.Second)
	if got := out.String(); strings.Contains(got, "[api] exited") {
		t.Fatalf("an unrelated config change restarted api:\n%s", got)
	}

	// A save rerun cannot parse only reports itself.
	line := saveUntilLine(t, out, filepath.Join(dir, ConfigFileName), "web:\n  cmd:\n", "config reload failed", 5*time.Second)
	if !strings.Contains(line, `target "web"`) {
		t.Fatalf("error line = %q, want the parse error naming web", line)
	}
	time.Sleep(500 * time.Millisecond)
	if got := out.String(); strings.Contains(got, "[web] exited") || strings.Contains(got, "[worker] exited") {
		t.Fatalf("a broken config touched a running target:\n%s", got)
	}

	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

// pidOf reads one pid out of a file a target's shell wrote.
func pidOf(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				var pids []int
				for _, f := range fields {
					pid, err := strconv.Atoi(f)
					if err != nil {
						t.Fatalf("pid file %s holds %q", path, data)
					}
					pids = append(pids, pid)
				}
				return pids
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pids in %s within 3s", path)
	return nil
}

func assertDead(t *testing.T, pid int, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // ESRCH: the process is gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: pid %d is still alive after the shutdown", why, pid)
}

// A shutdown must kill the running child for real, not just stop the runner
// loop: the shell writes its own pid and its sleep's, and both must be gone.
func TestShutdownLeavesNoChildProcess(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ConfigFileName), `svc1:
  on: src
  cmd: 'sleep 60 & echo "$$ $!" > pids; wait'
`)
			if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)

			cfg, err := LoadConfig(dir)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}

			signals := make(chan os.Signal, 2)
			code := make(chan int, 1)
			go func() { code <- runTargets(cfg.Targets, io.Discard, cfg.Path, signals) }()

			pids := pidOf(t, filepath.Join(dir, "pids"))
			signals <- sig

			select {
			case got := <-code:
				if got != 0 {
					t.Errorf("exit code = %d, want 0 after one signal", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("rerun did not stop after the signal")
			}

			for _, pid := range pids {
				assertDead(t, pid, sig.String())
			}
		})
	}
}

// A file event racing a config removal must not restart a dropped target: the
// retire wins over the change.
func TestRetiredRunnerDoesNotRespawnOnChange(t *testing.T) {
	r := newTargetRunner(Target{
		Name:     "svc",
		Cmd:      "sleep 30",
		On:       []string{"src"},
		Grace:    rerun.DefaultGrace,
		Selected: true,
	}, newPlainSink(io.Discard))
	close(r.retire)

	r.handleChange(context.Background(), rerun.ChangeSet{Files: map[string]struct{}{"a.go": {}}})

	if cmd := r.currentCmd(); cmd != nil {
		_ = cmd.Kill()
		t.Fatal("a retired runner respawned on a change event")
	}
}

// A burst of queued spec swaps collapses to the newest, and a control queued
// behind it waits in stash instead of being lost.
func TestDrainSpecsKeepsLatestAndStashesOthers(t *testing.T) {
	r := newTargetRunner(recTarget("web", "web"), &recordingSink{})
	newer := recTarget("web", "web-v2")
	newest := recTarget("web", "web-v3")
	r.ctrlCh <- ctrlMsg{kind: ctrlSpec, target: &newer}
	r.ctrlCh <- ctrlMsg{kind: ctrlSpec, target: &newest}
	r.ctrlCh <- ctrlMsg{kind: ctrlStop}

	r.drainSpecs()

	if r.target.Cmd != "web-v3" {
		t.Fatalf("spec cmd = %q, want the newest swap", r.target.Cmd)
	}
	if r.stash == nil || r.stash.kind != ctrlStop {
		t.Fatalf("stash = %+v, want the queued stop", r.stash)
	}
	select {
	case c := <-r.ctrlCh:
		t.Fatalf("drain left %+v queued, want the channel empty", c)
	default:
	}
}

// A signal that lands while a target waits out its cooldown must still be
// prompt, and must leave no child of the previous generation behind.
func TestShutdownDuringCooldownKillsTheChild(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ConfigFileName), `svc1:
  on: src
  cmd: 'sleep 60 & echo $! > sleep.pid; echo $$ > sh.pid; wait'
  delay: 20ms
  cooldown: 5s
`)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	signals := make(chan os.Signal, 2)
	code := make(chan int, 1)
	go func() { code <- runTargets(cfg.Targets, io.Discard, cfg.Path, signals) }()

	first := waitForPids(t, dir)

	// A change restarts the target: the old generation dies, and the cooldown
	// holds the next one back.
	writeFile(t, filepath.Join(dir, "src", "change.txt"), "x")
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	signals <- syscall.SIGINT
	select {
	case <-code:
	case <-time.After(3 * time.Second):
		t.Fatal("rerun waited out the cooldown instead of stopping")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("shutdown took %v, want it not to wait out the cooldown", waited)
	}

	// Whatever the run last recorded, and whatever it recorded before the
	// restart, has to be gone.
	for _, pid := range append(first, waitForPids(t, dir)...) {
		assertDead(t, pid, "cooldown shutdown")
	}
}

// waitForPids reads the shell pid and the sleep pid a target's generation
// recorded.
func waitForPids(t *testing.T, dir string) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var pids []int
		for _, name := range []string{"sh.pid", "sleep.pid"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				break
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 0 {
				break
			}
			pids = append(pids, pid)
		}
		if len(pids) == 2 {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pids in %s within 3s", dir)
	return nil
}

// restoreKnobs restores the timing knobs a test shrank.
func restoreKnobs(t *testing.T) func() {
	t.Helper()
	limit, timeout := staggerLimit, staggerSlotTimeout
	window, delays := backoffWindow, backoffDelays
	return func() {
		staggerLimit, staggerSlotTimeout = limit, timeout
		backoffWindow, backoffDelays = window, delays
	}
}

// The gate lets staggerLimit starts through and serves its queue in rerun.yml
// order, not in the order the waiters happened to arrive.
func TestStartGateServesQueuedRunnersInConfigOrder(t *testing.T) {
	g := newStartGate(2)
	ctx := context.Background()

	first, _, ok := g.acquire(ctx, nil, nil, 0, nil)
	if !ok {
		t.Fatal("first acquire refused")
	}
	second, _, ok := g.acquire(ctx, nil, nil, 1, nil)
	if !ok {
		t.Fatal("second acquire refused")
	}

	granted := make(chan int, 2)
	go func() {
		_, _, ok := g.acquire(ctx, nil, nil, 3, nil)
		if ok {
			granted <- 3
		}
	}()
	go func() {
		_, _, ok := g.acquire(ctx, nil, nil, 2, nil)
		if ok {
			granted <- 2
		}
	}()
	// Let both queue before either slot frees.
	time.Sleep(50 * time.Millisecond)

	first()
	if got := <-granted; got != 2 {
		t.Fatalf("first waiter served = %d, want the lower config index", got)
	}
	second()
	if got := <-granted; got != 3 {
		t.Fatalf("second waiter served = %d, want 3", got)
	}
}

// A queued acquire aborts on ctx and hands no slot back to the pool, so the
// next acquire still works.
func TestStartGateAbortsOnContext(t *testing.T) {
	g := newStartGate(1)
	release, _, ok := g.acquire(context.Background(), nil, nil, 0, nil)
	if !ok {
		t.Fatal("first acquire refused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, _, ok := g.acquire(ctx, nil, nil, 1, nil)
		done <- ok
	}()
	cancel()
	if ok := <-done; ok {
		t.Fatal("aborted acquire reported a slot")
	}

	release()
	queued := false
	_, _, ok = g.acquire(context.Background(), nil, nil, 2, func() { queued = true })
	if !ok || queued {
		t.Fatalf("acquire after abort = (queued %v, ok %v), want a free slot", queued, ok)
	}
}

// A config with more targets than slots starts them in waves: the queued ones
// wait until a running target talks.
func TestRunTargetsStaggersStarts(t *testing.T) {
	defer restoreKnobs(t)()
	staggerLimit = 4
	staggerSlotTimeout = 10 * time.Second

	_, stop, dir := startRunner(t, `t1:
  on: src
  cmd: echo tick >> started.txt; sleep 2; echo up; sleep 30
t2:
  on: src
  cmd: echo tick >> started.txt; sleep 2; echo up; sleep 30
t3:
  on: src
  cmd: echo tick >> started.txt; sleep 2; echo up; sleep 30
t4:
  on: src
  cmd: echo tick >> started.txt; sleep 2; echo up; sleep 30
t5:
  on: src
  cmd: echo tick >> started.txt; sleep 2; echo up; sleep 30
t6:
  on: src
  cmd: echo tick >> started.txt; sleep 2; echo up; sleep 30
`)

	started := filepath.Join(dir, "started.txt")
	waitForCount(t, started, 4, 3*time.Second)
	waitForStableCount(t, started, 4, 400*time.Millisecond)
	waitForCount(t, started, 6, 5*time.Second)

	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
}

// A shutdown must abort the targets still queued behind the gate instead of
// waiting for them to acquire a slot.
func TestShutdownWhileQueuedDoesNotHang(t *testing.T) {
	defer restoreKnobs(t)()
	staggerLimit = 1
	staggerSlotTimeout = time.Hour

	_, stop, _ := startRunner(t, `t1:
  on: src
  cmd: sleep 30
t2:
  on: src
  cmd: sleep 30
t3:
  on: src
  cmd: sleep 30
t4:
  on: src
  cmd: sleep 30
`)

	time.Sleep(300 * time.Millisecond) // let the first one take the only slot

	start := time.Now()
	if got := stop(); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("shutdown took %v, want the queued acquires aborted", waited)
	}
}

// The backoff waits before the third restart in a window, and never before the
// first two.
func TestWaitBackoffDelaysTheThirdRestart(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	backoffWindow = time.Minute

	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true}, sink)
	r.selfExited = true
	r.lastRun = time.Second

	for restart := 1; restart <= 3; restart++ {
		sink.events = nil
		start := time.Now()
		if !r.waitBackoff(context.Background()) {
			t.Fatalf("restart %d aborted", restart)
		}
		waited := time.Since(start)
		want := backoffDelay(restart)
		if waited < want {
			t.Fatalf("restart %d waited %v, want at least %v", restart, waited, want)
		}
		if len(r.restarts) != restart {
			t.Fatalf("window after restart %d = %d, want %d", restart, len(r.restarts), restart)
		}
		var backedOff bool
		for _, ev := range sink.all() {
			if ev.kind == evBackoff {
				backedOff = true
				if ev.delay != want || ev.ordinal != restart {
					t.Fatalf("restart %d backoff = %+v, want delay %v ordinal %d", restart, ev, want, restart)
				}
			}
		}
		if backedOff != (want > 0) {
			t.Fatalf("restart %d backed off = %v, want %v", restart, backedOff, want > 0)
		}
	}
}

// The delay ladder caps at the last entry.
func TestBackoffDelayLadder(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second}

	for _, tt := range []struct {
		ordinal int
		want    time.Duration
	}{
		{1, 0}, {2, 0}, {3, time.Second}, {4, 2 * time.Second},
		{5, 4 * time.Second}, {6, 8 * time.Second}, {7, 10 * time.Second}, {40, 10 * time.Second},
	} {
		if got := backoffDelay(tt.ordinal); got != tt.want {
			t.Errorf("backoffDelay(%d) = %v, want %v", tt.ordinal, got, tt.want)
		}
	}
}

// A run that outlived the backoff window does not feed it: its restart neither
// counts toward the ordinal nor waits.
func TestWaitBackoffResetsAfterAStableRun(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{time.Second}
	backoffWindow = 30 * time.Millisecond

	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true}, sink)
	r.selfExited = true
	r.lastRun = 35 * time.Millisecond // already past the window
	r.restarts = []time.Time{time.Now(), time.Now()}

	start := time.Now()
	if !r.waitBackoff(context.Background()) {
		t.Fatal("waitBackoff aborted")
	}
	if waited := time.Since(start); waited > 10*time.Millisecond {
		t.Fatalf("waited %v after a stable run, want no backoff", waited)
	}
	if len(r.restarts) != 0 {
		t.Fatalf("window = %d, want cleared without a new entry", len(r.restarts))
	}
	for _, ev := range sink.all() {
		if ev.kind == evBackoff {
			t.Fatalf("a stable run still backed off: %+v", ev)
		}
	}
}

// A manual stop clears the crash-loop window, so the next start runs at once.
func TestManualStopClearsBackoff(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{10 * time.Second}
	backoffWindow = time.Minute

	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
	r.selfExited = true
	r.lastRun = time.Second
	r.restarts = []time.Time{time.Now(), time.Now()}

	r.handleControl(context.Background(), ctrlMsg{kind: ctrlStop})
	if r.selfExited || len(r.restarts) != 0 {
		t.Fatalf("ctrlStop left the window: selfExited=%v restarts=%d", r.selfExited, len(r.restarts))
	}

	start := time.Now()
	r.handleControl(context.Background(), ctrlMsg{kind: ctrlStart})
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("manual start waited %v, want immediate", waited)
	}
	for _, ev := range sink.all() {
		if ev.kind == evBackoff {
			t.Fatalf("a manual start backed off: %+v", ev)
		}
	}
}

// A stop queued while a runner waits out the backoff aborts the wait instead
// of being ignored until the delay elapses.
func TestBackoffAbortsOnStopControl(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{time.Hour}
	backoffWindow = time.Minute

	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true}, sink)
	r.selfExited = true
	r.lastRun = time.Second
	r.restarts = []time.Time{time.Now(), time.Now()}

	done := make(chan bool, 1)
	go func() { done <- r.waitBackoff(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	r.control(ctrlStop)

	select {
	case ok := <-done:
		if ok {
			t.Fatal("waitBackoff reported a completed wait after a stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitBackoff did not abort on a stop control")
	}
	if r.stash == nil || r.stash.kind != ctrlStop {
		t.Fatalf("stash = %+v, want the stop for the run loop", r.stash)
	}
}

// A stop that lands while a start waits for a gate slot aborts the wait: the
// target must never spawn.
func TestQueuedStartAbortsOnStopControl(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
	g := newStartGate(1)
	r.gate = g
	r.order.Store(1)

	hold, _, ok := g.acquire(context.Background(), nil, nil, 0, nil)
	if !ok {
		t.Fatal("holding acquire refused")
	}
	defer hold()

	done := make(chan error, 1)
	go func() { done <- r.start(context.Background()) }()
	// The stop is buffered before the runner reaches acquire, so the queued
	// wait has to see it and abort.
	r.control(ctrlStop)

	if err := <-done; !errors.Is(err, errStartAborted) {
		t.Fatalf("start = %v, want errStartAborted", err)
	}
	if r.stash == nil || r.stash.kind != ctrlStop {
		t.Fatalf("stash = %+v, want the queued stop", r.stash)
	}
	if r.currentCmd() != nil {
		t.Fatal("a stopped queued start spawned anyway")
	}
	for _, ev := range sink.all() {
		if ev.kind == evStarted {
			t.Fatal("a stopped queued start reported a spawn")
		}
	}
}

// An aborted backoff does not count its crash twice: the next spawn pays the
// same delay again.
func TestAbortedBackoffDoesNotCountTwice(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{50 * time.Millisecond}
	backoffWindow = time.Minute

	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true}, sink)
	r.selfExited = true
	r.lastRun = time.Second
	r.restarts = []time.Time{time.Now(), time.Now()}

	done := make(chan bool, 1)
	go func() { done <- r.waitBackoff(context.Background()) }()
	time.Sleep(10 * time.Millisecond)
	r.control(ctrlStop)
	<-done
	if len(r.restarts) != 2 {
		t.Fatalf("window after abort = %d, want the crash rolled back", len(r.restarts))
	}

	// The run loop consumes the stash before the next spawn; clear it here.
	r.stash = nil
	if !r.waitBackoff(context.Background()) {
		t.Fatal("second waitBackoff aborted")
	}
	if len(r.restarts) != 3 {
		t.Fatalf("window after the retry = %d, want 3 crashes counted once each", len(r.restarts))
	}
	var ordinal int
	for _, ev := range sink.all() {
		if ev.kind == evBackoff {
			ordinal = ev.ordinal
		}
	}
	if ordinal != 3 {
		t.Fatalf("last backoff ordinal = %d, want 3", ordinal)
	}
}

// A manual start or reboot after a crash loop runs at once: the user took
// over, so no backoff applies.
func TestManualStartAndRebootSkipBackoff(t *testing.T) {
	defer restoreKnobs(t)()
	backoffDelays = []time.Duration{time.Hour}
	backoffWindow = time.Minute

	for _, kind := range []ctrlKind{ctrlStart, ctrlReboot} {
		sink := &recordingSink{}
		r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
		r.selfExited = true
		r.lastRun = time.Second
		r.restarts = []time.Time{time.Now(), time.Now()}

		start := time.Now()
		r.handleControl(context.Background(), ctrlMsg{kind: kind})
		if waited := time.Since(start); waited > time.Second {
			t.Fatalf("%v waited %v, want immediate", kind, waited)
		}
		for _, ev := range sink.all() {
			if ev.kind == evBackoff {
				t.Fatalf("%v backed off: %+v", kind, ev)
			}
		}
	}
}

// waitDelay leaves a control already stashed for the run loop alone and
// returns at once so the loop can handle it.
func TestWaitDelayLeavesAStashAlone(t *testing.T) {
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true}, &recordingSink{})
	r.stash = &ctrlMsg{kind: ctrlSpec}

	start := time.Now()
	if r.waitDelay(context.Background(), time.Hour) {
		t.Fatal("waitDelay completed with a stash pending")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waitDelay waited %v with a stash pending", waited)
	}
	if r.stash == nil || r.stash.kind != ctrlSpec {
		t.Fatalf("stash = %+v, want it untouched", r.stash)
	}
}

// A free slot waits for a lower-order waiter already queued; a newcomer never
// overtakes it.
func TestStartGateDoesNotLetANewcomerJumpTheQueue(t *testing.T) {
	g := newStartGate(2)
	g.active = 1 // one slot free
	g.waiters = []*gateWaiter{{order: 0, grant: make(chan struct{})}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if release, _, ok := g.acquire(ctx, nil, nil, 1, nil); ok {
		release()
		t.Fatal("a newcomer took a free slot ahead of a queued waiter")
	}
}
