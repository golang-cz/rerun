package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// composeKind is the Target.Kind of a docker-compose service.
const composeKind = "compose"

// Pacing and limits for the docker CLI. Package vars so tests can shrink them.
var (
	composeRetryMin = time.Second
	composeRetryMax = 30 * time.Second
	// composeStableRun is how long an events stream must live before its retry delay resets.
	composeStableRun = 10 * time.Second
	// composeLogTail is how many lines an already-running container shows at attach.
	composeLogTail = 200
	// composeResolveTimeout bounds `docker compose config`, which can hang on a dead daemon.
	composeResolveTimeout = 15 * time.Second
)

// composeTarget is the compose service a compose target runs.
type composeTarget struct {
	File     string
	Project  string
	Service  string
	Profiles []string
}

// composeArgs prefixes a docker compose subcommand with this target's file and profiles.
func (c *composeTarget) composeArgs(args ...string) []string {
	out := []string{"compose", "-f", c.File}
	// Compose hides profile-gated services unless a profile is named; `*` names them all.
	if len(c.Profiles) == 0 {
		out = append(out, "--profile", "*")
	}
	for _, p := range c.Profiles {
		out = append(out, "--profile", p)
	}
	return append(out, args...)
}

// resolveCompose asks docker compose for the project name, the services a file defines, and each one's profiles.
func resolveCompose(file string, profiles, services []string) (project string, names []string, profilesOf map[string][]string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), composeResolveTimeout)
	defer cancel()
	ct := &composeTarget{File: file, Profiles: profiles}
	cmd := exec.CommandContext(ctx, "docker", ct.composeArgs("config", "--format", "json")...)
	cmd.Dir = filepath.Dir(file)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", nil, nil, fmt.Errorf("docker compose config: no answer after %s", composeResolveTimeout)
	}
	if err != nil {
		return "", nil, nil, fmt.Errorf("docker compose config: %s", firstLineOr(stderr.String(), err))
	}

	var cfg struct {
		Name     string `json:"name"`
		Services map[string]struct {
			Profiles []string `json:"profiles"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return "", nil, nil, fmt.Errorf("docker compose config: %w", err)
	}

	all := make([]string, 0, len(cfg.Services))
	profilesOf = map[string][]string{}
	for name, svc := range cfg.Services {
		all = append(all, name)
		profilesOf[name] = svc.Profiles
	}
	slices.Sort(all)
	if len(services) == 0 {
		return cfg.Name, all, profilesOf, nil
	}
	for _, s := range services {
		if _, ok := cfg.Services[s]; !ok {
			if len(profiles) == 0 {
				return "", nil, nil, fmt.Errorf("unknown service %q (not defined)", s)
			}
			return "", nil, nil, fmt.Errorf("unknown service %q (not defined, or excluded by profiles)", s)
		}
	}
	var keep []string
	for _, s := range all {
		if slices.Contains(services, s) {
			keep = append(keep, s)
		}
	}
	return cfg.Name, keep, profilesOf, nil
}

func firstLineOr(text string, fallback error) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if line == "" {
		return fallback.Error()
	}
	return line
}

// ownAction is a stop or restart rerun issued itself, so its die events are intended.
type ownAction int

const (
	ownNone ownAction = iota
	ownStop
	ownRestart
)

// logMark is the newest log line rerun has shown for a container, and the
// other lines at that same timestamp, so a re-attach does not repeat them.
type logMark struct {
	at    time.Time
	lines map[string]bool
}

// composeBackend runs one compose service through the docker CLI. Container
// state comes from `docker events`, reconciled against `docker compose ps`
// after every (re)connect, because events sent while disconnected are lost.
// Containers are keyed by container ID, so a recreated service is a new one.
type composeBackend struct {
	name string
	ct   *composeTarget
	sink sink

	mu    sync.Mutex
	live  int
	seen  bool
	own   ownAction
	marks map[string]logMark

	// Serve goroutine only.
	ctrs  map[string]*composeCtr
	tails sync.WaitGroup
}

// composeCtr is what rerun has learned about one container.
type composeCtr struct {
	running   bool
	startedAt time.Time
	health    healthState
	// intended is set by a kill with signal 15, 2 or 3, so the next die is a stop.
	intended bool
	oom      bool
	// crashed is set when the last run ended on its own, so the next start is a restart.
	crashed    bool
	cancelLogs func()
	tailDone   chan struct{}
}

func newComposeBackend(t Target, s sink) *composeBackend {
	return &composeBackend{
		name:  t.Name,
		ct:    t.Compose,
		sink:  s,
		marks: map[string]logMark{},
		ctrs:  map[string]*composeCtr{},
	}
}

func (c *composeBackend) Start(ctx context.Context, _ Target, slot *startSlot, _ func(time.Time)) (<-chan struct{}, error) {
	// The events stream reports the start, so there is no evStarted to announce here.
	if slot != nil {
		slot.free()
	}
	if out, err := c.compose(ctx, "up", "-d", c.ct.Service); err != nil {
		return nil, fmt.Errorf("docker compose up %s: %s", c.ct.Service, firstLineOr(string(out), err))
	}
	return nil, nil
}

func (c *composeBackend) Stop(ctx context.Context) {
	c.setOwn(ownStop)
	defer c.setOwn(ownNone)
	if out, err := c.compose(ctx, "stop", c.ct.Service); err != nil {
		c.sink.emit(event{kind: evRunnerErr, target: c.name, err: fmt.Errorf("docker compose stop %s: %s", c.ct.Service, firstLineOr(string(out), err))})
	}
}

// Restart bounces the service in place, as `r` asks. With no container yet, it brings the service up.
func (c *composeBackend) Restart(ctx context.Context) {
	c.setOwn(ownRestart)
	defer c.setOwn(ownNone)
	if _, err := c.compose(ctx, "restart", c.ct.Service); err == nil {
		return
	}
	if out, err := c.compose(ctx, "up", "-d", c.ct.Service); err != nil {
		c.sink.emit(event{kind: evRunnerErr, target: c.name, err: fmt.Errorf("docker compose restart %s: %s", c.ct.Service, firstLineOr(string(out), err))})
	}
}

// Release leaves containers running: rerun never stops them on exit.
func (c *composeBackend) Release(context.Context) {}

// Kill has no signal to send to a container rerun did not start, so it is a no-op.
func (c *composeBackend) Kill(bool) {}

func (c *composeBackend) ExitState() (int, error, bool) { return 0, nil, false }

func (c *composeBackend) Started() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

func (c *composeBackend) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live > 0
}

func (c *composeBackend) setOwn(a ownAction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.own = a
}

// Serve follows the service's container state until ctx ends.
func (c *composeBackend) Serve(ctx context.Context, emit func(event)) {
	defer func() {
		for _, ct := range c.ctrs {
			if ct.cancelLogs != nil {
				ct.cancelLogs()
			}
		}
		c.tails.Wait()
	}()

	group, _, _ := strings.Cut(c.name, "/")
	delay := composeRetryMin
	outage := false
	for ctx.Err() == nil {
		t0 := time.Now()
		err := c.reconcile(ctx, t0, emit)
		if err == nil {
			if outage {
				emit(event{kind: evNotice, line: group + ": docker reconnected"})
				outage = false
			}
			lived := time.Now()
			n, streamErr := c.followEvents(ctx, t0, emit)
			err = streamErr
			if n > 0 && time.Since(lived) >= composeStableRun {
				delay = composeRetryMin
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !outage {
			emit(event{kind: evNotice, line: fmt.Sprintf("%s: docker unreachable (%v), retrying", group, err)})
			outage = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, composeRetryMax)
	}
}

// followEvents runs one docker events stream from since, and returns how many events it delivered.
func (c *composeBackend) followEvents(ctx context.Context, since time.Time, emit func(event)) (int, error) {
	args := []string{"events", "--format", "{{json .}}", "--since", since.Format(time.RFC3339Nano),
		"--filter", "type=container",
		"--filter", "label=com.docker.compose.project=" + c.ct.Project,
		"--filter", "label=com.docker.compose.service=" + c.ct.Service}
	cmd := exec.CommandContext(ctx, "docker", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	n := 0
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var ev dockerEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		n++
		c.handleEvent(ctx, ev, emit)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return n, nil
	}
	if waitErr != nil {
		return n, waitErr
	}
	return n, errors.New("events stream ended")
}

// dockerEvent is one line of `docker events --format '{{json .}}'`.
type dockerEvent struct {
	Action   string `json:"Action"`
	Status   string `json:"status"`
	Time     int64  `json:"time"`
	TimeNano int64  `json:"timeNano"`
	Actor    struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

func (ev dockerEvent) at() time.Time {
	if ev.TimeNano != 0 {
		return time.Unix(0, ev.TimeNano)
	}
	return time.Unix(ev.Time, 0)
}

func (c *composeBackend) handleEvent(ctx context.Context, ev dockerEvent, emit func(event)) {
	if ev.Actor.Attributes["com.docker.compose.service"] != c.ct.Service {
		return
	}
	id := ev.Actor.ID
	action := ev.Action
	if action == "" {
		action = ev.Status
	}
	switch {
	case action == "start":
		c.onStart(ctx, id, ev.at(), emit)
	case action == "kill":
		// Signal 15, 2 or 3 is a stop, as `docker stop` sends. Signal 9 is a crash, as `docker kill` is.
		switch ev.Actor.Attributes["signal"] {
		case "15", "2", "3":
			c.container(id).intended = true
		}
	case action == "oom":
		c.container(id).oom = true
	case action == "die":
		code, _ := strconv.Atoi(ev.Actor.Attributes["exitCode"])
		c.onDie(id, code, ev.at(), emit)
	case strings.HasPrefix(action, "health_status: "):
		c.onHealth(id, healthState(strings.TrimPrefix(action, "health_status: ")), ev.at(), emit)
	}
}

func (c *composeBackend) container(id string) *composeCtr {
	ct, ok := c.ctrs[id]
	if !ok {
		ct = &composeCtr{}
		c.ctrs[id] = ct
	}
	return ct
}

func (c *composeBackend) onStart(ctx context.Context, id string, at time.Time, emit func(event)) {
	ct := c.container(id)
	if ct.running {
		return
	}
	ct.running, ct.startedAt, ct.health = true, at, ""
	restart := ct.crashed
	ct.crashed, ct.intended, ct.oom = false, false, false
	c.mu.Lock()
	c.live++
	c.seen = true
	c.mu.Unlock()
	emit(event{kind: evStarted, target: c.name, at: at, restart: restart})
	c.startLogs(ctx, id, ct, emit)
}

func (c *composeBackend) onDie(id string, code int, at time.Time, emit func(event)) {
	ct := c.container(id)
	c.mu.Lock()
	own := c.own
	c.mu.Unlock()
	intended := ct.intended || own != ownNone
	oom := ct.oom
	ct.intended, ct.oom = false, false
	if !ct.running {
		return
	}
	ct.running = false
	c.mu.Lock()
	c.live--
	c.mu.Unlock()

	if intended {
		// rerun's own restart counts as a restart, so the next start reads as one.
		ct.crashed = own == ownRestart
		emit(event{kind: evStopped, target: c.name})
		return
	}
	ct.crashed = true
	status := fmt.Sprintf("exited %d", code)
	if oom {
		status = "oom"
	}
	emit(event{kind: evExited, target: c.name, status: status, code: code, at: at})
}

func (c *composeBackend) onHealth(id string, state healthState, at time.Time, emit func(event)) {
	ct := c.container(id)
	if !ct.running || (state != healthHealthy && state != healthUnhealthy) || state == ct.health {
		return
	}
	ct.health = state
	emit(event{kind: evHealth, target: c.name, health: state, at: at, runAt: ct.startedAt, dur: at.Sub(ct.startedAt)})
}

// reconcile brings the known container state in line with `docker compose ps`.
// Deaths come before starts, so a replaced container reads as one run ending, then the next.
func (c *composeBackend) reconcile(ctx context.Context, now time.Time, emit func(event)) error {
	out, err := c.compose(ctx, "ps", "-a", "--format", "json", c.ct.Service)
	if err != nil {
		return err
	}
	items, err := parseComposePS(out)
	if err != nil {
		return err
	}

	exitCode := map[string]int{}
	running := map[string]time.Time{}
	for _, it := range items {
		exitCode[it.ID] = it.ExitCode
		if it.State == "running" {
			running[it.ID] = c.inspectStart(ctx, it.ID, now)
		}
	}
	// A container that is gone or stopped died while rerun was disconnected.
	for id, ct := range c.ctrs {
		if _, up := running[id]; !up && ct.running {
			c.onDie(id, exitCode[id], now, emit)
		}
	}
	for id := range c.ctrs {
		if _, known := exitCode[id]; !known {
			if ct := c.ctrs[id]; ct.cancelLogs != nil {
				ct.cancelLogs()
			}
			delete(c.ctrs, id)
		}
	}
	for _, it := range items {
		at, up := running[it.ID]
		if !up {
			continue
		}
		ct := c.container(it.ID)
		if ct.running && !ct.startedAt.Equal(at) {
			// A new run this container missed while disconnected: its die, then its start.
			c.onDie(it.ID, 0, now, emit)
		}
		if !ct.running {
			c.onStart(ctx, it.ID, at, emit)
		} else if ct.tailExited() {
			c.startLogs(ctx, it.ID, ct, emit)
		}
		c.onHealth(it.ID, healthState(it.Health), now, emit)
	}
	return nil
}

// tailExited reports whether the container's log tail has ended, so it needs a re-attach.
func (ct *composeCtr) tailExited() bool {
	if ct.tailDone == nil {
		return true
	}
	select {
	case <-ct.tailDone:
		return true
	default:
		return false
	}
}

// composePS is one entry of `docker compose ps --format json`.
type composePS struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	State    string `json:"State"`
	ExitCode int    `json:"ExitCode"`
	Health   string `json:"Health"`
}

// parseComposePS accepts both a JSON array and one object per line, which
// compose versions differ on.
func parseComposePS(out []byte) ([]composePS, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var items []composePS
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("docker compose ps: %w", err)
		}
		return items, nil
	}
	var items []composePS
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var it composePS
		if err := json.Unmarshal(line, &it); err != nil {
			return nil, fmt.Errorf("docker compose ps: %w", err)
		}
		items = append(items, it)
	}
	return items, nil
}

// inspectStart is when a container's current run began, or now when docker won't say.
func (c *composeBackend) inspectStart(ctx context.Context, id string, now time.Time) time.Time {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.StartedAt}}", id).Output()
	if err != nil {
		return now
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		return now
	}
	return at
}

// startLogs attaches to a container's log from the last line rerun showed. A die does not cancel it: `docker logs -f` ends by itself, after the crash output.
func (c *composeBackend) startLogs(ctx context.Context, id string, ct *composeCtr, emit func(event)) {
	if ct.cancelLogs != nil {
		ct.cancelLogs()
	}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ct.cancelLogs, ct.tailDone = cancel, done

	args := []string{"logs", "-f", "--timestamps"}
	c.mu.Lock()
	mark, ok := c.marks[id]
	c.mu.Unlock()
	if ok {
		args = append(args, "--since", mark.at.Format(time.RFC3339Nano))
	} else {
		args = append(args, "--tail", strconv.Itoa(composeLogTail))
	}
	args = append(args, id)

	c.tails.Add(1)
	go func() {
		defer c.tails.Done()
		defer close(done)
		c.tailLogs(lctx, id, args, emit)
	}()
}

func (c *composeBackend) tailLogs(ctx context.Context, id string, args []string, emit func(event)) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return
	}
	defer pr.Close()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return
	}
	_ = pw.Close()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ts, rest, found := strings.Cut(line, " ")
		at, err := time.Parse(time.RFC3339Nano, ts)
		if !found || err != nil {
			emit(event{kind: evOutput, target: c.name, line: line})
			continue
		}
		if c.fresh(id, at, rest) {
			emit(event{kind: evOutput, target: c.name, line: rest})
		}
	}
	_ = cmd.Wait()
}

// fresh reports whether a log line is new. Lines at the newest timestamp are
// compared by text, because --since is inclusive and re-sends them.
func (c *composeBackend) fresh(id string, at time.Time, line string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.marks[id]
	switch {
	case !ok || at.After(m.at):
		c.marks[id] = logMark{at: at, lines: map[string]bool{line: true}}
		return true
	case at.Equal(m.at):
		if m.lines[line] {
			return false
		}
		m.lines[line] = true
		return true
	default:
		return false
	}
}

// compose runs a docker compose subcommand for this service's file.
func (c *composeBackend) compose(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", c.ct.composeArgs(args...)...)
	cmd.Dir = filepath.Dir(c.ct.File)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("docker not found on PATH")
	}
	return out.Bytes(), err
}
