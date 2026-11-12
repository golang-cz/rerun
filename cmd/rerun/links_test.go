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
