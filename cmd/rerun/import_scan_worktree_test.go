package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// gitMain makes dir a main checkout with a Makefile.
func gitMain(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)
}

// gitLinked makes dir a linked worktree of main with a Makefile.
func gitLinked(t *testing.T, dir, main, name string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", name)+"\n")
	writeFile(t, filepath.Join(dir, "Makefile"), scanMakefile)
}

func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func paths(cs []importCandidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}

func TestScanWorktreesKeepOnlyMainCheckout(t *testing.T) {
	dir := realTemp(t)
	gitMain(t, filepath.Join(dir, "repo"))
	gitLinked(t, filepath.Join(dir, "wt1"), filepath.Join(dir, "repo"), "wt1")
	gitLinked(t, filepath.Join(dir, "wt2"), filepath.Join(dir, "repo"), "wt2")

	r := scanImportableFull(dir, importScanOpts{})
	if want := []string{"repo/Makefile"}; !reflect.DeepEqual(paths(r.Found), want) {
		t.Fatalf("found %v, want %v", paths(r.Found), want)
	}
	if want := []string{"2 other worktrees of repo skipped"}; !reflect.DeepEqual(r.Skips, want) {
		t.Fatalf("skips %q, want %q", r.Skips, want)
	}
}

func TestScanWorktreesPreferMainOverEarlierWorktree(t *testing.T) {
	dir := realTemp(t)
	gitLinked(t, filepath.Join(dir, "a-wt"), filepath.Join(dir, "z-main"), "a-wt")
	gitMain(t, filepath.Join(dir, "z-main"))

	r := scanImportableFull(dir, importScanOpts{})
	if want := []string{"z-main/Makefile"}; !reflect.DeepEqual(paths(r.Found), want) {
		t.Fatalf("found %v, want %v", paths(r.Found), want)
	}
	if want := []string{"1 other worktree of z-main skipped"}; !reflect.DeepEqual(r.Skips, want) {
		t.Fatalf("skips %q, want %q", r.Skips, want)
	}
}

func TestScanWorktreesWithoutMainKeepFirst(t *testing.T) {
	dir := realTemp(t)
	outside := filepath.Join(realTemp(t), "app")
	gitLinked(t, filepath.Join(dir, "wt1"), outside, "wt1")
	gitLinked(t, filepath.Join(dir, "wt2"), outside, "wt2")

	r := scanImportableFull(dir, importScanOpts{})
	if want := []string{"wt1/Makefile"}; !reflect.DeepEqual(paths(r.Found), want) {
		t.Fatalf("found %v, want %v", paths(r.Found), want)
	}
	if want := []string{"1 other worktree of app skipped"}; !reflect.DeepEqual(r.Skips, want) {
		t.Fatalf("skips %q, want %q", r.Skips, want)
	}
}

func TestScanStartedInsideLinkedWorktreeListsIt(t *testing.T) {
	dir := realTemp(t)
	main := filepath.Join(dir, "repo")
	gitMain(t, main)
	wt := filepath.Join(dir, "wt1")
	gitLinked(t, wt, main, "wt1")

	r := scanImportableFull(wt, importScanOpts{})
	if want := []string{"Makefile"}; !reflect.DeepEqual(paths(r.Found), want) || len(r.Skips) != 0 {
		t.Fatalf("found %v skips %q, want only the worktree's Makefile", paths(r.Found), r.Skips)
	}

	// A sibling worktree nested below the root is skipped, even when it sorts first.
	gitLinked(t, filepath.Join(wt, "a-nested"), main, "nested")
	writeFile(t, filepath.Join(wt, "svc", "Makefile"), scanMakefile)
	r = scanImportableFull(filepath.Join(wt, "svc"), importScanOpts{})
	if want := []string{"Makefile"}; !reflect.DeepEqual(paths(r.Found), want) {
		t.Fatalf("found %v, want %v", paths(r.Found), want)
	}
	r = scanImportableFull(wt, importScanOpts{})
	if want := []string{"Makefile", "svc/Makefile"}; !reflect.DeepEqual(paths(r.Found), want) {
		t.Fatalf("found %v, want %v", paths(r.Found), want)
	}
	if want := []string{"1 other worktree of repo skipped"}; !reflect.DeepEqual(r.Skips, want) {
		t.Fatalf("skips %q, want %q", r.Skips, want)
	}
}

func TestScanKeepsSubmodulesAndSeparateRepos(t *testing.T) {
	dir := realTemp(t)
	gitMain(t, filepath.Join(dir, "repo"))
	writeFile(t, filepath.Join(dir, "repo", "libs", "sub", ".git"), "gitdir: ../../.git/modules/sub\n")
	writeFile(t, filepath.Join(dir, "repo", "libs", "sub", "Makefile"), scanMakefile)
	gitMain(t, filepath.Join(dir, "other"))

	r := scanImportableFull(dir, importScanOpts{})
	want := []string{"other/Makefile", "repo/Makefile", "repo/libs/sub/Makefile"}
	if !reflect.DeepEqual(paths(r.Found), want) || len(r.Skips) != 0 {
		t.Fatalf("found %v skips %q, want %v and no skips", paths(r.Found), r.Skips, want)
	}
}

func TestScanWorktreeInsideMainIsSkipped(t *testing.T) {
	dir := realTemp(t)
	gitMain(t, filepath.Join(dir, "repo"))
	gitLinked(t, filepath.Join(dir, "repo", ".wt", "feat"), filepath.Join(dir, "repo"), "feat")

	r := scanImportableFull(dir, importScanOpts{})
	if want := []string{"repo/Makefile"}; !reflect.DeepEqual(paths(r.Found), want) || len(r.Skips) != 1 {
		t.Fatalf("found %v skips %q, want one Makefile and one skip row", paths(r.Found), r.Skips)
	}
}

func TestScanTimeBudgetTruncates(t *testing.T) {
	dir := realTemp(t)
	for _, d := range []string{"a", "b", "c", "d"} {
		writeFile(t, filepath.Join(dir, d, "Makefile"), scanMakefile)
	}
	var tick time.Time
	opts := importScanOpts{Budget: 2 * time.Second, now: func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}}
	r := scanImportableFull(dir, opts)
	if !r.Truncated || len(r.Found) >= 4 {
		t.Fatalf("found %v truncated=%v, want a partial list and truncated", paths(r.Found), r.Truncated)
	}

	r = scanImportableFull(dir, importScanOpts{})
	if r.Truncated || len(r.Found) != 4 {
		t.Fatalf("default budget: found %v truncated=%v, want all four", paths(r.Found), r.Truncated)
	}
}
