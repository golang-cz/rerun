package main

import (
	"os"
	"path/filepath"
	"strings"
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

// captureStdio points os.Stdout/os.Stderr at temp files for the duration of
// run and returns what it wrote, so error paths can be checked line by line.
func captureStdio(t *testing.T, run func() int) (stdout, stderr string, code int) {
	t.Helper()
	outFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	code = run()

	for _, f := range []*os.File{outFile, errFile} {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.ReadFile(outFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.ReadFile(errFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(errOut), code
}

// A broken rerun.yml prints the parse error and one hint on stderr. Stdout
// stays empty and the usage wall never appears.
func TestRunConfigModeBrokenConfigIsStderrOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ConfigFileName), "api:\n  cmd: echo hi\n  start: true\n")
	t.Chdir(dir)

	stdout, stderr, code := captureStdio(t, func() int { return runConfigMode(Args{}) })

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr has %d lines, want 2: %q", len(lines), stderr)
	}
	if !strings.HasPrefix(lines[0], "rerun: ") || !strings.Contains(lines[0], `unknown field "start"`) {
		t.Errorf("first line = %q, want the parse error", lines[0])
	}
	if lines[1] != "rerun: "+configHint {
		t.Errorf("second line = %q, want %q", lines[1], "rerun: "+configHint)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr leaks the usage wall: %q", stderr)
	}
}

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

// -no-gitignore reaches both the ad-hoc target and every rerun.yml target.
func TestNoGitignorePlumbsIntoTargets(t *testing.T) {
	args := Args{NoGitignore: true, Cmd: []string{"cmd"}}
	if !adHocTarget(args).NoGitignore {
		t.Fatal("ad-hoc target did not inherit NoGitignore")
	}

	cfg := configFromYAML(t, threeTargetsYAML)
	for _, target := range resolveTargets(cfg, args) {
		if !target.NoGitignore {
			t.Fatalf("target %q did not inherit NoGitignore", target.Name)
		}
	}
}
