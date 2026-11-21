package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// importsNode is the `imports:` sequence for checked: paths relative to the rerun.yml, and a group for files in a subfolder.
func importsNode(checked []importCandidate) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, c := range checked {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.Content = append(entry.Content, scalarNode("path"), scalarNode(c.Path))
		if c.Group != "" {
			entry.Content = append(entry.Content, scalarNode("group"), scalarNode(c.Group))
		}
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

// sessionConfig loads the rerun.yml's own targets, groups and links plus the checked files; its imports: are replaced, nothing is written.
func sessionConfig(rootFile string, checked []importCandidate, rootSchemas map[string]groupDef) (*Config, error) {
	abs, err := filepath.Abs(rootFile)
	if err != nil {
		return nil, err
	}
	cfg := &Config{Path: rootFile, Files: []string{rootFile}}
	if fileExists(rootFile) {
		data, err := os.ReadFile(rootFile)
		if err != nil {
			return nil, err
		}
		src, root, err := parseConfigBytes(rootFile, data)
		if err != nil {
			return nil, err
		}
		file, err := parseFileBody(src, root, abs)
		if err != nil {
			return nil, err
		}
		links, err := parseLinks(src, root, filepath.Dir(abs))
		if err != nil {
			return nil, err
		}
		cfg.Targets, cfg.Queries, cfg.Links = file.targets, file.queries, links
		rootSchemas = file.schemas
	}
	if err := cfg.parseImports(&configSource{path: rootFile}, importsNode(checked), rootFile, rootSchemas, true); err != nil {
		return nil, err
	}
	if err := cfg.validateQueries(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// saveImports rewrites only the imports: key of the rerun.yml at path, creating the file when it is missing.
func saveImports(path string, checked []importCandidate) error {
	block := importsNode(checked)
	if !fileExists(path) {
		lines, err := renderKey(importsKey, block, 2, 0)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}

	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %s", real, cleanYAMLError(err))
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		root = firstOrEmpty(root)
	}

	ed := newLineEdit(string(data))
	if err := ed.setImports(root, block); err != nil {
		return fmt.Errorf("%s: %w", real, err)
	}
	out := []byte(ed.String())
	if err := checkImportsRoundTrip(out, checked); err != nil {
		return fmt.Errorf("%s: %w", real, err)
	}
	return replaceFile(real, out)
}

// firstOrEmpty is the document's root node, or an empty node when the file has no content.
func firstOrEmpty(doc *yaml.Node) *yaml.Node {
	if len(doc.Content) == 0 {
		return &yaml.Node{}
	}
	return doc.Content[0]
}

// setImports replaces the imports: key's lines with block, or appends the key when the file has none.
func (ed *lineEdit) setImports(root, block *yaml.Node) error {
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && root.Tag == "!!null") {
		lines, err := renderKey(importsKey, block, ed.unit, 0)
		if err != nil {
			return err
		}
		ed.appendLines(lines)
		return nil
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping of target names")
	}
	if root.Style&yaml.FlowStyle != 0 {
		return fmt.Errorf("%s is in flow style, edit it by hand", ConfigFileName)
	}
	kn, vn := entry(root, importsKey)
	if kn == nil {
		lines, err := renderKey(importsKey, block, ed.unit, 0)
		if err != nil {
			return err
		}
		if len(root.Content) == 0 {
			ed.appendLines(lines)
			return nil
		}
		return ed.appendTo(root, root.Content[0].Column-1, lines)
	}
	switch {
	case vn.Kind == yaml.AliasNode:
		return fmt.Errorf("%s is an alias, edit it by hand", importsKey)
	case vn.Anchor != "" && aliased(root, vn):
		return anchorError{fmt.Sprintf("%s is a YAML anchor; edit rerun.yml by hand", importsKey)}
	}
	lines, err := renderKey(importsKey, block, ed.unit, kn.Column-1)
	if err != nil {
		return err
	}
	if comment := firstNonEmpty(kn.LineComment, vn.LineComment); comment != "" {
		lines[0] += " " + comment
	}
	start := kn.Line - 1
	blockSeq := vn.Kind == yaml.SequenceNode && vn.Style&yaml.FlowStyle == 0
	ed.replace(start, scanEnd(ed.lines, start, kn.Column-1, blockSeq), lines)
	return nil
}

// appendLines adds lines at the end of the file, which may be empty.
func (ed *lineEdit) appendLines(lines []string) {
	if len(ed.lines) == 0 {
		for _, l := range lines {
			ed.lines = append(ed.lines, textLine{text: l, eol: ed.eol})
		}
		return
	}
	ed.insertAfter(len(ed.lines)-1, lines)
}

// renderKey is `key: value` as lines at column col, with the value encoded by yaml.v3 at indent.
func renderKey(key string, value *yaml.Node, indent, col int) ([]string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(indent)
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalarNode(key), value}}
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad(col) + l
		}
	}
	return lines, nil
}

// checkImportsRoundTrip re-reads the edited file so a bad splice is refused, not written.
func checkImportsRoundTrip(data []byte, checked []importCandidate) error {
	var got struct {
		Imports []struct {
			Path string `yaml:"path"`
		} `yaml:"imports"`
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		return fmt.Errorf("edited imports do not parse: %s", cleanYAMLError(err))
	}
	if len(got.Imports) != len(checked) {
		return errors.New("edited imports do not match the checked files")
	}
	for i, c := range checked {
		if got.Imports[i].Path != c.Path {
			return errors.New("edited imports do not match the checked files")
		}
	}
	return nil
}
