package main

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// configDebounce is how long a config write must settle before the reload:
// editors write a temp file and rename it, which arrives as several events.
const configDebounce = 150 * time.Millisecond

// reconcileKind is what a config reload asks of one target.
type reconcileKind int

const (
	recAdd     reconcileKind = iota // a new target: start it
	recRemove                       // a dropped target: stop it, release its watcher
	recRestart                      // its cmd changed: bounce the process
	recRewatch                      // its watched set changed: rebuild the watcher
)

type reconcileAction struct {
	name string
	kind reconcileKind
}

// diffTargets compares the running targets with a freshly parsed config and
// returns what reconciles them. A field that only applies at the next start, or
// that is cosmetic, produces no action: a comment edit or a new `desc:` is
// silent.
func diffTargets(old, new []Target) []reconcileAction {
	oldByName := make(map[string]Target, len(old))
	for _, t := range old {
		oldByName[t.Name] = t
	}
	newNames := make(map[string]struct{}, len(new))
	for _, t := range new {
		newNames[t.Name] = struct{}{}
	}

	var actions []reconcileAction
	for _, t := range new {
		prev, ok := oldByName[t.Name]
		switch {
		case !ok:
			actions = append(actions, reconcileAction{name: t.Name, kind: recAdd})
		case prev.Cmd != t.Cmd || prev.Build != t.Build || prev.Health != t.Health:
			actions = append(actions, reconcileAction{name: t.Name, kind: recRestart})
		case watchSpecDiffers(prev, t):
			actions = append(actions, reconcileAction{name: t.Name, kind: recRewatch})
		}
	}
	// Removed targets keep their old order, so the banner reads the same
	// wherever the survivors moved to.
	for _, t := range old {
		if _, ok := newNames[t.Name]; !ok {
			actions = append(actions, reconcileAction{name: t.Name, kind: recRemove})
		}
	}
	return actions
}

// watchSpecDiffers reports whether the fields the file watcher is built from
// changed, which leaves the running watch list stale. A new start mode is one
// of them: manual drops the watcher, the others build one.
func watchSpecDiffers(a, b Target) bool {
	return !slices.Equal(a.On, b.On) ||
		!slices.Equal(a.Except, b.Except) ||
		a.Delay != b.Delay ||
		a.NoGitignore != b.NoGitignore ||
		a.Start != b.Start
}

// reconcileSummary renders the actions as one banner line:
// `── config reloaded ── +auth-api  ~omsx(cmd)  ↺webhooks(watch) ──`.
func reconcileSummary(actions []reconcileAction) string {
	if len(actions) == 0 {
		return ""
	}
	tokens := make([]string, 0, len(actions))
	for _, a := range actions {
		// Flattened: the banner is one pane row, and the name is the config's
		// own text.
		name := oneLine(a.name)
		switch a.kind {
		case recAdd:
			tokens = append(tokens, "+"+name)
		case recRemove:
			tokens = append(tokens, "−"+name)
		case recRestart:
			tokens = append(tokens, "~"+name+"(cmd)")
		case recRewatch:
			tokens = append(tokens, "↺"+name+"(watch)")
		}
	}
	return "── config reloaded ── " + strings.Join(tokens, "  ") + " ──"
}

// staleSpecLine reports targets whose new spec never reached their runner, so a
// row and a process that disagree are at least visible in the pane.
func staleSpecLine(names []string) string {
	flat := make([]string, 0, len(names))
	for _, name := range names {
		flat = append(flat, oneLine(name))
	}
	return "── config reloaded ── stale spec: " + strings.Join(flat, "  ") + " (the runner had stopped) ──"
}

// configErrorLine is the banner for a config rerun could not parse.
func configErrorLine(err error) string {
	return "── config reload failed: " + oneLine(printable(err.Error(), false)) + " ──"
}

// oneLine flattens text, which may carry newlines, onto one pane row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// watchConfig reloads the config whenever the root file or an import changes.
func (g *targetGroup) watchConfig(path string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		g.sink.emit(event{kind: evNotice, line: configErrorLine(err), err: err})
		return
	}
	defer watcher.Close()

	clean := path
	if abs, err := filepath.Abs(path); err == nil {
		clean = abs
	}
	w := &configWatch{watcher: watcher, dirs: map[string]bool{}, sink: g.sink, rootDir: filepath.Dir(clean)}
	cfg, _ := ParseConfig(clean)
	w.syncAfter(clean, cfg)

	var timer *time.Timer
	var timeoutC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			return

		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !w.interesting(filepath.Clean(ev.Name)) {
				continue
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(configDebounce)
				timeoutC = timer.C
				continue
			}
			// A value a fired timer parked in C must be drained first, or
			// Reset delivers it at once and splits the write in progress.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(configDebounce)

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			g.sink.emit(event{kind: evNotice, line: configErrorLine(err), err: err})

		case <-timeoutC:
			// A failed parse still watches the imports: one that is half written reloads on its next write.
			// Dirs added by this sync may hold a file written before its watch existed, so reload once more.
			if w.syncAfter(clean, g.reload(clean)) {
				w.syncAfter(clean, g.reload(clean))
			}
		}
	}
}

// configWatch holds the dirs the config watcher watches: a file's dir, or an absent import's nearest existing dir.
type configWatch struct {
	watcher *fsnotify.Watcher
	sink    sink
	dirs    map[string]bool
	// known and absent are the last synced inputs; files is the same paths as a set.
	known  []string
	files  map[string]bool
	absent []missingImport
	// rootDir is the dir of rerun.yml, where a Makefile written later also changes the config.
	rootDir string
}

// sync makes the watched dirs match the files and the absent imports.
func (w *configWatch) sync(files []string, missing []missingImport) (added bool) {
	w.files = make(map[string]bool, len(files))
	want := make(map[string]bool)
	for _, f := range files {
		f = filepath.Clean(f)
		w.files[f] = true
		want[nearestDir(filepath.Dir(f))] = true
	}
	w.known, w.absent = files, missing
	for _, m := range missing {
		want[nearestDir(filepath.Dir(filepath.Clean(m.Path)))] = true
	}

	for dir := range want {
		if w.dirs[dir] {
			continue
		}
		if err := w.watcher.Add(dir); err != nil {
			w.sink.emit(event{kind: evNotice, line: configErrorLine(err), err: err})
			continue
		}
		w.dirs[dir] = true
		added = true
	}
	for dir := range w.dirs {
		if !want[dir] {
			_ = w.watcher.Remove(dir)
			delete(w.dirs, dir)
		}
	}
	return added
}

// syncAfter watches what a load read; a failed load, cfg nil, also watches the root's import paths.
func (w *configWatch) syncAfter(root string, cfg *Config) (added bool) {
	if cfg != nil {
		return w.sync(cfg.Files, cfg.Missing)
	}
	files := append(append([]string{root}, w.known...), importedPaths(root)...)
	// The implicit Makefile is not a listed import, so a broken load still has to watch it.
	if mk := implicitMakefile(filepath.Dir(root)); mk != "" {
		files = append(files, mk)
	}
	return w.sync(slices.Compact(slices.Sorted(slices.Values(files))), w.absent)
}

// importedPaths lists the root file's import paths, read apart from the full parse.
func importedPaths(root string) []string {
	data, err := os.ReadFile(root)
	if err != nil {
		return nil
	}
	var doc struct {
		Imports []struct {
			Path string `yaml:"path"`
		} `yaml:"imports"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	var out []string
	for _, imp := range doc.Imports {
		if abs, err := resolveImportPath(filepath.Dir(root), imp.Path); err == nil {
			out = append(out, abs)
		}
	}
	return out
}

// interesting reports whether an event on name can change the config.
func (w *configWatch) interesting(name string) bool {
	if w.files[name] {
		return true
	}
	if filepath.Dir(name) == w.rootDir && slices.Contains(makeNames, filepath.Base(name)) {
		return true
	}
	for _, m := range w.absent {
		p := filepath.Clean(m.Path)
		if p == name || strings.HasPrefix(p, name+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// nearestDir is dir, or its closest ancestor that exists.
func nearestDir(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// migrateNotified is the config paths whose migrate notice a front-end already showed.
var migrateNotified sync.Map

// dedupNotices drops the migrate notice's repeats, so one process shows it once per config path.
func dedupNotices(cfg *Config) []string {
	out := make([]string, 0, len(cfg.Notices))
	for _, n := range cfg.Notices {
		if strings.Contains(n, migrateNoticeText) {
			if _, seen := migrateNotified.LoadOrStore(filepath.Clean(cfg.Path), struct{}{}); seen {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// reload parses the config again and reconciles the running targets with it. A
// reload returns the parsed config, or nil when it did not load.
func (g *targetGroup) reload(path string) *Config {
	cfg, err := ParseConfig(path)
	if err != nil {
		g.sink.emit(event{kind: evConfigErr, line: configErrorLine(err), err: err})
		return nil
	}
	g.sink.emit(event{kind: evConfigOK})
	for _, n := range dedupNotices(cfg) {
		g.sink.emit(event{kind: evNotice, line: "── " + oneLine(n) + " ──"})
	}
	g.mu.Lock()
	g.missing = cfg.Missing
	g.mu.Unlock()
	g.applyConfig(cfg.Targets, cfg.Queries)
	return cfg
}

// applyConfig reconciles the running targets with a freshly parsed list: added
// targets start, dropped ones stop, a changed cmd restarts that target alone,
// and a changed watch set rebuilds that target's watcher alone. It reports the
// delta as one banner line through the sink. A list identical to the running
// one is a no-op.
func (g *targetGroup) applyConfig(targets []Target, queries map[string]string) []reconcileAction {
	old := g.snapshotTargets()
	if reflect.DeepEqual(old, targets) {
		// Only the saved queries changed: adopt them without touching a runner.
		g.mu.Lock()
		if g.closing || reflect.DeepEqual(g.queries, queries) {
			g.mu.Unlock()
			return nil
		}
		g.queries = maps.Clone(queries)
		runners := append([]*targetRunner(nil), g.runners...)
		missing := g.missing
		g.mu.Unlock()
		stopped := make([]bool, len(runners))
		for i, r := range runners {
			stopped[i] = r.userStopped()
		}
		g.sink.emit(event{
			kind: evReconcile,
			at:   time.Now(),
			cfg: &configChange{
				targets: old,
				runners: runners,
				stopped: stopped,
				missing: missing,
				queries: queries,
			},
		})
		return nil
	}
	actions := diffTargets(old, targets)

	oldByName := make(map[string]Target, len(old))
	for _, t := range old {
		oldByName[t.Name] = t
	}

	g.mu.Lock()
	if g.closing {
		// The run is winding down: no runner may be born now.
		g.mu.Unlock()
		return nil
	}

	runnerByName := make(map[string]*targetRunner, len(g.runners))
	for i, t := range old {
		runnerByName[t.Name] = g.runners[i]
	}

	fresh := append([]Target(nil), targets...)
	runners := make([]*targetRunner, 0, len(targets))
	stopped := make([]bool, 0, len(targets))
	var spawn []*targetRunner
	var stale []string
	for i, spec := range targets {
		r, running := runnerByName[spec.Name]
		if !running {
			r = newTargetRunner(spec, g.sink)
			r.gate = g.gate
			if g.soloGuard.Load() {
				// Added while one stream is soloed: hold it for an explicit start.
				atomic.StoreInt32(&r.stopped, 1)
			}
			spawn = append(spawn, r)
			g.wg.Add(1)
		} else {
			prev := oldByName[spec.Name]
			switch {
			case prev.Cmd != spec.Cmd || prev.Build != spec.Build || prev.Health != spec.Health:
				if !r.controlSpec(ctrlSpec, spec) {
					stale = append(stale, spec.Name)
				}
			case watchSpecDiffers(prev, spec):
				if !r.controlSpec(ctrlRewatch, spec) {
					stale = append(stale, spec.Name)
				}
			}
		}
		r.order.Store(int64(i))
		runners = append(runners, r)
		stopped = append(stopped, r.userStopped())
	}

	// What left the config stops for good and releases its file watcher.
	kept := make(map[string]struct{}, len(targets))
	for _, spec := range targets {
		kept[spec.Name] = struct{}{}
	}
	for _, t := range old {
		if _, ok := kept[t.Name]; ok {
			continue
		}
		if r, ok := runnerByName[t.Name]; ok {
			close(r.retire)
		}
	}

	g.targets = fresh
	g.runners = runners
	g.queries = maps.Clone(queries)
	missing := g.missing
	g.mu.Unlock()

	line := reconcileSummary(actions)
	g.sink.emit(event{
		kind: evReconcile,
		at:   time.Now(),
		line: line,
		cfg: &configChange{
			targets: fresh,
			runners: runners,
			stopped: stopped,
			missing: missing,
			queries: queries,
		},
	})

	// Spawn after the banner: the new rows must exist before the runners'
	// first evStarted lands in the same channel. The wait group counted them
	// under the lock, so a shutdown cannot pass in between.
	for _, r := range spawn {
		go g.runTarget(r)
	}
	if len(stale) > 0 {
		g.sink.emit(event{kind: evNotice, line: staleSpecLine(stale)})
	}

	return actions
}
