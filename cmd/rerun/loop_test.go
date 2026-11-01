package main

import (
	"context"
	"testing"
	"time"

	"github.com/golang-cz/rerun"
)

// loopChange is a one-file change batch, the trigger set a cycle carries.
func loopChange(path string) rerun.ChangeSet {
	return rerun.ChangeSet{Files: map[string]struct{}{path: {}}, FirstFile: path}
}

// shortCycle drives one change the way a running generation would: the last
// start just happened, so the restart reads as a short run.
func shortCycle(t *testing.T, r *targetRunner, path string) {
	t.Helper()
	r.mu.Lock()
	r.lastStart = time.Now().Add(-time.Millisecond)
	r.mu.Unlock()
	r.handleChange(context.Background(), loopChange(path))
}

func longCycle(t *testing.T, r *targetRunner, path string) {
	t.Helper()
	r.mu.Lock()
	r.lastStart = time.Now().Add(-loopRunWindow - time.Second)
	r.mu.Unlock()
	r.handleChange(context.Background(), loopChange(path))
}

func countKind(s *recordingSink, kind eventKind) []event {
	var out []event
	for _, ev := range s.all() {
		if ev.kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// Four short runs that all shared gen.go are a self-write loop; the warning
// names the file once and never repeats for the same target and path.
func TestLoopWarningOncePerPath(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)

	for i := 0; i < loopCycles; i++ {
		shortCycle(t, r, "gen.go")
	}

	loops := countKind(sink, evLoop)
	if len(loops) != 1 {
		t.Fatalf("evLoop count = %d, want 1", len(loops))
	}
	if loops[0].target != "t" || loops[0].line != "gen.go" {
		t.Fatalf("evLoop = %+v, want target t path gen.go", loops[0])
	}

	shortCycle(t, r, "gen.go")
	if got := len(countKind(sink, evLoop)); got != 1 {
		t.Fatalf("evLoop count after a 5th cycle = %d, want 1", got)
	}
}

// Disjoint triggers do not intersect, so four short runs stay quiet.
func TestNoLoopWarningForDisjointTriggers(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)

	for _, path := range []string{"a.go", "b.go", "c.go", "d.go"} {
		shortCycle(t, r, path)
	}

	if got := len(countKind(sink, evLoop)); got != 0 {
		t.Fatalf("evLoop count = %d, want 0", got)
	}
}

// A run that outlived the window resets the count, so an earlier short streak
// cannot combine with later ones.
func TestLongRunResetsLoopCount(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)

	for i := 0; i < loopCycles-1; i++ {
		shortCycle(t, r, "gen.go")
	}
	longCycle(t, r, "gen.go")
	shortCycle(t, r, "gen.go")

	if got := len(countKind(sink, evLoop)); got != 0 {
		t.Fatalf("evLoop count after a long run = %d, want 0", got)
	}
}

// modelSink drives a TUI model from a runner without a terminal.
type modelSink struct{ m *tuiModel }

func (s *modelSink) emit(ev event) { s.m.applyEvent(ev) }

// The warning lands in the pane once, even across a 5th looping cycle.
func TestLoopWarningLineAppearsOnce(t *testing.T) {
	m := newTestModel(t, target("web"))
	r := newTargetRunner(Target{Name: "web", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, &modelSink{m: m})

	for i := 0; i < loopCycles+1; i++ {
		shortCycle(t, r, "gen.go")
	}

	want := "rerun: loop suspected — web restarted 4× in a row after gen.go changed; it may be writing it — add it to except"
	var got int
	for _, line := range m.merged {
		if line.text == want {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("warning line appears %d times, want 1", got)
	}
}
