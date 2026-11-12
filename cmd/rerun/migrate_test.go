package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// migrateInPlace plans and writes a migration the way runMigrate does, without printing.
func migrateInPlace(root string) ([]migration, error) {
	plans, err := planMigration(root)
	if err != nil {
		return nil, err
	}
	return plans, applyMigration(plans)
}

// -migrate renames each target's open: to link: in place, keeping comments and quoting.
func TestMigrateRenamesOpenKeysInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "# top comment\ntargets:\n  web:\n    run: make dev   # keep this\n    open: http://localhost:8080  # the dashboard\n  api:\n    run: go run .\n    'open': make dev\n")

	plans, err := migrateInPlace(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(plans) != 1 || !slices.Equal(plans[0].targets, []string{"web", "api"}) {
		t.Fatalf("plans = %+v, want one file with web and api", plans)
	}
	got, _ := os.ReadFile(path)
	want := "# top comment\ntargets:\n  web:\n    run: make dev   # keep this\n    link: http://localhost:8080  # the dashboard\n  api:\n    run: go run .\n    'link': make dev\n"
	if string(got) != want {
		t.Fatalf("migrated file:\n%s\nwant:\n%s", got, want)
	}

	again, err := migrateInPlace(path)
	if err != nil || len(again) != 0 {
		t.Fatalf("second migrate = %v, %v; want a no-op", again, err)
	}
	if after, _ := os.ReadFile(path); string(after) != want {
		t.Fatal("second migrate changed the file")
	}
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("migrated file does not parse: %v", err)
	}
	if cfg.Targets[0].Link != "http://localhost:8080" || cfg.Targets[1].Link != "make dev" {
		t.Errorf("links = %q, %q", cfg.Targets[0].Link, cfg.Targets[1].Link)
	}
}

// Imported rerun files are migrated too, once each, while Makefiles and compose files are left alone.
func TestMigrateRewritesImportedRerunFilesOnce(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub.yml")
	writeFile(t, sub, "targets:\n  api:\n    run: y\n    open: https://api.test\n")
	writeFile(t, filepath.Join(dir, "Makefile"), "dev:\n\techo dev\n")
	compose := "services:\n  db:\n    image: postgres\n"
	writeFile(t, filepath.Join(dir, "compose.yml"), compose)
	root := filepath.Join(dir, ConfigFileName)
	writeFile(t, root, "imports:\n  - path: sub.yml\n  - path: ./sub.yml\n  - path: Makefile\n    targets: [dev]\n  - path: compose.yml\n    services: [db]\n  - path: missing.yml\n    optional: true\ntargets:\n  web:\n    run: x\n    open: http://localhost:8080\n")

	plans, err := migrateInPlace(root)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %d files, want the root and sub.yml once each", len(plans))
	}
	if got, _ := os.ReadFile(sub); !strings.Contains(string(got), "    link: https://api.test\n") {
		t.Fatalf("sub.yml = %q, want its open: renamed", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "compose.yml")); string(got) != compose {
		t.Fatalf("compose.yml changed to %q", got)
	}
}

// A bad open: anywhere means nothing is written, not even the valid files beside it.
func TestMigrateWritesNothingOnBadOpen(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"both keys", "targets:\n  web:\n    run: x\n    open: https://a.test\n    link: https://b.test\n", "has both open: and link:"},
		{"bool value", "targets:\n  web:\n    run: x\n    open: true\n", "open: must be a string"},
		{"empty value", "targets:\n  web:\n    run: x\n    open:\n", "open: must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			sub := filepath.Join(dir, "sub.yml")
			subBody := "targets:\n  api:\n    run: y\n    open: https://api.test\n"
			writeFile(t, sub, subBody)
			root := filepath.Join(dir, ConfigFileName)
			rootBody := "imports:\n  - path: sub.yml\n" + tt.body
			writeFile(t, root, rootBody)

			_, err := migrateInPlace(root)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantSub)
			}
			if got, _ := os.ReadFile(root); string(got) != rootBody {
				t.Errorf("root changed to %q", got)
			}
			if got, _ := os.ReadFile(sub); string(got) != subBody {
				t.Errorf("sub.yml changed to %q", got)
			}
		})
	}
}

// Columns count runes, so a multibyte rune before the key must not shift the rewrite.
func TestMigrateKeyColumnCountsRunes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  web: {d: \"open\", name: \"é\", open: \"https://x.test/é\"}\n  api:\n    run: \"é\"\n    d: \"open\"\n    open: https://api.test\n")

	if _, err := migrateInPlace(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "targets:\n  web: {d: \"open\", name: \"é\", link: \"https://x.test/é\"}\n  api:\n    run: \"é\"\n    d: \"open\"\n    link: https://api.test\n"
	if string(got) != want {
		t.Fatalf("migrated file:\n%s\nwant:\n%s", got, want)
	}
}

// A value that reads open is not a key, so renameKeyText refuses to touch it.
func TestRenameKeyTextRejectsAValue(t *testing.T) {
	lines := []string{"    d: \"open\"\n"}
	value := &yaml.Node{Kind: yaml.ScalarNode, Value: "open", Line: 1, Column: 8}
	if err := renameKeyText(lines, value); err == nil {
		t.Fatal("renameKeyText on a value = nil, want an error")
	}
	if lines[0] != "    d: \"open\"\n" {
		t.Fatalf("line changed to %q", lines[0])
	}
}
