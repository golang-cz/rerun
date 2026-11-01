package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// threeTargetsYAML is the fixture the arg-flow tests share: one default-on
// target, one `selected: false`, one default-on target nobody names.
const threeTargetsYAML = `frontend:
  cmd: pnpm run dev

api:
  cmd: go run ./cmd/api
  selected: false

test:
  cmd: go test ./...
`

func configFromYAML(t *testing.T, yml string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, yml)
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return cfg
}

// Naming targets picks what starts; the list itself always stays whole.
func TestResolveTargetsNamedIsTheStartSet(t *testing.T) {
	cfg := configFromYAML(t, threeTargetsYAML)

	tests := []struct {
		name string
		args Args
		want []bool // Selected, in rerun.yml order
	}{
		{name: "two named", args: Args{Targets: []string{"api", "frontend"}}, want: []bool{true, true, false}},
		{name: "one named, yml selected false", args: Args{Target: "api"}, want: []bool{false, true, false}},
		{name: "one named, yml selected true", args: Args{Targets: []string{"test"}}, want: []bool{false, false, true}},
		{name: "no args keeps yml defaults", args: Args{}, want: []bool{true, false, true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := resolveTargets(cfg, tt.args)
			if len(targets) != len(cfg.Targets) {
				t.Fatalf("got %d targets, want all %d", len(targets), len(cfg.Targets))
			}
			for i, want := range tt.want {
				if targets[i].Name != cfg.Targets[i].Name {
					t.Fatalf("target %d = %q, want %q", i, targets[i].Name, cfg.Targets[i].Name)
				}
				if targets[i].Selected != want {
					t.Errorf("%s selected = %v, want %v", targets[i].Name, targets[i].Selected, want)
				}
			}
		})
	}
}

// A CLI override must reach the targets a user can start by hand later.
func TestResolveTargetsAppliesOverridesToEveryTarget(t *testing.T) {
	cfg := configFromYAML(t, threeTargetsYAML)
	targets := resolveTargets(cfg, Args{Targets: []string{"frontend"}, Delay: 500 * time.Millisecond})

	for _, target := range targets {
		if target.Delay != 500*time.Millisecond {
			t.Errorf("%s delay = %v, want 500ms", target.Name, target.Delay)
		}
	}
}

func TestResolveTargetsLeavesConfigUntouched(t *testing.T) {
	cfg := configFromYAML(t, threeTargetsYAML)
	resolveTargets(cfg, Args{Targets: []string{"api"}})

	if !cfg.Targets[0].Selected {
		t.Error("resolveTargets cleared Selected on cfg.Targets[0]")
	}
	if cfg.Targets[1].Selected {
		t.Error("resolveTargets set Selected on cfg.Targets[1]")
	}
}

func TestWarnNoGitignore(t *testing.T) {
	var stderr bytes.Buffer
	warnNoGitignore(Args{NoGitignore: true}, &stderr)
	if got, want := stderr.String(), noGitignoreWarning+"\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}

	stderr.Reset()
	warnNoGitignore(Args{}, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("unexpected warning without the flag: %q", stderr.String())
	}
}
