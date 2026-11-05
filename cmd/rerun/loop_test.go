package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-cz/rerun"
)

// loopChange is a one-file change batch, the trigger set a cycle carries.
func loopChange(path string) rerun.ChangeSet {
	return rerun.ChangeSet{Files: map[string]struct{}{path: {}}, FirstFile: path}
}

// loopFile writes path now, so its mtime is the moment of the call.
func loopFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(time.Now().String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// shortCycle drives one change the way a running generation would: the file was
// written the instant after the last start, which only the process can do.
func shortCycle(t *testing.T, r *targetRunner, path string) {
	t.Helper()
	r.mu.Lock()
	r.lastStart = time.Now().Add(-time.Millisecond)
	r.mu.Unlock()
	loopFile(t, path)
	r.handleChange(context.Background(), loopChange(path))
}

// longCycle is a human save: the run had been up long before the file was written.
func longCycle(t *testing.T, r *targetRunner, path string) {
	t.Helper()
	r.mu.Lock()
	r.lastStart = time.Now().Add(-loopSkew - time.Second)
	r.mu.Unlock()
	loopFile(t, path)
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

// Six self-written re-triggers of gen.go are a loop: one event pauses the
// target, and every change held afterwards says so again.
func TestLoopPausesAndNamesTheFile(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
	gen := filepath.Join(t.TempDir(), "gen.go")

	for i := 0; i < loopCycles; i++ {
		shortCycle(t, r, gen)
	}
	loops := countKind(sink, evLoop)
	if len(loops) != 1 || loops[0].target != "t" || loops[0].line != gen {
		t.Fatalf("evLoop = %+v, want one for %s", loops, gen)
	}

	shortCycle(t, r, gen)
	if got := len(countKind(sink, evLoop)); got != 2 {
		t.Fatalf("a held change must still be announced: evLoop count = %d, want 2", got)
	}
}

// Disjoint triggers do not intersect, so a streak of different files stays quiet.
func TestNoLoopForDisjointTriggers(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
	dir := t.TempDir()

	for i := 0; i < loopCycles+2; i++ {
		shortCycle(t, r, filepath.Join(dir, string(rune('a'+i))+".go"))
	}
	if got := len(countKind(sink, evLoop)); got != 0 {
		t.Fatalf("evLoop count = %d, want 0", got)
	}
}

// A save long after the run started resets the streak, so earlier cycles
// cannot combine with later ones.
func TestHumanSaveResetsLoopCount(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, sink)
	gen := filepath.Join(t.TempDir(), "gen.go")

	for i := 0; i < loopCycles-1; i++ {
		shortCycle(t, r, gen)
	}
	longCycle(t, r, gen)
	shortCycle(t, r, gen)

	if got := len(countKind(sink, evLoop)); got != 0 {
		t.Fatalf("evLoop count after a human save = %d, want 0", got)
	}
}

// modelSink drives a TUI model from a runner without a terminal.
type modelSink struct{ m *tuiModel }

func (s *modelSink) emit(ev event) { s.m.applyEvent(ev) }

// The warning goes to the footer, never the stream, even across a 5th looping cycle.
func TestLoopWarningGoesToFooter(t *testing.T) {
	m := newTestModel(t, target("web"))
	r := newTargetRunner(Target{Name: "web", Cmd: "true", Selected: true, Grace: rerun.DefaultGrace}, &modelSink{m: m})

	gen := filepath.Join(t.TempDir(), "gen.go")
	for i := 0; i < loopCycles+1; i++ {
		shortCycle(t, r, gen)
	}

	want := "rerun: loop suspected — web paused after 6× re-triggered by " + gen + "; add it to except, or change another file to resume"
	if m.notice != want || !m.noticeErr {
		t.Fatalf("footer = %q (error %v), want the loop warning as an error", m.notice, m.noticeErr)
	}
	for _, line := range m.visibleRaw {
		if line == want {
			t.Fatalf("the loop warning reached the stream: %q", line)
		}
	}
}

// Once paused, the same file no longer restarts the target; another file does.
func TestLoopPausesAndResumesOnAnotherFile(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "sleep 30", Selected: true, Grace: 100 * time.Millisecond}, sink)
	t.Cleanup(func() { r.killCurrent(context.Background()) })
	dir := t.TempDir()
	gen, other := filepath.Join(dir, "gen.go"), filepath.Join(dir, "other.go")

	for i := 0; i < loopCycles; i++ {
		shortCycle(t, r, gen)
	}
	started := len(countKind(sink, evStarted))
	shortCycle(t, r, gen)
	if got := len(countKind(sink, evStarted)); got != started {
		t.Fatalf("paused target restarted: %d -> %d", started, got)
	}
	if cmd := r.currentCmd(); cmd == nil || cmd.Exited() {
		t.Fatal("the last good run was not kept")
	}
	longCycle(t, r, other)
	if got := len(countKind(sink, evStarted)); got != started+1 {
		t.Fatalf("a change to another file did not resume: %d -> %d", started, got)
	}
}

// A held save after a quiet gap resumes the target instead of being dropped.
func TestPauseExpiresAfterAQuietGap(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "sleep 30", Selected: true, Grace: 100 * time.Millisecond}, sink)
	t.Cleanup(func() { r.killCurrent(context.Background()) })
	gen := filepath.Join(t.TempDir(), "gen.go")

	for i := 0; i < loopCycles; i++ {
		shortCycle(t, r, gen)
	}
	started := len(countKind(sink, evStarted))
	r.loopHeldAt = time.Now().Add(-loopQuiet - time.Second)
	longCycle(t, r, gen)
	if got := len(countKind(sink, evStarted)); got != started+1 {
		t.Fatalf("a save after the quiet gap was not applied: %d -> %d", started, got)
	}
}

// A pause ends after loopMaxPause even when held changes keep arriving.
func TestPauseHasAHardCap(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(Target{Name: "t", Cmd: "sleep 30", Selected: true, Grace: 100 * time.Millisecond}, sink)
	t.Cleanup(func() { r.killCurrent(context.Background()) })
	gen := filepath.Join(t.TempDir(), "gen.go")

	for i := 0; i < loopCycles; i++ {
		shortCycle(t, r, gen)
	}
	started := len(countKind(sink, evStarted))
	r.loopPausedAt = time.Now().Add(-loopMaxPause - time.Second)
	longCycle(t, r, gen)
	if got := len(countKind(sink, evStarted)); got != started+1 {
		t.Fatalf("a save past the pause cap was not applied: %d -> %d", started, got)
	}
}
