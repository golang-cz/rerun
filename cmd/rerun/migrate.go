package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// migrateStatus is what migrateConfig did, for the command's one line of output.
type migrateStatus int

const (
	migrated       migrateStatus = iota // targets moved under `targets:`
	migrateAlready                     // the file already uses `targets:`
	migrateNoTargets                   // no top-level target keys to move
)

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
		case importsKey, groupsBlock.block, schemasBlock.block:
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
