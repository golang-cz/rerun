//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
