package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-cz/rerun"
)

type healthState string

const (
	healthStarting  healthState = "starting"
	healthHealthy   healthState = "healthy"
	healthUnhealthy healthState = "unhealthy"
)

// Package vars so tests can shrink them to milliseconds.
var (
	healthInterval = time.Second
	healthTimeout  = time.Second
	// healthFailures is how many consecutive failures turn healthy into unhealthy.
	healthFailures = 3
)

type probeFunc func(ctx context.Context) error

func newProbe(dir, spec string) probeFunc {
	switch {
	case strings.HasPrefix(spec, "http://"), strings.HasPrefix(spec, "https://"):
		return httpProbe(spec)
	case strings.HasPrefix(spec, "tcp://"):
		return tcpProbe(strings.TrimPrefix(spec, "tcp://"))
	default:
		return commandProbe(dir, spec)
	}
}

func httpProbe(url string) probeFunc {
	client := &http.Client{
		// A redirect is itself a 3xx answer, which counts as healthy.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		if resp.StatusCode < 200 || resp.StatusCode > 399 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}
}

func tcpProbe(addr string) probeFunc {
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// commandProbe runs the shell command in rerun's own dir and env, which the
// target's run command shares. A timeout kills its whole process group.
func commandProbe(dir, script string) probeFunc {
	return func(ctx context.Context) error {
		devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer devNull.Close()
		cmd, err := rerun.StartCommand([]string{script}, time.Second,
			rerun.WithDir(dir), rerun.WithStdin(devNull), rerun.WithStdout(devNull), rerun.WithStderr(devNull))
		if err != nil {
			return err
		}
		if err := cmd.WaitExit(ctx); err != nil {
			_ = cmd.KillForce()
			_ = cmd.WaitExit(context.Background())
			return err
		}
		code, err := cmd.ExitState()
		if code != 0 || err != nil {
			return fmt.Errorf("exit %d", code)
		}
		return nil
	}
}

func monitorHealth(ctx context.Context, probe probeFunc, started time.Time, report func(state healthState, at time.Time, sinceStart time.Duration)) {
	state := healthStarting
	fails := 0
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		pctx, cancel := context.WithTimeout(ctx, healthTimeout)
		err := probe(pctx)
		cancel()
		if ctx.Err() != nil {
			return
		}

		next := state
		if err == nil {
			fails = 0
			next = healthHealthy
		} else {
			fails++
			if state == healthHealthy && fails >= healthFailures {
				next = healthUnhealthy
			}
		}
		if next != state {
			state = next
			now := time.Now()
			report(state, now, now.Sub(started))
		}
		timer.Reset(healthInterval)
	}
}
