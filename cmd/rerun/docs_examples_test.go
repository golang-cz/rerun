package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// docTargetBlocks returns the yaml fenced blocks of markdown whose document
// root holds a targets: mapping, so a snippet is a whole rerun.yml example.
func docTargetBlocks(markdown string) []string {
	var blocks []string
	lines := strings.Split(markdown, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```") {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(lines[i], "```"))
		var body []string
		for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
			body = append(body, lines[i])
		}
		if info != "yaml" {
			continue
		}
		block := strings.Join(body, "\n") + "\n"
		if isTargetsExample(block) {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// isTargetsExample reports whether the root mapping holds a targets: mapping.
// A nested key, such as a Makefile import's targets: filter, does not count.
func isTargetsExample(text string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return false
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == targetsKey && root.Content[i+1].Kind == yaml.MappingNode {
			return true
		}
	}
	return false
}

// The README and SKILL.md examples are the docs' contract, so every full
// rerun.yml example must load in the targets: form.
func TestDocExamplesParse(t *testing.T) {
	for _, name := range []string{"README.md", "SKILL.md"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			blocks := docTargetBlocks(string(raw))
			t.Logf("%s: %d targets: examples", name, len(blocks))
			if len(blocks) < 2 {
				t.Fatalf("%s: found %d targets: examples, want the doc's full examples", name, len(blocks))
			}
			for n, block := range blocks {
				// A fresh dir per block keeps a relative import path out of the others.
				path := filepath.Join(t.TempDir(), ConfigFileName)
				writeFile(t, path, block)
				if _, err := ParseConfig(path); err != nil {
					t.Errorf("%s: example %d: %v\nexample:\n%s", name, n+1, err, block)
				}
			}
		})
	}
}
