package main

import (
	"strings"
	"testing"
)

func TestAutoHideDefaults(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../tools/Makefile\n  - path: ../bpn/rerun.yml\n    auto-hide: true\n\nlocal:\n  run: echo\n", map[string]string{
		"tools/Makefile": "build:\n\tgo build\n",
		"bpn/rerun.yml":  "targets:\n  api:\n    run: echo\n  web:\n    run: echo\n",
	})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"make/tools/build": true,  // Makefile imports auto-hide by default
		"bpn/api":          true,  // the import's own auto-hide: true
		"local":            false, // a root target with no group is never auto-hidden
	}
	for name, auto := range want {
		tgt, ok := cfg.Find(name)
		if !ok {
			t.Fatalf("target %q missing; have %v", name, targetNames(cfg))
		}
		if tgt.AutoHide != auto {
			t.Errorf("%s AutoHide = %v, want %v", name, tgt.AutoHide, auto)
		}
	}
}

func TestAutoHideGroupsApplyToTheirMembers(t *testing.T) {
	f := newImportFixture(t, "groups:\n  ops:\n    auto-hide: true\n\nworker:\n  group: ops\n  run: echo\n\nplain:\n  run: echo\n", nil)
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := cfg.Find("worker"); !w.AutoHide {
		t.Error("worker AutoHide = false, want the group's auto-hide: true")
	}
	if p, _ := cfg.Find("plain"); p.AutoHide {
		t.Error("plain AutoHide = true, want false")
	}
}

func TestAutoHideRootGroupOverridesMakefileDefault(t *testing.T) {
	f := newImportFixture(t, "groups:\n  make:\n    auto-hide: false\n\nimports:\n  - path: Makefile\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := cfg.Find("make/build"); b.AutoHide {
		t.Error("make/build AutoHide = true, want the root make group's auto-hide: false")
	}
}

func TestAutoHideRejectsNonBoolean(t *testing.T) {
	f := newImportFixture(t, "groups:\n  ops:\n    auto-hide: maybe\n", nil)
	_, err := ParseConfig(f.root)
	if err == nil || !strings.Contains(err.Error(), "expected a boolean") {
		t.Fatalf("err = %v, want a boolean error", err)
	}
}
