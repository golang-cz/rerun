package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fastHealth(t *testing.T) {
	t.Helper()
	i, to, f := healthInterval, healthTimeout, healthFailures
	healthInterval, healthTimeout, healthFailures = 10*time.Millisecond, 300*time.Millisecond, 3
	t.Cleanup(func() { healthInterval, healthTimeout, healthFailures = i, to, f })
}

func TestHTTPProbeStatusRange(t *testing.T) {
	var code atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()
	probe := newProbe(srv.URL)
	for c, healthy := range map[int32]bool{200: true, 204: true, 302: true, 399: true, 400: false, 404: false, 500: false} {
		code.Store(c)
		if err := probe(context.Background()); (err == nil) != healthy {
			t.Errorf("status %d: err = %v, want healthy=%v", c, err, healthy)
		}
	}
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	probe := newProbe("tcp://" + ln.Addr().String())
	if err := probe(context.Background()); err != nil {
		t.Errorf("open port: %v", err)
	}
	ln.Close()
	if err := probe(context.Background()); err == nil {
		t.Error("closed port reported healthy")
	}
}

func TestCommandProbeExitCodesAndTimeout(t *testing.T) {
	if err := newProbe("exit 0")(context.Background()); err != nil {
		t.Errorf("exit 0: %v", err)
	}
	if err := newProbe("exit 2")(context.Background()); err == nil {
		t.Error("exit 2 reported healthy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := newProbe("sleep 30")(ctx); err == nil {
		t.Error("timed-out command reported healthy")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timed-out command was not killed")
	}
}

type healthLog struct {
	mu     sync.Mutex
	states []healthState
}

func (l *healthLog) report(s healthState, _ time.Time, _ time.Duration) {
	l.mu.Lock()
	l.states = append(l.states, s)
	l.mu.Unlock()
}

func (l *healthLog) get() []healthState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]healthState(nil), l.states...)
}

func waitStates(t *testing.T, l *healthLog, n int) []healthState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := l.get(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("states = %v, want %d", l.get(), n)
	return nil
}

func TestMonitorHealthTransitions(t *testing.T) {
	fastHealth(t)
	var ok atomic.Bool
	var calls atomic.Int32
	probe := func(context.Context) error {
		calls.Add(1)
		if ok.Load() {
			return nil
		}
		return context.DeadlineExceeded
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	l := &healthLog{}
	go func() { defer close(done); monitorHealth(ctx, probe, time.Now(), l.report) }()

	// Failing while starting never turns unhealthy.
	time.Sleep(100 * time.Millisecond)
	if got := l.get(); len(got) != 0 {
		t.Fatalf("states while starting = %v", got)
	}
	ok.Store(true)
	waitStates(t, l, 1)

	// Two failures are not enough.
	ok.Store(false)
	n := calls.Load()
	for calls.Load() < n+2 {
		time.Sleep(time.Millisecond)
	}
	ok.Store(true)
	time.Sleep(60 * time.Millisecond)
	if got := l.get(); len(got) != 1 {
		t.Fatalf("two failures changed state: %v", got)
	}

	ok.Store(false)
	got := waitStates(t, l, 2)
	if got[1] != healthUnhealthy {
		t.Fatalf("states = %v", got)
	}
	ok.Store(true)
	got = waitStates(t, l, 3)
	if got[2] != healthHealthy {
		t.Fatalf("states = %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop")
	}
	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != after {
		t.Error("probes continued after stop")
	}
}

func TestMonitorHealthNeverOverlapsProbes(t *testing.T) {
	fastHealth(t)
	var inflight, maxSeen atomic.Int32
	probe := func(ctx context.Context) error {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		if n > maxSeen.Load() {
			maxSeen.Store(n)
		}
		time.Sleep(40 * time.Millisecond)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	monitorHealth(ctx, probe, time.Now(), func(healthState, time.Time, time.Duration) {})
	if maxSeen.Load() != 1 {
		t.Errorf("max concurrent probes = %d", maxSeen.Load())
	}
}

func TestMonitorHealthLeavesNoGoroutines(t *testing.T) {
	fastHealth(t)
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			monitorHealth(ctx, func(context.Context) error { return nil }, time.Now(), func(healthState, time.Time, time.Duration) {})
		}()
		time.Sleep(5 * time.Millisecond)
		cancel()
		<-done
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines %d -> %d", before, after)
	}
}
