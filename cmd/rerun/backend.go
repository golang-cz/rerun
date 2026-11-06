package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/golang-cz/rerun"
)

// backend is the side of a target that actually runs: a local process or a
// compose service. The runner keeps watch, build and restart policy.
type backend interface {
	// Start brings one generation up. slot, when non-nil, is freed once the
	// target is under way. announce runs once the generation is live, before
	// its health probe begins. exited closes when the generation ends on its
	// own, or is nil when the backend reports exits some other way.
	Start(ctx context.Context, t Target, slot *startSlot, announce func(started time.Time)) (exited <-chan struct{}, err error)
	// Stop ends the live generation and waits for it to end.
	Stop(ctx context.Context)
	// Serve follows what the backend learns on its own, such as compose events, until ctx ends.
	Serve(ctx context.Context, emit func(event))
	// Release ends rerun's hold on the target at shutdown; a compose service keeps running.
	Release(ctx context.Context)
	// Kill signals the live generation; force skips the grace period.
	Kill(force bool)
	// ExitState reports how the generation that ended exited. ok is false when none ran.
	ExitState() (code int, err error, ok bool)
	// Started reports whether a generation was ever brought up.
	Started() bool
	// Running reports whether a generation is up now.
	Running() bool
}

// processBackend runs a target's command as a child process, with its output
// piped line by line and its health probe.
type processBackend struct {
	sink   sink
	out    io.Writer
	probes *sync.WaitGroup

	mu  sync.Mutex
	cmd rerun.Command
	// healthCancel and healthDone belong to the live run's probe. Run goroutine only.
	healthCancel func()
	healthDone   chan struct{}
}

// newBackend picks the backend a target runs under: compose services get their own, everything else a process.
func newBackend(t Target, s sink, probes *sync.WaitGroup) backend {
	if t.Kind == composeKind {
		return newComposeBackend(t, s)
	}
	return newProcessBackend(s, probes)
}

// restarter is implemented by backends whose service restarts in place, without a stop and a start.
type restarter interface {
	Restart(ctx context.Context)
}

func newProcessBackend(s sink, probes *sync.WaitGroup) *processBackend {
	p := &processBackend{sink: s, probes: probes}
	if w, ok := s.(interface{ writer() io.Writer }); ok {
		p.out = w.writer()
	}
	return p
}

func (p *processBackend) Start(ctx context.Context, t Target, slot *startSlot, announce func(time.Time)) (<-chan struct{}, error) {
	var (
		cmd rerun.Command
		pw  *os.File
		err error
	)
	if t.Name == "" {
		// The ad-hoc command is passed through raw: no pipe, no prefixing.
		cmd, err = rerun.StartCommand([]string{t.Cmd}, t.Grace, rerun.WithDir(t.Dir), rerun.WithStdout(p.out))
	} else {
		cmd, pw, err = p.startPiped(t, slot)
	}
	if err != nil {
		if slot != nil {
			slot.free()
		}
		return nil, err
	}

	p.mu.Lock()
	p.cmd = cmd
	started := time.Now()
	p.mu.Unlock()

	beginHealth, stopHealth := p.startHealth(ctx, t, started)
	exited := p.watchGeneration(cmd, pw, slot, stopHealth)
	announce(started)
	beginHealth()
	return exited, nil
}

// startPiped spawns a generation whose output is reported line by line.
func (p *processBackend) startPiped(t Target, slot *startSlot) (rerun.Command, *os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	// Keeps children from reading keys meant for the TUI or leaving the tty raw.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, nil, err
	}
	defer devNull.Close()

	cmd, err := rerun.StartCommand([]string{t.Cmd}, t.Grace,
		rerun.WithDir(t.Dir), rerun.WithStdin(devNull), rerun.WithStdout(pw), rerun.WithStderr(pw))
	if err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, nil, err
	}

	go p.readLines(pr, t.Name, slot)
	return cmd, pw, nil
}

// watchGeneration closes the pipe writer once the generation is reaped, which
// unblocks the readLines side.
func (p *processBackend) watchGeneration(cmd rerun.Command, pw *os.File, slot *startSlot, stopHealth func()) <-chan struct{} {
	exited := make(chan struct{})
	go func() {
		_ = cmd.WaitExit(context.Background())
		stopHealth()
		if slot != nil {
			slot.free()
		}
		if pw != nil {
			_ = pw.Close()
		}
		close(exited)
	}()
	return exited
}

func (p *processBackend) readLines(pr *os.File, name string, slot *startSlot) {
	defer pr.Close()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := slot != nil
	for scanner.Scan() {
		if first {
			slot.free() // the target is talking; it no longer needs the start slot
			first = false
		}
		line := scanner.Text()
		p.sink.emit(event{kind: evOutput, target: name, line: line})
	}
	// A line past the cap ends the read, so say so instead of going quiet.
	if err := scanner.Err(); err != nil {
		p.sink.emit(event{kind: evOutput, target: name, line: fmt.Sprintf("rerun: reading output: %v", err)})
	}
}

// begin runs after evStarted so no evHealth can precede its run.
func (p *processBackend) startHealth(ctx context.Context, t Target, started time.Time) (begin, stop func()) {
	if t.Health == "" {
		return func() {}, func() {}
	}
	hctx, cancel := context.WithCancel(ctx)
	name := t.Name
	probe := newProbe(t.Dir, t.Health)
	done := make(chan struct{})
	p.healthCancel, p.healthDone = cancel, done
	begin = func() {
		p.probes.Add(1)
		go func() {
			defer p.probes.Done()
			defer close(done)
			monitorHealth(hctx, probe, started, func(state healthState, at time.Time, since time.Duration) {
				p.sink.emit(event{kind: evHealth, target: name, health: state, at: at, runAt: started, dur: since})
			})
		}()
	}
	return begin, cancel
}

// stopHealthProbe waits out the previous run's probe before the next start.
func (p *processBackend) stopHealthProbe() {
	if p.healthCancel == nil {
		return
	}
	p.healthCancel()
	<-p.healthDone
	p.healthCancel, p.healthDone = nil, nil
}

func (p *processBackend) Stop(ctx context.Context) {
	cmd := p.current()
	if cmd == nil || cmd.Exited() {
		return
	}
	_ = cmd.Kill()
	_ = cmd.WaitExit(ctx)
	p.stopHealthProbe()
}

// Serve has nothing to follow: a process's exits come through its exited channel.
func (p *processBackend) Serve(context.Context, func(event)) {}

func (p *processBackend) Release(ctx context.Context) {
	p.Stop(ctx)
}

func (p *processBackend) Kill(force bool) {
	cmd := p.current()
	if cmd == nil {
		return
	}
	if force {
		_ = cmd.KillForce()
		return
	}
	_ = cmd.Kill()
}

func (p *processBackend) ExitState() (int, error, bool) {
	cmd := p.current()
	if cmd == nil {
		return 0, nil, false
	}
	code, err := cmd.ExitState()
	return code, err, true
}

func (p *processBackend) Started() bool {
	return p.current() != nil
}

func (p *processBackend) Running() bool {
	cmd := p.current()
	return cmd != nil && !cmd.Exited()
}

func (p *processBackend) current() rerun.Command {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd
}
