package main

import (
	"bytes"
	"container/list"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// yqNav evaluates parsed expressions; the parser is package state that must be
// initialized before ParseExpression.
var yqNav = yqlib.NewDataTreeNavigator()

func init() { yqlib.InitExpressionParser() }

// yqParse compiles a filter expression once per pane rebuild.
func yqParse(expr string) (*yqlib.ExpressionNode, error) {
	return yqlib.ExpressionParser.ParseExpression(expr)
}

// yqMatches reports whether one raw pane line is kept by the expression. A
// line that is not JSON, or that the expression cannot evaluate, is dropped.
func yqMatches(expr *yqlib.ExpressionNode, line string) bool {
	dec := yqlib.NewJSONDecoder()
	if err := dec.Init(strings.NewReader(line)); err != nil {
		return false
	}
	node, err := dec.Decode()
	if err != nil {
		return false
	}
	in := list.New()
	in.PushBack(node)
	out, err := yqNav.GetMatchingNodes(yqlib.Context{MatchingNodes: in}, expr)
	if err != nil {
		return false
	}
	for el := out.MatchingNodes.Front(); el != nil; el = el.Next() {
		node, ok := el.Value.(*yqlib.CandidateNode)
		if !ok {
			continue
		}
		if yqTruthy(node) {
			return true
		}
	}
	return false
}

// columnProgram is one compiled column: a single expression, or a template
// whose parts alternate literal text and expressions.
type columnProgram struct {
	template bool
	parts    []columnPart
}

// columnPart is literal text when expr is nil, otherwise one compiled placeholder.
type columnPart struct {
	lit  string
	expr *yqlib.ExpressionNode
}

func compileColumn(col Column) (columnProgram, error) {
	if col.Template == "" {
		expr, err := yqParse(col.Expr)
		if err != nil {
			return columnProgram{}, err
		}
		return columnProgram{parts: []columnPart{{expr: expr}}}, nil
	}
	segs, err := templateSegments(col.Template)
	if err != nil {
		return columnProgram{}, err
	}
	prog := columnProgram{template: true}
	for _, s := range segs {
		if !s.expr {
			prog.parts = append(prog.parts, columnPart{lit: s.text})
			continue
		}
		expr, err := yqParse(s.text)
		if err != nil {
			return columnProgram{}, err
		}
		prog.parts = append(prog.parts, columnPart{expr: expr})
	}
	return prog, nil
}

// value renders the column for one decoded line. A template joins its parts,
// then collapses whitespace, so a missing placeholder leaves no double space.
// When no placeholder has a value, its literal text is dropped too.
func (c columnProgram) value(root *yqlib.CandidateNode) string {
	if !c.template {
		return yqScalar(c.parts[0].expr, root)
	}
	var b strings.Builder
	filled := false
	for _, p := range c.parts {
		if p.expr == nil {
			b.WriteString(p.lit)
			continue
		}
		v := yqScalar(p.expr, root)
		filled = filled || v != ""
		b.WriteString(v)
	}
	if !filled {
		return ""
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// yqObjectValues evaluates one compiled column per table column against a JSON
// object line and returns the extracted values. ok is false when the line is
// not a JSON object, which falls the row back to its raw text.
func yqObjectValues(cols []columnProgram, line string) ([]string, bool) {
	dec := yqlib.NewJSONDecoder()
	if err := dec.Init(strings.NewReader(line)); err != nil {
		return nil, false
	}
	root, err := dec.Decode()
	if err != nil || root == nil || root.Kind != yqlib.MappingNode {
		return nil, false
	}
	vals := make([]string, len(cols))
	for i, col := range cols {
		vals[i] = col.value(root)
	}
	return vals, true
}

var cellSpaces = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")

// yqScalar runs one expression over a decoded line and renders its first
// non-null result: a scalar keeps its text, a map or sequence becomes compact
// JSON. A missing value is the empty cell.
func yqScalar(expr *yqlib.ExpressionNode, root *yqlib.CandidateNode) string {
	in := list.New()
	in.PushBack(root)
	out, err := yqNav.GetMatchingNodes(yqlib.Context{MatchingNodes: in}, expr)
	if err != nil {
		return ""
	}
	for el := out.MatchingNodes.Front(); el != nil; el = el.Next() {
		node, ok := el.Value.(*yqlib.CandidateNode)
		if !ok || node == nil || node.Tag == "!!null" {
			continue
		}
		if node.Kind == yqlib.ScalarNode {
			// A tab or newline in a value would break the row's columns on screen.
			return cellSpaces.Replace(node.Value)
		}
		var buf bytes.Buffer
		if err := yqlib.NewJSONEncoder(yqlib.JsonPreferences{}).Encode(&buf, node); err != nil {
			continue
		}
		return strings.TrimRight(buf.String(), "\n")
	}
	return ""
}

// yqTruthy mirrors yqlib's own select semantics: null is false, a bool is what
// it says, anything else is true.
func yqTruthy(node *yqlib.CandidateNode) bool {
	if node == nil || node.Tag == "!!null" {
		return false
	}
	if node.Kind == yqlib.ScalarNode && node.Tag == "!!bool" {
		switch strings.ToLower(node.Value) {
		case "y", "yes", "on", "true":
			return true
		}
		return false
	}
	return true
}
