package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example from plan.md, the generated imports: block.
const planImportsYAML = `imports:
  - path: Makefile
  - path: docker-compose.yml
  - path: api/Makefile
    group: api
  - path: web/rerun.yml
    group: web
`

func planCandidates() []importCandidate {
	return []importCandidate{
		{Path: "Makefile", Kind: makeKind},
		{Path: "docker-compose.yml", Kind: composeKind},
		{Path: "api/Makefile", Group: "api", Kind: makeKind},
		{Path: "web/rerun.yml", Group: "web", Kind: rerunKind},
	}
}

func renderImports(t *testing.T, checked []importCandidate) string {
	t.Helper()
	lines, err := renderKey(importsKey, importsNode(checked), 2, 0)
	if err != nil {
		t.Fatalf("renderKey: %v", err)
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestImportsNodeMatchesPlanExample(t *testing.T) {
	if got := renderImports(t, planCandidates()); got != planImportsYAML {
		t.Fatalf("generated YAML:\n%s\nwant:\n%s", got, planImportsYAML)
	}
}

func TestSaveImportsCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := saveImports(path, planCandidates()); err != nil {
		t.Fatalf("saveImports: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != planImportsYAML {
		t.Fatalf("file:\n%s\nwant:\n%s", data, planImportsYAML)
	}
}

func TestSaveImportsKeepsCommentsAndOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	before := `# team config
groups:
  backend:
    columns: ["level {.level}"]

targets:
  local:
    run: echo hi  # keep me

imports:   # old list
  - path: old/Makefile

# trailing note
`
	writeFile(t, path, before)
	if err := saveImports(path, planCandidates()[:1]); err != nil {
		t.Fatalf("saveImports: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := `# team config
groups:
  backend:
    columns: ["level {.level}"]

targets:
  local:
    run: echo hi  # keep me

imports: # old list
  - path: Makefile

# trailing note
`
	if string(got) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveImportsAppendsWhenKeyMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "# only a comment\ntargets:\n  local:\n    run: echo\n")
	if err := saveImports(path, planCandidates()[:2]); err != nil {
		t.Fatalf("saveImports: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "# only a comment\ntargets:\n  local:\n    run: echo\nimports:\n  - path: Makefile\n  - path: docker-compose.yml\n"
	if string(got) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveImportsEmptyChecksetWritesEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "imports:\n  - path: Makefile\ntargets:\n  x:\n    run: y\n")
	if err := saveImports(path, nil); err != nil {
		t.Fatalf("saveImports: %v", err)
	}
	got, _ := os.ReadFile(path)
	if want := "imports: []\ntargets:\n  x:\n    run: y\n"; string(got) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveImportsRefusesAliasedImports(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	original := "imports: &imp\n  - path: Makefile\nother: *imp\n"
	writeFile(t, path, original)
	if err := saveImports(path, planCandidates()[:1]); err == nil {
		t.Fatal("saveImports succeeded on an anchored imports key, want an error")
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Fatalf("file changed on refusal:\n%s", got)
	}
}

func TestSessionConfigLoadsOnlyCheckedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Makefile"), "build:\n\tgo build\n")
	writeFile(t, filepath.Join(dir, "api", "Makefile"), "serve:\n\tgo run\n")

	cfg, err := sessionConfig(filepath.Join(dir, ConfigFileName), []importCandidate{
		{Path: "api/Makefile", Abs: filepath.Join(dir, "api", "Makefile"), Group: "api", Kind: makeKind},
	}, nil)
	if err != nil {
		t.Fatalf("sessionConfig: %v", err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "api/serve" {
		t.Fatalf("targets = %v, want only api/serve (the unchecked root Makefile must not load)", cfg.Names())
	}
	if len(cfg.Notices) != 0 {
		t.Fatalf("notices = %v, want none", cfg.Notices)
	}
}

func TestSessionConfigBesideRerunYMLAddsNoImplicitFiles(t *testing.T) {
	dir := t.TempDir()
	rootFile := filepath.Join(dir, ConfigFileName)
	writeFile(t, rootFile, "targets:\n  local:\n    run: echo\n")
	writeFile(t, filepath.Join(dir, "Makefile"), "build:\n\tgo build\n")

	cfg, err := sessionConfig(rootFile, nil, nil)
	if err != nil {
		t.Fatalf("sessionConfig: %v", err)
	}
	if len(cfg.Targets) != 0 || len(cfg.Notices) != 0 {
		t.Fatalf("targets = %v notices = %v, want none", cfg.Names(), cfg.Notices)
	}
}
