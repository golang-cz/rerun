package main

import (
	"reflect"
	"strings"
	"testing"
)

// schemaTarget is a target that names a schema, the way rerun.yml does.
func schemaTarget(name, schema string) Target {
	t := target(name)
	t.Schema = schema
	return t
}

// listNames is the list as drawn: `▾name` for a header, the name for a target.
func listNames(m *tuiModel) []string {
	var out []string
	for _, r := range m.listRows() {
		if r.header {
			out = append(out, "▾"+groupLabel(r.schema))
			continue
		}
		out = append(out, m.targets[r.target].spec.Name)
	}
	return out
}

// Groups list in first-use order; ungrouped targets come last.
func TestSchemaGroupsListBeforeUngroupedTargets(t *testing.T) {
	m := newTestModel(t,
		target("web"),
		schemaTarget("api", "backend"),
		schemaTarget("db", "data"),
		schemaTarget("worker", "backend"),
		target("job"),
	)
	m.showTimes = true
	want := []string{"▾backend", "api", "worker", "▾data", "db", "▾all", "web", "job"}
	if got := listNames(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}

	lines := leftPane(m)
	if !strings.HasPrefix(lines[1], "▾") || !strings.Contains(lines[1], "backend") || !strings.Contains(lines[1], "2/2") {
		t.Fatalf("header = %q, want the backend header with its running count", lines[1])
	}
	if !strings.HasPrefix(lines[2], " ● api") {
		t.Fatalf("member = %q, want the name indented under its header", lines[2])
	}
	if !strings.HasPrefix(lines[6], "▾") || !strings.Contains(lines[6], "all") {
		t.Fatalf("header = %q, want the trailing default header", lines[6])
	}
	if !strings.HasPrefix(lines[7], " ● web") {
		t.Fatalf("ungrouped member = %q, want it indented under default", lines[7])
	}
}

// The header counts running members, and a stopped member drops out of it.
func TestSchemaHeaderCountsRunningMembers(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	m.showTimes = true
	m.targets[1].state = stateStopped
	if got := leftPane(m)[1]; !strings.Contains(got, "1/2") {
		t.Fatalf("header = %q, want 1/2 with one member stopped", got)
	}
}

// Selecting a schema merges only its members' output, prefixes kept.
func TestSchemaSelectionMergesOnlyItsMembers(t *testing.T) {
	m := newTestModel(t, target("web"), schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	paneOut(m, "web", "web-line")
	paneOut(m, "api", "api-line")
	paneOut(m, "worker", "worker-line")

	m.liveSelect() // cursor starts on the backend header
	if m.schemaSel != "backend" {
		t.Fatalf("schemaSel = %q, want backend", m.schemaSel)
	}
	if got, want := m.paneLines(), []string{"api     api-line", "worker  worker-line"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("schema pane = %q, want %q", got, want)
	}
}

// A target solos inside a schema group, and clears the schema selection.
func TestSoloOnAMemberReplacesTheSchemaSelection(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	paneOut(m, "api", "api-line")
	paneOut(m, "worker", "worker-line")

	m.liveSelect()
	press(m, "down", "down")
	if m.solo != 1 || m.schemaSel != "" {
		t.Fatalf("solo = %d schemaSel = %q, want worker soloed and no schema", m.solo, m.schemaSel)
	}
	if got, want := m.paneLines(), []string{"worker-line"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo pane = %q, want %q", got, want)
	}
}

// esc drops the schema selection, and with nothing selected the pane merges every target.
func TestEscClearsSchemaSelectionToTheMergedPane(t *testing.T) {
	m := newTestModel(t, target("web"), schemaTarget("api", "backend"))
	paneOut(m, "web", "web-line")
	paneOut(m, "api", "api-line")

	m.liveSelect()
	press(m, "esc")
	if m.schemaSel != "" {
		t.Fatalf("schemaSel = %q after esc, want none", m.schemaSel)
	}
	if got := len(m.paneLines()); got != 2 {
		t.Fatalf("merged pane has %d lines, want both targets", got)
	}
}

// space on a header stops its running members, else starts its auto members.
func TestSpaceOnSchemaRowTogglesItsMembers(t *testing.T) {
	m := newTestModel(t, target("web"), schemaTarget("api", "backend"), schemaTarget("worker", "backend"))

	press(m, " ")
	for _, i := range []int{0, 1} {
		if m.targets[i].state != stateStopped {
			t.Fatalf("member %s state = %v, want stopped", m.targets[i].spec.Name, m.targets[i].state)
		}
	}
	if m.targets[2].state != stateStarting {
		t.Fatalf("web state = %v, want it untouched", m.targets[2].state)
	}

	press(m, " ")
	for _, i := range []int{0, 1} {
		if m.targets[i].state != stateRunning {
			t.Fatalf("member %s state = %v, want running", m.targets[i].spec.Name, m.targets[i].state)
		}
	}
}

// r on a header reboots its selected members only.
func TestRebootOnSchemaRowTouchesSelectedMembersOnly(t *testing.T) {
	off := schemaTarget("job", "backend")
	off.Selected = false
	m := newTestModel(t, off, schemaTarget("api", "backend"))

	press(m, "r")
	if m.targets[0].state != stateOff {
		t.Errorf("unselected member state = %v, want off", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[1].runner); got != ctrlReboot {
		t.Fatalf("selected member control = %v, want ctrlReboot", got)
	}
}

// m on a header mutes every member, and a second m unmutes them.
func TestMuteOnSchemaRowTogglesAllMembers(t *testing.T) {
	m := newTestModel(t, target("web"), schemaTarget("api", "backend"), schemaTarget("worker", "backend"))

	press(m, "m")
	if !m.targets[0].muted || !m.targets[1].muted {
		t.Fatal("m on the header did not mute both members")
	}
	if m.targets[2].muted {
		t.Fatal("m on the header muted a target outside the schema")
	}

	press(m, "m")
	if m.targets[0].muted || m.targets[1].muted {
		t.Fatal("second m on the header did not unmute the members")
	}
}

func TestSchemaSelectionTablesByDefault(t *testing.T) {
	api := tableTarget("api")
	api.Schema = "backend"
	m := newTestModel(t, api)
	addLines(m, "api", jsonGet)

	m.liveSelect()
	if !m.tableOn() {
		t.Fatal("a schema with columns did not start in table mode")
	}
}

// With nothing selected, the table starts only when every target shares its columns.
func TestNothingSelectedTablesOnlyWhenColumnsAreShared(t *testing.T) {
	m := newTestModel(t, tableTarget("api"), target("web"))
	if m.tableOn() {
		t.Fatal("mixed columns started in table mode with nothing selected")
	}

	shared := newTestModel(t, tableTarget("api"), tableTarget("web"))
	if !shared.tableOn() {
		t.Fatal("shared columns did not start in table mode with nothing selected")
	}
}

// A header whose members are all down and one auto-starts shows the play glyph.
func TestHeaderShowsPlayGlyphWhenSpaceWouldStart(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"))
	m.targets[0].state = stateStopped

	if line := leftPane(m)[1]; !strings.HasPrefix(line, "▶ backend") {
		t.Fatalf("header = %q, want the play glyph in the fold column", line)
	}
}

// A member already up hides the play glyph: space would stop, not start.
func TestHeaderHidesPlayGlyphWhileAMemberRuns(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("dash", "webapps"))
	m.targets[0].state = stateRunning
	m.targets[1].state = stateStopped
	lines := leftPane(m)

	if strings.Contains(lines[1], playGlyph) {
		t.Fatalf("backend header = %q, want no play glyph while its member runs", lines[1])
	}
	if !strings.Contains(lines[3], playGlyph) {
		t.Fatalf("webapps header = %q, want the play glyph on a startable group", lines[3])
	}
}

// The play glyph shares the fold marker's column, so a header name keeps the
// column it had before the glyph: zero indent, one glyph, one space.
func TestHeaderNameColumnMatchesPreGlyphColumn(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("dash", "webapps"))
	m.targets[0].state = stateRunning
	m.targets[1].state = stateStopped
	lines := leftPane(m)

	if strings.Contains(lines[1], playGlyph) || !strings.Contains(lines[3], playGlyph) {
		t.Fatalf("headers = %q / %q, want the glyph only on webapps", lines[1], lines[3])
	}
	nameCol := func(line, name string) int {
		return displayWidth(line[:strings.Index(line, name)])
	}
	if got := nameCol(lines[1], "backend"); got != 2 {
		t.Fatalf("header name column = %d, want the pre-glyph 2", got)
	}
	if got := nameCol(lines[3], "webapps"); got != 2 {
		t.Fatalf("header with glyph name column = %d, want the pre-glyph 2", got)
	}
}

// A collapsed header keeps its fold marker, never the play glyph.
func TestCollapsedHeaderKeepsFoldMarker(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"))
	m.targets[0].state = stateStopped
	m.collapsed = map[string]bool{"backend": true}

	if line := leftPane(m)[1]; strings.Contains(line, playGlyph) || !strings.HasPrefix(line, "▸ backend") {
		t.Fatalf("header = %q, want the fold marker on a collapsed header", line)
	}
}

// The play glyph steps back, so it reads as a hint, not as the header's own state.
func TestPlayGlyphCellIsFaint(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"))
	m.targets[0].state = stateStopped
	m.cursor = m.indexOfKey("target:api")

	if play := drawnRow(t, m, "schema:backend"); !faintLine(play) {
		t.Fatalf("play glyph row = %q, want the glyph cell faint", play)
	}
	m.collapsed = map[string]bool{"backend": true}
	if fold := drawnRow(t, m, "schema:backend"); faintLine(fold) {
		t.Fatalf("fold marker row = %q, want the header's own style", fold)
	}
}
