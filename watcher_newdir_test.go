package rerun

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func watchTree(t *testing.T, root string, ignore ...string) (*Watcher, <-chan ChangeSet) {
	t.Helper()
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(root)
	w.Ignore(ignore...)
	changes := w.Watch(30 * time.Millisecond)
	t.Cleanup(func() { w.Close() })
	return w, changes
}

func waitForFile(t *testing.T, changes <-chan ChangeSet, file string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-changes:
			if _, ok := c.Files[file]; ok {
				return
			}
		case <-deadline:
			t.Fatalf("no change for %s", file)
		}
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Join(root, ".git"), filepath.Join(root, "src"))
	writeGitignore(t, root, "bin/\nnode_modules/\n")
	return root
}

// The file lands before the watch exists, so the dir walk must report it.
func TestNewDirIsWatchedAndItsFilesReported(t *testing.T) {
	root := repoRootDir(t)
	src := filepath.Join(root, "src")
	_, changes := watchTree(t, src)

	fresh := filepath.Join(src, "pkg", "deep")
	mkdirs(t, fresh)
	first := filepath.Join(fresh, "a.go")
	writeFiles(t, first)
	waitForFile(t, changes, first)

	second := filepath.Join(fresh, "b.go")
	change := writeUntilChange(t, changes, second, 2*time.Second)
	if _, ok := change.Files[second]; !ok {
		t.Fatalf("a file written in the new dir was missed: %v", change.Files)
	}
}

func TestNewIgnoredDirStaysQuiet(t *testing.T) {
	root := repoRootDir(t)
	src := filepath.Join(root, "src")
	w, changes := watchTree(t, src)
	before := len(w.watcher.WatchList())

	for _, name := range []string{"bin", "node_modules"} {
		dir := filepath.Join(src, name, "sub")
		mkdirs(t, dir)
		writeFiles(t, filepath.Join(dir, "x"))
	}
	assertNoChange(t, changes, 400*time.Millisecond)
	if after := len(w.watcher.WatchList()); after != before {
		t.Fatalf("watches grew from %d to %d for ignored dirs", before, after)
	}

	// Removing them is just as quiet.
	if err := os.RemoveAll(filepath.Join(src, "bin")); err != nil {
		t.Fatal(err)
	}
	assertNoChange(t, changes, 400*time.Millisecond)
}

func TestNewDirUnderExceptIsSkipped(t *testing.T) {
	root := repoRootDir(t)
	src := filepath.Join(root, "src")
	skipped := filepath.Join(src, "tests")
	_, changes := watchTree(t, src, "**/tests", "**/*.md")

	mkdirs(t, skipped)
	writeFiles(t, filepath.Join(skipped, "a_test.go"), filepath.Join(src, "notes.md"))
	assertNoChange(t, changes, 400*time.Millisecond)
}

func TestRemovedDirCanBeRecreated(t *testing.T) {
	root := repoRootDir(t)
	src := filepath.Join(root, "src")
	_, changes := watchTree(t, src)

	dir := filepath.Join(src, "gen")
	for i := 0; i < 3; i++ {
		mkdirs(t, dir)
		file := filepath.Join(dir, "a.go")
		writeFiles(t, file)
		waitForFile(t, changes, file)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, changes, dir)
	}
}

// Creating and deleting dirs over and over must not grow the watch set.
func TestDirChurnLeavesNoWatches(t *testing.T) {
	root := repoRootDir(t)
	src := filepath.Join(root, "src")
	w, changes := watchTree(t, src)
	go func() {
		for range changes {
		}
	}()
	before := len(w.watcher.WatchList())

	for i := 0; i < 40; i++ {
		dir := filepath.Join(src, "tmp", "a", "b")
		mkdirs(t, dir)
		writeFiles(t, filepath.Join(dir, "x.go"))
		if err := os.RemoveAll(filepath.Join(src, "tmp")); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if after := len(w.watcher.WatchList()); after > before {
		t.Fatalf("watch list grew from %d to %d", before, after)
	}
}

// A tree moved in with more files than the cap reports itself, not every file.
func TestHugeNewDirReportsItselfOnce(t *testing.T) {
	root := repoRootDir(t)
	src := filepath.Join(root, "src")
	_, changes := watchTree(t, src)

	staged := filepath.Join(root, "staged")
	mkdirs(t, staged)
	for i := 0; i < newDirFileCap+100; i++ {
		writeFiles(t, filepath.Join(staged, fmt.Sprintf("f%d.go", i)))
	}
	dest := filepath.Join(src, "vendored")
	if err := os.Rename(staged, dest); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-changes:
		if _, ok := c.Files[dest]; !ok {
			t.Fatalf("the dir itself was not reported")
		}
		if len(c.Files) > newDirFileCap+1 {
			t.Fatalf("reported %d files, want at most %d", len(c.Files), newDirFileCap+1)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no change for the moved-in tree")
	}
}

// Editing any rule source takes effect without a restart.
func TestRuleFileEditsInvalidateTheCache(t *testing.T) {
	repo := parityRepo(t)
	api := filepath.Join(repo, "apps", "api")
	_, changes := watchTree(t, api)
	home := os.Getenv("HOME")

	cases := []struct{ name, file, ext string }{
		{"root .gitignore", filepath.Join(repo, ".gitignore"), "*.one"},
		{"info/exclude", filepath.Join(repo, ".git", "info", "exclude"), "*.two"},
		{"global excludes", filepath.Join(home, ".config", "git", "ignore"), "*.three"},
	}
	main := filepath.Join(api, "main.go")
	writeUntilChange(t, changes, main, 2*time.Second)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generated := filepath.Join(api, "x"+tc.ext[1:])
			writeUntilChange(t, changes, generated, 2*time.Second)

			old, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tc.file, append(old, []byte(tc.ext+"\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			// The poll runs at most once a second.
			time.Sleep(1100 * time.Millisecond)

			deadline := time.Now().Add(3 * time.Second)
			for {
				writeFiles(t, generated)
				select {
				case c := <-changes:
					if _, ok := c.Files[generated]; ok && time.Now().Before(deadline) {
						continue
					}
				case <-time.After(300 * time.Millisecond):
				}
				break
			}
			writeFiles(t, generated)
			assertNoChange(t, changes, 400*time.Millisecond)
			writeUntilChange(t, changes, main, 2*time.Second)
		})
	}
}
