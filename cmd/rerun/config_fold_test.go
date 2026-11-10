package main

import (
	"strings"
	"testing"
)

func TestFoldDefaults(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../tools/Makefile\n  - path: ../bpn/rerun.yml\n    fold: true\n\ntargets:\n  local:\n    run: echo\n", map[string]string{
		"tools/Makefile": "build:\n\tgo build\n",
		"bpn/rerun.yml":  "targets:\n  api:\n    run: echo\n  web:\n    run: echo\n",
	})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"make/tools/build": true,  // Makefile imports fold by default
		"bpn/api":          true,  // the import's own fold: true
		"local":            false, // a root target with no group never folds
	}
	for name, fold := range want {
		tgt, ok := cfg.Find(name)
		if !ok {
			t.Fatalf("target %q missing; have %v", name, targetNames(cfg))
		}
		if tgt.AutoFold != fold {
			t.Errorf("%s AutoFold = %v, want %v", name, tgt.AutoFold, fold)
		}
	}
}

func TestFoldGroupsApplyToTheirMembers(t *testing.T) {
	f := newImportFixture(t, "groups:\n  ops:\n    fold: true\n\ntargets:\n  worker:\n    group: ops\n    run: echo\n\n  plain:\n    run: echo\n", nil)
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := cfg.Find("worker"); !w.AutoFold {
		t.Error("worker AutoFold = false, want the group's fold: true")
	}
	if p, _ := cfg.Find("plain"); p.AutoFold {
		t.Error("plain AutoFold = true, want false")
	}
}

func TestFoldRootGroupOverridesMakefileDefault(t *testing.T) {
	f := newImportFixture(t, "groups:\n  make:\n    fold: false\n\nimports:\n  - path: Makefile\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := cfg.Find("make/build"); b.AutoFold {
		t.Error("make/build AutoFold = true, want the root make group's fold: false")
	}
}

func TestFoldRejectsNonBoolean(t *testing.T) {
	f := newImportFixture(t, "groups:\n  ops:\n    fold: maybe\n", nil)
	_, err := ParseConfig(f.root)
	if err == nil || !strings.Contains(err.Error(), "expected a boolean") {
		t.Fatalf("err = %v, want a boolean error", err)
	}
}
