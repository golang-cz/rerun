package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang-cz/rerun"
)

// recTarget is a spec for the reconcile tables.
func recTarget(name, cmd string) Target {
	return Target{Name: name, Cmd: cmd, On: []string{"src"}, Delay: defaultDelay, Grace: rerun.DefaultGrace, Selected: true}
}

func TestDiffTargetsReconcileActions(t *testing.T) {
	web, api, worker := recTarget("web", "web"), recTarget("api", "api"), recTarget("worker", "worker")

	cmdChanged := recTarget("api", "api-v2")
	onChanged := recTarget("api", "api")
	onChanged.On = []string{"pkg"}
	exceptChanged := recTarget("api", "api")
	exceptChanged.Except = []string{"pkg/vendor"}
	delayChanged := recTarget("api", "api")
	delayChanged.Delay = time.Second
	ignoreChanged := recTarget("api", "api")
	ignoreChanged.NoGitignore = true
	cosmetic := recTarget("api", "api")
	cosmetic.Desc = "the api"
	cosmetic.URL = "http://localhost:8080"
	cosmetic.Grace = time.Second
	cosmetic.Cooldown = 2 * time.Second
	cosmetic.Open = true
	cosmetic.Selected = false

	tests := []struct {
		name string
		old  []Target
		new  []Target
		want []reconcileAction
	}{
		{
			name: "identical config",
			old:  []Target{web, api},
			new:  []Target{web, api},
		},
		{
			name: "added target",
			old:  []Target{web, api},
			new:  []Target{web, api, worker},
			want: []reconcileAction{{name: "worker", kind: recAdd}},
		},
		{
			name: "removed target",
			old:  []Target{web, api, worker},
			new:  []Target{web, api},
			want: []reconcileAction{{name: "worker", kind: recRemove}},
		},
		{
			name: "changed cmd restarts that target only",
			old:  []Target{web, api},
			new:  []Target{web, cmdChanged},
			want: []reconcileAction{{name: "api", kind: recRestart}},
		},
		{
			name: "changed on rewires the watcher",
			old:  []Target{web, api},
			new:  []Target{web, onChanged},
			want: []reconcileAction{{name: "api", kind: recRewatch}},
		},
		{
			name: "changed except rewires the watcher",
			old:  []Target{web, api},
			new:  []Target{web, exceptChanged},
			want: []reconcileAction{{name: "api", kind: recRewatch}},
		},
		{
			name: "changed delay rewires the watcher",
			old:  []Target{web, api},
			new:  []Target{web, delayChanged},
			want: []reconcileAction{{name: "api", kind: recRewatch}},
		},
		{
			name: "changed no-gitignore rewires the watcher",
			old:  []Target{web, api},
			new:  []Target{web, ignoreChanged},
			want: []reconcileAction{{name: "api", kind: recRewatch}},
		},
		{
			name: "cosmetic fields are no action",
			old:  []Target{web, api},
			new:  []Target{web, cosmetic},
		},
		{
			// A reorder is not an action: the apply step follows the new order.
			name: "reordered config",
			old:  []Target{web, api},
			new:  []Target{api, web},
		},
		{
			name: "one save with every action",
			old:  []Target{web, api, recTarget("legacy", "legacy")},
			new:  []Target{recTarget("web", "web-v2"), onChanged, worker},
			want: []reconcileAction{
				{name: "web", kind: recRestart},
				{name: "api", kind: recRewatch},
				{name: "worker", kind: recAdd},
				{name: "legacy", kind: recRemove},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffTargets(tt.old, tt.new)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("diffTargets = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The banner names every action once, in the new config's order.
func TestReconcileSummaryGlyphs(t *testing.T) {
	actions := []reconcileAction{
		{name: "auth-api", kind: recAdd},
		{name: "omsx", kind: recRestart},
		{name: "webhooks", kind: recRewatch},
		{name: "old", kind: recRemove},
	}
	want := "── config reloaded ── +auth-api  ~omsx(cmd)  ↺webhooks(watch)  −old ──"
	if got := reconcileSummary(actions); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got := reconcileSummary(nil); got != "" {
		t.Fatalf("summary without actions = %q, want nothing", got)
	}
}

func TestConfigErrorLineIsOneLine(t *testing.T) {
	got := configErrorLine(errors.New("yaml: line 3:\n  mapping values are not allowed"))
	want := "── config reload failed: yaml: line 3: mapping values are not allowed ──"
	if got != want {
		t.Fatalf("error line = %q, want %q", got, want)
	}
}

func TestOpGlyphMapping(t *testing.T) {
	tests := []struct {
		name string
		op   fsnotify.Op
		want string
	}{
		{"create", fsnotify.Create, "+"},
		{"write", fsnotify.Write, "~"},
		{"rename", fsnotify.Rename, "~"},
		{"remove", fsnotify.Remove, "−"},
		// A create arrives with its write, and the create is the interesting half.
		{"create and write", fsnotify.Create | fsnotify.Write, "+"},
		{"no op detail", 0, "~"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opText(opGlyph(tt.op)); got != tt.want {
				t.Fatalf("opGlyph(%v) = %q, want %q", tt.op, got, tt.want)
			}
		})
	}
}

func TestRelToWatchTrimsTheRoot(t *testing.T) {
	tests := []struct {
		name  string
		roots []string
		path  string
		want  string
	}{
		{name: "default root", path: "src/a.go", want: "src/a.go"},
		{name: "dot slash root", roots: []string{"./"}, path: "./src/a.go", want: "src/a.go"},
		{name: "named root", roots: []string{"apps/api"}, path: "apps/api/main.go", want: "main.go"},
		{name: "second root matches", roots: []string{"web", "api"}, path: "api/main.go", want: "main.go"},
		{name: "outside every root", roots: []string{"api"}, path: "web/main.go", want: "web/main.go"},
		{name: "the root itself", roots: []string{"api"}, path: "api", want: "api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relToWatch(tt.roots, tt.path); got != tt.want {
				t.Fatalf("relToWatch(%v, %q) = %q, want %q", tt.roots, tt.path, got, tt.want)
			}
		})
	}
}

// sevenFiles is a change batch big enough to hit the tree cap.
func sevenFiles() rerun.ChangeSet {
	files := map[string]struct{}{}
	ops := map[string]fsnotify.Op{}
	for _, name := range []string{"z.go", "a.go", "c.go", "b.go", "e.go", "d.go", "f.go"} {
		files[name] = struct{}{}
		ops[name] = fsnotify.Write
	}
	ops["a.go"] = fsnotify.Create
	return rerun.ChangeSet{FirstFile: "a.go", Files: files, Ops: ops}
}

// treeModel is three targets whose first row carries one seven-file spawn.
func treeModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, target("web"), target("api"), target("worker"))
	spawnWithChange(m, "web", sevenFiles(), ledgerClock)
	return m
}

// The left column interleaves target rows with reboot ledgers, so a screen
// line is not a target index: every line has to resolve back to a row.
func TestVisualRowsMapScreenLinesToTargets(t *testing.T) {
	m := treeModel(t)
	m.cursor = 1 // web, whose ledger opens on its own
	m.View()
	if m.layout.offset != 0 {
		t.Fatalf("offset = %d, want the ledger on screen from the top", m.layout.offset)
	}

	// ALL, web, its entry header, five files, "+2 more", api, worker.
	rows := m.visualRows()
	if len(rows) != 11 {
		t.Fatalf("visual rows = %d, want 11", len(rows))
	}
	want := []int{0, 1, 1, 1, 1, 1, 1, 1, 1, 2, 3}
	for y := range want {
		if got := m.rowAt(2, y); got != want[y] {
			t.Fatalf("rowAt line %d = %d, want %d", y, got, want[y])
		}
	}

	// Past the list is nothing, as before.
	if got := m.rowAt(2, 11); got != -1 {
		t.Fatalf("rowAt past the list = %d, want -1", got)
	}

	// A click on a file line selects the target row it hangs under.
	m.handleMouse(mouse(2, 4, false))
	if m.cursor != 1 {
		t.Fatalf("cursor after a file-line click = %d, want 1", m.cursor)
	}
}

// Newest first, sorted paths, capped at treeMax, with the rest counted.
func TestLedgerRowsAreSortedAndCapped(t *testing.T) {
	m := treeModel(t)
	m.cursor = 1

	rows := m.treeRows(0)
	if len(rows) != 1+treeMax+1 {
		t.Fatalf("ledger rows = %d, want the header, %d files and the more row", len(rows), treeMax)
	}
	if rows[0].kind != treeEntry || rows[0].count != 7 || !rows[0].at.Equal(ledgerClock) {
		t.Fatalf("header = %+v, want the spawn time and the file count", rows[0])
	}
	if rows[1].file != "a.go" || rows[1].op != "+" || rows[2].file != "b.go" {
		t.Fatalf("file rows = %+v, want sorted paths with their ops", rows[1:3])
	}
	if last := rows[len(rows)-1]; last.kind != treeMore || last.more != 2 {
		t.Fatalf("last row = %+v, want a +2 more row", last)
	}

	// A row without a ledger grows no tree, expanded or not.
	if got := m.treeRows(1); got != nil {
		t.Fatalf("tree rows for an unchanged target = %+v, want none", got)
	}

	view := m.View()
	if !strings.Contains(view, "↺ 14:32:07 · 7 files") || !strings.Contains(view, "+2 more") {
		t.Fatalf("the view is missing the reboot ledger:\n%s", view)
	}
}

// Only the cursor row shows a ledger, and t collapses the cursor row's.
func TestLedgerFollowsTheCursorAndTogglesByKey(t *testing.T) {
	m := treeModel(t)
	m.cursor = 1
	if got := len(m.visualRows()); got != 11 {
		t.Fatalf("rows with web selected = %d, want 11", got)
	}

	// Moving off the row collapses its ledger.
	press(m, "down")
	if got := len(m.visualRows()); got != 4 {
		t.Fatalf("rows after moving away = %d, want the plain list", got)
	}

	// Selecting it again expands it.
	press(m, "up")
	if got := len(m.visualRows()); got != 11 {
		t.Fatalf("rows after reselecting = %d, want the ledger back", got)
	}

	// t collapses the cursor row's ledger; another t brings it back.
	press(m, "t")
	if got := len(m.visualRows()); got != 4 {
		t.Fatalf("rows after collapsing the ledger = %d, want the plain list", got)
	}
	if !m.treeCollapsed {
		t.Fatal("t did not collapse the cursor row's ledger")
	}
	press(m, "t")
	if got := len(m.visualRows()); got != 11 {
		t.Fatalf("rows after expanding the ledger = %d, want the ledger back", got)
	}

	// A row with nothing to show ignores t: the flag covers the cursor's row.
	m.cursor = 2
	press(m, "t")
	if len(m.visualRows()) != 4 {
		t.Fatal("t opened a ledger on a row without one")
	}
}

// The +/− cell opens the row it belongs to, and toggles it once the cursor is
// there. It never solos.
func TestTreeCellClickOpensAndTogglesTheLedger(t *testing.T) {
	m := treeModel(t)
	m.cursor = 0
	m.View()

	leftW, _ := m.layoutWidths()
	cell := treeX(leftW)
	if got := m.rowAt(cell, 1); got != 1 {
		t.Fatalf("the ledger cell at line 1 maps to row %d, want 1", got)
	}

	m.handleMouse(mouse(cell, 1, false))
	if m.cursor != 1 {
		t.Fatalf("cursor after the cell click = %d, want the web row", m.cursor)
	}
	if m.solo != -1 {
		t.Fatalf("solo = %d, want none after a cell click", m.solo)
	}
	if !m.treeExpanded(0) {
		t.Fatal("the cell click did not open the ledger")
	}

	m.handleMouse(mouse(cell, 1, false))
	if !m.treeCollapsed || len(m.visualRows()) != 4 {
		t.Fatalf("the second cell click did not collapse the ledger: collapsed=%v rows=%d", m.treeCollapsed, len(m.visualRows()))
	}
}

// The cursor row stays visible even when its ledger cannot fit with it.
func TestListOffsetKeepsTheCursorRowVisible(t *testing.T) {
	m := treeModel(t)
	m.cursor = 3 // worker, the last row
	m.height = 4
	m.View()

	if got := m.rowAt(2, m.height-2); got != 3 {
		t.Fatalf("the last row is not on screen at offset %d: line %d maps to %d", m.layout.offset, m.height-2, got)
	}

	m.cursor = 1 // web with its eight ledger lines and room for three
	m.View()
	if m.layout.offset != 1 {
		t.Fatalf("offset = %d, want 1 so the ledger stays right below its row", m.layout.offset)
	}
}

// A reload adopts the new list without disturbing what the rows already hold.
func TestReconcileRebuildsRowsByName(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvent(event{kind: evOutput, target: "web", line: "web says hi"})
	m.applyEvent(event{kind: evOutput, target: "api", line: "api says hi"})
	m.cursor = 1    // web
	m.toggleSolo(1) // api
	m.targets[0].muted = true
	m.targets[0].unseen = true

	web, api := m.targets[0], m.targets[1]
	worker := newTargetRunner(recTarget("worker", "worker"), &recordingSink{})
	m.applyEvent(event{
		kind: evReconcile,
		line: "── config reloaded ── +worker ──",
		cfg: &configChange{
			targets: []Target{api.spec, web.spec, worker.target},
			runners: []*targetRunner{api.runner, web.runner, worker},
			stopped: []bool{false, false, true},
		},
	})

	if got := m.names(); !reflect.DeepEqual(got, []string{"api", "web", "worker"}) {
		t.Fatalf("rows = %v, want the new config's order", got)
	}
	// The cursor and the solo follow their target, not their old index.
	if m.cursor != 2 || m.solo != 0 {
		t.Fatalf("cursor = %d solo = %d, want web selected and api soloed", m.cursor, m.solo)
	}
	if !web.muted || !web.unseen {
		t.Error("the reload dropped a row's mute state")
	}
	if m.prefixW != len("worker") {
		t.Fatalf("prefix width = %d, want %d", m.prefixW, len("worker"))
	}
	// A held-down row starts off, and the banner landed in the pane.
	if got := m.targets[2].state; got != stateOff {
		t.Fatalf("the added row's state = %v, want off", got)
	}
	if got := strings.Join(m.paneLines(), "\n"); !strings.Contains(got, "config reloaded") {
		t.Fatalf("the banner is missing from the pane:\n%s", got)
	}
	// Retained output keeps its own target: api is soloed, so only it streams.
	if got := strings.Join(m.paneLines(), "\n"); !strings.Contains(got, "api says hi") || strings.Contains(got, "web says hi") {
		t.Fatalf("the stream filter lost its rows after the reload:\n%s", got)
	}
}

// names is the row order a test can assert on.
func (m *tuiModel) names() []string {
	names := make([]string, 0, len(m.targets))
	for _, t := range m.targets {
		names = append(names, t.spec.Name)
	}
	return names
}

// A target added while a stream is soloed waits for an explicit start.
func TestReconcileHoldsANewTargetWhileSoloed(t *testing.T) {
	sink := &recordingSink{}
	g := newTargetGroup([]Target{recTarget("web", "web")}, sink, "")
	g.setSoloGuard(true)
	t.Cleanup(g.cancel)

	g.applyConfig([]Target{recTarget("web", "web"), recTarget("api", "api")})

	var cfg *configChange
	for _, ev := range sink.all() {
		if ev.kind == evReconcile {
			cfg = ev.cfg
		}
	}
	if cfg == nil {
		t.Fatal("no reconcile event was reported")
	}
	if len(cfg.targets) != 2 || len(cfg.stopped) != 2 {
		t.Fatalf("config change = %+v, want both targets and their states", cfg)
	}
	if !cfg.stopped[1] {
		t.Fatal("the target added under a solo was not held down")
	}
}

// A changed cmd and a changed watch set reach the runner as different controls:
// one restarts the process, the other only rebuilds the watcher.
func TestApplyConfigHandsTheRunnerItsNewSpec(t *testing.T) {
	g := newTargetGroup([]Target{recTarget("web", "web"), recTarget("api", "api")}, &recordingSink{}, "")

	cmdChanged := []Target{recTarget("web", "web-v2"), recTarget("api", "api")}
	g.applyConfig(cmdChanged)
	if c := nextCtrl(t, g.runners[0]); c.kind != ctrlSpec || c.target == nil || c.target.Cmd != "web-v2" {
		t.Fatalf("control = %+v, want a ctrlSpec carrying the new cmd", c)
	}

	watchChanged := []Target{recTarget("web", "web-v2"), recTarget("api", "api")}
	watchChanged[0].On = []string{"pkg"}
	g.applyConfig(watchChanged)
	if c := nextCtrl(t, g.runners[0]); c.kind != ctrlRewatch || c.target == nil || c.target.On[0] != "pkg" {
		t.Fatalf("control = %+v, want a ctrlRewatch carrying the new roots", c)
	}
	select {
	case c := <-g.runners[1].ctrlCh:
		t.Fatalf("the untouched target got %+v, want no spec control", c)
	default:
	}
}

// A columns edit is cosmetic: no restart, no rewatch, and the new spec still
// reaches the TUI rows.
func TestColumnsChangeIsCosmeticAndReachesTheTUI(t *testing.T) {
	old := recTarget("web", "web")
	old.Columns = []Column{{Name: "a", Expr: ".a"}}
	fresh := recTarget("web", "web")
	fresh.Columns = []Column{{Name: "b", Expr: ".b"}}

	if actions := diffTargets([]Target{old}, []Target{fresh}); len(actions) != 0 {
		t.Fatalf("actions = %+v, want none for a columns-only change", actions)
	}

	m := newTestModel(t, old)
	m.applyEvent(event{kind: evReconcile, cfg: &configChange{
		targets: []Target{fresh},
		runners: []*targetRunner{m.targets[0].runner},
		stopped: []bool{false},
	}})
	if got := m.targets[0].spec.Columns; len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("columns = %+v, want the new spec on the row", got)
	}
}

// nextCtrl pops the control a runner's loop would have taken.
func nextCtrl(t *testing.T, r *targetRunner) ctrlMsg {
	t.Helper()
	select {
	case c := <-r.ctrlCh:
		return c
	case <-time.After(time.Second):
		t.Fatal("no control message was queued")
		return ctrlMsg{}
	}
}

// A target the user stopped takes the new cmd but never a surprise restart.
func TestChangedCmdNeverStartsAStoppedTarget(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(recTarget("web", "web"), sink)
	atomic.StoreInt32(&r.stopped, 1)

	fresh := recTarget("web", "web-v2")
	r.handleControl(context.Background(), ctrlMsg{kind: ctrlSpec, target: &fresh})

	if r.target.Cmd != "web-v2" {
		t.Fatalf("spec cmd = %q, want the new one", r.target.Cmd)
	}
	if got := sink.all(); len(got) != 0 {
		t.Fatalf("a stopped target restarted on a config edit: %+v", got)
	}
}

// reload is the config watch's whole body: parse, diff, apply, report. It is
// tested here rather than through fsnotify, so a reload cannot flake on a timer.
func TestReloadAppliesTheConfigOrReportsTheError(t *testing.T) {
	g := func(t *testing.T, running []Target, path string) (*targetGroup, *recordingSink) {
		t.Helper()
		sink := &recordingSink{}
		group := newTargetGroup(running, sink, path)
		t.Cleanup(group.cancel)
		return group, sink
	}
	save := func(t *testing.T, path, yml string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("applies the new list", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ConfigFileName)
		save(t, path, "web:\n  on: src\n  cmd: sleep 30\n")
		group, sink := g(t, []Target{recTarget("web", "sleep 30")}, path)

		save(t, path, "web:\n  on: src\n  cmd: sleep 31\n\napi:\n  on: src\n  cmd: sleep 30\n")
		group.reload(path)

		var banner string
		for _, ev := range sink.all() {
			if ev.kind == evReconcile {
				banner = ev.line
			}
		}
		if banner != "── config reloaded ── ~web(cmd)  +api ──" {
			t.Fatalf("banner = %q, want the reload summary", banner)
		}
		if got := group.snapshotTargets(); len(got) != 2 || got[0].Cmd != "sleep 31" || got[1].Name != "api" {
			t.Fatalf("targets = %+v, want the freshly parsed list", got)
		}
		if c := nextCtrl(t, group.runners[0]); c.kind != ctrlSpec || c.target.Cmd != "sleep 31" {
			t.Fatalf("control = %+v, want a ctrlSpec carrying the new cmd", c)
		}
	})

	t.Run("a broken save changes nothing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ConfigFileName)
		running := []Target{recTarget("web", "sleep 30"), recTarget("api", "sleep 30")}
		group, sink := g(t, running, path)
		save(t, path, "web:\n  cmd:\n")

		group.reload(path)

		got := sink.all()
		if len(got) != 1 || got[0].kind != evNotice {
			t.Fatalf("events = %+v, want one notice", got)
		}
		if line := got[0].line; !strings.HasPrefix(line, "── config reload failed:") {
			t.Fatalf("line = %q, want the parse error", line)
		}
		if targets := group.snapshotTargets(); len(targets) != 2 || targets[0].Cmd != "sleep 30" {
			t.Fatalf("a broken save touched the targets: %+v", targets)
		}
		for _, r := range group.runners {
			select {
			case c := <-r.ctrlCh:
				t.Fatalf("a broken save sent the runner %+v", c)
			default:
			}
		}
	})

	t.Run("a cosmetic save only refreshes the spec", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ConfigFileName)
		save(t, path, "web:\n  on: src\n  cmd: sleep 30\n  desc: the web\n")
		group, sink := g(t, []Target{recTarget("web", "sleep 30")}, path)

		group.reload(path)

		got := sink.all()
		if len(got) != 1 || got[0].kind != evReconcile || got[0].line != "" {
			t.Fatalf("events = %+v, want one silent reconcile", got)
		}
		if desc := group.snapshotTargets()[0].Desc; desc != "the web" {
			t.Fatalf("desc = %q, want the new one", desc)
		}
		select {
		case c := <-group.runners[0].ctrlCh:
			t.Fatalf("a desc-only save sent the runner %+v", c)
		default:
		}
	})
}

// A name is the config's own text: a newline in it must not break the banner's
// one-line invariant.
func TestReconcileBannerStaysOneLine(t *testing.T) {
	got := reconcileSummary([]reconcileAction{{name: "web\napi", kind: recAdd}})
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("summary = %q, want one line", got)
	}
	if !strings.Contains(got, "+web api") {
		t.Fatalf("summary = %q, want the flattened name", got)
	}
	if line := staleSpecLine([]string{"web\napi"}); strings.Contains(line, "\n") {
		t.Fatalf("stale line = %q, want one line", line)
	}
}

// A banner past the pane width is clamped, so it cannot wrap the frame.
func TestReconcileBannerClampsToPaneWidth(t *testing.T) {
	m := newTestModel(t, recTarget("web", "sleep 1"))
	line := reconcileSummary([]reconcileAction{{name: strings.Repeat("verylongname", 20), kind: recAdd}})
	if w := m.paneWidth(); displayWidth(line) <= w {
		t.Fatalf("the fixture banner is %d cells, not past the pane width %d", displayWidth(line), w)
	}

	m.addNotice(line)

	got := m.visibleRaw[len(m.visibleRaw)-1]
	if w := m.paneWidth(); displayWidth(got) > w {
		t.Fatalf("banner = %q (%d cells), want at most the pane width %d", got, displayWidth(got), w)
	}
}

// waitForEvent waits for a runner to report a kind.
func waitForEvent(t *testing.T, sink *recordingSink, kind eventKind, target string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range sink.all() {
			if ev.kind == kind && ev.target == target {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %v on %s; events: %+v", kind, target, sink.all())
}

// A target removed while its restart waits out the cooldown must not sit there
// for the rest of it, nor start the new generation on the way out.
func TestRetireInterruptsTheCooldown(t *testing.T) {
	sink := &recordingSink{}
	r := newTargetRunner(recTarget("web", "sleep 30"), sink)
	r.target.Cooldown = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.run(ctx)
	}()

	waitForEvent(t, sink, evStarted, "web", 3*time.Second)

	// A cmd change kills the old generation and then waits out the cooldown.
	spec := recTarget("web", "sleep 31")
	spec.Cooldown = 5 * time.Second
	if !r.controlSpec(ctrlSpec, spec) {
		t.Fatal("the spec control was not queued")
	}
	time.Sleep(150 * time.Millisecond)

	start := time.Now()
	close(r.retire)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retire did not interrupt the cooldown")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the runner returned after %v, want the cooldown interrupted", waited)
	}

	starts := 0
	for _, ev := range sink.all() {
		if ev.kind == evStarted {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("starts = %d, want 1: a removed target started a generation", starts)
	}
}
