package main

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"unicode/utf8"
)

// lockedStdout is the one writer for every terminal byte: bubbletea's frames
// and the status records share it, so neither can split the other. It keeps
// the descriptor, or bubbletea would stop seeing a terminal on its output.
type lockedStdout struct {
	mu   sync.Mutex
	out  io.Writer
	file *os.File
}

func (w *lockedStdout) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

func (w *lockedStdout) Read(p []byte) (int, error) { return w.file.Read(p) }
func (w *lockedStdout) Fd() uintptr                { return w.file.Fd() }

// Close is a no-op: the process owns stdout and term.File wants the method.
func (w *lockedStdout) Close() error { return nil }

// programStatus mirrors each row's build and health cells to the terminal as
// OSC 7501 program status. It holds no state of its own beyond what it last
// reported: sync derives every record from the rows.
type programStatus struct {
	out    io.Writer
	run    string
	active map[statusKey]statusRecord
}

type statusKey struct{ target, topic string }

type statusReport struct{ state, msg string }

// statusRecord is the id a record was reported under, so a change reuses it
// instead of hashing the target again.
type statusRecord struct {
	id   string
	last statusReport
}

var (
	reportBuilding    = statusReport{"working", "Building"}
	reportBuildFailed = statusReport{"error", "Build failed"}
	reportUnhealthy   = statusReport{"error", "Health check failed"}
)

func newProgramStatus(out io.Writer) *programStatus {
	return &programStatus{out: out, run: runID(), active: map[statusKey]statusRecord{}}
}

// runID names this run's subtree: two rerun instances watching the same target
// must not share records.
func runID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}

// rowReports is what a row's cells say. A compose row's health is its
// container's, not the service's, and a stopped row reports nothing.
// The zero report means none.
func rowReports(t *tuiTarget) (build, health statusReport) {
	if t.spec.Kind == composeKind || t.state == stateStopped {
		return
	}
	switch {
	case t.building:
		build = reportBuilding
	case t.buildFailed:
		build = reportBuildFailed
	}
	if t.runStarted && t.state.up() && t.health == healthUnhealthy {
		health = reportUnhealthy
	}
	return
}

// sync reports every record that changed since the last call and clears the
// ones no row holds any more, removed rows included. It runs on the model's
// event loop, not in a tea.Cmd: asynchronous writes could put an old error after its clear.
func (s *programStatus) sync(targets []*tuiTarget) {
	if s == nil {
		return
	}
	if s.active == nil {
		s.active = map[statusKey]statusRecord{}
	}
	want := make(map[statusKey]statusReport, 2*len(targets))
	for _, t := range targets {
		build, health := rowReports(t)
		if build != (statusReport{}) {
			want[statusKey{t.spec.Name, "build"}] = build
		}
		if health != (statusReport{}) {
			want[statusKey{t.spec.Name, "health"}] = health
		}
	}
	// Only what changed pays for a flag lookup, a sort, or a hash. An idle
	// spinner tick sends nothing.
	var clears, reports []statusKey
	for k := range s.active {
		if _, ok := want[k]; !ok {
			clears = append(clears, k)
		}
	}
	for k, r := range want {
		if rec, ok := s.active[k]; !ok || rec.last != r {
			reports = append(reports, k)
		}
	}
	if len(clears) == 0 && len(reports) == 0 {
		return
	}
	byKey := func(a, b statusKey) int {
		return cmp.Or(cmp.Compare(a.target, b.target), cmp.Compare(a.topic, b.topic))
	}
	slices.SortFunc(clears, byKey)
	slices.SortFunc(reports, byKey)
	for _, k := range clears {
		fmt.Fprintf(s.out, "\x1b]7501;id=%s:state=clear\x1b\\", s.idFor(k))
		delete(s.active, k)
	}
	enc := base64.StdEncoding.EncodeToString
	for _, k := range reports {
		r := want[k]
		id := s.idFor(k)
		fmt.Fprintf(s.out, "\x1b]7501;id=%s:app=rerun:state=%s:title=%s:msg=%s\x1b\\",
			id, r.state, enc([]byte(statusTitle(k.target))), enc([]byte(r.msg)))
		s.active[k] = statusRecord{id: id, last: r}
	}
}

// idFor is the record's id, computed once per target and key.
func (s *programStatus) idFor(k statusKey) string {
	if rec, ok := s.active[k]; ok {
		return rec.id
	}
	hash := sha256.Sum256([]byte(k.target))
	return fmt.Sprintf("rerun/%s/%x/%s", s.run, hash[:12], k.topic)
}

// close clears this run's subtree, on quit.
func (s *programStatus) close() {
	if s != nil && len(s.active) > 0 {
		fmt.Fprintf(s.out, "\x1b]7501;id=rerun/%s:state=clear\x1b\\", s.run)
		clear(s.active)
	}
}

// statusTitle is the target name as printable UTF-8 of at most 192 bytes.
func statusTitle(name string) string {
	title := printable(name, false)
	if len(title) <= 192 {
		return title
	}
	n := 192
	for n > 0 && !utf8.RuneStart(title[n]) {
		n--
	}
	return title[:n]
}
