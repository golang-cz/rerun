package main

import (
	"testing"
	"time"
)

func sharedPlainEvent(kind eventKind, target string) event {
	return event{kind: kind, target: target, owner: "api", cmd: "go build ./cmd/app", names: []string{"api", "worker"}}
}

// The plain log writes a shared build once, from its owner, in the TUI row's shape.
func TestPlainSinkLogsSharedBuildOnceByItsOwner(t *testing.T) {
	s := newPlainSink(nil)
	got := s.render(sharedPlainEvent(evBuildStarted, "api")) + s.render(sharedPlainEvent(evBuildStarted, "worker"))
	if want := "api +1 building\n"; got != want {
		t.Fatalf("start = %q, want %q", got, want)
	}

	ok := sharedPlainEvent(evBuildOK, "api")
	ok.dur = time.Second
	okWorker := sharedPlainEvent(evBuildOK, "worker")
	okWorker.dur = time.Second
	got = s.render(ok) + s.render(okWorker)
	if want := "api +1 built 1s\n"; got != want {
		t.Fatalf("built = %q, want %q", got, want)
	}
}

func TestPlainSinkLogsSharedFailureOnce(t *testing.T) {
	s := newPlainSink(nil)
	fail := sharedPlainEvent(evBuildFailed, "api")
	fail.dur, fail.err, fail.lines = time.Second, errTest, []string{"main.go:3: oops"}
	worker := sharedPlainEvent(evBuildFailed, "worker")
	worker.dur, worker.err = time.Second, errTest
	got := s.render(fail) + s.render(worker)
	want := "api +1 build failed · boom\n[api] build: main.go:3: oops\n"
	if got != want {
		t.Fatalf("failure = %q, want %q", got, want)
	}
}

// A single-target build has no +N.
func TestPlainSinkSingleTargetBuildHasNoCount(t *testing.T) {
	s := newPlainSink(nil)
	start := event{kind: evBuildStarted, target: "api", owner: "api", names: []string{"api"}}
	if got, want := s.render(start), "api building\n"; got != want {
		t.Fatalf("start = %q, want %q", got, want)
	}
	ok := event{kind: evBuildOK, target: "api", owner: "api", names: []string{"api"}, dur: time.Second}
	if got, want := s.render(ok), "api built 1s\n"; got != want {
		t.Fatalf("ok = %q, want %q", got, want)
	}
}

// The owner's log line covers a shared build, so no target's output lines print under its own label.
func TestPlainSinkRendersNoSharedBuildOutputPerTarget(t *testing.T) {
	s := newPlainSink(nil)
	for _, name := range []string{"api", "worker"} {
		ev := event{kind: evBuildOutput, target: name, owner: "api", line: "compiling", buildAt: time.Now()}
		if got := s.render(ev); got != "" {
			t.Errorf("%s: render = %q, want no line", name, got)
		}
	}
}
