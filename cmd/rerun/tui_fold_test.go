package main

import (
	"reflect"
	"strings"
	"testing"
)

// toggleKey folds or unfolds the header of a group key, as a click on its arrow does.
func toggleKey(t *testing.T, m *tuiModel, key string) {
	t.Helper()
	idx := m.indexOfKey("schema:" + key)
	if idx < 0 {
		t.Fatalf("no listed header for %q", key)
	}
	m.toggleCollapse(idx)
}

func TestRootGroupNamedLikeImportListsInItsSection(t *testing.T) {
	f := newImportFixture(t,
		"groups:\n  bpn:\n    columns: [\"msg {.msg}\"]\nimports:\n  - path: ../bpn/rerun.yml\nlocal:\n  group: bpn\n  run: echo hi\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})
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

func TestCollapsedImportHidesEverythingBelow(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, importKeyPrefix+"omsx")
	if got, want := nestedRows(m), []string{"▸omsx"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestCollapsedSubHeaderHidesOnlyItsTargets(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, "omsx/backend")
	want := []string{"▾omsx", " ▸backend", " ▾webapps", "  dash", " ▾all", "  misc"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestNestedCollapseRestoresOnExpand(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, "omsx/backend")
	toggleKey(t, m, importKeyPrefix+"omsx")
	toggleKey(t, m, importKeyPrefix+"omsx")
	want := []string{"▾omsx", " ▸backend", " ▾webapps", "  dash", " ▾all", "  misc"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("after collapsing the import and expanding it: rows = %v, want %v", got, want)
	}
	toggleKey(t, m, "omsx/backend")
	if got := nestedRows(m); len(got) != 8 {
		t.Fatalf("after expanding backend: %d rows, want all 8", len(got))
	}
}

func TestCollapsedHeaderCountsAllMembers(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, importKeyPrefix+"omsx")
	if line := leftPane(m)[0]; !strings.HasPrefix(line, "▸") || !strings.Contains(line, "0/4") {
		t.Fatalf("collapsed import header = %q, want ▸ with its 0/4 count", line)
	}
}

func TestCursorMovesToTheCollapsedHeader(t *testing.T) {
	m := nestedFixture(t)
	m.applyScope(-1, "omsx/backend")
	m.cursor = 2 // api, under backend
	toggleKey(t, m, "omsx/backend")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1, the collapsed backend header", m.cursor)
	}
	if m.schemaSel != "omsx/backend" {
		t.Fatalf("schemaSel = %q, want the scope kept", m.schemaSel)
	}

	m.cursor = 7 // misc, under the import
	toggleKey(t, m, importKeyPrefix+"omsx")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0, the collapsed import header", m.cursor)
	}
}

func TestCursorFollowsItsRowWhenRowsAboveFold(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 7 // misc
	toggleKey(t, m, "omsx/webapps")
	if got := m.listRows(); len(got) != 7 || m.cursor != 6 {
		t.Fatalf("rows = %d cursor = %d, want misc still under the cursor at 6 of 7", len(got), m.cursor)
	}
}

func TestCursorClampsWhenItPointsPastTheList(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 99
	toggleKey(t, m, importKeyPrefix+"omsx")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want it clamped to the one visible row", m.cursor)
	}
}

func TestArrowClickFoldsAndNameClickSelects(t *testing.T) {
	m := nestedFixture(t)
	m.frame()

	m.handleMouse(mouse(0, 0, false)) // the import header's arrow cell
	if !m.collapsed[importKeyPrefix+"omsx"] || m.schemaSel != "" {
		t.Fatalf("arrow click: collapsed=%v scope=%q, want folded and no scope", m.collapsed, m.schemaSel)
	}

	m.collapsed = nil
	m.frame()
	m.handleMouse(mouse(3, 0, false)) // the name cell
	if m.collapsed[importKeyPrefix+"omsx"] || m.schemaSel != importKeyPrefix+"omsx" {
		t.Fatalf("name click: collapsed=%v scope=%q, want selected and open", m.collapsed, m.schemaSel)
	}
}

func TestArrowClickOnSubHeaderFoldsItsGroup(t *testing.T) {
	m := nestedFixture(t)
	m.frame()
	m.handleMouse(mouse(1, 1, false)) // the backend sub-header sits one indent in
	if !m.collapsed["omsx/backend"] {
		t.Fatalf("sub-header arrow click left backend open: %v", m.collapsed)
	}
	if got := len(m.listRows()); got != 6 {
		t.Fatalf("rows after folding backend = %d, want 6", got)
	}
}

func TestHiddenImportSectionStartsCollapsed(t *testing.T) {
	f := newImportFixture(t, "imports:\n  - path: ../bpn/rerun.yml\n\ngroups:\n  bpn:\n    hide: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	if got, want := nestedRows(m), []string{"▸bpn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestRootGroupFoldsImportWhenTargetNamesIt(t *testing.T) {
	f := newImportFixture(t,
		"groups:\n  bpn:\n    hide: true\nimports:\n  - path: ../bpn/rerun.yml\nlocal:\n  group: bpn\n  run: echo hi\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	if got, want := nestedRows(m), []string{"▸bpn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestImportedGroupHideFoldsOnItsSubKey(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n",
		map[string]string{"bpn/rerun.yml": "groups:\n  tools:\n    hide: true\ntools-api:\n  group: tools\n  run: echo api\nweb:\n  run: echo web\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	if !m.collapsed["bpn/tools"] {
		t.Fatalf("collapsed = %v, want bpn/tools folded", m.collapsed)
	}
	want := []string{"▾bpn", " ▸tools", " ▾all", "  web"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestImportedTargetHideFalseUnmutesUnderFoldedGroup(t *testing.T) {
	f := newImportFixture(t,
		"groups:\n  bpn:\n    hide: true\nimports:\n  - path: ../bpn/rerun.yml\n",
		map[string]string{"bpn/rerun.yml": "api:\n  hide: false\n  run: echo api\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	if got, want := nestedRows(m), []string{"▸bpn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if m.targets[0].muted {
		t.Fatalf("api muted, want unmuted by its own hide: false")
	}
}

func TestHeaderKey(t *testing.T) {
	cases := []struct {
		name string
		t    Target
		want string
	}{
		{"ungrouped target", Target{Name: "web"}, ""},
		{"root group", Target{Name: "web", Schema: "ops"}, "ops"},
		{"import target without a sub-group", Target{Name: "bpn/api", Import: "bpn"}, importKeyPrefix + "bpn"},
		{"import target in a sub-group", Target{Name: "bpn/api", Import: "bpn", Sub: "bpn/tools"}, "bpn/tools"},
		{"group of an import without a sub-group", Target{Name: "api", GroupImport: "bpn"}, importKeyPrefix + "bpn"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := headerKey(tt.t); got != tt.want {
				t.Fatalf("headerKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReloadFoldingTheCursorRowMovesCursorToHeader(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 1 // api
	api := target("api")
	api.Schema, api.GroupHide = "ops", true
	m.rebuildRows(&configChange{targets: []Target{target("web"), api}})
	r, ok := m.cursorRow()
	if !ok || !r.header || r.schema != "ops" {
		t.Fatalf("cursor on %+v, want the collapsed ops header", r)
	}
}

func TestImportHideFoldsSectionNotSubGroup(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    hide: true\n",
		map[string]string{"bpn/rerun.yml": "groups:\n  tools:\n    hide: false\ntools-api:\n  group: tools\n  run: echo api\nweb:\n  run: echo web\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	if !m.collapsed[importKeyPrefix+"bpn"] {
		t.Fatalf("collapsed = %v, want the bpn section folded", m.collapsed)
	}
	if got, want := nestedRows(m), []string{"▸bpn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestImportHideReloadKeepsUserUnfold(t *testing.T) {
	f := newImportFixture(t,
		"imports:\n  - path: ../bpn/rerun.yml\n    hide: true\n",
		map[string]string{"bpn/rerun.yml": "api:\n  run: echo api\n"})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)
	m.collapsed[importKeyPrefix+"bpn"] = false
	m.rebuildRows(&configChange{targets: cfg.Targets})
	if m.collapsed[importKeyPrefix+"bpn"] {
		t.Fatalf("reload re-folded the bpn section the user opened")
	}
}
