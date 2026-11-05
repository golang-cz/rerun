package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// setColumns writes cols to `groups.<schema>.columns` (or `schemas`, whichever
// the file uses) when schema is set, else to `<target>.columns`. Only the edited key's lines change; the rest of the file
// keeps its bytes. A symlink is followed, so the file it names is the one written.
func setColumns(path, schema, target string, cols []string) error {
	for _, raw := range cols {
		if _, err := parseColumn(raw); err != nil {
			return fmt.Errorf("columns: %s in %q", err, raw)
		}
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
		if len(root.Content) == 0 {
			return fmt.Errorf("%s: no targets defined", real)
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a mapping of target names", real)
	}

	ed := newLineEdit(string(data))
	if schema != "" {
		err = ed.setSchemaColumns(root, groupBlockOf(root), schema, cols)
	} else {
		err = ed.setTargetColumns(root, target, cols)
	}
	var anchored anchorError
	if errors.As(err, &anchored) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%s: %w", real, err)
	}
	return replaceFile(real, []byte(ed.String()))
}

// anchorError refuses an edit that would change a YAML alias's other uses.
type anchorError struct{ msg string }

func (e anchorError) Error() string { return e.msg }

// textLine is one line of the file with its own line ending, so an edit can
// rewrite some lines and leave the rest byte for byte.
type textLine struct {
	text string
	eol  string
}

// lineEdit is the file as lines. unit is the file's indent step and eol its
// dominant line ending, which new lines take.
type lineEdit struct {
	lines []textLine
	unit  int
	eol   string
}

func newLineEdit(s string) *lineEdit {
	ed := &lineEdit{eol: "\n"}
	crlf := 0
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			ed.lines = append(ed.lines, textLine{text: s})
			break
		}
		l := textLine{text: s[:i], eol: "\n"}
		if strings.HasSuffix(l.text, "\r") {
			l.text, l.eol = l.text[:len(l.text)-1], "\r\n"
			crlf++
		}
		ed.lines = append(ed.lines, l)
		s = s[i+1:]
	}
	if crlf > 0 && crlf*2 >= len(ed.lines) {
		ed.eol = "\r\n"
	}
	ed.unit = indentUnit(ed.lines)
	return ed
}

func (ed *lineEdit) String() string {
	var b strings.Builder
	for _, l := range ed.lines {
		b.WriteString(l.text)
		b.WriteString(l.eol)
	}
	return b.String()
}

// setTargetColumns writes the columns of the target named target.
func (ed *lineEdit) setTargetColumns(root *yaml.Node, target string, cols []string) error {
	kn, vn := entry(root, target)
	if kn == nil {
		return fmt.Errorf("target %q: not found", target)
	}
	return ed.setColumnsIn(root, kn, vn, target, cols)
}

// groupBlockOf is the top-level spelling the file already uses. A file with
// neither gets the primary `groups`.
func groupBlockOf(root *yaml.Node) groupBlock {
	if k, _ := entry(root, groupsBlock.block); k == nil {
		if k, _ := entry(root, schemasBlock.block); k != nil {
			return schemasBlock
		}
	}
	return groupsBlock
}

// setSchemaColumns writes the columns of the group named schema under the block
// kw, creating the block or the group entry when either is missing.
func (ed *lineEdit) setSchemaColumns(root *yaml.Node, kw groupBlock, schema string, cols []string) error {
	sk, sv := entry(root, kw.block)
	if sk == nil {
		return ed.appendTo(root, 0, []string{
			kw.block + ":",
			pad(ed.unit) + yamlKey(schema) + ":",
			pad(2*ed.unit) + columnsLine(cols),
		})
	}
	switch {
	case sv.Kind == yaml.AliasNode:
		return fmt.Errorf("%s is an alias, edit it by hand", kw.block)
	case isEmpty(sv):
		if err := ed.openFlowEmpty(root, sk, sv); err != nil {
			return err
		}
		ci := sk.Column - 1 + ed.unit
		ed.insertAfter(sk.Line-1, []string{
			pad(ci) + yamlKey(schema) + ":",
			pad(ci+ed.unit) + columnsLine(cols),
		})
		return nil
	case sv.Kind != yaml.MappingNode:
		return fmt.Errorf("%s: expected a mapping of %s names", kw.block, kw.field)
	}
	kn, vn := entry(sv, schema)
	if kn == nil {
		ci := childIndent(sv, sk, ed.unit)
		return ed.appendTo(sv, ci, []string{
			pad(ci) + yamlKey(schema) + ":",
			pad(ci+ed.unit) + columnsLine(cols),
		})
	}
	return ed.setColumnsIn(root, kn, vn, schema, cols)
}

// setColumnsIn writes columns under the entry kn: value vn, which is the node
// that owns the columns key.
func (ed *lineEdit) setColumnsIn(root, kn, vn *yaml.Node, name string, cols []string) error {
	switch {
	case vn.Kind == yaml.AliasNode:
		return fmt.Errorf("%s is an alias, edit it by hand", name)
	case isEmpty(vn):
		if err := ed.openFlowEmpty(root, kn, vn); err != nil {
			return err
		}
		ed.insertAfter(kn.Line-1, []string{pad(kn.Column-1+ed.unit) + columnsLine(cols)})
		return nil
	case vn.Kind != yaml.MappingNode:
		return fmt.Errorf("%s: expected a mapping", name)
	}
	if vn.Anchor != "" && aliased(root, vn) {
		return anchorError{fmt.Sprintf("%s is a YAML anchor; edit rerun.yml by hand", name)}
	}
	ck, cv := entry(vn, "columns")
	if ck == nil {
		ci := childIndent(vn, kn, ed.unit)
		return ed.appendTo(vn, ci, []string{pad(ci) + columnsLine(cols)})
	}
	return ed.replaceColumns(root, ck, cv, name, cols)
}

// openFlowEmpty rewrites `key: {}` as a block key, so children can go under it.
func (ed *lineEdit) openFlowEmpty(root, kn, vn *yaml.Node) error {
	if vn.Kind != yaml.MappingNode || vn.Style&yaml.FlowStyle == 0 {
		return nil
	}
	if vn.Anchor != "" && aliased(root, vn) {
		return anchorError{fmt.Sprintf("%s is a YAML anchor; edit rerun.yml by hand", kn.Value)}
	}
	line := pad(kn.Column-1) + yamlKey(kn.Value) + ":"
	if comment := firstNonEmpty(kn.LineComment, vn.LineComment); comment != "" {
		line += " " + comment
	}
	ed.replace(kn.Line-1, kn.Line-1, []string{line})
	return nil
}

// replaceColumns rewrites the lines of an existing columns key. An anchored list
// with aliases elsewhere is refused, since rewriting it would change those uses.
func (ed *lineEdit) replaceColumns(root, ck, cv *yaml.Node, name string, cols []string) error {
	value := flowList(cols)
	if cv.Kind != yaml.AliasNode {
		if cv.Anchor != "" && aliased(root, cv) {
			return anchorError{fmt.Sprintf("columns of %s are a YAML anchor; edit rerun.yml by hand", name)}
		}
		value.Anchor = cv.Anchor
	}
	line, err := yamlLine("columns", value)
	if err != nil {
		return err
	}
	if comment := firstNonEmpty(ck.LineComment, cv.LineComment); comment != "" {
		line += " " + comment
	}
	start := ck.Line - 1
	blockSeq := cv.Kind == yaml.SequenceNode && cv.Style&yaml.FlowStyle == 0
	end := scanEnd(ed.lines, start, ck.Column-1, blockSeq)
	ed.replace(start, end, []string{pad(ck.Column-1) + line})
	return nil
}

// appendTo adds text after the last line of mapping m, which indents its keys at ci.
func (ed *lineEdit) appendTo(m *yaml.Node, ci int, text []string) error {
	ed.insertAfter(scanEnd(ed.lines, lastLine(m), ci-1, false), text)
	return nil
}

// insertAfter puts text after line i. A file with no final newline keeps none.
func (ed *lineEdit) insertAfter(i int, text []string) {
	block := make([]textLine, len(text))
	for k, t := range text {
		block[k] = textLine{text: t, eol: ed.eol}
	}
	if ed.lines[i].eol == "" {
		block[len(block)-1].eol = ""
		ed.lines[i].eol = ed.eol
	}
	ed.lines = slices.Insert(ed.lines, i+1, block...)
}

// replace swaps lines a through b, inclusive, for text.
func (ed *lineEdit) replace(a, b int, text []string) {
	block := make([]textLine, len(text))
	for k, t := range text {
		block[k] = textLine{text: t, eol: ed.eol}
	}
	block[len(block)-1].eol = ed.lines[b].eol
	ed.lines = slices.Replace(ed.lines, a, b+1, block...)
}

// scanEnd is the last line of a value that starts at line start under a key at
// indent: deeper lines belong to it, and trailing comment lines stay out.
func scanEnd(lines []textLine, start, indent int, blockSeq bool) int {
	end := start
	for i := start + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i].text)
		if t == "" {
			continue
		}
		ind := leadingSpaces(lines[i].text)
		if ind > indent || (blockSeq && ind == indent && strings.HasPrefix(t, "-")) {
			end = i
			continue
		}
		break
	}
	for end > start && strings.HasPrefix(strings.TrimSpace(lines[end].text), "#") {
		end--
	}
	return end
}

// entry returns the key and value nodes of key in mapping m, or nils.
func entry(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// lastLine is the 0-based line of the last node under n.
func lastLine(n *yaml.Node) int {
	last := n.Line - 1
	for _, c := range n.Content {
		last = max(last, lastLine(c))
	}
	return last
}

// childIndent is the column the keys of mapping m start at.
func childIndent(m, key *yaml.Node, unit int) int {
	if len(m.Content) > 0 {
		return m.Content[0].Column - 1
	}
	return key.Column - 1 + unit
}

// aliased reports whether an alias under n points at target.
func aliased(n, target *yaml.Node) bool {
	if n.Kind == yaml.AliasNode && n.Alias == target {
		return true
	}
	return slices.ContainsFunc(n.Content, func(c *yaml.Node) bool { return aliased(c, target) })
}

// isEmpty is a null value or a mapping with no keys: a key that owns nothing yet.
func isEmpty(n *yaml.Node) bool {
	return (n.Kind == yaml.ScalarNode && n.Tag == "!!null") || (n.Kind == yaml.MappingNode && len(n.Content) == 0)
}

// indentUnit is the indent of the first nested line, the file's own step.
func indentUnit(lines []textLine) int {
	for _, l := range lines {
		t := strings.TrimSpace(l.text)
		if ind := leadingSpaces(l.text); ind > 0 && t != "" && !strings.HasPrefix(t, "#") {
			return ind
		}
	}
	return 2
}

func leadingSpaces(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func pad(n int) string { return strings.Repeat(" ", max(0, n)) }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// yamlKey quotes a schema name only when YAML needs it.
func yamlKey(name string) string {
	out, err := yaml.Marshal(name)
	if err != nil {
		return name
	}
	return strings.TrimSuffix(string(out), "\n")
}

// columnsLine is `columns: [...]` on one line.
func columnsLine(cols []string) string {
	line, _ := yamlLine("columns", flowList(cols))
	return line
}

// yamlLine renders key: value as one line, the form yaml.v3 gives a flow sequence.
func yamlLine(key string, value *yaml.Node) (string, error) {
	out, err := yaml.Marshal(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalarNode(key), value}})
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
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
