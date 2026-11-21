package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The startup table: each row of the user-flow table in the plan.

func TestImportStartupRerunYMLExistsIsUnchanged(t *testing.T) {
	dir := makeDir(t, map[string]string{
		ConfigFileName: "targets:\n  web:\n    run: sleep 30\n",
		"api/Makefile": "build:\n\tgo build\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ImportPicker || cfg.MakeOnly {
		t.Fatalf("ImportPicker = %v, MakeOnly = %v; want a plain rerun.yml load", cfg.ImportPicker, cfg.MakeOnly)
	}
}

func TestImportStartupRootOnlyFilesLoadDirectly(t *testing.T) {
	dir := makeDir(t, map[string]string{"Makefile": "build:\n\tgo build\n"})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ImportPicker {
		t.Fatal("ImportPicker = true for a root-only Makefile")
	}
	if !cfg.MakeOnly || len(cfg.Targets) != 1 {
		t.Fatalf("MakeOnly = %v, targets = %v; want the root Makefile as today", cfg.MakeOnly, targetNames(cfg))
	}
}

func TestImportStartupSubfolderOnlyOpensPickerOnFirstFrame(t *testing.T) {
	dir := makeDir(t, map[string]string{"api/Makefile": "build:\n\tgo build\n"})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.ImportPicker || !cfg.MakeOnly {
		t.Fatalf("ImportPicker = %v, MakeOnly = %v; want the picker over a MakeOnly config", cfg.ImportPicker, cfg.MakeOnly)
	}
	m := newConfigTUI(cfg.Targets, cfg)
	m.width, m.height = 100, 24
	if !m.imports.open || m.finder.open {
		t.Fatalf("imports open = %v, finder open = %v; want the picker alone", m.imports.open, m.finder.open)
	}
	if view := m.View().Content; !strings.Contains(view, "No rerun.yml in") {
		t.Fatalf("first frame has no picker:\n%s", view)
	}
}

func TestImportStartupRootAndSubfolderKeepsRootTargetsUnderPicker(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"api/Makefile": "serve:\n\tgo run .\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.ImportPicker {
		t.Fatal("ImportPicker = false with a subfolder Makefile")
	}
	if got := targetNames(cfg); len(got) != 1 || !strings.HasSuffix(got[0], "build") {
		t.Fatalf("targets = %v, want the root Makefile's build", got)
	}
}

func TestImportStartupNothingFoundIsUnchanged(t *testing.T) {
	dir := makeDir(t, map[string]string{"README.md": "hi\n"})
	_, err := LoadConfig(dir)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("LoadConfig error = %v, want config not found", err)
	}
}

func TestImportStartupNamedTargetSkipsPicker(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"Makefile":     "build:\n\tgo build\n",
		"api/Makefile": "serve:\n\tgo run .\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	applyRunArgs(cfg, Args{Target: "build"})
	if cfg.ImportPicker {
		t.Fatal("ImportPicker still set after a named target")
	}
	if !cfg.MakeOnly || len(cfg.Targets) != 1 {
		t.Fatalf("named target lost its root load: MakeOnly = %v, targets = %v", cfg.MakeOnly, targetNames(cfg))
	}
}

func TestImportStartupNoGitignoreListsGitignoredFiles(t *testing.T) {
	dir := makeDir(t, map[string]string{
		".gitignore":   "gen/\n",
		"gen/Makefile": "build:\n\tgo build\n",
		"api/Makefile": "serve:\n\tgo run .\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if n := len(cfg.Importable); n != 1 {
		t.Fatalf("gitignored folder listed: %d files", n)
	}
	cfg, err = LoadConfigOpts(dir, importScanOpts{NoGitignore: true})
	if err != nil {
		t.Fatalf("LoadConfigOpts: %v", err)
	}
	if n := len(cfg.Importable); n != 2 {
		t.Fatalf("-no-gitignore lists %d files, want 2", n)
	}
	if !filepath.IsAbs(cfg.Importable[0].Abs) {
		t.Fatalf("candidate Abs %q is not absolute", cfg.Importable[0].Abs)
	}
}

func TestImportStartupNonTTYKeepsTheConfigNotFoundExit(t *testing.T) {
	dir := makeDir(t, map[string]string{"api/Makefile": "build:\n\tgo build\n"})
	t.Chdir(dir)

	stdout, stderr, code := captureStdio(t, func() int { return runConfigMode(Args{}) })

	if code != 1 || stdout != "" {
		t.Errorf("exit = %d, stdout = %q; want exit 1 and no stdout", code, stdout)
	}
	if !strings.Contains(stderr, ErrConfigNotFound.Error()) {
		t.Errorf("stderr = %q, want the config-not-found error", stderr)
	}
}

func TestImportStartupEscKeepsThePickerWhileNothingIsLoaded(t *testing.T) {
	dir := makeDir(t, map[string]string{"api/Makefile": "build:\n\tgo build\n"})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := newConfigTUI(cfg.Targets, cfg)
	m.width, m.height = 100, 24
	press(m, "esc")
	if !m.imports.open {
		t.Fatal("esc closed the picker with nothing loaded")
	}
	if len(m.group.snapshotTargets()) != 0 {
		t.Fatal("esc on an empty startup picker loaded targets")
	}
	if view := m.View().Content; !strings.Contains(view, importNothingLoaded) {
		t.Fatalf("no nothing-loaded hint:\n%s", view)
	}
}

func TestImportStartupNoGitignoreOpensPickerForOnlyGitignoredSubfolders(t *testing.T) {
	dir := makeDir(t, map[string]string{
		".gitignore":   "gen/\n",
		"gen/Makefile": "build:\n\tgo build\n",
	})
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("gitignored-only tree loaded without -no-gitignore")
	}
	cfg, err := LoadConfigOpts(dir, importScanOpts{NoGitignore: true})
	if err != nil {
		t.Fatalf("LoadConfigOpts: %v", err)
	}
	if !cfg.ImportPicker || len(cfg.Importable) != 1 {
		t.Fatalf("ImportPicker = %v, importable = %d; want the picker over the gitignored file", cfg.ImportPicker, len(cfg.Importable))
	}
}
