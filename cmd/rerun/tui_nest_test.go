package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// importTarget is a target an import named imp put in the list: full name, its file's group, and its compose profile.
func importTarget(imp, name, sub string) Target {
	t := target(imp + "/" + name)
	t.Import = imp
	t.Sub = sub
	return t
}

// nestedRows draws the list as an indented outline: headers as `▾label`, targets as their short name.
func nestedRows(m *tuiModel) []string {
	var out []string
	for _, r := range m.listRows() {
		pad := strings.Repeat(" ", r.indent())
		if r.more {
			out = append(out, pad+r.label)
			continue
		}
		if r.header {
			glyph := "▾"
			if r.hidden > 0 && r.hidden == len(r.members) {
				glyph = "▸"
			}
			out = append(out, pad+glyph+r.label)
			continue
		}
		out = append(out, pad+m.targets[r.target].spec.localName())
	}
	return out
}

func TestImportNestsGroupsAndAllUnderItsHeader(t *testing.T) {
	omsxAPI := importTarget("omsx", "omsx-api", "omsx/backend")
	omsxAPI.Schema = "omsx/backend"
	gateway := importTarget("omsx", "api-gateway", "omsx/backend")
	gateway.Schema = "omsx/backend"
	dash := importTarget("omsx", "dashboard", "omsx/webapps")
	dash.Schema = "omsx/webapps"
	misc := importTarget("omsx", "misc", "")
	server := schemaTarget("server", "backend")
	lone := target("lone")
	m := newTestModel(t, omsxAPI, gateway, dash, misc, server, lone)

	want := []string{
		"lone",
		"▾omsx",
		" ▾backend",
		"  omsx-api",
		"  api-gateway",
		" ▾webapps",
		"  dashboard",
		" ▾all",
		"  misc",
		"▾backend",
		" server",
	}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestImportWithoutGroupsListsTargetsDirectlyUnderItsHeader(t *testing.T) {
	m := newTestModel(t, importTarget("bpn", "api", ""), importTarget("bpn", "web", ""))

	want := []string{"▾bpn", " api", " web"}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestComposeImportGroupsServicesByFirstProfile(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"caddy":{"profiles":["edge"]},"home":{"profiles":["edge","debug"]},"postgres":{"profiles":["data"]},"redis":{}}}`)
	composeYML := "services:\n  caddy:\n    image: caddy\n  home:\n    image: home\n  postgres:\n    image: postgres\n  redis:\n    image: redis\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)

	want := []string{
		"▾devbox",
		" ▾edge",
		"  caddy",
		"  home",
		" ▾data",
		"  postgres",
		" ▾all",
		"  redis",
	}
	if got := nestedRows(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestComposeServiceKeepsItsRootGroupColumns(t *testing.T) {
	fastRetries(t)
	dir := fakeDocker(t)
	writeFake(t, dir, "config.json", `{"name":"devbox","services":{"postgres":{"profiles":["data"]}}}`)
	composeYML := "services:\n  postgres:\n    image: postgres\n    profiles: [data]\n"
	f := newImportFixture(t, "imports:\n  - path: ../devbox/docker-compose.yml\ngroups:\n  devbox:\n    columns: [\"msg {.msg}\"]\n",
		map[string]string{"devbox/docker-compose.yml": composeYML})
	cfg, err := ParseConfig(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, cfg.Targets...)

	svc := m.targets[0].spec
	if svc.Schema != "devbox" || len(svc.Columns) != 1 {
		t.Fatalf("schema = %q columns = %v, want the root devbox group's columns", svc.Schema, svc.Columns)
	}
	if got := groupLabel(groupOf(m.targets[0])); got != "devbox" {
		t.Fatalf("section = %q, want devbox", got)
	}
}

func TestImportHeaderScopesAllMembersAndSubHeaderItsOwn(t *testing.T) {
	gateway := importTarget("omsx", "api-gateway", "omsx/backend")
	gateway.Schema = "omsx/backend"
	dash := importTarget("omsx", "dashboard", "omsx/webapps")
	dash.Schema = "omsx/webapps"
	m := newTestModel(t, gateway, dash)

	rows := m.listRows()
	if !rows[0].header || len(rows[0].members) != 2 {
		t.Fatalf("import header members = %v, want both services", rows[0].members)
	}
	if rows[1].label != "backend" || len(rows[1].members) != 1 || rows[1].schema != "omsx/backend" {
		t.Fatalf("sub-header = %+v, want backend with one member", rows[1])
	}
	if got := groupLabel(rows[0].schema); got != "omsx" {
		t.Fatalf("import bottom-line label = %q, want omsx", got)
	}
	if got := groupLabel(rows[1].schema); got != "omsx/backend" {
		t.Fatalf("sub-header bottom-line label = %q, want omsx/backend", got)
	}
}

func TestAllLabelNamesAnImportsUngroupedTargets(t *testing.T) {
	if got := groupLabel(allKeyPrefix + "omsx"); got != "omsx/all" {
		t.Fatalf("import all label = %q, want omsx/all", got)
	}
}

func TestImportScopeCoversEveryMember(t *testing.T) {
	gateway := importTarget("omsx", "api-gateway", "omsx/backend")
	misc := importTarget("omsx", "misc", "")
	m := newTestModel(t, gateway, misc)

	m.applyScope(-1, importKeyPrefix+"omsx")
	if got := fmt.Sprint(m.schemaMembers(m.schemaSel)); got != "[0 1]" {
		t.Fatalf("import scope members = %s, want both targets", got)
	}
	if !m.memberOf(1, m.schemaSel) || m.memberOf(1, "omsx/backend") {
		t.Fatalf("misc should be in the import scope but not in omsx/backend")
	}
}
