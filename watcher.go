package rerun

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/denormal/go-gitignore"
	"github.com/fsnotify/fsnotify"
)

// errClosed aborts the initial walk when Close lands while it runs.
var errClosed = errors.New("watcher closed")

type Watcher struct {
	watcher     *fsnotify.Watcher
	watch       map[string]struct{}
	ignore      map[string]struct{}
	ignoreDirs  map[string]struct{}
	ignoreFiles map[string]struct{}
	gitignores  map[string]*gitignoreFile // dir -> its .gitignore, when one exists
	pinned      map[string]struct{}       // explicit on: roots, never ignored themselves
	done        chan struct{}
	watchOnce   sync.Once
	closeOnce   sync.Once

	respectGitignore bool
	stderr           io.Writer
}

type ChangeSet struct {
	FirstFile string
	Files     map[string]struct{}
	// Ops is the fsnotify operation last seen for each file in Files, so a
	// front-end can tell a created file from a written or removed one.
	Ops   map[string]fsnotify.Op
	Error error
}

func NewWatcher() (*Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		watcher:          watcher,
		watch:            make(map[string]struct{}),
		ignore:           make(map[string]struct{}),
		ignoreDirs:       make(map[string]struct{}),
		ignoreFiles:      make(map[string]struct{}),
		gitignores:       make(map[string]*gitignoreFile),
		pinned:           make(map[string]struct{}),
		respectGitignore: true,
		stderr:           os.Stderr,
		done:             make(chan struct{}),
	}

	return w, nil
}

// RespectGitignore turns .gitignore filtering on or off. It is on by default
// and must be configured before Watch.
func (w *Watcher) RespectGitignore(enabled bool) {
	w.respectGitignore = enabled
}

// Add registers a path to watch. Configure watches before calling Watch.
func (w *Watcher) Add(paths ...string) {
	for _, path := range paths {
		w.watch[path] = struct{}{}
	}
}

// Ignore registers a path or glob to skip. Configure ignores before Watch.
func (w *Watcher) Ignore(paths ...string) {
	for _, path := range paths {
		w.ignore[path] = struct{}{}
	}
}

// Watch starts the event goroutine and returns its ChangeSet channel. It is
// single-use: a second call returns a nil channel.
func (w *Watcher) Watch(delay time.Duration) <-chan ChangeSet {
	var changes chan ChangeSet

	w.watchOnce.Do(func() {
		// resolve add + ignore paths
		var roots []string
		for path := range w.watch {
			roots = append(roots, expandGlobPaths(path)...)
		}

		// Pins are global: an explicit on: root survives any ignore, and warns once
		// when .gitignore above it disagrees.
		for _, root := range roots {
			clean := filepath.Clean(root)
			if _, ok := w.pinned[clean]; ok {
				continue
			}
			w.pinned[clean] = struct{}{}

			if !w.respectGitignore {
				continue
			}
			if info, err := os.Stat(root); err == nil && ancestorIgnored(clean, info.IsDir()) {
				fmt.Fprintf(w.stderr, "rerun: warning: %s is .gitignored\n", clean)
			}
		}

		for _, root := range roots {
			if w.closed() {
				break
			}
			w.walkRoot(root)
		}

		changes = make(chan ChangeSet, 1)

		go func() {
			defer close(changes)

			// deliver hands the change to the consumer unless Close won the race.
			deliver := func(change ChangeSet) bool {
				select {
				case <-w.done:
					return false
				default:
				}
				select {
				case changes <- change:
					return true
				case <-w.done:
					return false
				}
			}

			for {
				change := ChangeSet{
					Files: make(map[string]struct{}),
					Ops:   make(map[string]fsnotify.Op),
				}

				// A nil timeoutC blocks forever, so no timer is needed before
				// the first event; the real timer is created on that event.
				var timeoutC <-chan time.Time
				var timer *time.Timer
				stopTimer := func() {
					if timer != nil {
						timer.Stop()
					}
				}

			deliverLoop:
				for {
					select {
					case event, ok := <-w.watcher.Events:
						if !ok {
							stopTimer()
							return
						}

						// Ignore CHMOD.
						if event.Op&fsnotify.Chmod == fsnotify.Chmod {
							continue
						}

						// NOTE: ignoreDirs is already ignored because it's never added to the watcher.
						if _, ok := w.ignoreFiles[event.Name]; ok {
							continue
						}

						if len(w.gitignores) > 0 && w.gitignored(event.Name, eventIsDir(event.Name)) {
							continue
						}

						if timeoutC == nil {
							timer = time.NewTimer(delay)
							timeoutC = timer.C
						} else {
							resetTimer(timer, delay)
						}

						if len(change.Files) == 0 {
							change.FirstFile = event.Name
						}
						change.Files[event.Name] = struct{}{}
						// Merged, not replaced: a create arrives with its write, and the
						// create is the interesting half.
						change.Ops[event.Name] |= event.Op

					case err, ok := <-w.watcher.Errors:
						if !ok {
							stopTimer()
							return
						}
						change.Error = err
						if !deliver(change) {
							stopTimer()
							return
						}
						stopTimer()
						break deliverLoop

					case <-timeoutC:
						if !deliver(change) {
							stopTimer()
							return
						}
						stopTimer()
						break deliverLoop

					case <-w.done:
						stopTimer()
						return
					}
				}
			}
		}()
	})

	return changes
}

// walkRoot watches root and everything below it that no ignore rule excludes.
func (w *Watcher) walkRoot(root string) {
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if w.closed() {
			return errClosed
		}

		if !info.IsDir() {

			// Files
			// Check if file path matches any ignore pattern
			for ignorePath := range w.ignore {
				if path == ignorePath || strings.HasPrefix(path, ignorePath+string(os.PathSeparator)) {
					// Check for exact match or if the file is in an ignored directory
					w.ignoreFiles[path] = struct{}{}
					return nil
				} else if strings.Contains(ignorePath, "*") {
					// Check for glob pattern match of filename
					if matchGlobPattern(ignorePath, path) {
						w.ignoreFiles[path] = struct{}{}
						return nil
					}
				}
			}

			if w.gitignored(path, false) {
				w.ignoreFiles[path] = struct{}{}
				return nil
			}

			// Watch single-file targets directly; fsnotify only watches dirs otherwise.
			if path == root {
				w.watcher.Add(path)
			}

			return nil
		}

		// Dirs
		if strings.HasSuffix(path, ".git") {
			return filepath.SkipDir
		}

		if _, ok := w.ignore[path]; ok {
			w.ignoreDirs[path] = struct{}{}
			return filepath.SkipDir
		}

		// Glob-ignored dirs must be skipped, or writes under them retrigger the target.
		for ignorePath := range w.ignore {
			if strings.Contains(ignorePath, "*") && matchGlobPattern(ignorePath, path) {
				w.ignoreDirs[path] = struct{}{}
				return filepath.SkipDir
			}
		}

		if w.gitignored(path, true) {
			w.ignoreDirs[path] = struct{}{}
			return filepath.SkipDir
		}

		// A .gitignore covers a directory's contents, never the directory itself.
		w.loadGitignore(filepath.Clean(path))
		w.watcher.Add(path)
		return nil
	})
}

// gitignoreFile is a parsed .gitignore anchored at base.
type gitignoreFile struct {
	base string
	gi   gitignore.GitIgnore
}

// match reports whether rel is ignored, and separately whether a pattern
// matched at all: a deeper .gitignore that does not match falls through to a
// shallower one.
func (g *gitignoreFile) match(rel string, isDir bool) (ignored, matched bool) {
	m := g.gi.Relative(rel, isDir)
	if m == nil {
		return false, false
	}
	return m.Ignore(), true
}

// readGitignore parses dir's .gitignore, or returns nil when there is none.
func readGitignore(dir string) *gitignoreFile {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	return &gitignoreFile{base: dir, gi: gitignore.New(bytes.NewReader(data), dir, nil)}
}

// loadGitignore caches a directory's .gitignore for the event-time checks.
func (w *Watcher) loadGitignore(dir string) {
	if !w.respectGitignore {
		return
	}
	if _, ok := w.gitignores[dir]; ok {
		return
	}
	if g := readGitignore(dir); g != nil {
		w.gitignores[dir] = g
	}
}

// gitignored reports whether path is ignored, walking its .gitignore chain
// deepest first: the closest file with a matching pattern decides.
func (w *Watcher) gitignored(path string, isDir bool) bool {
	clean := filepath.Clean(path)
	if _, ok := w.pinned[clean]; ok {
		return false
	}

	for dir := filepath.Dir(clean); ; {
		if g := w.gitignores[dir]; g != nil {
			if rel, err := filepath.Rel(dir, clean); err == nil {
				if ignored, matched := g.match(rel, isDir); matched {
					return ignored
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// ancestorIgnored reports whether a .gitignore above a pinned root ignores it.
// Only files up to the enclosing git repository count, as git scopes them.
func ancestorIgnored(root string, isDir bool) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}

	start := filepath.Dir(absRoot)
	stop := gitRoot(start)
	dirs := []string{}
	for dir := start; ; {
		dirs = append(dirs, dir)
		if stop != "" && dir == stop {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	for _, dir := range dirs {
		g := readGitignore(dir)
		if g == nil {
			continue
		}
		rel, err := filepath.Rel(dir, absRoot)
		if err != nil {
			continue
		}
		if ignored, matched := g.match(rel, isDir); matched {
			return ignored
		}
	}
	return false
}

// gitRoot returns the closest ancestor of dir that holds a .git, or "".
func gitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// eventIsDir lets directory-only .gitignore patterns match a created dir.
func eventIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resetTimer restarts t for another d. A value a fired timer already parked
// in t.C must be drained first: Reset alone would deliver it at once and split
// the batch the event just extended.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// closed reports whether Close has been called.
func (w *Watcher) closed() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// Close stops the watcher. It is safe to call more than once.
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.watcher.Close()
	})
	return err
}

func (c *ChangeSet) String() string {
	files := make([]string, 0, len(c.Files))
	for file := range c.Files {
		files = append(files, file)
	}
	sort.Strings(files)
	return strings.Join(files, "\n")
}

// expandGlobPaths expands a watch entry containing glob metachars into existing paths.
func expandGlobPaths(pattern string) []string {
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{pattern}
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	if len(matches) == 0 {
		// A literal path can contain metachars (e.g. a dir named "foo[1]");
		// the glob finds nothing, but the path itself may exist.
		if _, err := os.Stat(pattern); err != nil {
			return nil
		}
		return []string{pattern}
	}

	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		if _, err := os.Stat(match); err != nil {
			continue
		}
		paths = append(paths, match)
	}

	return paths
}

// matchGlobPattern reports whether path matches pattern. The pattern is
// relative, so it matches any suffix of the path: `dir/*.md` matches
// `dir/a.md` and `/abs/dir/a.md`, but not `dir/sub/a.md`. `**` spans segments.
func matchGlobPattern(pattern, path string) bool {
	patternSegs := strings.Split(filepath.ToSlash(pattern), "/")
	pathSegs := strings.Split(filepath.ToSlash(path), "/")

	for i := 0; i <= len(pathSegs); i++ {
		if matchSegments(patternSegs, pathSegs[i:]) {
			return true
		}
	}
	return false
}

// matchSegments matches path segments against pattern segments, with "**"
// absorbing any number of segments.
func matchSegments(patternSegs, pathSegs []string) bool {
	for len(patternSegs) > 0 {
		if patternSegs[0] == "**" {
			if matchSegments(patternSegs[1:], pathSegs) {
				return true
			}
			if len(pathSegs) == 0 {
				return false
			}
			pathSegs = pathSegs[1:]
			continue
		}
		if len(pathSegs) == 0 {
			return false
		}
		matched, err := filepath.Match(patternSegs[0], pathSegs[0])
		if err != nil || !matched {
			return false
		}
		patternSegs, pathSegs = patternSegs[1:], pathSegs[1:]
	}
	return len(pathSegs) == 0
}
