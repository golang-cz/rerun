package rerun

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parityRepo builds a real git repo with every rule source git reads.
func parityRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	mkdirs(t, filepath.Join(home, ".config", "git"))
	write := func(path, content string) {
		t.Helper()
		mkdirs(t, filepath.Dir(path))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The default global file (no core.excludesFile set) and a configured one.
	write(filepath.Join(home, ".config", "git", "ignore"), ".DS_Store\n*.orig\nnode_modules/\n")
	write(filepath.Join(repo, ".git", "info", "exclude"), "scratch/\n*.swp\n")
	write(filepath.Join(repo, ".gitignore"), "bin/\n*.log\n!keep.log\n/dist\ndocs/**/*.md\n*.tmp\n!important.tmp\nout/\n!out/keep.txt\n")
	write(filepath.Join(repo, "apps", "api", ".gitignore"), "generated/\n!debug.log\n/local.conf\n")
	return repo
}

func parityPaths(repo string) (dirs, files []string) {
	for _, d := range []string{"bin", "apps/api/bin", "dist", "apps/dist", "apps/api/generated", "scratch", "node_modules", "out", "apps/web/node_modules/pkg", "src"} {
		dirs = append(dirs, d)
	}
	for _, f := range []string{
		"apps/api/bin/svc", "a.log", "keep.log", "apps/api/a.log", "apps/api/debug.log",
		"docs/x/a.md", "other/a.md", "a.tmp", "important.tmp", "apps/api/generated/x.go",
		"apps/api/local.conf", "apps/local.conf", "a.swp", "x/.DS_Store", "a.orig",
		"apps/web/node_modules/pkg/index.js", "out/keep.txt", "main.go",
	} {
		files = append(files, f)
	}
	return dirs, files
}

// The watcher must ignore exactly what git does, whichever file holds the rule.
func TestGitignoreMatchesGitCheckIgnore(t *testing.T) {
	repo := parityRepo(t)
	dirs, files := parityPaths(repo)
	for _, d := range dirs {
		mkdirs(t, filepath.Join(repo, d))
	}
	for _, f := range files {
		mkdirs(t, filepath.Dir(filepath.Join(repo, f)))
		writeFiles(t, filepath.Join(repo, f))
	}

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	check := func(rel string, isDir bool) {
		cmd := exec.Command("git", "-C", repo, "check-ignore", "-q", "--", rel)
		want := cmd.Run() == nil
		if got := w.gitignored(filepath.Join(repo, rel), isDir); got != want {
			t.Errorf("gitignored(%s, dir=%v) = %v, git check-ignore says %v", rel, isDir, got, want)
		}
	}
	for _, d := range dirs {
		check(d, true)
	}
	for _, f := range files {
		check(f, false)
	}
}

// A dir created after startup is filtered by its rule, with no restart.
func TestGitignoreFiltersDirCreatedLater(t *testing.T) {
	repo := parityRepo(t)
	api := filepath.Join(repo, "apps", "api")
	src := filepath.Join(api, "main.go")
	writeFiles(t, src)

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	w.Add(api)
	changes := w.Watch(20 * time.Millisecond)
	defer w.Close()

	for _, name := range []string{"bin", "node_modules", "generated"} {
		dir := filepath.Join(api, name)
		mkdirs(t, dir)
		writeFiles(t, filepath.Join(dir, "x"))
	}
	assertNoChange(t, changes, 400*time.Millisecond)

	change := writeUntilChange(t, changes, src, 2*time.Second)
	for f := range change.Files {
		if strings.Contains(f, "node_modules") || strings.Contains(f, "/bin") {
			t.Fatalf("ignored path delivered: %s", f)
		}
	}
}

// core.excludesFile, with its ~ spelling, replaces the default global file.
func TestGitignoreHonoursConfiguredExcludesFile(t *testing.T) {
	repo := parityRepo(t)
	home := os.Getenv("HOME")
	if err := os.WriteFile(filepath.Join(home, ".myignore"), []byte("*.secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "config", "core.excludesFile", "~/.myignore").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	writeFiles(t, filepath.Join(repo, "a.secret"), filepath.Join(repo, "a.orig"))

	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for rel, want := range map[string]bool{"a.secret": true, "a.orig": false} {
		gitWant := exec.Command("git", "-C", repo, "check-ignore", "-q", "--", rel).Run() == nil
		if gitWant != want {
			t.Fatalf("test setup: git says %s ignored=%v", rel, gitWant)
		}
		if got := w.gitignored(filepath.Join(repo, rel), false); got != want {
			t.Errorf("gitignored(%s) = %v, want %v", rel, got, want)
		}
	}
}
