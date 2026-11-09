package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// nestedFixture is an import omsx with a backend group, a webapps group and one ungrouped target.
func nestedFixture(t *testing.T) *tuiModel {
	t.Helper()
	api := importTarget("omsx", "api", "omsx/backend")
	api.Schema = "omsx/backend"
	gw := importTarget("omsx", "gateway", "omsx/backend")
	gw.Schema = "omsx/backend"
	dash := importTarget("omsx", "dash", "omsx/webapps")
	dash.Schema = "omsx/webapps"
	misc := importTarget("omsx", "misc", "")
	m := newTestModel(t, api, gw, dash, misc)
	for i := range m.targets {
		m.targets[i].state = stateStopped
	}
	return m
}

// Rows: 0 omsx, 1 backend, 2 api, 3 gateway, 4 webapps, 5 dash, 6 all, 7 misc.
// Every target starts stopped, so space starts rather than stops them.
func TestEscStepsOutOneLevel(t *testing.T) {
	cases := []struct {
		name       string
		cursor     int
		wantCursor int
		wantScope  string
	}{
		{"from a sub-header", 1, 0, importKeyPrefix + "omsx"},
		{"from a target under a sub-header", 2, 1, "omsx/backend"},
		{"from the all sub-header", 6, 0, importKeyPrefix + "omsx"},
		{"from an import-level target", 7, 6, allKeyPrefix + "omsx"},
		{"from the import header", 0, 0, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := nestedFixture(t)
			if len(m.listRows()) != 8 {
				t.Fatalf("fixture rows = %d, want 8", len(m.listRows()))
			}
			m.cursor = tt.cursor
			m.escList()
			if m.cursor != tt.wantCursor || m.schemaSel != tt.wantScope || m.solo != -1 {
				t.Fatalf("cursor=%d scope=%q solo=%d, want cursor %d scope %q",
					m.cursor, m.schemaSel, m.solo, tt.wantCursor, tt.wantScope)
			}
		})
	}
}

// An import-level target (no sub-groups) backs out to its import header.
func TestEscFromImportLevelTargetWithoutSubGroups(t *testing.T) {
	m := newTestModel(t, importTarget("bpn", "api", ""), importTarget("bpn", "web", ""))
	m.cursor = 2
	m.escList()
	if m.cursor != 0 || m.schemaSel != importKeyPrefix+"bpn" {
		t.Fatalf("cursor=%d scope=%q, want the bpn header", m.cursor, m.schemaSel)
	}
}

func TestRowActionsActOnTheHeaderMembers(t *testing.T) {
	t.Run("mute on an import header mutes every member", func(t *testing.T) {
		m := nestedFixture(t)
		m.muteRow(m.listRows()[0])
		for i, tg := range m.targets {
			if !tg.muted {
				t.Fatalf("%s not muted", tg.spec.Name)
			}
			_ = i
		}
	})
	t.Run("mute on a sub-header mutes only its members", func(t *testing.T) {
		m := nestedFixture(t)
		m.muteRow(m.listRows()[1])
		want := []bool{true, true, false, false}
		for i, tg := range m.targets {
			if tg.muted != want[i] {
				t.Fatalf("%s muted = %v, want %v", tg.spec.Name, tg.muted, want[i])
			}
		}
	})
	t.Run("space on an import header starts every auto member", func(t *testing.T) {
		m := nestedFixture(t)
		m.toggleRow(m.listRows()[0])
		for _, tg := range m.targets {
			if tg.state != stateRunning {
				t.Fatalf("%s state = %v, want running", tg.spec.Name, tg.state)
			}
		}
	})
	t.Run("space on a sub-header stops only its running members", func(t *testing.T) {
		m := nestedFixture(t)
		m.toggleRow(m.listRows()[0]) // start all
		m.toggleRow(m.listRows()[4]) // webapps header: stop dash
		if m.targets[0].state != stateRunning || m.targets[1].state != stateRunning || m.targets[2].state == stateRunning {
			t.Fatalf("states api=%v gateway=%v dash=%v, want only dash stopped",
				m.targets[0].state, m.targets[1].state, m.targets[2].state)
		}
	})
	t.Run("reboot on a sub-header reboots only its selected members", func(t *testing.T) {
		m := nestedFixture(t)
		m.now = time.Unix(1000, 0)
		outside := []time.Time{m.targets[2].lastStart, m.targets[3].lastStart}
		m.rebootRow(m.listRows()[1])
		if m.targets[0].lastStart != m.now || m.targets[1].lastStart != m.now {
			t.Fatalf("backend members lastStart = %v, %v, want rebooted at %v", m.targets[0].lastStart, m.targets[1].lastStart, m.now)
		}
		if m.targets[2].lastStart != outside[0] || m.targets[3].lastStart != outside[1] {
			t.Fatalf("a member outside the backend header was rebooted")
		}
	})
}

// space on a header starts only the members rerun.yml auto-starts.
func TestToggleHeaderStartsOnlyAutoMembers(t *testing.T) {
	auto := schemaTarget("api", "backend")
	manual := schemaTarget("job", "backend")
	manual.Start = startManual
	unselected := schemaTarget("legacy", "backend")
	unselected.Selected = false
	m := newTestModel(t, auto, manual, unselected)
	for i := range m.targets {
		m.targets[i].state = stateStopped
	}
	hdr := headerRow(t, m)

	m.toggleRow(hdr)
	if m.targets[0].state != stateRunning {
		t.Fatalf("auto member state = %v, want running", m.targets[0].state)
	}
	if m.targets[1].state == stateRunning || m.targets[2].state == stateRunning {
		t.Fatalf("a non-auto member started: manual=%v unselected=%v",
			m.targets[1].state, m.targets[2].state)
	}
}

// space on a header of manual members (a Makefile import) does nothing, silently.
func TestToggleHeaderOfManualMembersStartsNothing(t *testing.T) {
	build := schemaTarget("build", "make")
	build.Start = startManual
	test := schemaTarget("test", "make")
	test.Start = startManual
	m := newTestModel(t, build, test)
	for i := range m.targets {
		m.targets[i].state = stateStopped
	}
	if line := leftPane(m)[1]; strings.Contains(line, playGlyph) {
		t.Fatalf("header = %q, want no play glyph without a startable member", line)
	}

	m.toggleRow(headerRow(t, m))
	for _, tg := range m.targets {
		if tg.state != stateStopped {
			t.Fatalf("%s state = %v, want stopped", tg.spec.Name, tg.state)
		}
	}
	if m.hint != "" {
		t.Fatalf("hint = %q, want none: space starts nothing here", m.hint)
	}
}

// headerRow is the first header of m's list, which the toggle tests act on.
func headerRow(t *testing.T, m *tuiModel) listRow {
	t.Helper()
	for _, r := range m.listRows() {
		if r.header {
			return r
		}
	}
	t.Fatal("fixture has no header row")
	return listRow{}
}

func TestReloadKeepsOrClearsASubScope(t *testing.T) {
	t.Run("a sub-group whose member moves away clears the scope", func(t *testing.T) {
		m := nestedFixture(t)
		m.applyScope(-1, "omsx/webapps")
		moved := importTarget("omsx", "dash", "omsx/backend")
		moved.Schema = "omsx/backend"
		m.rebuildRows(&configChange{targets: []Target{
			importTarget("omsx", "api", "omsx/backend"), importTarget("omsx", "gateway", "omsx/backend"),
			moved, importTarget("omsx", "misc", ""),
		}})
		if m.schemaSel != "" {
			t.Fatalf("schemaSel = %q, want cleared once omsx/webapps is empty", m.schemaSel)
		}
	})
	t.Run("a sub-group keeps its scope while one member remains", func(t *testing.T) {
		m := nestedFixture(t)
		m.applyScope(-1, "omsx/backend")
		moved := importTarget("omsx", "gateway", "omsx/webapps")
		moved.Schema = "omsx/webapps"
		m.rebuildRows(&configChange{targets: []Target{
			importTarget("omsx", "api", "omsx/backend"), moved,
			importTarget("omsx", "dash", "omsx/webapps"), importTarget("omsx", "misc", ""),
		}})
		if m.schemaSel != "omsx/backend" {
			t.Fatalf("schemaSel = %q, want omsx/backend kept", m.schemaSel)
		}
		if got := fmtMembers(m.schemaMembers(m.schemaSel)); got != "[0]" {
			t.Fatalf("backend members = %s, want only api", got)
		}
	})
	t.Run("the all sub-group clears when its last ungrouped target gets a group", func(t *testing.T) {
		m := nestedFixture(t)
		m.applyScope(-1, allKeyPrefix+"omsx")
		m.rebuildRows(&configChange{targets: []Target{
			importTarget("omsx", "api", "omsx/backend"), importTarget("omsx", "gateway", "omsx/backend"),
			importTarget("omsx", "dash", "omsx/webapps"), importTarget("omsx", "misc", "omsx/webapps"),
		}})
		if m.schemaSel != "" {
			t.Fatalf("schemaSel = %q, want cleared with no ungrouped members", m.schemaSel)
		}
	})
}

func fmtMembers(members []int) string {
	var b strings.Builder
	b.WriteString("[")
	for i, j := range members {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(string(rune('0' + j)))
	}
	b.WriteString("]")
	return b.String()
}

// Interleaved targets still draw as contiguous sections, sub-groups in first-use order, and all last.
func TestGroupTargetsOrdersMembersLikeTheRows(t *testing.T) {
	a := importTarget("a", "one", "a/backend")
	a.Schema = "a/backend"
	b := importTarget("b", "two", "b/webapps")
	b.Schema = "b/webapps"
	c := importTarget("a", "three", "a/backend")
	c.Schema = "a/backend"
	d := importTarget("a", "four", "")
	e := importTarget("a", "five", "a/webapps")
	e.Schema = "a/webapps"
	root := schemaTarget("server", "backend")
	lone := target("lone")
	m := newTestModel(t, a, b, root, c, d, e, lone)

	want := []string{
		"▾a", " ▾backend", "  one", "  three", " ▾webapps", "  five", " ▾all", "  four",
		"▾b", " ▾webapps", "  two",
		"▾backend", " server",
		"▾all", " lone",
	}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	var order []int
	for _, r := range m.listRows() {
		if !r.header {
			order = append(order, r.target)
		}
	}
	for i, j := range order {
		if j != i {
			t.Fatalf("draw order %v does not match m.targets order", order)
		}
	}
}

func TestComposeServicesGroupByProfileTheImportNames(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.debug.json", `{"name":"devbox","services":{"home":{"profiles":["edge","debug"]},"adminer":{"profiles":["debug"]}}}`)
	composeYML := "services:\n  home:\n    image: home\n  adminer:\n    image: adminer\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\n    profiles: [debug]\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range cfg.Targets {
		if tg.Sub != "devbox/debug" {
			t.Fatalf("%s sub = %q, want devbox/debug, the profile the import names", tg.Name, tg.Sub)
		}
	}
}

func TestListProfileChoosesTheFirstNamedProfile(t *testing.T) {
	if got := listProfile([]string{"edge", "debug"}, []string{"debug", "edge"}); got != "edge" {
		t.Fatalf("listProfile = %q, want edge, the first of the service's profiles the import names", got)
	}
	if got := listProfile([]string{"edge", "debug"}, nil); got != "edge" {
		t.Fatalf("listProfile without a filter = %q, want edge", got)
	}
	if got := listProfile(nil, []string{"debug"}); got != "" {
		t.Fatalf("listProfile for an ungated service = %q, want none", got)
	}
}

func TestColumnPicksNeverWriteAnImportScope(t *testing.T) {
	for _, scope := range []string{importKeyPrefix + "omsx", allKeyPrefix + "omsx"} {
		m := nestedFixture(t)
		m.applyScope(-1, scope)
		if _, ok := m.pickNode(); ok {
			t.Fatalf("pickNode for scope %q = ok, want none", scope)
		}
		m.pickKey(&jsonField{path: ".msg", key: "msg"})
		if m.hint != listKeyHint {
			t.Fatalf("hint = %q, want %q", m.hint, listKeyHint)
		}
		if len(m.edits) != 0 {
			t.Fatalf("edits = %v, want none written for scope %q", m.edits, scope)
		}
	}
}

func TestComposeArgsNameEveryProfileOnlyWithoutAFilter(t *testing.T) {
	unfiltered := &composeTarget{File: "f.yml"}
	for _, sub := range []string{"stop", "logs"} {
		args := strings.Join(unfiltered.composeArgs(sub, "svc"), " ")
		if !strings.Contains(args, "--profile *") {
			t.Errorf("%s without a profiles filter = %q, want --profile *", sub, args)
		}
	}
	filtered := &composeTarget{File: "f.yml", Profiles: []string{"debug"}}
	for _, sub := range []string{"stop", "logs"} {
		args := strings.Join(filtered.composeArgs(sub, "svc"), " ")
		if strings.Contains(args, "*") || !strings.Contains(args, "--profile debug") {
			t.Errorf("%s with profiles = %q, want only --profile debug", sub, args)
		}
	}
}
