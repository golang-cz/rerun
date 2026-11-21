package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// importsNode is the `imports:` sequence for checked: paths relative to the rerun.yml, and a group for files in a subfolder.
func importsNode(checked []importCandidate) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	groups := importGroups(checked)
	for i, c := range checked {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.Content = append(entry.Content, scalarNode("path"), scalarNode(c.Path))
		if groups[i] != "" {
			entry.Content = append(entry.Content, scalarNode("group"), scalarNode(groups[i]))
		}
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

// importGroups is each checked file's group. Files sharing a folder would clash on its name, so they get <folder>/<file stem>.
func importGroups(checked []importCandidate) []string {
	uses := map[string]int{}
	for _, c := range checked {
		uses[c.Group]++
	}
	out := make([]string, len(checked))
	for i, c := range checked {
		out[i] = c.Group
		if c.Group != "" && uses[c.Group] > 1 {
			base := filepath.Base(c.Path)
			out[i] = c.Group + "/" + strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
		}
	}
	return out
}

// importSession is the picker's checked set for one run, kept so a reload re-applies it.
type importSession struct {
	rootFile string
	picked   []importCandidate
	schemas  map[string]groupDef
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
	real, err := saveTarget(path)
	if err != nil {
		return err
	}
	if !fileExists(real) {
		lines, err := renderKey(importsKey, block, 2, 0)
		if err != nil {
			return err
		}
		return createFile(real, []byte(strings.Join(lines, "\n")+"\n"))
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

// saveTarget is the file a save of path writes: path itself, or its symlink target when that stays inside path's folder.
func saveTarget(path string) (string, error) {
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return filepath.Join(dir, filepath.Base(path)), nil
	}
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return filepath.Join(dir, filepath.Base(path)), nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s is a symlink to a missing file, edit it by hand", ConfigFileName)
	}
	if rel, err := filepath.Rel(dir, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is a symlink to %s outside %s, edit it by hand", ConfigFileName, real, dir)
	}
	return real, nil
}

// createFile writes a new file through a temp file and a rename, so a watcher never reads it half written.
func createFile(path string, data []byte) error {
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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
			Path  string `yaml:"path"`
			Group string `yaml:"group"`
		} `yaml:"imports"`
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		return fmt.Errorf("edited imports do not parse: %s", cleanYAMLError(err))
	}
	if len(got.Imports) != len(checked) {
		return errors.New("edited imports do not match the checked files")
	}
	groups := importGroups(checked)
	for i, c := range checked {
		if got.Imports[i].Path != c.Path || got.Imports[i].Group != groups[i] {
			return errors.New("edited imports do not match the checked files")
		}
	}
	return nil
}
