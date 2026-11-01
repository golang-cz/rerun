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

// yqObjectValues evaluates one compiled expression per column against a JSON
// object line and returns the extracted values. ok is false when the line is
// not a JSON object, which falls the row back to its raw text.
func yqObjectValues(exprs []*yqlib.ExpressionNode, line string) ([]string, bool) {
	dec := yqlib.NewJSONDecoder()
	if err := dec.Init(strings.NewReader(line)); err != nil {
		return nil, false
	}
	root, err := dec.Decode()
	if err != nil || root == nil || root.Kind != yqlib.MappingNode {
		return nil, false
	}
	vals := make([]string, len(exprs))
	for i, expr := range exprs {
		vals[i] = yqScalar(expr, root)
	}
	return vals, true
}

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
			return node.Value
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
