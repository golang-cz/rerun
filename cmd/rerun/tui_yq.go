package main

import (
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
