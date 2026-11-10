package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// migrateText migrates a rerun.yml body and returns the rewritten file.
func migrateText(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, body)
	status, err := migrateConfig(path)
	if err != nil {
		t.Fatalf("migrateConfig: %v", err)
	}
	if status != migrateRenamed {
		t.Fatalf("status = %v, want migrateRenamed", status)
	}
	text := readConfigText(t, path)
	after, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after migrate: %v\n%s", err, text)
	}
	if len(after.Notices) != 0 {
		t.Fatalf("notices after migrate = %q, want none\n%s", after.Notices, text)
	}
	return text
}

func TestMigrateGroupFoldKeys(t *testing.T) {
	for _, key := range []string{"collapsed", "auto-hide", "hide", "auto-fold"} {
		t.Run(key, func(t *testing.T) {
			text := migrateText(t, "groups:\n  ops:\n    "+key+": true\ntargets:\n  web:\n    run: x\n    group: ops\n")
			if !strings.Contains(text, "fold: true") || strings.Contains(text, key+":") && key != "auto-fold" {
				t.Fatalf("migrated file =\n%s\nwant fold: true only", text)
			}
			if strings.Contains(text, "auto-fold") {
				t.Fatalf("migrated file =\n%s\nstill has auto-fold", text)
			}
		})
	}
}

func TestMigrateGroupFoldConflictTrueWins(t *testing.T) {
	text := migrateText(t, "groups:\n  ops:\n    collapsed: false\n    auto-hide: true\ntargets:\n  web:\n    run: x\n    group: ops\n")
	if strings.Count(text, "fold:") != 1 || !strings.Contains(text, "fold: true") {
		t.Fatalf("migrated file =\n%s\nwant one fold: true", text)
	}
	text = migrateText(t, "groups:\n  ops:\n    collapsed: false\n    auto-hide: false\ntargets:\n  web:\n    run: x\n    group: ops\n")
	if !strings.Contains(text, "fold: false") || strings.Count(text, "fold:") != 1 {
		t.Fatalf("migrated file =\n%s\nwant one fold: false", text)
	}
}

func TestMigrateImportAutoHide(t *testing.T) {
	text := migrateText(t, "imports:\n  - path: Makefile\n    optional: true\n    auto-hide: true\ntargets:\n  web:\n    run: x\n")
	if !strings.Contains(text, "fold: true") || strings.Contains(text, "auto-hide") {
		t.Fatalf("migrated file =\n%s\nwant fold: true", text)
	}
}

func TestMigrateTargetKeys(t *testing.T) {
	text := migrateText(t, "targets:\n  web:\n    cmd: x\n    hide: true\n    url: http://localhost:1\n    open-on-start: true\n  api:\n    run: y\n    open: true\n")
	for _, gone := range []string{"cmd:", "hide:", "url:", "open-on-start", "open: true"} {
		if strings.Contains(text, gone) {
			t.Fatalf("migrated file =\n%s\nstill has %s", text, gone)
		}
	}
	for _, want := range []string{"run: x", "muted: true", "open: http://localhost:1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("migrated file =\n%s\nwant %s", text, want)
		}
	}
}

func TestMigrateTargetBothRunAndCmdIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "targets:\n  web:\n    run: x\n    cmd: y\n")
	if _, err := migrateConfig(path); err == nil || !strings.Contains(err.Error(), `"web"`) {
		t.Fatalf("err = %v, want the both-set target named", err)
	}
}

// Each retired target key loads with one note per file, so the user learns the new spelling once.
func TestRetiredTargetKeysNoteOncePerFile(t *testing.T) {
	cfg, err := writeOpenConfig(t, "targets:\n  a:\n    cmd: x\n    hide: true\n    open-on-start: true\n  b:\n    cmd: y\n    hide: true\n    open: false\n")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Targets[0].Cmd != "x" || cfg.Targets[0].Muted == nil || !*cfg.Targets[0].Muted {
		t.Fatalf("target a = %+v, want run x and muted", cfg.Targets[0])
	}
	for _, want := range []string{"cmd: is now run:", "hide: is now muted:", "open-on-start: removed, press o"} {
		n := 0
		for _, note := range cfg.Notices {
			if strings.Contains(note, want) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("notices = %q, want one %q", cfg.Notices, want)
		}
	}
}

func TestMigrateImportAndGroupNotesAreOnePerKey(t *testing.T) {
	cfg, err := writeOpenConfig(t, "groups:\n  a:\n    hide: true\n  b:\n    hide: true\ntargets:\n  web:\n    run: x\n")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, note := range cfg.Notices {
		if strings.Contains(note, "hide: is now fold:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("notices = %q, want one hide: notice", cfg.Notices)
	}
}
