package main

import (
	"os"
	"strings"
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

func TestMigrateAddsImportsForAutoImportedMakefile(t *testing.T) {
	f := newImportFixture(t, "targets:\n  local:\n    run: echo\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	added, err := migrateImports(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "Makefile" {
		t.Fatalf("added = %q, want [Makefile]", added)
	}
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Find("make/build"); !ok {
		t.Errorf("make/build missing after migrate; have %v", targetNames(cfg))
	}
	if again, err := migrateImports(f.root); err != nil || len(again) != 0 {
		t.Errorf("second migrate added %q, err %v; want nothing", again, err)
	}
}

func TestMigrateSkipsMakefileAlreadyListedOrOwnedByGroup(t *testing.T) {
	tests := map[string]string{
		"listed":      "imports:\n  - path: Makefile\n\ntargets:\n  local:\n    run: echo\n",
		"group owned": "groups:\n  make:\n\ntargets:\n  local:\n    run: echo\n",
	}
	for name, root := range tests {
		t.Run(name, func(t *testing.T) {
			f := newImportFixture(t, root, map[string]string{"app/Makefile": "build:\n\tgo build\n"})
			added, err := migrateImports(f.root)
			if err != nil || len(added) != 0 {
				t.Errorf("added = %q, err = %v; want nothing", added, err)
			}
		})
	}
}

func TestMigrateAppendsToNullImportsKey(t *testing.T) {
	f := newImportFixture(t, "imports:\n\ntargets:\n  local:\n    run: echo\n", map[string]string{
		"app/Makefile": "build:\n\tgo build\n",
	})
	if _, err := migrateImports(f.root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "path: Makefile") {
		t.Errorf("rerun.yml after migrate:\n%s", data)
	}
}
