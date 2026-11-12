package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// migration is one rerun file's rewritten bytes, held until every file has planned cleanly.
type migration struct {
	path    string
	data    []byte
	targets []string
}

// runMigrate rewrites open: to link: in rerun.yml and every rerun file it imports.
func runMigrate() int {
	path, err := findConfig(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", err)
		return 1
	}
	plans, err := planMigration(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: migrate: %v\n", err)
		return 1
	}
	if len(plans) == 0 {
		fmt.Fprintf(os.Stdout, "rerun: no open: keys to rename in %s or its imports\n", path)
		return 0
	}
	if err := applyMigration(plans); err != nil {
		fmt.Fprintf(os.Stderr, "rerun: migrate: %v\n", err)
		return 1
	}
	for _, p := range plans {
		fmt.Fprintf(os.Stdout, "rerun: %s: open: is now link: in targets %s\n", p.path, quoteNames(p.targets))
	}
	return 0
}

// planMigration rewrites open: keys in root and its rerun imports, and writes nothing.
func planMigration(root string) ([]migration, error) {
	files, err := migrationFiles(root)
	if err != nil {
		return nil, err
	}
	var plans []migration
	for _, f := range files {
		m, changed, err := planFile(f)
		if err != nil {
			return nil, err
		}
		if changed {
			plans = append(plans, m)
		}
	}
	return plans, nil
}

// applyMigration writes the planned files; planMigration has already rejected any bad one.
func applyMigration(plans []migration) error {
	for _, p := range plans {
		if err := replaceFile(p.path, p.data); err != nil {
			return err
		}
	}
	return nil
}

// migrationFiles lists root and the rerun files it imports, once each, skipping Makefiles and compose files.
func migrationFiles(root string) ([]string, error) {
	data, err := os.ReadFile(root)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s", root, cleanYAMLError(err))
	}
	files := []string{root}
	seen := map[string]bool{canonicalPath(root): true}
	top := documentMapping(&doc)
	if top == nil {
		return files, nil
	}
	imports := mappingValue(top, importsKey)
	if imports == nil || imports.Kind != yaml.SequenceNode {
		return files, nil
	}
	src := newConfigSource(root, data)
	for _, item := range imports.Content {
		spec, err := parseImportSpec(src, deref(item))
		if err != nil {
			return nil, err
		}
		abs, err := resolveImportPath(filepath.Dir(root), spec.path)
		if err != nil {
			return nil, err
		}
		if isMakefile(abs) || !fileExists(abs) || seen[canonicalPath(abs)] {
			continue
		}
		body, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		var imported yaml.Node
		if err := yaml.Unmarshal(body, &imported); err != nil {
			return nil, fmt.Errorf("%s: %s", abs, cleanYAMLError(err))
		}
		// A compose file has services:, not targets, so it is not a rerun file.
		if top := documentMapping(&imported); top == nil || mappingValue(top, "services") != nil {
			continue
		}
		seen[canonicalPath(abs)] = true
		files = append(files, abs)
	}
	return files, nil
}

// planFile rewrites open: keys in one file's targets, and reports false when it has none to rename.
func planFile(path string) (migration, bool, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return migration{}, false, err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return migration{}, false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return migration{}, false, fmt.Errorf("%s: %s", real, cleanYAMLError(err))
	}
	top := documentMapping(&doc)
	if top == nil {
		return migration{}, false, nil
	}
	block := mappingValue(top, targetsKey)
	if block == nil || block.Kind != yaml.MappingNode {
		return migration{}, false, nil
	}

	// Only the key text changes, so comments and every other byte stay as written.
	lines := strings.SplitAfter(string(data), "\n")
	var renamed []string
	visited := make(map[*yaml.Node]bool)
	for i := 0; i+1 < len(block.Content); i += 2 {
		name := deref(block.Content[i]).Value
		target := deref(block.Content[i+1])
		// An alias names a target already visited, whose key is renamed in place.
		if target.Kind != yaml.MappingNode || visited[target] {
			continue
		}
		visited[target] = true
		openKey, openVal, hasLink := targetOpenKey(target)
		if openKey == nil {
			continue
		}
		if hasLink {
			return migration{}, false, fmt.Errorf("%s: target %q has both open: and link:; delete open:", real, name)
		}
		if openVal.Kind != yaml.ScalarNode || openVal.Tag != "!!str" {
			return migration{}, false, fmt.Errorf("%s: target %q: open: must be a string", real, name)
		}
		if err := renameKeyText(lines, openKey); err != nil {
			return migration{}, false, fmt.Errorf("%s: target %q: %w", real, name, err)
		}
		renamed = append(renamed, name)
	}
	if len(renamed) == 0 {
		return migration{}, false, nil
	}
	out := []byte(strings.Join(lines, ""))
	if _, _, err := parseConfigBytes(real, out); err != nil {
		return migration{}, false, fmt.Errorf("%s: rewritten file does not parse, nothing written: %w", real, err)
	}
	return migration{path: real, data: out, targets: renamed}, true, nil
}

// targetOpenKey finds a target's open: key and its value, and whether the target also has link:.
func targetOpenKey(target *yaml.Node) (key, value *yaml.Node, hasLink bool) {
	for j := 0; j+1 < len(target.Content); j += 2 {
		k := target.Content[j]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "open":
			key, value = k, deref(target.Content[j+1])
		case "link":
			hasLink = true
		}
	}
	return key, value, hasLink
}

// documentMapping returns a parsed document's top-level mapping, or nil when it has none.
func documentMapping(doc *yaml.Node) *yaml.Node {
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	root = deref(root)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

// renameKeyText swaps the key's text on its line, keeping any quotes around it.
func renameKeyText(lines []string, key *yaml.Node) error {
	if key.Line < 1 || key.Line > len(lines) || key.Column < 1 {
		return fmt.Errorf("line %d: open: key not found", key.Line)
	}
	// yaml.v3 columns count runes, so the line is indexed by rune, not byte.
	line := []rune(lines[key.Line-1])
	at := key.Column - 1
	if at >= len(line) {
		return fmt.Errorf("line %d: open: key not found", key.Line)
	}
	rest := string(line[at:])
	for _, q := range []string{"", `"`, "'"} {
		prefix := q + "open" + q
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		tail := rest[len(prefix):]
		if !strings.HasPrefix(strings.TrimLeft(tail, " "), ":") {
			break
		}
		lines[key.Line-1] = string(line[:at]) + q + "link" + q + tail
		return nil
	}
	return fmt.Errorf("line %d: open: key not found", key.Line)
}

// quoteNames formats target names for a log line.
func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}
