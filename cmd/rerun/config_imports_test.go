package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// importFixture lays out app/rerun.yml with a sibling bpn dir; app/.git stops LoadConfig.
type importFixture struct {
	dir  string
	root string
}

func newImportFixture(t *testing.T, rootYML string, files map[string]string) importFixture {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app", ".git", "HEAD"), "targets:\n  ref: refs/heads/main\n")
	f := importFixture{dir: dir, root: filepath.Join(dir, "app", ConfigFileName)}
	writeFile(t, f.root, rootYML)
	for rel, content := range files {
		writeFile(t, filepath.Join(dir, rel), content)
	}
	return f
}

func targetNames(cfg *Config) []string {
	var names []string
	for _, t := range cfg.Targets {
		names = append(names, t.Name)
	}
	return names
}

func TestParseImports(t *testing.T) {
	bpn := "targets:\n  api:\n    run: go run ./api\n\n  web:\n    run: pnpm dev\n\n  worker:\n    run: go run ./worker\n"
	grouped := "groups:\n  backend:\n    columns: [\"level {.level}\"]\n  frontend:\n\n" +
		"targets:\n  api:\n    group: backend\n    run: go run ./api\n\n" +
		"  web:\n    group: frontend\n    run: pnpm dev\n\n" +
		"  util:\n    run: make util\n"

	tests := []struct {
		name     string
		root     string
		files    map[string]string
		wantErr  string
		wantNext []string // target names, in config order
		check    func(t *testing.T, cfg *Config)
	}{
		{
			name:    "required missing file is an error at the import line",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: echo\n",
			wantErr: "file not found",
		},
		{
			name:     "optional missing file is skipped and recorded",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n    optional: true\n\ntargets:\n  local:\n    run: echo\n",
			wantNext: []string{"local"},
			check: func(t *testing.T, cfg *Config) {
				if len(cfg.Missing) != 1 || cfg.Missing[0].Group != "bpn" {
					t.Fatalf("missing = %+v, want one bpn entry", cfg.Missing)
				}
			},
		},
		{
			name:     "imported targets and groups get the group prefix",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"bpn/rerun.yml": bpn},
			wantNext: []string{"local", "bpn/api", "bpn/web", "bpn/worker"},
			check: func(t *testing.T, cfg *Config) {
				api, _ := cfg.Find("bpn/api")
				if api.Import != "bpn" || api.localName() != "api" {
					t.Errorf("import = %q, local = %q", api.Import, api.localName())
				}
				if api.File != filepath.Join(filepath.Dir(filepath.Dir(cfg.Path)), "bpn", ConfigFileName) {
					t.Errorf("file = %q, want the bpn rerun.yml", api.File)
				}
				if api.Dir != filepath.Dir(api.File) {
					t.Errorf("dir = %q, want the file's dir", api.Dir)
				}
				if local, _ := cfg.Find("local"); local.Import != "" || local.Dir != filepath.Dir(cfg.Path) {
					t.Errorf("root target import = %q dir = %q", local.Import, local.Dir)
				}
			},
		},
		{
			name:    "two imports with one group name clash",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n  - path: ../bpn/sub/rerun.yml\n    group: bpn\n",
			files:   map[string]string{"bpn/rerun.yml": bpn, "bpn/sub/rerun.yml": bpn},
			wantErr: "duplicate import group",
		},
		{
			name:     "a group named like the import gives its targets columns",
			root:     "groups:\n  bpn:\n    columns: [\"msg {.msg}\"]\n\nimports:\n  - path: ../bpn/rerun.yml\n",
			files:    map[string]string{"bpn/rerun.yml": bpn},
			wantNext: []string{"bpn/api", "bpn/web", "bpn/worker"},
			check: func(t *testing.T, cfg *Config) {
				api, _ := cfg.Find("bpn/api")
				if api.Schema != "bpn" || len(api.Columns) != 1 || api.SchemaLocal != "bpn" {
					t.Errorf("schema = %q, local = %q, columns = %+v", api.Schema, api.SchemaLocal, api.Columns)
				}
			},
		},
		{
			name:     "import start fills in for its targets, own start wins",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n    start: manual\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo\n\n  web:\n    run: echo\n    start: auto\n"},
			wantNext: []string{"local", "bpn/api", "bpn/web"},
			check: func(t *testing.T, cfg *Config) {
				if api, _ := cfg.Find("bpn/api"); api.Start != startManual {
					t.Errorf("api start = %q, want the import's manual", api.Start)
				}
				if web, _ := cfg.Find("bpn/web"); web.Start != startAuto {
					t.Errorf("web start = %q, want its own auto", web.Start)
				}
			},
		},
		{
			name:     "a group's start beats the import's start",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n    start: manual\n",
			files:    map[string]string{"bpn/rerun.yml": "groups:\n  backend:\n    start: onchange\n\ntargets:\n  api:\n    group: backend\n    run: echo\n"},
			wantNext: []string{"bpn/api"},
			check: func(t *testing.T, cfg *Config) {
				api, _ := cfg.Find("bpn/api")
				if api.Start != startOnChange {
					t.Errorf("start = %q, want the group's onchange", api.Start)
				}
			},
		},
		{
			name:    "an invalid import start is an error",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n    start: sometimes\n",
			files:   map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo\n"},
			wantErr: `expected auto, onchange, or manual, got "sometimes"`,
		},
		{
			name:     "groups filter keeps only the listed groups",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n    groups: [backend]\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"bpn/rerun.yml": grouped},
			wantNext: []string{"local", "bpn/api"},
			check: func(t *testing.T, cfg *Config) {
				api, _ := cfg.Find("bpn/api")
				if api.Schema != "bpn/backend" {
					t.Errorf("schema = %q, want the prefixed backend group", api.Schema)
				}
				if api.SchemaFile != filepath.Join(filepath.Dir(filepath.Dir(cfg.Path)), "bpn", ConfigFileName) {
					t.Errorf("schema file = %q, want the bpn file", api.SchemaFile)
				}
			},
		},
		{
			name:    "groups filter names an unknown group",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n    groups: [nope]\n",
			files:   map[string]string{"bpn/rerun.yml": grouped},
			wantErr: `unknown group "nope"`,
		},
		{
			name:     "the same file imported twice loads once",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n  - path: ../bpn/./rerun.yml\n    optional: true\n",
			files:    map[string]string{"bpn/rerun.yml": bpn},
			wantNext: []string{"bpn/api", "bpn/web", "bpn/worker"},
		},
		{
			name:    "a file that imports itself is an error",
			root:    "imports:\n  - path: rerun.yml\n\ntargets:\n  local:\n    run: echo\n",
			wantErr: "cannot import itself",
		},
		{
			name:     "an imports key in an imported file is ignored with a notice",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n",
			files:    map[string]string{"bpn/rerun.yml": "imports:\n  - path: ../carol/rerun.yml\n\n" + bpn, "carol/rerun.yml": "targets:\n  carol:\n    run: echo\n"},
			wantNext: []string{"bpn/api", "bpn/web", "bpn/worker"},
			check: func(t *testing.T, cfg *Config) {
				notices := cfg.Notices
				if len(notices) != 1 || !strings.Contains(notices[0], "one level deep") {
					t.Errorf("notices = %q, want the nested import notice", notices)
				}
			},
		},
		{
			name:    "an unknown import key is an error at its line",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n    frob: 1\n",
			files:   map[string]string{"bpn/rerun.yml": bpn},
			wantErr: `unknown field "frob"`,
		},
		{
			name: "a same-named group fills only targets with no group of their own",
			root: "groups:\n  bpn:\n    columns: [\"root {.r}\"]\n\nimports:\n  - path: ../bpn/rerun.yml\n",
			files: map[string]string{"bpn/rerun.yml": "groups:\n  backend:\n    columns: [\"level {.level}\"]\n\n" +
				"targets:\n  api:\n    group: backend\n    run: go run ./api\n\n  web:\n    run: pnpm dev\n"},
			wantNext: []string{"bpn/api", "bpn/web"},
			check: func(t *testing.T, cfg *Config) {
				api, _ := cfg.Find("bpn/api")
				if api.Schema != "bpn/backend" || len(api.Columns) != 1 || api.Columns[0].Name != "level" {
					t.Errorf("api schema = %q, columns = %+v; want its own file's group", api.Schema, api.Columns)
				}
				web, _ := cfg.Find("bpn/web")
				if web.Schema != "bpn" || len(web.Columns) != 1 || web.Columns[0].Name != "root" {
					t.Errorf("web schema = %q, columns = %+v; want the root's bpn group", web.Schema, web.Columns)
				}
			},
		},
		{
			name:     "a second listing of one file with other groups is noticed",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n  - path: ../bpn/rerun.yml\n    groups: [backend]\n",
			files:    map[string]string{"bpn/rerun.yml": grouped},
			wantNext: []string{"bpn/api", "bpn/web", "bpn/util"},
			check: func(t *testing.T, cfg *Config) {
				notices := cfg.Notices
				if len(notices) != 1 || !strings.Contains(notices[0], "the first one wins") {
					t.Errorf("notices = %q, want the duplicate-listing notice", notices)
				}
			},
		},
		{
			name:    "two default group names from different dirs clash at the path",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n  - path: ../other/bpn/rerun.yml\n",
			files:   map[string]string{"bpn/rerun.yml": bpn, "other/bpn/rerun.yml": bpn},
			wantErr: `duplicate import group "bpn"`,
		},
		{
			name:    "a duplicate target name from an import is an error",
			root:    "targets:\n  bpn/api:\n    run: echo\n\nimports:\n  - path: ../bpn/rerun.yml\n",
			files:   map[string]string{"bpn/rerun.yml": bpn},
			wantErr: `duplicate target "bpn/api"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImportFixture(t, tt.root, tt.files)
			cfg, err := ParseConfig(f.root)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), f.root+":") {
					t.Errorf("err = %q, want it to point at the importing file %s", err, f.root)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if got := targetNames(cfg); !equalStrings(got, tt.wantNext) {
				t.Errorf("targets = %v, want %v", got, tt.wantNext)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// A target runs from the dir of the file that defines it, whatever dir rerun started in.
func TestImportedTargetsRunFromTheirOwnDir(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    on: src\n    run: pwd -P\n",
		map[string]string{
			"bpn/rerun.yml": "targets:\n  api:\n    run: pwd -P\n",
			"app/src/.keep": "",
		})
	subdir := filepath.Join(f.dir, "app", "src")
	t.Chdir(subdir)

	cfg, err := LoadConfig(".")
	if err != nil {
		t.Fatalf("LoadConfig from a subdir: %v", err)
	}
	wantRoot, _ := filepath.EvalSymlinks(filepath.Dir(f.root))
	wantBpn, _ := filepath.EvalSymlinks(filepath.Join(f.dir, "bpn"))

	for _, tc := range []struct{ name, want string }{{"local", wantRoot}, {"bpn/api", wantBpn}} {
		target, ok := cfg.Find(tc.name)
		if !ok {
			t.Fatalf("target %q missing", tc.name)
		}
		if got, _ := filepath.EvalSymlinks(target.Dir); got != tc.want {
			t.Errorf("%s dir = %q, want %q", tc.name, got, tc.want)
		}
		cmd, err := rerun.StartCommand([]string{target.Cmd}, time.Second, rerun.WithDir(target.Dir), rerun.WithStdout(bytes.NewBuffer(nil)))
		if err != nil {
			t.Fatalf("start %s: %v", tc.name, err)
		}
		if err := cmd.WaitExit(t.Context()); err != nil {
			t.Fatalf("wait %s: %v", tc.name, err)
		}
	}

	local, _ := cfg.Find("local")
	if got := inDir(local.Dir, local.On); len(got) != 1 || got[0] != filepath.Join(local.Dir, "src") {
		t.Errorf("on: resolved to %v, want the src dir under the root dir", got)
	}
	if got := inDir("", []string{"./"}); got[0] != "./" {
		t.Errorf("inDir with no dir = %v, want the path as written", got)
	}
}

// A reload picks up an edit to an imported file.
func TestConfigWatchReloadsOnImportEdits(t *testing.T) {
	bpnPath := filepath.Join("bpn", ConfigFileName)
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: sleep 30\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: sleep 30\n"})
	group, sink := startConfigWatch(t, f.root)

	// The watch starts in a goroutine, so the write repeats until a reload lands.
	edit := func() { writeFile(t, filepath.Join(f.dir, bpnPath), "targets:\n  api:\n    run: sleep 31\n") }
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, edit)

	api, ok := findSnapshot(group, "bpn/api")
	if !ok || api.Cmd != "sleep 31" {
		t.Fatalf("bpn/api = %+v, want the edited cmd", api)
	}
}

// Creating an absent optional import loads it.
func TestConfigWatchLoadsAnOptionalImportThatAppears(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    optional: true\n\ntargets:\n  local:\n    run: sleep 30\n",
		nil)
	group, sink := startConfigWatch(t, f.root)
	if _, ok := findSnapshot(group, "bpn/api"); ok {
		t.Fatal("bpn/api loaded before its file existed")
	}

	create := func() {
		writeFile(t, filepath.Join(f.dir, "bpn", ConfigFileName), "targets:\n  api:\n    run: sleep 30\n")
	}
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, create)

	if _, ok := findSnapshot(group, "bpn/api"); !ok {
		t.Fatal("bpn/api did not load once its file appeared")
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if len(group.missing) != 0 {
		t.Errorf("missing = %+v after the file appeared, want none", group.missing)
	}
}

// startConfigWatch runs the config watch; the runners are built but not started.
func startConfigWatch(t *testing.T, path string) (*targetGroup, *recordingSink) {
	t.Helper()
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	sink := &recordingSink{}
	group := newTargetGroup(cfg.Targets, sink, path)
	t.Cleanup(group.cancel)
	group.mu.Lock()
	group.missing = cfg.Missing
	group.mu.Unlock()
	go group.watchConfig(path)
	return group, sink
}

func findSnapshot(g *targetGroup, name string) (Target, bool) {
	for _, t := range g.snapshotTargets() {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// waitForKindPoking waits for an event of kind, calling poke until it arrives.
// Poke slower than configDebounce, or Linux inotify keeps resetting it.
func waitForKindPoking(t *testing.T, sink *recordingSink, kind eventKind, timeout time.Duration, poke func()) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	nextPoke := time.Now()
	for time.Now().Before(deadline) {
		for _, ev := range sink.all() {
			if ev.kind == kind {
				return
			}
		}
		if time.Now().After(nextPoke) {
			poke()
			nextPoke = time.Now().Add(2 * configDebounce)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for event kind %v; events: %+v", kind, sink.all())
}

// Columns save into the file that defines the node: the imported file for its targets, the root for its groups.
func TestSaveColumnsWritesTheDefiningFile(t *testing.T) {
	rootYML := "groups:\n  bpn:\n    columns: [\"msg {.msg}\"]\n\ntargets:\n  local:\n    run: echo\n\nimports:\n  - path: ../bpn/rerun.yml\n    groups: [backend]\n"
	bpnYML := "groups:\n  backend:\n    columns: [\"level {.level}\"]\n\ntargets:\n  api:\n    group: backend\n    run: go run ./api\n"
	f := newImportFixture(t, rootYML, map[string]string{"bpn/rerun.yml": bpnYML})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	m := newTestModel(t, cfg.Targets...)
	m.group.configPath = f.root
	m.edits = []colEdit{{node: colNode{name: "bpn/api"}, cols: []Column{{Name: "msg", Expr: ".msg"}}}}
	m.saveColumns()

	bpnAfter, _ := os.ReadFile(filepath.Join(f.dir, "bpn", ConfigFileName))
	if !strings.Contains(string(bpnAfter), "msg {.msg}") {
		t.Errorf("bpn file = %s, want the saved column", bpnAfter)
	}
	rootAfter, _ := os.ReadFile(f.root)
	if string(rootAfter) != rootYML {
		t.Errorf("root file changed on a target save:\n%s", rootAfter)
	}

	m.edits = []colEdit{{node: colNode{schema: true, name: "bpn"}, cols: []Column{{Name: "t", Expr: ".t"}}}}
	m.saveColumns()
	rootAfter, _ = os.ReadFile(f.root)
	if !strings.Contains(string(rootAfter), "t {.t}") {
		t.Errorf("root file = %s, want the group columns saved into groups.bpn", rootAfter)
	}
}

// A missing optional import is a dim row the cursor cannot reach.
func TestMissingImportRowIsNotSelectable(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.missing = []missingImport{{Group: "bpn", Path: "/x/bpn/rerun.yml"}}
	m.frame()

	if got := m.listLen(); got != 2 {
		t.Fatalf("listLen = %d, want only the two target rows", got)
	}
	if view := m.View().Content; !strings.Contains(view, "bpn (not found)") {
		t.Errorf("view does not show the not-found row:\n%s", view)
	}
	press(m, "j", "j", "j")
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want it to stop on the last list row", m.cursor)
	}
}

// A broken import still reloads once it is fixed.
func TestConfigWatchRecoversFromABrokenImport(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: sleep 30\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run:\n"})
	if _, err := ParseConfig(f.root); err == nil {
		t.Fatal("the broken import parsed")
	}

	sink := &recordingSink{}
	group := newTargetGroup(nil, sink, f.root)
	t.Cleanup(group.cancel)
	go group.watchConfig(f.root)

	fix := func() {
		writeFile(t, filepath.Join(f.dir, "bpn", ConfigFileName), "targets:\n  api:\n    run: sleep 30\n")
	}
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, fix)

	if _, ok := findSnapshot(group, "bpn/api"); !ok {
		t.Fatal("bpn/api did not load after the import was fixed")
	}
}

// Both spellings of one file dedupe, and the logical path stays the Dir.
func TestSymlinkedImportLoadsOnce(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: link.yml\n    group: via\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: echo\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: go run ./api\n"})
	if err := os.Symlink(filepath.Join(f.dir, "bpn", ConfigFileName), filepath.Join(f.dir, "app", "link.yml")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := targetNames(cfg); !equalStrings(got, []string{"local", "via/api"}) {
		t.Fatalf("targets = %v, want the symlinked file loaded once under via", got)
	}
	api, _ := cfg.Find("via/api")
	if api.Dir != filepath.Join(f.dir, "app") {
		t.Errorf("dir = %q, want the logical import dir", api.Dir)
	}
}

// Zero targets and only absent optional imports is a valid config: plain mode
// runs nothing, and the TUI shows the not-found row.
func TestOnlyMissingOptionalImportsIsValid(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n    optional: true\n", nil)
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Targets) != 0 || len(cfg.Missing) != 1 {
		t.Fatalf("targets = %d, missing = %+v; want none and one", len(cfg.Targets), cfg.Missing)
	}

	done := make(chan int, 1)
	go func() { done <- runTargets(nil, &bytes.Buffer{}, f.root, make(chan os.Signal)) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("plain mode exit = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plain mode with no targets did not return")
	}

	m := newTUI(nil)
	m.group.cancel()
	m.missing = cfg.Missing
	m.width, m.height = 60, 10
	if view := m.View().Content; !strings.Contains(view, "bpn (not found)") {
		t.Errorf("TUI view has no not-found row:\n%s", view)
	}
}

// Removing an import unwatches its dir.
func TestRemovedImportUnwatchesItsDir(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: echo\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo\n"})
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer fsw.Close()
	w := &configWatch{watcher: fsw, dirs: map[string]bool{}, sink: &recordingSink{}}
	bpnDir := filepath.Join(f.dir, "bpn")

	w.sync([]string{f.root, filepath.Join(bpnDir, ConfigFileName)}, nil)
	if !w.dirs[bpnDir] {
		t.Fatal("the import's dir is not watched")
	}
	w.sync([]string{f.root}, nil)
	if w.dirs[bpnDir] {
		t.Error("the removed import's dir is still in the watch set")
	}
	for _, p := range fsw.WatchList() {
		if p == bpnDir {
			t.Errorf("fsnotify still watches %s", bpnDir)
		}
	}
}

// A reload that fails leaves the old targets; the next good save reloads.
func TestConfigWatchReloadsAfterAFailedReload(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n\ntargets:\n  local:\n    run: sleep 30\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: sleep 30\n"})
	group, sink := startConfigWatch(t, f.root)
	bpnFile := filepath.Join(f.dir, "bpn", ConfigFileName)

	broken := func() { writeFile(t, bpnFile, "targets:\n  api:\n    run:\n") }
	waitForKindPoking(t, sink, evConfigErr, 10*time.Second, broken)

	fixed := func() { writeFile(t, bpnFile, "targets:\n  api:\n    run: sleep 31\n") }
	waitForKindPoking(t, sink, evReconcile, 10*time.Second, fixed)

	if api, ok := findSnapshot(group, "bpn/api"); !ok || api.Cmd != "sleep 31" {
		t.Fatalf("bpn/api = %+v, want the fixed cmd", api)
	}
}

// An except glob with no "/" matches at any depth; a path-like entry anchors to the dir.
func TestExceptGlobMatchesDeepFiles(t *testing.T) {
	if got := exceptPaths("/d", []string{"*.log", "node_modules", "build/*.o"}); !equalStrings(got, []string{"*.log", "/d/node_modules", "/d/build/*.o"}) {
		t.Fatalf("exceptPaths = %v", got)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sub", "keep.txt"), "")
	target := Target{Name: "x", Cmd: "true", Dir: dir, On: []string{"./"}, Except: []string{"*.log"}, Delay: 20 * time.Millisecond, NoGitignore: true}
	r := newTargetRunner(target, &recordingSink{})
	watcher, err := r.newWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	changes := r.startWatch(watcher)

	writeFile(t, filepath.Join(dir, "sub", "x.log"), "noise")
	writeFile(t, filepath.Join(dir, "sub", "y.txt"), "signal")
	deadline := time.After(10 * time.Second)
	for {
		select {
		case cs, ok := <-changes:
			if !ok {
				t.Fatal("changes closed before y.txt")
			}
			for path := range cs.Files {
				if strings.HasSuffix(path, "x.log") {
					t.Fatalf("except: *.log let %s through", path)
				}
				if strings.HasSuffix(path, "y.txt") {
					return
				}
			}
		case <-deadline:
			t.Fatal("no change for y.txt")
		}
	}
}

// A file's feed key is relative to its owner's dir, with the import name in front.
func TestFeedKeysStayUnderTheOwnerDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app", ConfigFileName)
	m := &tuiModel{group: &targetGroup{configPath: root}}
	bpnDir := filepath.Join(filepath.Dir(filepath.Dir(root)), "bpn-api")

	if got := m.feedPath(filepath.Join(bpnDir, "internal", "x.go"), Target{Dir: bpnDir, Import: "bpn-api"}); got != "bpn-api/internal/x.go" {
		t.Errorf("imported key = %q, want bpn-api/internal/x.go", got)
	}
	if got := m.feedPath(filepath.Join(filepath.Dir(root), "src", "a.go"), Target{Dir: filepath.Dir(root)}); got != "src/a.go" {
		t.Errorf("root key = %q, want src/a.go", got)
	}
}

func TestMakefileImports(t *testing.T) {
	tools := "# Build the thing\nbuild: ## Build it\n\tgo build\n\ntest:\n\tgo test\n"
	tests := []struct {
		name     string
		root     string
		files    map[string]string
		wantErr  string
		wantNext []string
		check    func(t *testing.T, cfg *Config)
	}{
		{
			name:     "explicit import lists each rule as a manual make target",
			root:     "imports:\n  - path: ../tools/Makefile\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"tools/Makefile": tools},
			wantNext: []string{"local", "make/tools/build", "make/tools/test"},
			check: func(t *testing.T, cfg *Config) {
				build, _ := cfg.Find("make/tools/build")
				if build.Kind != makeKind || build.Cmd != "make build" || build.Start != startManual {
					t.Errorf("kind = %q cmd = %q start = %q", build.Kind, build.Cmd, build.Start)
				}
				if build.Desc != "Build it" || build.Import != "make/tools" {
					t.Errorf("desc = %q import = %q", build.Desc, build.Import)
				}
				if want := filepath.Join(filepath.Dir(filepath.Dir(cfg.Path)), "tools"); build.Dir != want {
					t.Errorf("dir = %q, want %q", build.Dir, want)
				}
				if test, _ := cfg.Find("make/tools/test"); test.Desc != "" {
					t.Errorf("test desc = %q, want none", test.Desc)
				}
			},
		},
		{
			name:     "targets keeps only the listed rules in file order",
			root:     "imports:\n  - path: ../tools/Makefile\n    targets: [test, build]\n",
			files:    map[string]string{"tools/Makefile": tools},
			wantNext: []string{"make/tools/build", "make/tools/test"},
		},
		{
			name:    "targets names a rule the Makefile lacks",
			root:    "imports:\n  - path: ../tools/Makefile\n    targets: [nope]\n",
			files:   map[string]string{"tools/Makefile": tools},
			wantErr: `no target "nope"`,
		},
		{
			name:    "targets on a rerun import is an error",
			root:    "imports:\n  - path: ../bpn/rerun.yml\n    targets: [api]\n",
			files:   map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo\n"},
			wantErr: "targets applies to Makefile imports",
		},
		{
			name:    "targets on a compose import is an error",
			root:    "imports:\n  - path: ../stack/compose.yml\n    targets: [api]\n",
			files:   map[string]string{"stack/compose.yml": "services:\n  api:\n    image: x\n"},
			wantErr: "targets applies to Makefile imports",
		},
		{
			name:     "a Makefile beside rerun.yml defaults to the make group",
			root:     "imports:\n  - path: Makefile\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"app/Makefile": "build:\n"},
			wantNext: []string{"local", "make/build"},
		},
		{
			name:     "a listed Makefile beside rerun.yml is imported as make",
			root:     "imports:\n  - path: Makefile\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"app/Makefile": "build:\n"},
			wantNext: []string{"local", "make/build"},
			check: func(t *testing.T, cfg *Config) {
				build, _ := cfg.Find("make/build")
				if !build.AutoFold {
					t.Errorf("make group does not fold by default")
				}
				if !slices.ContainsFunc(cfg.Files, func(f string) bool {
					return filepath.Dir(f) == filepath.Dir(cfg.Path) && strings.EqualFold(filepath.Base(f), "makefile")
				}) {
					t.Errorf("files = %v, want the listed Makefile watched", cfg.Files)
				}
			},
		},
		{
			name:     "an unlisted Makefile beside rerun.yml is not imported",
			root:     "targets:\n  local:\n    run: echo\n",
			files:    map[string]string{"app/Makefile": "build:\n"},
			wantNext: []string{"local"},
			check: func(t *testing.T, cfg *Config) {
				if !containsNotice(cfg, "Makefile not imported · add it under imports:") {
					t.Errorf("notices = %q, want the not-imported notice", cfg.Notices)
				}
			},
		},
		{
			name:     "an explicit root import with targets replaces the implicit one",
			root:     "imports:\n  - path: Makefile\n    targets: [test]\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"app/Makefile": "build:\ntest:\n"},
			wantNext: []string{"local", "make/test"},
		},
		{
			name:     "no Makefile beside rerun.yml means no make group",
			root:     "targets:\n  local:\n    run: echo\n",
			wantNext: []string{"local"},
		},
		{
			name:     "a nested rerun file does not pull in a Makefile beside it",
			root:     "imports:\n  - path: ../bpn/rerun.yml\n",
			files:    map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo\n", "bpn/Makefile": "build:\n"},
			wantNext: []string{"bpn/api"},
		},
		{
			name:     "import start auto overrides the manual Makefile default",
			root:     "imports:\n  - path: ../tools/Makefile\n    start: auto\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"tools/Makefile": tools},
			wantNext: []string{"local", "make/tools/build", "make/tools/test"},
			check: func(t *testing.T, cfg *Config) {
				build, _ := cfg.Find("make/tools/build")
				if build.Start != startAuto {
					t.Errorf("start = %q, want the import's auto", build.Start)
				}
			},
		},
		{
			name:     "groups entry fold false expands one make group",
			root:     "groups:\n  make/tools:\n    fold: false\n\nimports:\n  - path: ../tools/Makefile\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"tools/Makefile": tools},
			wantNext: []string{"local", "make/tools/build", "make/tools/test"},
			check: func(t *testing.T, cfg *Config) {
				build, _ := cfg.Find("make/tools/build")
				if build.AutoFold {
					t.Errorf("AutoFold = true, want unfolded")
				}
			},
		},
		{
			name:     "make groups fold by default",
			root:     "imports:\n  - path: ../tools/Makefile\n\ntargets:\n  local:\n    run: echo\n",
			files:    map[string]string{"tools/Makefile": tools},
			wantNext: []string{"local", "make/tools/build", "make/tools/test"},
			check: func(t *testing.T, cfg *Config) {
				build, _ := cfg.Find("make/tools/build")
				if !build.AutoFold || build.Muted != nil {
					t.Errorf("AutoFold = %v, muted = %v; want folded and not muted", build.AutoFold, build.Muted)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImportFixture(t, tt.root, tt.files)
			cfg, err := ParseConfig(f.root)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if got := targetNames(cfg); !equalStrings(got, tt.wantNext) {
				t.Errorf("targets = %v, want %v", got, tt.wantNext)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestMakeGroupsSortLast(t *testing.T) {
	makeRow := &tuiTarget{spec: Target{Name: "make/build", Kind: makeKind, Import: "make"}}
	apiRow := &tuiTarget{spec: Target{Name: "bpn/api", Import: "bpn"}}
	localRow := &tuiTarget{spec: Target{Name: "local"}}
	rows := []*tuiTarget{makeRow, apiRow, localRow}
	groupTargets(rows)
	want := []*tuiTarget{localRow, apiRow, makeRow}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, rows[i].spec.Name, want[i].spec.Name)
		}
	}
}

// An import's same-named root group hands its saved queries down to the
// imported targets, like it does its columns; the file's own win a name clash.
func TestImportGroupPassesQueriesDown(t *testing.T) {
	f := newImportFixture(t,
		"groups:\n  bpn:\n    queries:\n      errors: level=error\n      slow: group-slow\n\nimports:\n  - path: ../bpn/rerun.yml\n",
		map[string]string{
			"bpn/rerun.yml": "targets:\n  api:\n    run: go run ./api\n    queries:\n      slow: api-slow\n",
		})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	api, _ := cfg.Find("bpn/api")
	if got := api.Queries["errors"]; got != "level=error" {
		t.Fatalf("errors = %q, want the root group's query handed down", got)
	}
	if got := api.Queries["slow"]; got != "api-slow" {
		t.Fatalf("slow = %q, want the file's own winning the clash", got)
	}
	if got := api.groupQueries["errors"]; got != "level=error" {
		t.Fatalf("groupQueries = %v, want errors tracked as inherited", api.groupQueries)
	}
}
