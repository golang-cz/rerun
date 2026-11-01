package main

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
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
		case prev.Cmd != t.Cmd:
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
// changed, which leaves the running watch list stale.
func watchSpecDiffers(a, b Target) bool {
	return !slices.Equal(a.On, b.On) ||
		!slices.Equal(a.Except, b.Except) ||
		a.Delay != b.Delay ||
		a.NoGitignore != b.NoGitignore
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
	return "── config reload failed: " + oneLine(err.Error()) + " ──"
}

// oneLine flattens text, which may carry newlines, onto one pane row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// watchConfig reloads the config whenever the file changes. Its watcher is its
// own: a config write is rerun's business, not a target's watched change.
func (g *targetGroup) watchConfig(path string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		g.sink.emit(event{kind: evNotice, line: configErrorLine(err)})
		return
	}
	defer watcher.Close()

	clean := filepath.Clean(path)
	// The directory, not the file: an editor that swaps the file in with a
	// rename would otherwise leave the watch pointing at the old inode.
	if err := watcher.Add(filepath.Dir(clean)); err != nil {
		g.sink.emit(event{kind: evNotice, line: configErrorLine(err)})
		return
	}

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
			if filepath.Clean(ev.Name) != clean {
				continue
			}
			if ev.Op&fsnotify.Chmod == fsnotify.Chmod {
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
			g.sink.emit(event{kind: evNotice, line: configErrorLine(err)})

		case <-timeoutC:
			g.reload(clean)
		}
	}
}

// reload parses the config again and reconciles the running targets with it. A
// config that does not parse leaves every running target untouched.
func (g *targetGroup) reload(path string) {
	cfg, err := ParseConfig(path)
	if err != nil {
		g.sink.emit(event{kind: evNotice, line: configErrorLine(err)})
		return
	}
	g.applyConfig(cfg.Targets)
}

// applyConfig reconciles the running targets with a freshly parsed list: added
// targets start, dropped ones stop, a changed cmd restarts that target alone,
// and a changed watch set rebuilds that target's watcher alone. It reports the
// delta as one banner line through the sink. A list identical to the running
// one is a no-op.
func (g *targetGroup) applyConfig(targets []Target) []reconcileAction {
	old := g.snapshotTargets()
	if reflect.DeepEqual(old, targets) {
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
	for _, spec := range targets {
		r, running := runnerByName[spec.Name]
		if !running {
			r = newTargetRunner(spec, g.sink)
			if g.soloGuard.Load() {
				// Added while one stream is soloed: hold it for an explicit start.
				atomic.StoreInt32(&r.stopped, 1)
			}
			spawn = append(spawn, r)
			g.wg.Add(1)
		} else {
			prev := oldByName[spec.Name]
			switch {
			case prev.Cmd != spec.Cmd:
				if !r.controlSpec(ctrlSpec, spec) {
					stale = append(stale, spec.Name)
				}
			case watchSpecDiffers(prev, spec):
				if !r.controlSpec(ctrlRewatch, spec) {
					stale = append(stale, spec.Name)
				}
			}
		}
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
	g.mu.Unlock()

	line := reconcileSummary(actions)
	g.sink.emit(event{
		kind: evReconcile,
		line: line,
		cfg: &configChange{
			targets: fresh,
			runners: runners,
			stopped: stopped,
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
