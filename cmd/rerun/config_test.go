package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-cz/rerun"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseConfigStringOrList(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `frontend:
  desc: Vite dev server
  on: web
  except: [node_modules, dist]
  cmd: pnpm run dev
  delay: 250ms
  cooldown: 2s
  grace: 1s

api:
  on: [cmd, internal]
  cmd: go run ./cmd/api
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(cfg.Targets))
	}

	frontend := cfg.Targets[0]
	if frontend.Name != "frontend" {
		t.Errorf("name = %q, want frontend", frontend.Name)
	}
	if frontend.Desc != "Vite dev server" {
		t.Errorf("desc = %q", frontend.Desc)
	}
	if !reflect.DeepEqual(frontend.On, []string{"web"}) {
		t.Errorf("on = %q, want [web]", frontend.On)
	}
	if !reflect.DeepEqual(frontend.Except, []string{"node_modules", "dist"}) {
		t.Errorf("except = %q", frontend.Except)
	}
	if frontend.Cmd != "pnpm run dev" {
		t.Errorf("cmd = %q", frontend.Cmd)
	}
	if frontend.Delay != 250*time.Millisecond {
		t.Errorf("delay = %v", frontend.Delay)
	}
	if frontend.Cooldown != 2*time.Second {
		t.Errorf("cooldown = %v", frontend.Cooldown)
	}
	if frontend.Grace != time.Second {
		t.Errorf("grace = %v", frontend.Grace)
	}

	api := cfg.Targets[1]
	if !reflect.DeepEqual(api.On, []string{"cmd", "internal"}) {
		t.Errorf("api on = %q", api.On)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	target := cfg.Targets[0]
	if target.Delay != defaultDelay {
		t.Errorf("delay = %v, want %v", target.Delay, defaultDelay)
	}
	if target.Grace != rerun.DefaultGrace {
		t.Errorf("grace = %v, want %v", target.Grace, rerun.DefaultGrace)
	}
	if target.Cooldown != 0 {
		t.Errorf("cooldown = %v, want 0", target.Cooldown)
	}
}

func TestParseConfigPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `zeta:
  cmd: echo z
alpha:
  cmd: echo a
middle:
  cmd: echo m
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := []string{"zeta", "alpha", "middle"}
	if !reflect.DeepEqual(cfg.Names(), want) {
		t.Fatalf("names = %q, want %q", cfg.Names(), want)
	}
}

func TestParseConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub []string
	}{
		{
			name:    "missing cmd",
			content: "api:\n  desc: x\n",
			wantSub: []string{"api", "cmd is required"},
		},
		{
			name:    "unknown field",
			content: "api:\n  cmd: echo hi\n  bogus: 1\n",
			wantSub: []string{`target "api"`, `"bogus"`},
		},
		{
			name:    "bad duration",
			content: "api:\n  cmd: echo hi\n  delay: abc\n",
			wantSub: []string{`target "api"`, "delay", `invalid duration "abc"`},
		},
		{
			name:    "non-mapping target",
			content: "api: echo hi\n",
			wantSub: []string{`target "api"`},
		},
		{
			name:    "duplicate target",
			content: "api:\n  cmd: a\napi:\n  cmd: b\n",
			wantSub: []string{"duplicate target", "api"},
		},
		{
			name:    "duplicate field",
			content: "api:\n  cmd: a\n  cmd: b\n",
			wantSub: []string{`target "api"`, `duplicate field "cmd"`},
		},
		{
			name:    "no targets",
			content: "\n",
			wantSub: []string{"no targets"},
		},
		{
			name:    "integer on",
			content: "api:\n  cmd: echo hi\n  on: 5\n",
			wantSub: []string{`target "api"`, `"on"`, "got int"},
		},
		{
			name:    "mapping on",
			content: "api:\n  cmd: echo hi\n  on: {a: b}\n",
			wantSub: []string{`target "api"`, `"on"`, "expected a string or a list of strings"},
		},
		{
			name:    "null target",
			content: "api:\n",
			wantSub: []string{`target "api"`, "expected a mapping"},
		},
		{
			name:    "whitespace cmd",
			content: "api:\n  cmd: \"  \"\n",
			wantSub: []string{`target "api"`, "cmd is required"},
		},
		{
			name:    "non-string list item",
			content: "api:\n  cmd: echo hi\n  on: [\"a\", 5]\n",
			wantSub: []string{`target "api"`, `"on"`, "got int"},
		},
		{
			name:    "non-string cmd",
			content: "api:\n  cmd: 123\n",
			wantSub: []string{`target "api"`, `field "cmd"`, "got int"},
		},
		{
			name:    "non-string desc",
			content: "api:\n  cmd: echo hi\n  desc: true\n",
			wantSub: []string{`target "api"`, `field "desc"`, "got bool"},
		},
		{
			name:    "integer delay",
			content: "api:\n  cmd: echo hi\n  delay: 5\n",
			wantSub: []string{`target "api"`, `field "delay"`, "got int"},
		},
		{
			name:    "negative delay",
			content: "api:\n  cmd: echo hi\n  delay: -1s\n",
			wantSub: []string{`target "api"`, `field "delay"`, "must not be negative"},
		},
		{
			name:    "negative grace",
			content: "api:\n  cmd: echo hi\n  grace: -2s\n",
			wantSub: []string{`target "api"`, `field "grace"`, "must not be negative"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigFileName)
			writeFile(t, path, tt.content)

			_, err := ParseConfig(path)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want substring %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestParseConfigNullOnIsEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n  on:\n  except: \"\"\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Targets[0].On) != 0 {
		t.Errorf("on = %q, want empty", cfg.Targets[0].On)
	}
	if len(cfg.Targets[0].Except) != 0 {
		t.Errorf("except = %q, want empty", cfg.Targets[0].Except)
	}
}

func TestParseConfigAliasOnAndExcept(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `api:
  on: &api_watch [apps/api, pkg, proto]
  except: &api_skip [vendor, node_modules]
  cmd: go run ./cmd/api

web:
  on: *api_watch
  except: *api_skip
  cmd: pnpm run dev

docs:
  on: *api_watch
  cmd: echo docs
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	wantWatch := []string{"apps/api", "pkg", "proto"}
	for _, target := range cfg.Targets {
		if !reflect.DeepEqual(target.On, wantWatch) {
			t.Errorf("%s: on = %q, want %q", target.Name, target.On, wantWatch)
		}
	}

	wantSkip := []string{"vendor", "node_modules"}
	for _, name := range []string{"api", "web"} {
		target, ok := cfg.Find(name)
		if !ok {
			t.Fatalf("target %q not found", name)
		}
		if !reflect.DeepEqual(target.Except, wantSkip) {
			t.Errorf("%s: except = %q, want %q", name, target.Except, wantSkip)
		}
	}
}

func TestParseConfigAliasScalars(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `base: &base
  on: [&shared proto]
  except: &skip vendor
  cmd: &api_cmd go run ./cmd/api
  desc: &api_desc API server

copy: *base

web:
  on: [*shared, apps]
  except: *skip
  cmd: *api_cmd
  desc: *api_desc
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	copyTarget, ok := cfg.Find("copy")
	if !ok {
		t.Fatal("target \"copy\" not found")
	}
	if !reflect.DeepEqual(copyTarget.On, []string{"proto"}) {
		t.Errorf("copy on = %q", copyTarget.On)
	}
	if copyTarget.Cmd != "go run ./cmd/api" {
		t.Errorf("copy cmd = %q", copyTarget.Cmd)
	}
	if copyTarget.Desc != "API server" {
		t.Errorf("copy desc = %q", copyTarget.Desc)
	}

	web, ok := cfg.Find("web")
	if !ok {
		t.Fatal("target \"web\" not found")
	}
	if !reflect.DeepEqual(web.On, []string{"proto", "apps"}) {
		t.Errorf("web on = %q", web.On)
	}
	if !reflect.DeepEqual(web.Except, []string{"vendor"}) {
		t.Errorf("web except = %q", web.Except)
	}
	if web.Cmd != "go run ./cmd/api" {
		t.Errorf("web cmd = %q", web.Cmd)
	}
	if web.Desc != "API server" {
		t.Errorf("web desc = %q", web.Desc)
	}
}

func TestParseConfigAliasErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub []string
	}{
		{
			name:    "alias to bool on",
			content: "api:\n  cmd: echo hi\n  open: &b true\n\nweb:\n  cmd: echo hi\n  on: *b\n",
			wantSub: []string{`target "web"`, `field "on"`, "expected a string or a list of strings, got bool"},
		},
		{
			name:    "alias to bool as list item",
			content: "api:\n  cmd: echo hi\n  open: &b true\n\nweb:\n  cmd: echo hi\n  on: [*b]\n",
			wantSub: []string{`target "web"`, `field "on"`, "expected a list of strings, got bool"},
		},
		{
			name:    "alias to bool desc",
			content: "api:\n  cmd: echo hi\n  open: &b true\n\nweb:\n  cmd: echo hi\n  desc: *b\n",
			wantSub: []string{`target "web"`, `field "desc"`, "expected a string, got bool"},
		},
		{
			name:    "alias to non-duration string",
			content: "api:\n  cmd: echo hi\n  desc: &d abc\n\nweb:\n  cmd: echo hi\n  delay: *d\n",
			wantSub: []string{`target "web"`, `field "delay"`, `invalid duration "abc"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConfigFileName)
			writeFile(t, path, tt.content)

			_, err := ParseConfig(path)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, sub := range append([]string{path}, tt.wantSub...) {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want substring %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestParseConfigUndefinedAliasErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: echo hi\n  on: *nope\n")

	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.HasPrefix(err.Error(), path) {
		t.Errorf("error %q does not start with the file path", err.Error())
	}
	if strings.Contains(err.Error(), "yaml:") {
		t.Errorf("error leaks the yaml prefix: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not name the missing anchor", err.Error())
	}
}

func TestParseConfigURLSelectedOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `frontend:
  cmd: pnpm run dev
  url: http://localhost:5173
  open: true

api:
  cmd: go run ./cmd/api
  selected: false
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	frontend := cfg.Targets[0]
	if frontend.URL != "http://localhost:5173" {
		t.Errorf("url = %q", frontend.URL)
	}
	if !frontend.Open {
		t.Error("open = false, want true")
	}
	if !frontend.Selected {
		t.Error("selected = false by default, want true")
	}

	api := cfg.Targets[1]
	if api.Selected {
		t.Error("selected = true for `selected: false`")
	}
	if api.Open {
		t.Error("open = true by default")
	}
	if api.URL != "" {
		t.Errorf("url = %q, want empty", api.URL)
	}
}

func TestParseConfigBoolFieldErrors(t *testing.T) {
	for _, field := range []string{"open", "selected"} {
		path := filepath.Join(t.TempDir(), ConfigFileName)
		writeFile(t, path, "api:\n  cmd: echo hi\n  "+field+": 1\n")

		_, err := ParseConfig(path)
		if err == nil {
			t.Fatalf("%s: expected an error, got nil", field)
		}
		for _, sub := range []string{`target "api"`, `field "` + field + `"`, "expected a boolean, got int"} {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("%s: error = %q, want substring %q", field, err.Error(), sub)
			}
		}
	}
}

func TestParseConfigInvalidYAMLNamesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "api:\n  cmd: [unterminated\n")

	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.HasPrefix(err.Error(), path) {
		t.Errorf("error %q does not start with the file path", err.Error())
	}
	if strings.Contains(err.Error(), "yaml:") {
		t.Errorf("error leaks the yaml prefix: %q", err.Error())
	}
}

func TestLoadConfigDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ConfigFileName), "api:\n  cmd: echo hi\n")

	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(nested)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Path != filepath.Join(root, ConfigFileName) {
		t.Fatalf("path = %q, want %q", cfg.Path, filepath.Join(root, ConfigFileName))
	}
}

func TestLoadConfigNearestWins(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ConfigFileName), "outer:\n  cmd: echo outer\n")

	nested := filepath.Join(root, "a")
	writeFile(t, filepath.Join(nested, ConfigFileName), "inner:\n  cmd: echo inner\n")

	cfg, err := LoadConfig(nested)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(cfg.Names(), []string{"inner"}) {
		t.Fatalf("names = %q, want [inner]", cfg.Names())
	}
}

func TestLoadConfigStopsAtGitRoot(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, ConfigFileName), "outer:\n  cmd: echo outer\n")

	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(nested)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("LoadConfig = %v, want ErrConfigNotFound", err)
	}
}

func TestLoadConfigNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(dir)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("LoadConfig = %v, want ErrConfigNotFound", err)
	}
}
