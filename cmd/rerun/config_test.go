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
	writeFile(t, path, `targets:
  frontend:
    desc: Vite dev server
    on: web
    except: [node_modules, dist]
    run: pnpm run dev
    delay: 250ms
    cooldown: 2s
    grace: 1s

  api:
    on: [cmd, internal]
    run: go run ./cmd/api
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
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n")

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
	writeFile(t, path, `targets:
  zeta:
    run: echo z
  alpha:
    run: echo a
  middle:
    run: echo m
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
			content: "targets:\n  api:\n    desc: x\n",
			wantSub: []string{"api", "run is required"},
		},
		{
			name:    "unknown field",
			content: "targets:\n  api:\n    run: echo hi\n    bogus: 1\n",
			wantSub: []string{`target "api"`, `"bogus"`, "fields: desc, on, except, run, build, health, open, columns, queries, group, muted, selected, start, delay, cooldown, grace"},
		},
		{
			name:    "unknown group",
			content: "groups:\n  backend: {}\n\ntargets:\n  api:\n    group: nope\n    run: echo hi\n",
			wantSub: []string{`target "api"`, `field "group"`, `unknown group "nope"`},
		},
		{
			name:    "groups is not a mapping",
			content: "groups: 5\ntargets:\n  api:\n    run: echo hi\n",
			wantSub: []string{"groups: expected a mapping of group names"},
		},
		{
			name:    "unknown field inside a group",
			content: "groups:\n  backend:\n    columns: [\"a {.b}\"]\n    bogus: 1\n\ntargets:\n  api:\n    run: echo hi\n",
			wantSub: []string{`group "backend"`, `unknown field "bogus"`, "fields: columns, queries, muted, fold, start"},
		},
		{
			name:    "groups with no targets",
			content: "groups:\n  backend:\n    columns: [\"a {.b}\"]\n",
			wantSub: []string{"no targets defined"},
		},
		{
			name:    "bad duration",
			content: "targets:\n  api:\n    run: echo hi\n    delay: abc\n",
			wantSub: []string{`target "api"`, "delay", `invalid duration "abc"`},
		},
		{
			name:    "non-mapping target",
			content: "targets:\n  api: echo hi\n",
			wantSub: []string{`target "api"`},
		},
		{
			name:    "duplicate target",
			content: "targets:\n  api:\n    run: a\n  api:\n    run: b\n",
			wantSub: []string{"duplicate target", "api"},
		},
		{
			name:    "duplicate field",
			content: "targets:\n  api:\n    run: a\n    run: b\n",
			wantSub: []string{`target "api"`, `duplicate field "run"`},
		},
		{
			name:    "no targets",
			content: "\n",
			wantSub: []string{"no targets"},
		},
		{
			name:    "integer on",
			content: "targets:\n  api:\n    run: echo hi\n    on: 5\n",
			wantSub: []string{`target "api"`, `"on"`, "got int"},
		},
		{
			name:    "mapping on",
			content: "targets:\n  api:\n    run: echo hi\n    on: {a: b}\n",
			wantSub: []string{`target "api"`, `"on"`, "expected a string or a list of strings"},
		},
		{
			name:    "null target",
			content: "targets:\n  api:\n",
			wantSub: []string{`target "api"`, "expected a mapping"},
		},
		{
			name:    "whitespace cmd",
			content: "targets:\n  api:\n    run: \"  \"\n",
			wantSub: []string{`target "api"`, "run is required"},
		},
		{
			name:    "column without expression",
			content: "targets:\n  api:\n    run: echo hi\n    columns: [\"method\"]\n",
			wantSub: []string{`target "api"`, `field "columns"`, "method"},
		},
		{
			name:    "column without name",
			content: "targets:\n  api:\n    run: echo hi\n    columns: [\"{.a}\"]\n",
			wantSub: []string{`target "api"`, "empty column name"},
		},
		{
			name:    "column with empty expression",
			content: "targets:\n  api:\n    run: echo hi\n    columns: [\"a {}\"]\n",
			wantSub: []string{`target "api"`, "empty column expression"},
		},
		{
			name:    "non-string column",
			content: "targets:\n  api:\n    run: echo hi\n    columns: [5]\n",
			wantSub: []string{`target "api"`, `"columns"`, "got int"},
		},
		{
			name:    "non-string list item",
			content: "targets:\n  api:\n    run: echo hi\n    on: [\"a\", 5]\n",
			wantSub: []string{`target "api"`, `"on"`, "got int"},
		},
		{
			name:    "non-string cmd",
			content: "targets:\n  api:\n    run: 123\n",
			wantSub: []string{`target "api"`, `field "run"`, "got int"},
		},
		{
			name:    "non-string desc",
			content: "targets:\n  api:\n    run: echo hi\n    desc: true\n",
			wantSub: []string{`target "api"`, `field "desc"`, "got bool"},
		},
		{
			name:    "integer delay",
			content: "targets:\n  api:\n    run: echo hi\n    delay: 5\n",
			wantSub: []string{`target "api"`, `field "delay"`, "got int"},
		},
		{
			name:    "negative delay",
			content: "targets:\n  api:\n    run: echo hi\n    delay: -1s\n",
			wantSub: []string{`target "api"`, `field "delay"`, "must not be negative"},
		},
		{
			name:    "negative grace",
			content: "targets:\n  api:\n    run: echo hi\n    grace: -2s\n",
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

// A bad field names the file, the line and the column, then echoes the source
// line with a caret under the offending token.
func TestParseConfigErrorPositions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		head    string
		excerpt []string
	}{
		{
			name:    "unknown field",
			content: "targets:\n  codegen:\n    run: make generate\n    bogus: true\n",
			head:    `:4:5: target "codegen": unknown field "bogus"`,
			excerpt: []string{"  4 |     bogus: true", "    |     ^^^^^", "fields: " + strings.Join(targetFields, ", ")},
		},
		{
			name:    "duplicate field",
			content: "targets:\n  api:\n    run: echo hi\n    run: again\n",
			head:    `:4:5: target "api": duplicate field "run"`,
			excerpt: []string{"  4 |     run: again", "    |     ^^^"},
		},
		{
			name:    "duplicate target",
			content: "targets:\n  api:\n    run: a\n  api:\n    run: b\n",
			head:    `:4:3: duplicate target "api"`,
			excerpt: []string{"  4 |   api:", "    |   ^^^"},
		},
		{
			name:    "type error points at the value",
			content: "targets:\n  api:\n    run: echo hi\n    on: 5\n",
			head:    `:4:9: target "api": field "on": expected a string or a list of strings, got int`,
			excerpt: []string{"  4 |     on: 5", "    |         ^"},
		},
		{
			name:    "unknown group points at the name",
			content: "groups:\n  backend:\n    columns: [\"a {.b}\"]\n\ntargets:\n  api:\n    group: nope\n    run: echo hi\n",
			head:    `:7:12: target "api": field "group": unknown group "nope"`,
			excerpt: []string{"  7 |     group: nope", "    |" + strings.Repeat(" ", 12) + "^^^^"},
		},
		{
			name:    "unknown field inside a group points at the key",
			content: "groups:\n  backend:\n    columns: [\"a {.b}\"]\n    bogus: 1\n\ntargets:\n  api:\n    run: echo hi\n",
			head:    `:4:5: group "backend": unknown field "bogus"`,
			excerpt: []string{"  4 |     bogus: 1", "    |" + strings.Repeat(" ", 5) + "^^^^^", "fields: columns, queries, muted, fold, start"},
		},
		{
			name:    "cmd is required points at the target",
			content: "targets:\n  api:\n    desc: x\n",
			head:    `:2:3: target "api": run is required`,
			excerpt: []string{"  2 |   api:", "    |   ^^^"},
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
			want := path + tt.head + "\n\n" + strings.Join(tt.excerpt, "\n")
			if err.Error() != want {
				t.Errorf("error =\n%s\nwant\n%s", err.Error(), want)
			}
		})
	}
}

// An error with no node to point at stays a single line.
func TestParseConfigNodeLessErrorsStayOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "\n")

	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if err.Error() != path+": no targets defined" {
		t.Errorf("error = %q, want %q", err.Error(), path+": no targets defined")
	}
}

// A line longer than the excerpt cap is windowed around the column, and the
// caret still lands under the offending token.
func TestParseConfigLongLineWindow(t *testing.T) {
	pad := strings.Repeat("a", 150)
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    on: ["+pad+", 5]\n")

	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 4 {
		t.Fatalf("error has %d lines, want 4:\n%s", len(lines), err.Error())
	}
	if !strings.HasPrefix(lines[0], path+":4:") {
		t.Errorf("first line = %q, want a line 4 position", lines[0])
	}
	if !strings.HasPrefix(lines[2], "  4 | ") || !strings.HasPrefix(lines[3], "    | ") {
		t.Fatalf("excerpt = %q / %q, want the gutter and caret rows", lines[2], lines[3])
	}

	text := []rune(strings.TrimPrefix(lines[2], "  4 | "))
	if len(text) > excerptWidth {
		t.Errorf("excerpt is %d cells, want at most %d", len(text), excerptWidth)
	}

	caret := strings.Index(lines[3], "^") - len("    | ")
	if caret < 0 || caret >= len(text) {
		t.Fatalf("caret at %d, excerpt is %d cells", caret, len(text))
	}
	if text[caret] != '5' {
		t.Errorf("caret at %d sits on %q, want the offending 5", caret, text[caret])
	}
}

func TestParseConfigColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    columns:\n      - method {.request.method}\n      - \"status {.response.status}\"\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := []Column{
		{Name: "method", Expr: ".request.method"},
		{Name: "status", Expr: ".response.status"},
	}
	if !reflect.DeepEqual(cfg.Targets[0].Columns, want) {
		t.Fatalf("columns = %+v, want %+v", cfg.Targets[0].Columns, want)
	}
}

func TestParseConfigNullOnIsEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    on:\n    except: \"\"\n")

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
	writeFile(t, path, `targets:
  api:
    on: &api_watch [apps/api, pkg, proto]
    except: &api_skip [vendor, node_modules]
    run: go run ./cmd/api

  web:
    on: *api_watch
    except: *api_skip
    run: pnpm run dev

  docs:
    on: *api_watch
    run: echo docs
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
	writeFile(t, path, `targets:
  base: &base
    on: [&shared proto]
    except: &skip vendor
    run: &api_cmd go run ./cmd/api
    desc: &api_desc API server

  copy: *base

  web:
    on: [*shared, apps]
    except: *skip
    run: *api_cmd
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
			content: "targets:\n  api:\n    run: echo hi\n    selected: &b true\n\n  web:\n    run: echo hi\n    on: *b\n",
			wantSub: []string{`target "web"`, `field "on"`, "expected a string or a list of strings, got bool"},
		},
		{
			name:    "alias to bool as list item",
			content: "targets:\n  api:\n    run: echo hi\n    selected: &b true\n\n  web:\n    run: echo hi\n    on: [*b]\n",
			wantSub: []string{`target "web"`, `field "on"`, "expected a list of strings, got bool"},
		},
		{
			name:    "alias to bool desc",
			content: "targets:\n  api:\n    run: echo hi\n    selected: &b true\n\n  web:\n    run: echo hi\n    desc: *b\n",
			wantSub: []string{`target "web"`, `field "desc"`, "expected a string, got bool"},
		},
		{
			name:    "alias to non-duration string",
			content: "targets:\n  api:\n    run: echo hi\n    desc: &d abc\n\n  web:\n    run: echo hi\n    delay: *d\n",
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
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    on: *nope\n")

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

func TestParseConfigOpenLinkSelected(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `targets:
  frontend:
    run: pnpm run dev
    open: http://localhost:5173

  api:
    run: go run ./cmd/api
    selected: false
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	frontend := cfg.Targets[0]
	if frontend.Open != "http://localhost:5173" {
		t.Errorf("open = %q", frontend.Open)
	}
	if !frontend.Selected {
		t.Error("selected = false by default, want true")
	}

	api := cfg.Targets[1]
	if api.Selected {
		t.Error("selected = true for `selected: false`")
	}
	if api.Open != "" {
		t.Errorf("open = %q, want empty", api.Open)
	}
}

func TestParseConfigStartMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `targets:
  plain:
    run: echo plain

  svc:
    run: echo svc
    start: auto

  worker:
    run: echo worker
    start: onchange

  job:
    run: echo job
    start: manual
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	want := []startMode{startAuto, startAuto, startOnChange, startManual}
	for i, mode := range want {
		if got := cfg.Targets[i].Start; got != mode {
			t.Errorf("%s start = %q, want %q", cfg.Targets[i].Name, got, mode)
		}
	}
}

func TestParseConfigGroupStartMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, `groups:
  services:
    start: manual

targets:
  worker:
    group: services
    run: echo worker

  api:
    group: services
    run: echo api
    start: auto
`)

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if worker, _ := cfg.Find("worker"); worker.Start != startManual {
		t.Errorf("worker start = %q, want the group's manual", worker.Start)
	}
	if api, _ := cfg.Find("api"); api.Start != startAuto {
		t.Errorf("api start = %q, want its own auto", api.Start)
	}
}

func TestParseConfigRejectsUnknownGroupStartMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    start: sometimes\n\ntargets:\n  api:\n    group: backend\n    run: echo hi\n")

	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, sub := range []string{`group "backend"`, `field "start"`, `expected auto, onchange, or manual, got "sometimes"`} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want substring %q", err.Error(), sub)
		}
	}
}

func TestParseConfigRejectsUnknownStartMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    start: sometimes\n")

	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, sub := range []string{`target "api"`, `field "start"`, `expected auto, onchange, or manual, got "sometimes"`} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want substring %q", err.Error(), sub)
		}
	}
}

func TestParseConfigBoolFieldErrors(t *testing.T) {
	for _, field := range []string{"selected"} {
		path := filepath.Join(t.TempDir(), ConfigFileName)
		writeFile(t, path, "targets:\n  api:\n    run: echo hi\n    "+field+": 1\n")

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

// Every key the hint offers must actually parse, so the list cannot drift
// from the parser. Add a row here when a field joins the switch in parseTarget.
func TestTargetFieldsAllAccepted(t *testing.T) {
	values := map[string]string{
		"desc":     "x",
		"on":       "src",
		"except":   "vendor",
		"run":      "echo hi",
		"build":    "make",
		"health":   "tcp://127.0.0.1:1",
		"columns":  `["a {.b}"]`,
		"queries":  "{errors: level=error}",
		"group":    "backend",
		"muted":    "true",
		"open":     "http://localhost:1",
		"selected": "false",
		"start":    "onchange",
		"delay":    "1s",
		"cooldown": "1s",
		"grace":    "1s",
	}
	for _, field := range targetFields {
		value, ok := values[field]
		if !ok {
			t.Errorf("field %q has no test value", field)
			continue
		}
		path := filepath.Join(t.TempDir(), ConfigFileName)
		line := "    " + field + ": " + value
		base := "    run: echo hi\n"
		if field == "run" {
			base = ""
		}
		prefix := ""
		if field == "group" {
			prefix = "groups:\n  backend:\n    columns: [\"a {.b}\"]\n"
		}
		writeFile(t, path, prefix+"targets:\n  api:\n"+base+line+"\n")

		if _, err := ParseConfig(path); err != nil {
			t.Errorf("field %q: ParseConfig = %v, want nil", field, err)
		}
	}
}

func TestParseConfigInvalidYAMLNamesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: [unterminated\n")

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
	writeFile(t, filepath.Join(root, ConfigFileName), "targets:\n  api:\n    run: echo hi\n")

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
	writeFile(t, filepath.Join(root, ConfigFileName), "targets:\n  outer:\n    run: echo outer\n")

	nested := filepath.Join(root, "a")
	writeFile(t, filepath.Join(nested, ConfigFileName), "targets:\n  inner:\n    run: echo inner\n")

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
	writeFile(t, filepath.Join(parent, ConfigFileName), "targets:\n  outer:\n    run: echo outer\n")

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

func TestParseConfigSchemaColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    columns: [\"level {.level}\", \"msg {.msg}\"]\n\ntargets:\n  api:\n    group: backend\n    run: echo hi\n\n  web:\n    run: echo web\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("api")
	want := []Column{{Name: "level", Expr: ".level"}, {Name: "msg", Expr: ".msg"}}
	if api.Schema != "backend" || !reflect.DeepEqual(api.Columns, want) {
		t.Errorf("api schema = %q columns = %+v, want backend and %+v", api.Schema, api.Columns, want)
	}
	web, _ := cfg.Find("web")
	if web.Schema != "" || len(web.Columns) != 0 {
		t.Errorf("web schema = %q columns = %+v, want none", web.Schema, web.Columns)
	}
}

func TestParseConfigTargetColumnsBeatSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    columns: [\"level {.level}\"]\n\ntargets:\n  api:\n    group: backend\n    columns: [\"status {.status}\"]\n    run: echo hi\n\n  empty:\n    group: backend\n    columns: []\n    run: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("api")
	if api.Schema != "backend" || !reflect.DeepEqual(api.Columns, []Column{{Name: "status", Expr: ".status"}}) {
		t.Errorf("api schema = %q columns = %+v, want the target's own columns", api.Schema, api.Columns)
	}
	empty, _ := cfg.Find("empty")
	if len(empty.Columns) != 0 {
		t.Errorf("empty columns = %+v, want an explicit empty list to win", empty.Columns)
	}
}

func TestParseConfigGroupsOnlyBlockIsNullSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\ntargets:\n  api:\n    run: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Names(); !reflect.DeepEqual(got, []string{"api"}) {
		t.Errorf("names = %v, want [api]", got)
	}
}

func TestParseConfigGroupsKeyMatchesSchemas(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  backend:\n    columns: [\"level {.level}\", \"msg {.msg}\"]\n\ntargets:\n  api:\n    group: backend\n    run: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("api")
	want := []Column{{Name: "level", Expr: ".level"}, {Name: "msg", Expr: ".msg"}}
	if api.Schema != "backend" || !reflect.DeepEqual(api.Columns, want) {
		t.Errorf("api group = %q columns = %+v, want backend and %+v", api.Schema, api.Columns, want)
	}
}

// A group with no columns is valid in every spelling, like `columns: []`.
func TestParseConfigEmptyGroupsAreValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  webapps: {}\n  nulls:\n  lists:\n    columns: []\n\ntargets:\n  a:\n    group: webapps\n    run: echo a\n  b:\n    group: nulls\n    run: echo b\n  c:\n    group: lists\n    run: echo c\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	for _, name := range []string{"a", "b", "c"} {
		target, _ := cfg.Find(name)
		if target.Schema == "" || len(target.Columns) != 0 {
			t.Errorf("%s group = %q columns = %+v, want a group with no columns", name, target.Schema, target.Columns)
		}
	}
}

func TestParseConfigRunBuildHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  a:\n    run: go run .\n    build: go build ./...\n    health: http://localhost:1/up\n  b:\n    run: echo old\n")
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Find("a")
	if a.Cmd != "go run ." || a.Build != "go build ./..." || a.Health != "http://localhost:1/up" {
		t.Errorf("a = %+v", a)
	}
	if b, _ := cfg.Find("b"); b.Cmd != "echo old" || b.Build != "" {
		t.Errorf("b = %+v", b)
	}
}
