package main

import (
	"path/filepath"
	"strings"
	"testing"
)

const collapsedGroupsYAML = "groups:\n  ops:\n    collapsed: true\n  dev:\n    hide: true\ntargets:\n  web:\n    cmd: x\n    group: ops\n  api:\n    cmd: y\n    group: dev\n"

// collapsed: true starts the group's header collapsed, as hide: true did.
func TestGroupCollapsedStartsCollapsed(t *testing.T) {
	cfg, err := writeOpenConfig(t, collapsedGroupsYAML)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !cfg.Targets[0].GroupHide || !cfg.Targets[1].GroupHide {
		t.Fatalf("GroupHide = %v, %v, want both collapsed", cfg.Targets[0].GroupHide, cfg.Targets[1].GroupHide)
	}
}

// hide: on a group still collapses it, and one notice per file names the new key.
func TestGroupHideAliasNoticesOncePerFile(t *testing.T) {
	cfg, err := writeOpenConfig(t, "groups:\n  a:\n    hide: true\n  b:\n    hide: false\ntargets:\n  web:\n    cmd: x\n    group: a\n")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !cfg.Targets[0].GroupHide {
		t.Fatal("hide: true no longer collapses the group")
	}
	var notes int
	for _, n := range cfg.Notices {
		if strings.Contains(n, "hide: is now collapsed:") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("notices = %q, want one hide: notice", cfg.Notices)
	}
}

func TestGroupHideAndCollapsedTogetherIsAnError(t *testing.T) {
	_, err := writeOpenConfig(t, "groups:\n  ops:\n    hide: true\n    collapsed: true\ntargets:\n  web:\n    cmd: x\n    group: ops\n")
	if err == nil || !strings.Contains(err.Error(), `"hide" and "collapsed"`) || !strings.Contains(err.Error(), `"ops"`) {
		t.Fatalf("err = %v, want a both-set error naming group ops", err)
	}
	_, err = writeOpenConfig(t, "groups:\n  ops:\n    collapsed: true\n    hide: true\ntargets:\n  web:\n    cmd: x\n    group: ops\n")
	if err == nil {
		t.Fatal("collapsed before hide: no error")
	}
}

func TestMigrateRenamesGroupHideToCollapsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  ops:\n    hide: true\ntargets:\n  web:\n    cmd: x\n    group: ops\n")
	status, err := migrateConfig(path)
	if err != nil {
		t.Fatalf("migrateConfig: %v", err)
	}
	if status != migrateRenamed {
		t.Fatalf("status = %v, want migrateRenamed", status)
	}
	text := readConfigText(t, path)
	if !strings.Contains(text, "collapsed: true") || strings.Contains(text, "hide: true") {
		t.Fatalf("migrated file = \n%s\nwant collapsed: true and no group hide:", text)
	}
	after, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig after migrate: %v", err)
	}
	if !after.Targets[0].GroupHide || len(after.Notices) != 0 {
		t.Fatalf("after migrate GroupHide=%v notices=%q", after.Targets[0].GroupHide, after.Notices)
	}
}

func TestMigrateGroupBothSetIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "groups:\n  ops:\n    hide: true\n    collapsed: true\ntargets:\n  web:\n    cmd: x\n    group: ops\n")
	if _, err := migrateConfig(path); err == nil || !strings.Contains(err.Error(), `"ops"`) {
		t.Fatalf("err = %v, want the both-set group named", err)
	}
}

// The auto-hide block row names what it does: (+) N more expands, (−) collapse folds back.
func TestMoreRowLabels(t *testing.T) {
	m := newTestModel(t, target("web"))
	if got := m.moreRow("ops", 1, 2).label; got != "(+) 2 more" {
		t.Fatalf("collapsed label = %q, want (+) 2 more", got)
	}
	m.revealed = map[string]bool{"ops": true}
	if got := m.moreRow("ops", 1, 2).label; got != "(−) collapse" {
		t.Fatalf("revealed label = %q, want (−) collapse", got)
	}
}
