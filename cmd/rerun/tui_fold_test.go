package main

import (
	"reflect"
	"strings"
	"testing"
)

// toggleKey folds or expands the header of a group key, as a click on its arrow does.
func toggleKey(t *testing.T, m *tuiModel, key string) {
	t.Helper()
	idx := m.indexOfKey("schema:" + key)
	if idx < 0 {
		t.Fatalf("no listed header for %q", key)
	}
	m.toggleFold(idx)
}

func TestRootGroupNamedLikeImportListsInItsSection(t *testing.T) {
	f := newImportFixture(t,
		"groups:\n  bpn:\n    columns: [\"msg {.msg}\"]\nimports:\n  - path: ../bpn/rerun.yml\ntargets:\n  local:\n    group: bpn\n    run: echo hi\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)

	want := []string{"▾bpn", " local", " api"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for _, r := range m.allRows() {
		if r.header && r.label == "bpn" && r.schema != importKeyPrefix+"bpn" {
			t.Fatalf("a second bpn header %q, want one header for the import", r.schema)
		}
	}
	for _, tg := range m.targets {
		if len(tg.spec.Columns) != 1 {
			t.Fatalf("%s columns = %v, want the root bpn group's column", tg.spec.Name, tg.spec.Columns)
		}
	}
}

func TestEveryHeaderHasMembers(t *testing.T) {
	m := nestedFixture(t)
	for _, r := range m.allRows() {
		if r.header && len(r.members) == 0 {
			t.Fatalf("header %q has no members", r.schema)
		}
	}
}

// idle marks every target never run, so a folded block folds all of them.
func idle(m *tuiModel) {
	m.now = spawnClock
	for _, tg := range m.targets {
		tg.state, tg.runStarted, tg.lastStart = stateManual, false, spawnClock
	}
}

func TestFoldedImportFoldsEverySubGroup(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, importKeyPrefix+"omsx")
	want := []string{"▸omsx", " (+) 4 more"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFoldedSubHeaderFoldsOnlyItsTargets(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, "omsx/backend")
	want := []string{"▾omsx", " ▾webapps", "  dash", " ▾all", "  misc", " (+) 2 more"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestNestedFoldRestoresOnExpand(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, "omsx/backend")
	toggleKey(t, m, importKeyPrefix+"omsx") // some folded: the import expands every block
	if got := nestedRows(m); len(got) != 8 {
		t.Fatalf("after expanding the import: %d rows, want all 8: %v", len(got), got)
	}
	toggleKey(t, m, importKeyPrefix+"omsx")
	if got := nestedRows(m); len(got) != 2 {
		t.Fatalf("after folding the import: %d rows, want 2: %v", len(got), got)
	}
}

func TestFoldedHeaderCountsAllMembers(t *testing.T) {
	m := nestedFixture(t)
	m.showTimes = true
	toggleKey(t, m, importKeyPrefix+"omsx")
	if line := leftPane(m)[1]; !strings.HasPrefix(line, "▸") || !strings.Contains(line, "0/4") {
		t.Fatalf("folded import header = %q, want ▸ with its 0/4 count", line)
	}
}

func TestCursorMovesToTheFoldedHeader(t *testing.T) {
	m := nestedFixture(t)
	m.applyScope(-1, "omsx/backend")
	m.cursor = 2 // api, under backend
	toggleKey(t, m, "omsx/backend")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0, the import header that outlives the emptied backend header", m.cursor)
	}
	if m.schemaSel != "omsx/backend" {
		t.Fatalf("schemaSel = %q, want the scope kept", m.schemaSel)
	}
}

func TestCursorFollowsItsRowWhenRowsAboveFold(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 7 // misc
	toggleKey(t, m, "omsx/webapps")
	if got := m.listRows(); len(got) != 8 || m.cursor != 7 {
		t.Fatalf("rows = %d cursor = %d, want misc still under the cursor at 7 of 8", len(got), m.cursor)
	}
}

func TestCursorClampsWhenItPointsPastTheList(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 99
	toggleKey(t, m, importKeyPrefix+"omsx")
	if m.cursor < 0 || m.cursor >= m.listLen() {
		t.Fatalf("cursor = %d, want it inside %d rows", m.cursor, m.listLen())
	}
}

func TestArrowClickFoldsAndNameClickSelects(t *testing.T) {
	m := nestedFixture(t)
	m.frame()

	m.handleMouse(mouse(0, 1, false)) // the import header's arrow cell
	if !m.foldState["omsx/backend"] || m.schemaSel != "" {
		t.Fatalf("arrow click: fold=%v scope=%q, want folded and no scope", m.foldState, m.schemaSel)
	}

	m.foldState = nil
	m.frame()
	m.handleMouse(mouse(3, 1, false)) // the name cell
	if m.foldState["omsx/backend"] || m.schemaSel != importKeyPrefix+"omsx" {
		t.Fatalf("name click: fold=%v scope=%q, want selected and open", m.foldState, m.schemaSel)
	}
}

func TestArrowClickOnSubHeaderFoldsItsGroup(t *testing.T) {
	m := nestedFixture(t)
	m.frame()
	m.handleMouse(mouse(1, 2, false)) // the backend sub-header sits one indent in
	if !m.foldState["omsx/backend"] {
		t.Fatalf("sub-header arrow click left backend open: %v", m.foldState)
	}
	if got := len(m.listRows()); got != 6 {
		t.Fatalf("rows after folding backend = %d, want 6", got)
	}
}

func TestFoldedImportSectionStartsFolded(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n    fold: true\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n  web:\n    run: echo web\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	idle(m)
	if got, want := nestedRows(m), []string{"▸bpn", " (+) 2 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestRootFoldGroupNamedLikeImportFoldsTheSection(t *testing.T) {
	f := newImportFixture(t,
		"groups:\n  bpn:\n    fold: true\nimports:\n  - path: ../bpn/rerun.yml\ntargets:\n  local:\n    group: bpn\n    run: echo hi\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	idle(m)
	if got, want := nestedRows(m), []string{"▸bpn", " (+) 2 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestImportedGroupFoldFoldsOnItsSubKey(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n",
		map[string]string{"bpn/rerun.yml": "groups:\n  tools:\n    fold: true\ntargets:\n  tools-api:\n    group: tools\n    run: echo api\n  tools-db:\n    group: tools\n    run: echo db\n  web:\n    run: echo web\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	idle(m)
	want := []string{"▾bpn", " ▾all", "  web", " (+) 2 more"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReloadFoldingTheCursorRowMovesCursorToHeader(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 1 // api
	api, web := target("api"), target("web")
	api.Schema, api.AutoFold, api.Start = "ops", true, startManual
	web.Schema, web.AutoFold, web.Start = "ops", true, startManual
	m.now = spawnClock
	m.targets[0].state, m.targets[1].state = stateManual, stateManual
	m.rebuildRows(&configChange{targets: []Target{web, api}})
	r, ok := m.cursorRow()
	if !ok || !r.header || r.schema != "ops" {
		t.Fatalf("cursor on %+v, want the folded ops header", r)
	}
}

func TestImportFoldReloadKeepsUserExpansion(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    fold: true\n",
		map[string]string{"bpn/rerun.yml": "targets:\n  api:\n    run: echo api\n  web:\n    run: echo web\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	idle(m)
	m.cursor = rowAtKey(t, m, "more:"+importKeyPrefix+"bpn")
	r, _ := m.listRowAt(m.cursor)
	m.expandMore(r)
	m.rebuildRows(&configChange{targets: cfg.Targets})
	if got := len(listedNames(m)); got != 2 {
		t.Fatalf("reload re-folded the bpn section the user expanded; rows = %v", nestedRows(m))
	}
}
