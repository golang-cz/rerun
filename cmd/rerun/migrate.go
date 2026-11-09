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
)

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
	if value := mappingValue(root, targetsKey); value != nil {
		if looksLikeTarget(value) {
			return migrateNoTargets, fmt.Errorf("%s: %s", real, errTargetsReserved)
		}
		return migrateAlready, nil
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
		return migrateNoTargets, nil
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(rebuilt); err != nil {
		return migrateNoTargets, err
	}
	if err := enc.Close(); err != nil {
		return migrateNoTargets, err
	}
	if err := replaceFile(real, buf.Bytes()); err != nil {
		return migrateNoTargets, err
	}
	return migrated, nil
}
