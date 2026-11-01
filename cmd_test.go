//go:build !windows

package rerun

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func startTestCommand(t *testing.T, grace time.Duration, script string) Command {
	t.Helper()
	cmd, err := StartCommand([]string{script}, grace)
	if err != nil {
		t.Fatalf("StartCommand(%q): %v", script, err)
	}
	t.Cleanup(func() { _ = cmd.KillForce() })
	return cmd
}

func TestKillReapsProcess(t *testing.T) {
	cmd := startTestCommand(t, 100*time.Millisecond, "sleep 30")
	if cmd.Exited() {
		t.Fatal("Exited() = true while the process is running")
	}

	begin := time.Now()
	if err := cmd.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Fatalf("Kill took %v, want < 3s", elapsed)
	}

	if !cmd.Exited() {
		t.Fatal("Exited() = false after Kill")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cmd.WaitExit(ctx); err != nil {
		t.Fatalf("WaitExit after Kill: %v", err)
	}
	if code, _ := cmd.ExitState(); code == 0 {
		t.Fatal("ExitState code = 0, want a signalled exit")
	}
}

// TestStartCommandJoinsWordsWithQuoting proves the words reach the command as
// arguments instead of becoming $0, $1, ... of the sh -c wrapper.
func TestStartCommandJoinsWordsWithQuoting(t *testing.T) {
	var out bytes.Buffer
	cmd, err := StartCommand([]string{"echo", "hi there", "x"}, time.Second, WithStdout(&out))
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	if err := cmd.WaitExit(context.Background()); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "hi there x" {
		t.Fatalf("output = %q, want %q", got, "hi there x")
	}
}

// TestStartCommandQuotesShellMetachars proves metacharacters stay literal.
func TestStartCommandQuotesShellMetachars(t *testing.T) {
	var out bytes.Buffer
	cmd, err := StartCommand([]string{"echo", "a'b", "$HOME", "x;y"}, time.Second, WithStdout(&out))
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	if err := cmd.WaitExit(context.Background()); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "a'b $HOME x;y" {
		t.Fatalf("output = %q, want %q", got, "a'b $HOME x;y")
	}
}

// TestStartCommandSingleWordIsRawScript locks in the rerun.yml `cmd` shape: a
// lone word is already a shell line and must not be quoted as a whole.
func TestStartCommandSingleWordIsRawScript(t *testing.T) {
	var out bytes.Buffer
	cmd, err := StartCommand([]string{"echo one two"}, time.Second, WithStdout(&out))
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	if err := cmd.WaitExit(context.Background()); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "one two" {
		t.Fatalf("output = %q, want %q", got, "one two")
	}
}

// TestEscalateSparesNewerGeneration covers the Kill grace window: by the time
// escalation fires, gen A may already be reaped and gen B current. Escalating
// the captured generation must not touch B.
//
// The public race is sub-microsecond and cannot be hit from a test: Start only
// accepts a new generation once the old one is reaped, and that reaping makes
// Kill's grace wait succeed. So the identity check is driven directly.
func TestEscalateSparesNewerGeneration(t *testing.T) {
	c := newCommand([]string{"sleep 30"}, time.Second)
	if err := c.Start(); err != nil {
		t.Fatalf("Start gen A: %v", err)
	}
	procA, stA := c.current()

	if err := c.KillForce(); err != nil {
		t.Fatalf("KillForce gen A: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start gen B: %v", err)
	}
	t.Cleanup(func() { _ = c.KillForce() })

	if err := c.escalate(procA, stA); err != nil {
		t.Fatalf("escalate gen A: %v", err)
	}
	if c.Exited() {
		t.Fatal("escalating gen A force-killed the newer generation B")
	}
}

func TestSelfExitIsReapedWithoutKill(t *testing.T) {
	cmd := startTestCommand(t, 5*time.Second, "sleep 0.2")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	begin := time.Now()
	if err := cmd.WaitExit(ctx); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("WaitExit took %v, want < 2s", elapsed)
	}

	if !cmd.Exited() {
		t.Fatal("Exited() = false after the process finished")
	}
	if code, err := cmd.ExitState(); code != 0 || err != nil {
		t.Fatalf("ExitState = %d, %v; want 0, nil", code, err)
	}

	// Killing an already reaped command is a no-op.
	if err := cmd.Kill(); err != nil {
		t.Fatalf("Kill after self-exit: %v", err)
	}
}

func TestKillEscalatesAfterGrace(t *testing.T) {
	const grace = 200 * time.Millisecond
	cmd := startTestCommand(t, grace, `trap "" TERM; sleep 30`)

	// Give the shell time to install the trap; a signal before that kills it.
	time.Sleep(300 * time.Millisecond)

	begin := time.Now()
	if err := cmd.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	elapsed := time.Since(begin)

	if elapsed < grace {
		t.Fatalf("Kill returned after %v, want >= %v: SIGTERM was ignored", elapsed, grace)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Kill took %v, want < 3s", elapsed)
	}
	if !cmd.Exited() {
		t.Fatal("Exited() = false after the SIGKILL escalation")
	}
}

func TestKillTwiceIsSafe(t *testing.T) {
	cmd := startTestCommand(t, 100*time.Millisecond, "sleep 30")

	if err := cmd.Kill(); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	if err := cmd.Kill(); err != nil {
		t.Fatalf("second Kill: %v", err)
	}
}

func TestWaitExitTwiceReusesOneResult(t *testing.T) {
	cmd := startTestCommand(t, 100*time.Millisecond, "sleep 0.2")

	if err := cmd.WaitExit(context.Background()); err != nil {
		t.Fatalf("first WaitExit: %v", err)
	}
	if err := cmd.WaitExit(context.Background()); err != nil {
		t.Fatalf("second WaitExit: %v", err)
	}

	code1, err1 := cmd.ExitState()
	code2, err2 := cmd.ExitState()
	if code1 != code2 {
		t.Fatalf("exit codes differ: %d vs %d", code1, code2)
	}
	if (err1 == nil) != (err2 == nil) {
		t.Fatalf("wait errors differ: %v vs %v", err1, err2)
	}
}

func TestWaitExitHonoursContext(t *testing.T) {
	cmd := startTestCommand(t, 100*time.Millisecond, "sleep 30")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := cmd.WaitExit(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitExit = %v, want context.DeadlineExceeded", err)
	}

	if err := cmd.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

func TestEveryGenerationIsReaped(t *testing.T) {
	cmd := startTestCommand(t, 100*time.Millisecond, "sleep 0.1")

	for generation := 0; generation < 3; generation++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := cmd.WaitExit(ctx)
		cancel()
		if err != nil {
			t.Fatalf("generation %d WaitExit: %v", generation, err)
		}
		if !cmd.Exited() {
			t.Fatalf("generation %d was not reaped", generation)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("generation %d Start: %v", generation+1, err)
		}
	}
}

// A spawned child sees RERUN=1 even when the parent does not, so a rerun
// target that spawns rerun is stopped by the guard.
func TestSpawnedChildSeesRerunMarker(t *testing.T) {
	t.Setenv("RERUN", "")
	var out bytes.Buffer
	cmd, err := StartCommand([]string{"echo $RERUN"}, time.Second, WithStdout(&out))
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cmd.WaitExit(ctx); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "1" {
		t.Fatalf("child RERUN = %q, want 1", got)
	}
}
