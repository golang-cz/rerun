package rerun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultGrace is how long a command gets between SIGTERM and SIGKILL.
const DefaultGrace = 5 * time.Second

// killWaitCap bounds the wait after the force kill.
const killWaitCap = 5 * time.Second

var (
	ErrNotStarted     = errors.New("command not started")
	ErrAlreadyRunning = errors.New("command already running")
	ErrStillRunning   = errors.New("command still running after SIGKILL")
)

// Command is a child process that rerun owns and reaps.
//
// Each generation spawned by Start is reaped exactly once by an internal
// goroutine, so a child never lingers as a zombie, however it dies.
type Command interface {
	Start() error
	// Kill sends a graceful signal to the process group, waits the grace
	// period, then force-kills whatever is left.
	Kill() error
	// KillForce force-kills the process group immediately, no grace period.
	KillForce() error
	// WaitExit blocks until the current generation has been reaped. A nil
	// ctx blocks forever. It returns ctx.Err() if the context ends first.
	WaitExit(ctx context.Context) error
	// Exited reports whether the current generation has been reaped. It
	// returns false before Start.
	Exited() bool
	// ExitState returns the exit code and wait error of the current
	// generation, blocking until it is reaped.
	ExitState() (int, error)
	PID() string
}

// Option customizes the child process before every start.
type Option func(*exec.Cmd)

// WithStdout routes the child's stdout to w instead of os.Stdout.
func WithStdout(w io.Writer) Option {
	return func(cmd *exec.Cmd) { cmd.Stdout = w }
}

// WithStderr routes the child's stderr to w instead of os.Stderr.
func WithStderr(w io.Writer) Option {
	return func(cmd *exec.Cmd) { cmd.Stderr = w }
}

type command struct {
	cmdline []string
	grace   time.Duration
	opts    []Option

	mu    sync.Mutex
	cmd   *exec.Cmd
	state *exitState
}

// exitState is one generation's reaping result. The reaper fills code and err,
// then closes done; everyone else only reads after receiving from done.
type exitState struct {
	done chan struct{}
	code int
	err  error
}

func newCommand(cmdline []string, grace time.Duration, opts ...Option) *command {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return &command{cmdline: cmdline, grace: grace, opts: opts}
}

func (c *command) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state != nil && !reaped(c.state) {
		return ErrAlreadyRunning
	}

	cmd := newExecCmd(c.cmdline)
	for _, opt := range c.opts {
		opt(cmd)
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	st := &exitState{done: make(chan struct{}), code: -1}
	c.cmd = cmd
	c.state = st
	// Exactly one Wait per generation, started now, so a child that exits by
	// itself is reaped immediately instead of sitting as a zombie.
	go c.reap(cmd, st)
	return nil
}

func (c *command) reap(cmd *exec.Cmd, st *exitState) {
	err := cmd.Wait()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	st.code, st.err = code, err
	close(st.done)
}

func (c *command) Kill() error {
	proc, st := c.current()
	if proc == nil || reaped(st) {
		return nil
	}

	// The signal can race an exit that already happened; that is not an error.
	signalGroup(proc, st, false)
	if waitDone(st, c.grace) {
		return nil
	}

	// Grace expired: escalate this generation only. A Start during the grace
	// window must not get its child force-killed for the old one's sins.
	return c.escalate(proc, st)
}

func (c *command) KillForce() error {
	proc, st := c.current()
	if proc == nil || reaped(st) {
		return nil
	}
	return c.killForce(proc, st)
}

// killForce force-kills one specific generation, not whatever is current.
func (c *command) killForce(proc *exec.Cmd, st *exitState) error {
	if reaped(st) {
		return nil
	}

	signalGroup(proc, st, true)
	if waitDone(st, killWaitCap) {
		return nil
	}
	return ErrStillRunning
}

// escalate force-kills the generation Kill captured, and only while it is
// still the current one.
func (c *command) escalate(proc *exec.Cmd, st *exitState) error {
	curProc, curState := c.current()
	if curProc != proc || curState != st {
		return nil
	}
	return c.killForce(proc, st)
}

func (c *command) WaitExit(ctx context.Context) error {
	st := c.currentState()
	if st == nil {
		return ErrNotStarted
	}
	if ctx == nil {
		<-st.done
		return nil
	}
	select {
	case <-st.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *command) Exited() bool {
	return reaped(c.currentState())
}

func (c *command) ExitState() (int, error) {
	st := c.currentState()
	if st == nil {
		return -1, ErrNotStarted
	}
	<-st.done
	return st.code, st.err
}

func (c *command) PID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		return "PID unknown"
	}
	return fmt.Sprintf("PID %v", c.cmd.Process.Pid)
}

func (c *command) current() (*exec.Cmd, *exitState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd, c.state
}

func (c *command) currentState() *exitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// reaped reports whether st is a finished generation.
func reaped(st *exitState) bool {
	if st == nil {
		return false
	}
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

func waitDone(st *exitState, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-st.done:
		return true
	case <-timer.C:
		return false
	}
}

// ShellScript turns command words into one shell script line. A single word
// is taken as an already-composed command line, which is how rerun.yml passes
// `cmd`; several words are quoted and joined so the shell sees them as
// arguments instead of as $0, $1, ...
func ShellScript(words []string) string {
	if len(words) == 1 {
		return words[0]
	}
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = quoteWord(word)
	}
	return strings.Join(quoted, " ")
}

// childEnv is the parent environment plus RERUN=1, the marker that stops a
// spawned rerun from spawning another.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "RERUN=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "RERUN=1")
}

// StartCommand spawns cmdline through the platform shell and reaps it.
// grace defaults to DefaultGrace when non-positive. Options override the
// default stdio wiring on every generation.
func StartCommand(cmdline []string, grace time.Duration, opts ...Option) (Command, error) {
	cmd := newPlatformCmd(cmdline, grace, opts...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
