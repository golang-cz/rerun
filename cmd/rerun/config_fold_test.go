package main

import (
	"strings"
	"testing"
)

func TestFoldDefaults(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../tools/Makefile\n  - path: ../bpn/rerun.yml\n    fold: true\n\nlocal:\n  run: echo\n", map[string]string{
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
	f := newImportFixture(t, "groups:\n  ops:\n    fold: true\n\nworker:\n  group: ops\n  run: echo\n\nplain:\n  run: echo\n", nil)
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

// Each retired spelling still loads as fold:, with one notice per file; true wins when they disagree.
func TestRetiredFoldKeysLoadWithOneNotice(t *testing.T) {
	for _, key := range []string{"collapsed", "auto-hide", "hide", "auto-fold"} {
		t.Run(key, func(t *testing.T) {
			cfg, err := writeOpenConfig(t, "groups:\n  a:\n    "+key+": true\n  b:\n    "+key+": false\ntargets:\n  web:\n    run: x\n    group: a\n  api:\n    run: y\n    group: b\n")
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if !cfg.Targets[0].AutoFold || cfg.Targets[1].AutoFold {
				t.Fatalf("AutoFold = %v, %v, want true, false", cfg.Targets[0].AutoFold, cfg.Targets[1].AutoFold)
			}
			want := key + ": is now fold:"
			var notes int
			for _, n := range cfg.Notices {
				if strings.Contains(n, want) {
					notes++
				}
			}
			if notes != 1 {
				t.Fatalf("notices = %q, want one %q", cfg.Notices, want)
			}
		})
	}
	cfg, err := writeOpenConfig(t, "groups:\n  a:\n    collapsed: false\n    auto-hide: true\ntargets:\n  web:\n    run: x\n    group: a\n")
	if err != nil || !cfg.Targets[0].AutoFold {
		t.Fatalf("collapsed: false beside auto-hide: true: err = %v, want fold on", err)
	}
}

func TestRetiredImportFoldKeysLoad(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n    auto-hide: true\n", map[string]string{"bpn/rerun.yml": "api:\n  run: echo\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Find("bpn/api"); !a.AutoFold {
		t.Error("bpn/api AutoFold = false, want the import's auto-hide: true")
	}
	if !strings.Contains(strings.Join(cfg.Notices, "\n"), "auto-hide: is now fold:") {
		t.Fatalf("notices = %q, want the auto-hide notice", cfg.Notices)
	}
}
