package main

import (
	"os"
	"path/filepath"
	"testing"
)

const unlistedMakefileNotice = "Makefile not imported · add it under imports:"

func TestUnlistedMakefileIsNotImportedBesideRerunYML(t *testing.T) {
	f := newImportFixture(t, "targets:\n  local:\n    run: echo\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Find("make/build"); ok {
		t.Error("make/build loaded without an imports: entry")
	}
	if !containsNotice(cfg, unlistedMakefileNotice) {
		t.Errorf("notices = %q, want %q", cfg.Notices, unlistedMakefileNotice)
	}
}

func TestListedMakefileLoadsBesideRerunYML(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: Makefile\n\ntargets:\n  local:\n    run: echo\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Find("make/build"); !ok {
		t.Errorf("make/build missing; have %v", targetNames(cfg))
	}
	if containsNotice(cfg, "not imported") {
		t.Errorf("notices = %q, want none", cfg.Notices)
	}
}

// An optional import keeps rerun.yml loading after its Makefile is removed.
func TestOptionalMakefileImportSurvivesRemoval(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: app/Makefile\n    optional: true\n\ntargets:\n  local:\n    run: echo\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	if _, err := ParseConfig(f.root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dir, "app", "Makefile")); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(f.root); err != nil {
		t.Fatalf("ParseConfig after the Makefile is removed: %v", err)
	}
}
