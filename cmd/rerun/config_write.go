package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// setColumns writes cols to `schemas.<schema>.columns` when schema is set, else
// to `<target>.columns`. Missing `schemas`, schema and `columns` nodes are
// created. The file is replaced atomically.
func setColumns(path, schema, target string, cols []string) error {
	for _, raw := range cols {
		if _, err := parseColumn(raw); err != nil {
			return fmt.Errorf("columns: %s in %q", err, raw)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %s", path, cleanYAMLError(err))
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return fmt.Errorf("%s: no targets defined", path)
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a mapping of target names", path)
	}

	var owner *yaml.Node
	if schema != "" {
		schemas, err := mappingIn(root, schemasKey, true)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		owner, err = mappingIn(schemas, schema, true)
		if err != nil {
			return fmt.Errorf("%s: schema %q: %w", path, schema, err)
		}
	} else {
		owner, err = mappingIn(root, target, false)
		if err != nil {
			return fmt.Errorf("%s: target %q: %w", path, target, err)
		}
	}
	setKey(owner, "columns", flowList(cols))

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	out := restoreBlankLines(strings.ReplaceAll(string(data), "\r\n", "\n"), buf.String())
	return replaceFile(path, []byte(out))
}

// mappingIn returns the mapping under key in m. A missing key is created when
// create is set, and a null value becomes an empty mapping. An alias is refused:
// editing it would change the anchored node everywhere it is used.
func mappingIn(m *yaml.Node, key string, create bool) (*yaml.Node, error) {
	idx := valueIndex(m, key)
	if idx < 0 {
		if !create {
			return nil, fmt.Errorf("not found")
		}
		m.Content = append(m.Content, scalarNode(key), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		idx = len(m.Content) - 1
	}
	node := m.Content[idx]
	switch {
	case node.Kind == yaml.AliasNode:
		return nil, fmt.Errorf("is an alias, edit it by hand")
	case node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		node.Kind, node.Tag, node.Value = yaml.MappingNode, "!!map", ""
	case node.Kind != yaml.MappingNode:
		return nil, fmt.Errorf("expected a mapping")
	}
	return node, nil
}

// setKey sets key to value in mapping m. An existing value is rewritten in place
// so its comments and anchor stay put; an alias value is replaced, so the node
// it points at is left alone.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	idx := valueIndex(m, key)
	if idx < 0 {
		m.Content = append(m.Content, scalarNode(key), value)
		return
	}
	existing := m.Content[idx]
	if existing.Kind == yaml.AliasNode {
		m.Content[idx] = value
		return
	}
	existing.Kind, existing.Style, existing.Tag = value.Kind, value.Style, value.Tag
	existing.Value, existing.Content, existing.Alias = "", value.Content, nil
}

// valueIndex returns the index of key's value in mapping m, or -1.
func valueIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// flowList renders cols as a flow sequence. yaml.v3 quotes an entry that a flow
// context cannot carry plain, such as `level {.level}`.
func flowList(cols []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, c := range cols {
		seq.Content = append(seq.Content, scalarNode(c))
	}
	return seq
}

// restoreBlankLines puts back the blank lines yaml.v3 drops when it encodes.
// Each blank line returns before the same occurrence of the next non-blank line
// it preceded, unless the encoder already kept it, and trailing blanks stay put.
func restoreBlankLines(orig, out string) string {
	origLines := strings.Split(strings.TrimSuffix(orig, "\n"), "\n")
	type anchor struct {
		text string
		occ  int
	}
	before := make(map[anchor]int)
	counts := make(map[string]int)
	blanks := 0
	for _, line := range origLines {
		if strings.TrimSpace(line) == "" {
			blanks++
			continue
		}
		counts[line]++
		if blanks > 0 {
			before[anchor{line, counts[line]}] = blanks
			blanks = 0
		}
	}

	var b strings.Builder
	counts = make(map[string]int)
	run := 0
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			run++
		} else {
			counts[line]++
			if missing := before[anchor{line, counts[line]}] - run; missing > 0 {
				b.WriteString(strings.Repeat("\n", missing))
			}
			run = 0
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(strings.Repeat("\n", blanks))
	return b.String()
}

// replaceFile writes data to a temp file beside path, then renames it over path.
func replaceFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
