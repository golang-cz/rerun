package rerun

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func writeFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeGitignore(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpandGlobPaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a_test.go")
	b := filepath.Join(dir, "b.go")
	writeFiles(t, a, b)

	got := expandGlobPaths(filepath.Join(dir, "*_test.go"))
	if len(got) != 1 || got[0] != a {
		t.Fatalf("glob = %v, want [%s]", got, a)
	}

	got = expandGlobPaths(b)
	if len(got) != 1 || got[0] != b {
		t.Fatalf("non-glob = %v, want [%s]", got, b)
	}

	if got := expandGlobPaths(filepath.Join(dir, "*.nomatch")); len(got) != 0 {
		t.Fatalf("no match = %v, want empty", got)
	}
}

func TestExpandGlobPathsLiteralMetachars(t *testing.T) {
	dir := t.TempDir()
	literal := filepath.Join(dir, "file[1].go")
	writeFiles(t, literal)

	got := expandGlobPaths(literal)
	if len(got) != 1 || got[0] != literal {
		t.Fatalf("literal metachars = %v, want [%s]", got, literal)
	}

	if got := expandGlobPaths(filepath.Join(dir, "nope[1].go")); len(got) != 0 {
		t.Fatalf("missing literal metachars = %v, want empty", got)
	}
}

// writeUntilChange rewrites file until a ChangeSet arrives. macOS kqueue may
// report the first write after Add as a CHMOD-only event, which the watcher
// intentionally drops, so a single write is not a reliable probe there.
func writeUntilChange(t *testing.T, changes <-chan ChangeSet, file string, deadline time.Duration) ChangeSet {
	t.Helper()
	return pokeUntilChange(t, changes, file, deadline, func() { writeFiles(t, file) })
}

// renameSwap replaces target the way editors and pnpm save: write a temp file, rename it over target.
func renameSwap(t *testing.T, target string) {
	t.Helper()
	tmp := target + ".tmp"
	writeFiles(t, tmp)
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
}

func pokeUntilChange(t *testing.T, changes <-chan ChangeSet, file string, deadline time.Duration, poke func()) ChangeSet {
	t.Helper()
	stop := time.Now().Add(deadline)
	for {
		poke()

		wait := 200 * time.Millisecond
		if left := time.Until(stop); left < wait {
			wait = left
		}

		select {
		case change := <-changes:
			return change
		case <-time.After(wait):
		}

		if time.Now().After(stop) {
			t.Fatalf("no ChangeSet for %s within %v", file, deadline)
		}
	}
}

func assertNoChange(t *testing.T, changes <-chan ChangeSet, window time.Duration) {
	t.Helper()
	select {
	case change := <-changes:
		t.Fatalf("unexpected ChangeSet: %v", change.Files)
	case <-time.After(window):
	}
}

// A single-file root must keep firing after its file is replaced by rename.
func TestWatchSingleFileSurvivesRenameSwap(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "rerun.yml")
	writeFiles(t, target)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(target)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	first := pokeUntilChange(t, changes, target, 2*time.Second, func() { renameSwap(t, target) })
	if _, ok := first.Files[target]; !ok {
		t.Fatalf("first ChangeSet files = %v, want %s", first.Files, target)
	}

	second := pokeUntilChange(t, changes, target, 2*time.Second, func() { renameSwap(t, target) })
	if _, ok := second.Files[target]; !ok {
		t.Fatalf("second ChangeSet files = %v, want %s", second.Files, target)
	}
}

func TestWatchSingleFileIgnoresSiblingInSameDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "rerun.yml")
	sibling := filepath.Join(dir, "other.txt")
	writeFiles(t, target, sibling)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(target)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	writeFiles(t, sibling)
	assertNoChange(t, changes, 400*time.Millisecond)

	// Proves the watch is alive, so the silence above means the filter worked.
	change := pokeUntilChange(t, changes, target, 2*time.Second, func() { renameSwap(t, target) })
	if _, ok := change.Files[target]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, target)
	}
}

func TestWatchSingleFileWithParentRootDeliversSiblings(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "rerun.yml")
	sibling := filepath.Join(dir, "other.txt")
	writeFiles(t, target, sibling)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(target, dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	change := writeUntilChange(t, changes, sibling, 2*time.Second)
	if _, ok := change.Files[sibling]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, sibling)
	}
}

func TestWatchGlobOnlyWatchesMatches(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a_test.go")
	b := filepath.Join(dir, "b.go")
	writeFiles(t, a, b)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(filepath.Join(dir, "*_test.go"))
	w.Watch(10 * time.Millisecond)
	defer w.Close()

	list := w.watcher.WatchList()
	if len(list) != 1 || list[0] != dir {
		t.Fatalf("watch list = %v, want [%s]", list, dir)
	}
}

func TestWatchGlobDeliversChanges(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a_test.go")
	b := filepath.Join(dir, "b.go")
	writeFiles(t, a, b)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(filepath.Join(dir, "*_test.go"))
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	change := writeUntilChange(t, changes, a, 2*time.Second)
	if _, ok := change.Files[a]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, a)
	}

	// b.go is not matched by the glob, so no watch covers it.
	writeFiles(t, b)
	assertNoChange(t, changes, 300*time.Millisecond)
}

// A change set carries what happened to each file, so a front-end can tell a
// created file from a written one.
func TestWatchRecordsTheOpPerFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a_test.go")
	writeFiles(t, a)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	change := writeUntilChange(t, changes, a, 2*time.Second)
	op, ok := change.Ops[a]
	if !ok {
		t.Fatalf("Ops is missing %s: %v", a, change.Ops)
	}
	if !op.Has(fsnotify.Write) && !op.Has(fsnotify.Create) {
		t.Fatalf("op for %s = %v, want a create or a write", a, op)
	}
}

func TestMatchGlobPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"logs/*.log", "logs/x.log", true},
		{"logs/*.log", "/tmp/t/logs/x.log", true},
		{"logs/*.log", "logs/sub/x.log", false},
		{"logs/*.log", "x.log", false},
		{"logs/*.log", "otherlogs/x.log", false},
		{"dir/*.md", "dir/a.md", true},
		{"dir/*.md", "otherdir/a.md", false},
		{"dir/*.md", "a.md", false},
		{"*.md", "a.md", true},
		{"*.md", "dir/a.md", true},
		{"logs/**/*.log", "logs/x.log", true},
		{"logs/**/*.log", "logs/sub/x.log", true},
	}
	for _, tt := range tests {
		if got := matchGlobPattern(tt.pattern, tt.path); got != tt.want {
			t.Errorf("matchGlobPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

// A directory-qualified glob ignores only that directory, not its subdirs.
func TestIgnoreDirQualifiedGlob(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "logs", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(dir, "logs", "x.log")
	deep := filepath.Join(sub, "x.log")
	root := filepath.Join(dir, "x.log")
	writeFiles(t, top, deep, root)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	w.Ignore("logs/*.log")
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	writeFiles(t, top)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, deep, 2*time.Second)
	if _, ok := change.Files[deep]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, deep)
	}

	change = writeUntilChange(t, changes, root, 2*time.Second)
	if _, ok := change.Files[root]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, root)
	}
}

// The production rebuild loop: `except: ["**/bin"]` must keep bin/ out of fsnotify.
func TestIgnoreGlobSkipsDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	src := filepath.Join(dir, "src")
	for _, d := range []string{bin, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(bin, "app")
	source := filepath.Join(src, "main.go")
	writeFiles(t, binary, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	w.Ignore("**/bin")
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	for _, watched := range w.watcher.WatchList() {
		if watched == bin {
			t.Fatalf("watch list contains ignored dir %s", bin)
		}
	}

	writeFiles(t, binary)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}

// An exact-path dir ignore keeps behaving as before the glob check.
func TestIgnoreExactDirSkipsDir(t *testing.T) {
	dir := t.TempDir()
	vendor := filepath.Join(dir, "vendor")
	src := filepath.Join(dir, "src")
	for _, d := range []string{vendor, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dep := filepath.Join(vendor, "dep.go")
	source := filepath.Join(src, "main.go")
	writeFiles(t, dep, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	w.Ignore(vendor)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	for _, watched := range w.watcher.WatchList() {
		if watched == vendor {
			t.Fatalf("watch list contains ignored dir %s", vendor)
		}
	}

	writeFiles(t, dep)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}

// Skipping glob-ignored dirs must not disable the file-level glob ignore.
func TestIgnoreGlobFileStillIgnored(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(sub, "run.log")
	source := filepath.Join(sub, "run.go")
	writeFiles(t, log, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	w.Ignore("**/*.log")
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	writeFiles(t, log)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}

// After Close nothing more is delivered; the channel closes instead.
func TestCloseDuringWatchDropsChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	writeFiles(t, file)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	writeUntilChange(t, changes, file, 2*time.Second)

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case change, ok := <-changes:
			if !ok {
				return
			}
			t.Fatalf("unexpected ChangeSet after Close: %v", change.Files)
		case <-deadline:
			t.Fatal("changes channel still open after Close")
		}
	}
}

func TestCloseIdempotent(t *testing.T) {
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// A fired timer value must not survive a reset, or the batch an event just
// extended would be delivered at once instead of after another delay.
func TestResetTimerDrainsFiredValue(t *testing.T) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	time.Sleep(50 * time.Millisecond) // let it fire and park a value in t.C

	resetTimer(timer, time.Hour)
	select {
	case <-timer.C:
		t.Fatal("stale timer value survived the reset")
	default:
	}
}

func TestResetTimerRearms(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	resetTimer(timer, time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(time.Second):
		t.Fatal("timer did not fire after the reset")
	}
}

func TestWatchSecondCallReturnsNil(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	first := w.Watch(10 * time.Millisecond)
	second := w.Watch(10 * time.Millisecond)
	defer w.Close()

	if first == nil {
		t.Fatal("first Watch returned nil")
	}
	if second != nil {
		t.Fatalf("second Watch = %v, want nil", second)
	}
}

func TestWatchLiteralBracketDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub[1]")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "a.go")
	writeFiles(t, file)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(sub)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	change := writeUntilChange(t, changes, file, 2*time.Second)
	if _, ok := change.Files[file]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, file)
	}
}

// Pins the rules the walker relies on, so a library swap cannot change them
// silently.
func TestGitignorePatternSemantics(t *testing.T) {
	dir := t.TempDir()
	writeGitignore(t, dir, "# comment\n\nbuild/\n/dist\ndocs/**/*.md\n*.log\n!keep.log\n")

	g := readGitignore(dir)
	if g == nil {
		t.Fatal("readGitignore returned nil")
	}

	tests := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"build", true, true},
		{"build", false, false},
		{"dist", true, true},
		{"sub/dist", true, false},
		{"docs/a.md", false, true},
		{"docs/sub/a.md", false, true},
		{"other/docs/a.md", false, false},
		{"a.log", false, true},
		{"sub/a.log", false, true},
		{"keep.log", false, false},
		{"sub/keep.log", false, false},
		{"notes.txt", false, false},
	}
	for _, tt := range tests {
		ignored, _ := g.match(tt.rel, tt.isDir)
		if ignored != tt.want {
			t.Errorf("match(%q, dir=%v) = %v, want %v", tt.rel, tt.isDir, ignored, tt.want)
		}
	}
}

// gitignore is additive to --except: an ignored dir never reaches fsnotify.
func TestGitignoreSkipsDir(t *testing.T) {
	dir := t.TempDir()
	ignored := filepath.Join(dir, "node_modules")
	src := filepath.Join(dir, "src")
	mkdirs(t, filepath.Join(ignored, "pkg"), src)
	writeGitignore(t, dir, "node_modules/\n")

	dep := filepath.Join(ignored, "pkg", "index.js")
	source := filepath.Join(src, "main.go")
	writeFiles(t, dep, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	for _, watched := range w.watcher.WatchList() {
		if watched == ignored {
			t.Fatalf("watch list contains ignored dir %s", ignored)
		}
	}

	writeFiles(t, dep)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}

func TestGitignoreFiltersFile(t *testing.T) {
	dir := t.TempDir()
	writeGitignore(t, dir, "*.log\n")
	log := filepath.Join(dir, "run.log")
	source := filepath.Join(dir, "run.go")
	writeFiles(t, log, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	writeFiles(t, log)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}

func TestGitignoreNegationReincludes(t *testing.T) {
	dir := t.TempDir()
	writeGitignore(t, dir, "*.log\n!keep.log\n")
	kept := filepath.Join(dir, "keep.log")
	dropped := filepath.Join(dir, "other.log")
	writeFiles(t, kept, dropped)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	writeFiles(t, dropped)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, kept, 2*time.Second)
	if _, ok := change.Files[kept]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, kept)
	}
}

// A deeper .gitignore overrides a shallower one, even where the root ignores.
func TestGitignoreNestedOverridesRoot(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	mkdirs(t, sub)
	writeGitignore(t, dir, "*.log\n")
	writeGitignore(t, sub, "!keep.log\n")

	kept := filepath.Join(sub, "keep.log")
	dropped := filepath.Join(sub, "other.log")
	writeFiles(t, kept, dropped)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	writeFiles(t, dropped)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, kept, 2*time.Second)
	if _, ok := change.Files[kept]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, kept)
	}
}

// A watch root nested in a repo must still pick up the repo-root .gitignore,
// which the walk alone never visits.
func TestGitignoreLoadsAncestorRules(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "sub")
	bin := filepath.Join(sub, "bin")
	mkdirs(t, bin, filepath.Join(repo, ".git"))
	writeGitignore(t, repo, "bin/\n")
	writeGitignore(t, sub, "*.log\n")

	binary := filepath.Join(bin, "x")
	log := filepath.Join(sub, "a.log")
	source := filepath.Join(sub, "a.go")
	writeFiles(t, binary, log, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(sub)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	// repo/ is above the pinned watch root, so this only sees the ancestor rule
	// once it is loaded on demand.
	if !w.gitignored(bin, true) {
		t.Fatalf("gitignored(%s) = false, want true", bin)
	}
	for _, watched := range w.watcher.WatchList() {
		if watched == bin {
			t.Fatalf("watch list contains ignored dir %s", bin)
		}
	}

	writeFiles(t, binary)
	assertNoChange(t, changes, 400*time.Millisecond)

	if !w.gitignored(log, false) {
		t.Fatalf("gitignored(%s) = false, want true", log)
	}
	writeFiles(t, log)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}

func TestNoGitignoreDisablesFiltering(t *testing.T) {
	dir := t.TempDir()
	writeGitignore(t, dir, "*.log\n")
	log := filepath.Join(dir, "run.log")
	writeFiles(t, log)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.RespectGitignore(false)
	w.Add(dir)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	change := writeUntilChange(t, changes, log, 2*time.Second)
	if _, ok := change.Files[log]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, log)
	}
}

// A .gitignore above the repo root is outside git's scope and must not apply.
func TestGitignoreStopsAtRepoRoot(t *testing.T) {
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	mkdirs(t, filepath.Join(repo, ".git"))
	writeGitignore(t, outer, "*.log\n")

	log := filepath.Join(repo, "a.log")
	writeFiles(t, log)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(repo)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	if w.gitignored(log, false) {
		t.Fatalf("gitignored(%s) = true, want false: outer .gitignore leaked past repo root", log)
	}
	change := writeUntilChange(t, changes, log, 2*time.Second)
	if _, ok := change.Files[log]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, log)
	}
}

// An explicit pin outranks .gitignore, with one warning that it did.
func TestGitignorePinnedRootWarnsAndWatches(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, filepath.Join(dir, ".git"))
	writeGitignore(t, dir, "dist/\n")
	dist := filepath.Join(dir, "dist")
	mkdirs(t, dist)
	file := filepath.Join(dist, "app.js")
	writeFiles(t, file)

	var stderr bytes.Buffer
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.stderr = &stderr

	w.Add(dist)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	if got := stderr.String(); !strings.Contains(got, dist+" is .gitignored") {
		t.Fatalf("stderr = %q, want a warning about %s", got, dist)
	}
	if got := strings.Count(stderr.String(), "is .gitignored"); got != 1 {
		t.Fatalf("stderr = %q, want exactly one warning, got %d", stderr.String(), got)
	}

	change := writeUntilChange(t, changes, file, 2*time.Second)
	if _, ok := change.Files[file]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, file)
	}
}

// Ancestor .gitignore rules cascade down into a pinned root's subtree, next to
// the root's own .gitignore.
func TestGitignoreAncestorRulesApplyBelowPinnedRoot(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, filepath.Join(dir, ".git"))
	writeGitignore(t, dir, "*.log\n")

	dist := filepath.Join(dir, "dist")
	mkdirs(t, dist)
	writeGitignore(t, dist, "*.tmp\n")

	log := filepath.Join(dist, "a.log")
	tmp := filepath.Join(dist, "b.tmp")
	source := filepath.Join(dist, "c.go")
	writeFiles(t, log, tmp, source)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(dist)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	if !w.gitignored(log, false) {
		t.Fatalf("gitignored(%s) = false, want true", log)
	}
	writeFiles(t, log)
	assertNoChange(t, changes, 400*time.Millisecond)

	writeFiles(t, tmp)
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, source, 2*time.Second)
	if _, ok := change.Files[source]; !ok {
		t.Fatalf("ChangeSet files = %v, want %s", change.Files, source)
	}
}
