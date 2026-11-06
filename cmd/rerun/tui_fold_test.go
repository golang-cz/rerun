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

	want := []string{"▾bpn", "  local", "  api"}
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
	want := []string{"▾omsx", "  ▸backend", "  ▾webapps", "    dash", "  ▾all", "    misc"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestNestedCollapseRestoresOnExpand(t *testing.T) {
	m := nestedFixture(t)
	toggleKey(t, m, "omsx/backend")
	toggleKey(t, m, importKeyPrefix+"omsx")
	toggleKey(t, m, importKeyPrefix+"omsx")
	want := []string{"▾omsx", "  ▸backend", "  ▾webapps", "    dash", "  ▾all", "    misc"}
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
	m.View()

	m.handleMouse(mouse(0, 0, false)) // the import header's arrow cell
	if !m.collapsed[importKeyPrefix+"omsx"] || m.schemaSel != "" {
		t.Fatalf("arrow click: collapsed=%v scope=%q, want folded and no scope", m.collapsed, m.schemaSel)
	}

	m.collapsed = nil
	m.View()
	m.handleMouse(mouse(3, 0, false)) // the name cell
	if m.collapsed[importKeyPrefix+"omsx"] || m.schemaSel != importKeyPrefix+"omsx" {
		t.Fatalf("name click: collapsed=%v scope=%q, want selected and open", m.collapsed, m.schemaSel)
	}
}

func TestArrowClickOnSubHeaderFoldsItsGroup(t *testing.T) {
	m := nestedFixture(t)
	m.View()
	m.handleMouse(mouse(2, 1, false)) // the backend sub-header sits one indent in
	if !m.collapsed["omsx/backend"] {
		t.Fatalf("sub-header arrow click left backend open: %v", m.collapsed)
	}
	if got := len(m.listRows()); got != 6 {
		t.Fatalf("rows after folding backend = %d, want 6", got)
	}
}
