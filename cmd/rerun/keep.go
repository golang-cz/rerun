package main

import (
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// defaultKeepLines is the scrollback a target keeps when no keep: says otherwise.
const defaultKeepLines = 10_000

// keepAgeLineCap bounds a time-based keep, so a chatty target cannot grow without limit.
const keepAgeLineCap = 200_000

// keepSpec is a `keep:` value: a line count, or an age with a hidden line cap.
type keepSpec struct {
	lines int
	age   time.Duration
}

// lineCap is the most lines the spec holds.
func (k keepSpec) lineCap() int {
	switch {
	case k.age > 0:
		return keepAgeLineCap
	case k.lines > 0:
		return k.lines
	}
	return defaultKeepLines
}

// keep is the target's effective retention.
func (t Target) keep() keepSpec {
	if t.Keep != nil {
		return *t.Keep
	}
	return keepSpec{lines: defaultKeepLines}
}

// keepField reads a `keep:` value: a line count like 5000, or a duration like 1h.
func keepField(src *configSource, owner, field string, node *yaml.Node) (keepSpec, error) {
	node = deref(node)
	bad := func(format string, args ...any) (keepSpec, error) {
		return keepSpec{}, src.at(node, nodeSpan(node), fmt.Sprintf("%s: field %q: ", owner, field)+fmt.Sprintf(format, args...))
	}
	if node.Kind != yaml.ScalarNode {
		return bad("expected a line count like 5000 or a duration like 1h, got %s", yamlType(node))
	}
	switch node.Tag {
	case "!!int":
		n, err := strconv.Atoi(node.Value)
		if err != nil || n < 1 {
			return bad("a line count must be at least 1, got %q", node.Value)
		}
		return keepSpec{lines: n}, nil
	case "!!str":
		d, err := time.ParseDuration(node.Value)
		if err != nil {
			return bad("expected a line count like 5000 or a duration like 1h, got %q", node.Value)
		}
		if d <= 0 {
			return bad("a duration must be positive, got %q", node.Value)
		}
		return keepSpec{age: d}, nil
	}
	return bad("expected a line count like 5000 or a duration like 1h, got %s", yamlType(node))
}
