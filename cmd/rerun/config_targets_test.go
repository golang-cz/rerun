package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseConfigRejectsUnknownTopLevelKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n\napi:\n  run: echo hi\n")

	_, err := ParseConfig(path)
	if err == nil || !strings.Contains(err.Error(), `unknown top-level key "api"`) {
		t.Fatalf("err = %v, want an unknown top-level key error", err)
	}
}

func TestParseConfigTargetsKeyAcceptsReservedTargetNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  groups:\n    run: echo groups\n  imports:\n    run: echo imports\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Names(); !reflect.DeepEqual(got, []string{"groups", "imports"}) {
		t.Errorf("names = %v, want the reserved names as targets", got)
	}
}

func TestParseConfigImportsOnlyRootHasNoNotices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	writeFile(t, path, "imports:\n  - path: b/rerun.yml\n")
	writeFile(t, filepath.Join(dir, "b", ConfigFileName), "targets:\n  api:\n    run: echo hi\n")

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %q, want none for an imports-only root", cfg.Notices)
	}
}

func TestSetColumnsWritesTheTargetInTargetsForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: go run ./cmd/api\n")

	if err := setColumns(path, "", "api", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}
	want := "targets:\n  api:\n    run: go run ./cmd/api\n    columns: ['level {.level}']\n"
	if got := readConfigText(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetColumnsSchemaInTargetsFormKeepsTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  api:\n    run: echo hi\n")

	if err := setColumns(path, "backend", "", []string{"level {.level}"}); err != nil {
		t.Fatalf("setColumns: %v", err)
	}
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after write: %v", err)
	}
	if got := cfg.Names(); !reflect.DeepEqual(got, []string{"api"}) {
		t.Errorf("names = %v, want the target kept", got)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %q, want the groups block added, not a form change", cfg.Notices)
	}
}
