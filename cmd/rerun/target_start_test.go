//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A failed first start stays on screen: nothing clears the output after its error.
func TestFailedFirstStartIsNotClearedAfterItsError(t *testing.T) {
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	r := newTargetRunner(Target{Cmd: script, Start: startAuto, Grace: 200 * time.Millisecond, Selected: true}, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(countKind(sink, evRunnerErr)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if n := len(countKind(sink, evRunnerErr)); n != 1 {
		t.Fatalf("evRunnerErr = %d, want 1", n)
	}
	if n := len(countKind(sink, evCleared)); n != 0 {
		t.Fatalf("evCleared = %d after a start error, want 0", n)
	}
}
