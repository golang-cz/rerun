package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestMakefileTargets(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []makeTarget
	}{
		{
			name: "## desc on target line",
			src:  "build: ## Build it\n\tgo build\n",
			want: []makeTarget{{"build", "Build it"}},
		},
		{
			name: "comment lines above join with a space",
			src:  "# Run tests\n## with race\n#\ntest:\n",
			want: []makeTarget{{"test", "Run tests with race"}},
		},
		{
			name: "## on the line beats comment above",
			src:  "# above\nbuild: ## inline\n",
			want: []makeTarget{{"build", "inline"}},
		},
		{
			name: "plain # on the target line does not block comment above",
			src:  "# above\nbuild: # plain\n",
			want: []makeTarget{{"build", "above"}},
		},
		{
			name: "blank line resets pending comment",
			src:  "# orphan\n\nbuild:\n",
			want: []makeTarget{{"build", ""}},
		},
		{
			name: "non-comment line resets pending comment",
			src:  "# orphan\nVAR = 1\nbuild:\n",
			want: []makeTarget{{"build", ""}},
		},
		{
			name: "multi-name line makes separate targets",
			src:  "a b c: dep ## shared\n",
			want: []makeTarget{{"a", "shared"}, {"b", "shared"}, {"c", "shared"}},
		},
		{
			name: "assignments are not targets",
			src:  "A := 1\nB ::= 2\nC ?= 3\nD += 4\nE != echo\nF = a:b\nG:=x\n",
			want: nil,
		},
		{
			name: "control bytes are dropped from the desc",
			src:  "# red\x1b[31m text\nbuild:\n",
			want: []makeTarget{{"build", "red[31m text"}},
		},
		{
			name: "double colon rule is a target",
			src:  "a::\n\techo a\n",
			want: []makeTarget{{"a", ""}},
		},
		{
			name: "names with % or $ are skipped",
			src:  "%.o: %.c\n$(OUT): x\n$(X) real: y\nok: z\n",
			want: []makeTarget{{"real", ""}, {"ok", ""}},
		},
		{
			name: "special targets are skipped",
			src:  ".PHONY: build test\n.DEFAULT:\nbuild:\n",
			want: []makeTarget{{"build", ""}},
		},
		{
			name: "recipe lines are ignored",
			src:  "build:\n\techo fake: x\n\t# not a desc\n",
			want: []makeTarget{{"build", ""}},
		},
		{
			name: "comment inside recipe does not describe next target",
			src:  "build:\n\t# inside recipe\ntest:\n",
			want: []makeTarget{{"build", ""}, {"test", ""}},
		},
		{
			name: "dedupe keeps first-seen order and first non-empty desc",
			src:  "a: ## first\na: ## second\nb:\nb: ## late\na c:\n",
			want: []makeTarget{{"a", "first"}, {"b", "late"}, {"c", ""}},
		},
		{
			name: "include, export and conditionals are not targets",
			src: "include other.mk\n-include x.mk\nsinclude y.mk\n" +
				"export FOO: x\nifeq ($(A),b)\nelse ifdef X\nendif\ntarget: ## t\n",
			want: []makeTarget{{"target", "t"}},
		},
		{
			name: "define blocks are skipped, nested included",
			src:  "define BODY\nfoo: bar\ndefine INNER\nx:\nendef\nendef\nreal:\n",
			want: []makeTarget{{"real", ""}},
		},
		{
			name: "continuation lines are part of the line above",
			src:  "a: \\\n  b c\nVAR = x \\\n  y: z\nd:\n",
			want: []makeTarget{{"a", ""}, {"d", ""}},
		},
		{
			name: "CRLF line endings",
			src:  "build: ## Build\r\n\tgo build\r\na: \\\r\n  b\r\nd:\r\n",
			want: []makeTarget{{"build", "Build"}, {"a", ""}, {"d", ""}},
		},
		{
			name: "define as a prerequisite does not open a define block",
			src:  "build: define\ntest:\n",
			want: []makeTarget{{"build", ""}, {"test", ""}},
		},
		{
			name: "export and override define open a define block",
			src:  "export define A\nfoo: x\nendef\noverride define B\nbar\nendef\nreal:\n",
			want: []makeTarget{{"real", ""}},
		},
		{
			name: "## desc after a plain # comment",
			src:  "foo: a # note ## desc\n",
			want: []makeTarget{{"foo", "desc"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMakefile(strings.NewReader(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseMakefile()\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

func TestIsMakefile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"Makefile", true},
		{"makefile", true},
		{"GNUmakefile", true},
		{"sub/dir/Makefile", true},
		{"build.mk", true},
		{"tools/common.mk", true},
		{"Makefile.bak", false},
		{"makefile.txt", false},
		{"mk", false},
		{"rerun.yml", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isMakefile(tt.path); got != tt.want {
				t.Fatalf("isMakefile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestMakefileImportCommandIsShellSafe(t *testing.T) {
	dir := makeDir(t, map[string]string{
		"my build.mk":  "ok:\n\ttrue\nbad;touch:\n\ttrue\nsafe:\n\ttrue\n",
		ConfigFileName: "imports:\n  - path: \"my build.mk\"\n",
	})
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	c, ok := cfg.Find("make/ok")
	if !ok {
		t.Fatalf("targets = %v, want make/ok", targetNames(cfg))
	}
	if want := "make -f 'my build.mk' ok"; c.Cmd != want {
		t.Errorf("Cmd = %q, want %q", c.Cmd, want)
	}
	if _, ok := cfg.Find("make/bad;touch"); ok {
		t.Errorf("targets = %v, want unsafe name skipped", targetNames(cfg))
	}
}
