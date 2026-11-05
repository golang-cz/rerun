package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The stream keeps only a target's own output; rerun's lines go elsewhere.
func TestStreamHoldsOnlyTargetOutput(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{
		{kind: evStarting, target: "web"},
		{kind: evStarted, target: "web", at: time.Now()},
		{kind: evOutput, target: "web", line: "listening"},
		{kind: evChanged, target: "web", change: loopChange("main.go"), count: 1, first: "main.go"},
		{kind: evBackoff, target: "web", delay: time.Second},
		{kind: evExited, target: "web", code: 1},
		{kind: evReconcile, line: "── config reloaded ── +api ──"},
		{kind: evNotice, line: "── config reload failed: bad yaml ──", err: errors.New("bad yaml")},
		{kind: evWatcherErr, target: "web", err: errors.New("too many open files")},
		{kind: evRunnerErr, target: "web", err: errors.New("executable file not found")},
		{kind: evStarted, target: "web", at: time.Now()},
		{kind: evOutput, target: "web", line: "ready"},
	})

	var got []string
	for i, line := range m.visibleRaw {
		if !m.visibleSep[i].isSep() {
			got = append(got, line)
		}
	}
	if want := []string{"listening", "ready"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream = %q, want only the target's output %q", got, want)
	}
}

// An info notice fades on its own once noticeDuration has passed.
func TestInfoNoticeFadesAfterItsWindow(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = time.Unix(1000, 0)
	m.applyEvent(event{kind: evReconcile, line: "── config reloaded ── +api ──"})

	if got := bottomPaneLine(m); !strings.Contains(got, "config reloaded") {
		t.Fatalf("footer = %q, want the reload notice", got)
	}

	m.now = m.now.Add(noticeDuration - time.Second)
	if got := bottomPaneLine(m); !strings.Contains(got, "config reloaded") {
		t.Fatalf("footer = %q, want the notice before its window ends", got)
	}

	m.now = m.now.Add(2 * time.Second)
	if got := bottomPaneLine(m); strings.Contains(got, "config reloaded") {
		t.Fatalf("footer = %q, want the notice gone after %s", got, noticeDuration)
	}
}

// An error notice outlives any wait and clears only on the next key press.
func TestErrorNoticeStaysUntilKeyPress(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.now = time.Unix(1000, 0)
	m.applyEvent(event{kind: evWatcherErr, target: "web", err: errors.New("too many open files")})

	m.now = m.now.Add(time.Hour)
	if got := bottomPaneLine(m); !strings.Contains(got, "too many open files") {
		t.Fatalf("footer = %q, want the error after an hour without a key", got)
	}

	press(m, "j")
	if got := bottomPaneLine(m); strings.Contains(got, "too many open files") {
		t.Fatalf("footer = %q, want the error cleared by the key press", got)
	}
}
