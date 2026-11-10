package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// migrateStatus is what migrateConfig did, for the command's one line of output.
type migrateStatus int

const (
	migrated         migrateStatus = iota // targets moved under `targets:`
	migrateAlready                        // the file already uses `targets:`
	migrateNoTargets                      // no top-level target keys to move
	migrateRenamed                        // targets already live under targets:, and their retired keys were renamed
)

// migrateOpenKeys rewrites each target's retired keys in place: url: becomes open:, cmd: run:, hide: muted:,
// and open-on-start: and a boolean open: are dropped. It reports whether any key changed.
func migrateOpenKeys(block *yaml.Node) (bool, error) {
	changed := false
	for i := 0; i+1 < len(block.Content); i += 2 {
		name := deref(block.Content[i]).Value
		target := deref(block.Content[i+1])
		if target.Kind != yaml.MappingNode {
			continue
		}
		ok, err := renameOpenKeys(target, name)
		if err != nil {
			return false, err
		}
		changed = changed || ok
	}
	return changed, nil
}

// keyAt is the index of a mapping's key, or -1.
func keyAt(m *yaml.Node, name string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return i
		}
	}
	return -1
}

// dropKey removes the key and its value at i.
func dropKey(m *yaml.Node, i int) { m.Content = slices.Delete(m.Content, i, i+2) }

// renameOpenKeys rewrites one target's retired keys; a url: beside a string open: is an error.
func renameOpenKeys(target *yaml.Node, name string) (bool, error) {
	changed := false
	// A boolean open: and open-on-start: only said "open at startup", which is gone.
	if at := keyAt(target, "open"); at >= 0 && deref(target.Content[at+1]).Tag == "!!bool" {
		dropKey(target, at)
		changed = true
	}
	if at := keyAt(target, "open-on-start"); at >= 0 {
		dropKey(target, at)
		changed = true
	}
	urlAt, openAt := keyAt(target, "url"), keyAt(target, "open")
	if urlAt >= 0 && openAt >= 0 {
		return false, fmt.Errorf("target %q: both url: and a string open: are set; use open: alone", name)
	}
	if urlAt >= 0 {
		target.Content[urlAt].Value = "open"
		changed = true
	}
	if at := keyAt(target, "cmd"); at >= 0 {
		if keyAt(target, "run") >= 0 {
			return false, fmt.Errorf("target %q: both run: and cmd: are set; use run:", name)
		}
		target.Content[at].Value = "run"
		changed = true
	}
	if at := keyAt(target, "hide"); at >= 0 {
		if keyAt(target, "muted") >= 0 {
			dropKey(target, at)
		} else {
			target.Content[at].Value = "muted"
		}
		changed = true
	}
	return changed, nil
}

// foldSpellings are the retired keys that `fold:` replaces on a group or an import.
var foldSpellings = []string{"collapsed", "auto-hide", "hide", "auto-fold"}

// mergeFold folds a group's or import's retired keys into one `fold:`, where true wins.
// It skips a mapping whose values are not booleans, which the parser reports.
func mergeFold(m *yaml.Node) bool {
	var at []int
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i].Value; k == "fold" || slices.Contains(foldSpellings, k) {
			if deref(m.Content[i+1]).Tag != "!!bool" {
				return false
			}
			at = append(at, i)
		}
	}
	if len(at) == 0 || (len(at) == 1 && m.Content[at[0]].Value == "fold") {
		return false
	}
	value := "false"
	for _, i := range at {
		if m.Content[i+1].Value == "true" {
			value = "true"
		}
	}
	first := at[0]
	if f := keyAt(m, "fold"); f >= 0 {
		first = f
	}
	for k := len(at) - 1; k >= 0; k-- {
		if at[k] != first {
			dropKey(m, at[k])
		}
	}
	m.Content[first].Value = "fold"
	m.Content[first+1].Value = value
	return true
}

// migrateFold rewrites the retired fold keys of every group and import, in place.
func migrateFold(root *yaml.Node) bool {
	changed := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := deref(root.Content[i])
		block := deref(root.Content[i+1])
		if key.Kind != yaml.ScalarNode {
			continue
		}
		switch {
		case key.Value == groupsBlock.block || key.Value == schemasBlock.block:
			for j := 1; block.Kind == yaml.MappingNode && j < len(block.Content); j += 2 {
				if g := deref(block.Content[j]); g.Kind == yaml.MappingNode {
					changed = mergeFold(g) || changed
				}
			}
		case key.Value == importsKey:
			for _, item := range block.Content {
				if e := deref(item); e.Kind == yaml.MappingNode {
					changed = mergeFold(e) || changed
				}
			}
		}
	}
	return changed
}

// writeYAML writes a node to path, keeping its comments.
func writeYAML(path string, node *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return replaceFile(path, buf.Bytes())
}

// migrateImports adds an imports: entry for a Makefile that rerun.yml's auto-import used to load, and returns the paths it added.
func migrateImports(path string) ([]string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s", real, cleanYAMLError(err))
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil
	}
	dir := filepath.Dir(real)
	mk := implicitMakefile(dir)
	if mk == "" {
		return nil, nil
	}
	// A rerun.yml that defines the make group never auto-imported its Makefile.
	file, err := parseFileBody(newConfigSource(real, data), root, real)
	if err != nil {
		return nil, err
	}
	if _, owned := file.schemas[makeKind]; owned || slices.ContainsFunc(file.targets, func(t Target) bool { return t.Schema == makeKind }) {
		return nil, nil
	}
	imports := mappingValue(root, importsKey)
	if imports != nil && imports.Kind == yaml.SequenceNode {
		for _, item := range imports.Content {
			p := mappingValue(deref(item), "path")
			if p == nil {
				continue
			}
			if abs, err := resolveImportPath(dir, p.Value); err == nil && canonicalPath(abs) == canonicalPath(mk) {
				return nil, nil
			}
		}
	}

	name := filepath.Base(mk)
	// The implicit import skipped a missing Makefile, so the entry does too.
	optional := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalarNode("path"), scalarNode(name), scalarNode("optional"), optional}}
	switch {
	case imports == nil:
		root.Content = append(root.Content, scalarNode(importsKey), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{entry}})
	case imports.Kind == yaml.SequenceNode:
		imports.Content = append(imports.Content, entry)
	case imports.Tag == "!!null":
		imports.Kind, imports.Tag, imports.Value = yaml.SequenceNode, "!!seq", ""
		imports.Content = []*yaml.Node{entry}
	default:
		return nil, fmt.Errorf("%s: imports: expected a list of entries with a path", real)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if err := replaceFile(real, buf.Bytes()); err != nil {
		return nil, err
	}
	return []string{name}, nil
}

// migrateConfig rewrites path's legacy top-level targets under a `targets:`
// block. Only the shape of the top-level mapping changes, so the yaml.v3 node
// writer carries every comment and the entry order across.
func migrateConfig(path string) (migrateStatus, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return migrateNoTargets, err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return migrateNoTargets, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return migrateNoTargets, fmt.Errorf("%s: %s", real, cleanYAMLError(err))
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return migrateNoTargets, fmt.Errorf("%s: no targets defined", real)
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return migrateNoTargets, fmt.Errorf("%s: expected a mapping of target names", real)
	}
	groupsRenamed := migrateFold(root)
	if value := mappingValue(root, targetsKey); value != nil {
		if looksLikeTarget(value) {
			return migrateNoTargets, fmt.Errorf("%s: %s", real, errTargetsReserved)
		}
		renamed, err := migrateOpenKeys(value)
		if err != nil {
			return migrateNoTargets, fmt.Errorf("%s: %v", real, err)
		}
		if !renamed && !groupsRenamed {
			return migrateAlready, nil
		}
		if err := writeYAML(real, &doc); err != nil {
			return migrateNoTargets, err
		}
		return migrateRenamed, nil
	}

	targets := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	// The leading comment hangs off the document, not the mapping, so carry it over.
	rebuilt := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: doc.HeadComment, FootComment: doc.FootComment}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		switch key.Value {
		case importsKey, groupsBlock.block, schemasBlock.block, queriesKey:
			rebuilt.Content = append(rebuilt.Content, key, value)
			continue
		}
		// The block takes the first target's slot, so the top-level order holds.
		if len(targets.Content) == 0 {
			rebuilt.Content = append(rebuilt.Content, scalarNode(targetsKey), targets)
		}
		targets.Content = append(targets.Content, key, value)
	}
	if len(targets.Content) == 0 {
		if groupsRenamed {
			if err := writeYAML(real, &doc); err != nil {
				return migrateNoTargets, err
			}
			return migrateRenamed, nil
		}
		return migrateNoTargets, nil
	}
	if _, err := migrateOpenKeys(targets); err != nil {
		return migrateNoTargets, fmt.Errorf("%s: %v", real, err)
	}
	if err := writeYAML(real, rebuilt); err != nil {
		return migrateNoTargets, err
	}
	return migrated, nil
}
