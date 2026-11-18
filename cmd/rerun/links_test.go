package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Top-level links keep the order they are written in, so the o modal lists them as the file reads.
func TestParseLinksKeepsSourceOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "links:\n  zeta: https://z.test\n  alpha: ./alpha.sh\ntargets:\n  web:\n    run: x\n")
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	var names []string
	for _, l := range cfg.Links {
		names = append(names, l.Name)
	}
	if !slices.Equal(names, []string{"zeta", "alpha"}) {
		t.Fatalf("links = %v, want source order", names)
	}
	if cfg.Links[1].Value != "./alpha.sh" || cfg.Links[1].Dir != filepath.Dir(path) {
		t.Errorf("link = %+v, want its value and the config dir", cfg.Links[1])
	}
}

// A links: entry is a string or a mapping with link: and an optional desc:, and both keep source order.
func TestParseLinksMappingFormKeepsOrderAndDesc(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, "links:\n  Docs:\n    link: https://example.com/docs\n    desc: Product docs\n  Logs: http://localhost:9428\n  Reseed:\n    link: ./reseed.sh\ntargets:\n  web:\n    run: x\n")
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := []Link{
		{Name: "Docs", Value: "https://example.com/docs", Desc: "Product docs", Dir: filepath.Dir(path)},
		{Name: "Logs", Value: "http://localhost:9428", Dir: filepath.Dir(path)},
		{Name: "Reseed", Value: "./reseed.sh", Dir: filepath.Dir(path)},
	}
	if !slices.Equal(cfg.Links, want) {
		t.Fatalf("links = %+v, want %+v", cfg.Links, want)
	}
}

// A null desc: means no desc, in each spelling YAML accepts for null.
func TestParseLinksNullDescIsUnset(t *testing.T) {
	for _, desc := range []string{"desc:", "desc: null", "desc: ~"} {
		t.Run(desc, func(t *testing.T) {
			cfg, err := writeOpenConfig(t, "links:\n  docs:\n    link: https://a.test\n    "+desc+"\ntargets:\n  web:\n    run: x\n")
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if got := cfg.Links[0]; got.Value != "https://a.test" || got.Desc != "" {
				t.Fatalf("link = %+v, want the URL and no desc", got)
			}
		})
	}
}

// Aliases resolve to the anchored mapping or scalar, so both forms work through *name.
func TestParseLinksAliases(t *testing.T) {
	cfg, err := writeOpenConfig(t, "links:\n  base: &base\n    link: https://a.test\n    desc: Docs\n  copy: *base\n  logs:\n    link: &u https://b.test\n  feed:\n    link: *u\ntargets:\n  web:\n    run: x\n")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Links[1].Value != "https://a.test" || cfg.Links[1].Desc != "Docs" {
		t.Errorf("copy = %+v, want the anchored mapping", cfg.Links[1])
	}
	if cfg.Links[3].Value != "https://b.test" {
		t.Errorf("feed = %+v, want the aliased link value", cfg.Links[3])
	}
}

// An empty link: points its caret at the value, so the mapping's own line is not blamed.
func TestParseLinksEmptyLinkCaretWhenDescComesFirst(t *testing.T) {
	_, err := writeOpenConfig(t, "links:\n  docs:\n    desc: Product docs\n    link: \"  \"\ntargets:\n  web:\n    run: x\n")
	if err == nil || !strings.Contains(err.Error(), ":4:11: links: link \"docs\": link: is empty") {
		t.Fatalf("err = %v, want a caret on line 4 col 11 at the empty link:", err)
	}
}

// A missing link: points at the link name, not the mapping.
func TestParseLinksMissingLinkCaretsAtName(t *testing.T) {
	_, err := writeOpenConfig(t, "links:\n  docs:\n    desc: only a desc\ntargets:\n  web:\n    run: x\n")
	if err == nil || !strings.Contains(err.Error(), ":2:3: links: link \"docs\": missing link:") {
		t.Fatalf("err = %v, want a caret on line 2 col 3 at the name", err)
	}
}

// A duplicate key is reported at the repeated key, before its value is type-checked.
func TestParseLinksDuplicateKeyCaretsAtRepeat(t *testing.T) {
	_, err := writeOpenConfig(t, "links:\n  docs:\n    link: https://a.test\n    link: 8080\ntargets:\n  web:\n    run: x\n")
	if err == nil || !strings.Contains(err.Error(), ":4:5: links: link \"docs\": duplicate key \"link\"") {
		t.Fatalf("err = %v, want a duplicate-key caret on line 4 col 5", err)
	}
}

func TestParseLinksErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub string
	}{
		{"duplicate name", "links:\n  docs: https://a.test\n  docs: https://b.test\n", `links: duplicate link "docs"`},
		{"number value", "links:\n  docs: 8080\n", `links: link "docs": expected a string`},
		{"list instead of mapping", "links:\n  - https://a.test\n", "links: expected a mapping of link names"},
		{"blank value", "links:\n  docs: \"  \"\n", `links: link "docs": value is empty`},
		{"mapping unknown key", "links:\n  docs:\n    link: https://a.test\n    descr: oops\n", `links: link "docs": unknown key "descr"`},
		{"mapping without link", "links:\n  docs:\n    desc: only a desc\n", `links: link "docs": missing link:`},
		{"mapping blank link", "links:\n  docs:\n    link: \"  \"\n", `links: link "docs": link: is empty`},
		{"mapping number link", "links:\n  docs:\n    link: 8080\n", `links: link "docs": link: expected a string`},
		{"mapping list desc", "links:\n  docs:\n    link: https://a.test\n    desc: [a]\n", `links: link "docs": desc: expected a string`},
		{"list value", "links:\n  docs:\n    - https://a.test\n", `links: link "docs": expected a string or a mapping, got seq`},
		{"duplicate link key", "links:\n  docs:\n    link: https://a.test\n    link: https://b.test\n", `links: link "docs": duplicate key "link"`},
		{"duplicate desc key", "links:\n  docs:\n    link: https://a.test\n    desc: a\n    desc: b\n", `links: link "docs": duplicate key "desc"`},
		{"empty mapping", "links:\n  docs: {}\n", `links: link "docs": missing link:`},
		{"sequence key", "links:\n  docs:\n    ? [a]\n    : https://a.test\n", `links: link "docs": keys must be strings`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := writeOpenConfig(t, tt.content+"targets:\n  web:\n    run: x\n")
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}

// Imported files cannot add links, so their links: key is a notice rather than a silent drop.
func TestImportedLinksAreIgnoredWithANotice(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sub.yml"), "links:\n  docs: https://a.test\ntargets:\n  api:\n    run: y\n")
	root := filepath.Join(dir, ConfigFileName)
	writeFile(t, root, "imports:\n  - path: sub.yml\ntargets:\n  web:\n    run: x\n")
	cfg, err := ParseConfig(root)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Links) != 0 {
		t.Errorf("links = %+v, want none from an import", cfg.Links)
	}
	if !slices.ContainsFunc(cfg.Notices, func(n string) bool { return strings.Contains(n, "links are read from the root rerun.yml only") }) {
		t.Errorf("notices = %q, want the imported links notice", cfg.Notices)
	}
}

// The o modal lists top-level links first, then each target's link:, all fuzzy-matchable.
func TestLinkModalListsTopLevelLinksBeforeTargets(t *testing.T) {
	web := target("web")
	web.Link = "http://localhost:8080"
	m := newTestModel(t, web, target("api"))
	m.links = []Link{{Name: "Docs", Value: "https://example.com/docs", Dir: "/root"}, {Name: "Reseed", Value: "./reseed.sh", Dir: "/root"}}
	var names []string
	for _, c := range m.finderLinkCandidates() {
		names = append(names, c.name)
	}
	if !slices.Equal(names, []string{"Docs", "Reseed", "web"}) {
		t.Fatalf("modal rows = %v, want top-level links then targets", names)
	}
	m.finder = finderState{open: true, links: true}
	if res := m.finderResults("resd"); len(res) != 1 || res[0].name != "Reseed" {
		t.Fatalf("fuzzy results for resd = %v, want Reseed", res)
	}
}

// Enter on a top-level link opens a URL in the browser, or runs a command in the root dir.
func TestLinkModalOpensTopLevelLinks(t *testing.T) {
	opened := stubOpenURL(t)
	ran := stubOpenCommand(t, 0, nil)
	m := newTestModel(t, target("api"))
	m.links = []Link{{Name: "Docs", Value: "https://example.com/docs", Dir: "/root"}, {Name: "Reseed", Value: "./reseed.sh", Dir: "/root"}}

	press(m, "o")
	press(m, "d", "o", "c", "s")
	press(m, "enter")
	if !slices.Equal(*opened, []string{"https://example.com/docs"}) {
		t.Fatalf("opened = %v, want the docs URL", *opened)
	}

	press(m, "o")
	press(m, "r", "e", "s", "e", "e", "d")
	if cmd := press(m, "enter"); cmd != nil {
		cmd()
	}
	if !slices.Equal(*ran, []string{"./reseed.sh in /root"}) {
		t.Fatalf("ran = %v, want the reseed command in the root dir", *ran)
	}
}
