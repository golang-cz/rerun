package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang-cz/rerun"
	"gopkg.in/yaml.v3"
)

const ConfigFileName = "rerun.yml"

const defaultDelay = 200 * time.Millisecond

// ErrConfigNotFound reports that no rerun.yml was found on the way up.
var ErrConfigNotFound = errors.New("no rerun.yml found")

type configNotFoundError struct{ dir string }

func (e *configNotFoundError) Error() string {
	return fmt.Sprintf("%v in %s or any parent directory", ErrConfigNotFound, e.dir)
}

func (e *configNotFoundError) Unwrap() error { return ErrConfigNotFound }

// Column is one table-mode column: a display header and the yq expression
// that extracts one value from a JSON output line.
type Column struct {
	Name string
	Expr string
}

type Target struct {
	Name     string
	Desc     string
	On       []string
	Except   []string
	Cmd      string
	URL      string
	Columns  []Column
	Delay    time.Duration
	Cooldown time.Duration
	Grace    time.Duration
	// NoGitignore turns off .gitignore filtering for this target.
	NoGitignore bool
	// Selected is false for `selected: false` targets: they exist in the
	// list but start off until they are started by hand.
	Selected bool
	// Open asks rerun to open URL in the browser on the first start.
	Open bool
}

type Config struct {
	Path    string
	Targets []Target
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Targets))
	for _, t := range c.Targets {
		names = append(names, t.Name)
	}
	return names
}

func (c *Config) Find(name string) (Target, bool) {
	for _, t := range c.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

func LoadConfig(startDir string) (*Config, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, err
	}

	for {
		path := filepath.Join(dir, ConfigFileName)
		if _, err := os.Stat(path); err == nil {
			return ParseConfig(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}

		if isGitRoot(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return nil, &configNotFoundError{dir: startDir}
}

func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// excerptWidth caps the source line echoed in a config error. Longer lines are
// windowed around the offending column so the caret stays aligned.
const excerptWidth = 120

// configSource is the file's text, kept so errors can echo the line they point
// at without reading the file a second time.
type configSource struct {
	path  string
	lines []string
}

func newConfigSource(path string, data []byte) *configSource {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return &configSource{path: path, lines: strings.Split(text, "\n")}
}

// at positions an error at node, underlining span cells of it. A nil node, or
// one with no position, keeps the error on a single line.
func (s *configSource) at(node *yaml.Node, span int, msg string) *configErr {
	e := &configErr{path: s.path, msg: msg, span: span, src: s.lines}
	if node != nil && node.Line > 0 {
		e.line, e.col = node.Line, node.Column
	}
	return e
}

// configErr is a rerun.yml error that points at a line and echoes it.
type configErr struct {
	path string
	line int
	col  int
	span int
	msg  string
	note string
	src  []string
}

func (e *configErr) Error() string {
	var b strings.Builder
	if e.line > 0 {
		fmt.Fprintf(&b, "%s:%d:%d: %s", e.path, e.line, e.col, e.msg)
	} else {
		fmt.Fprintf(&b, "%s: %s", e.path, e.msg)
	}
	if e.line > 0 && e.line <= len(e.src) {
		b.WriteString("\n\n")
		b.WriteString(e.excerpt())
	}
	if e.note != "" {
		b.WriteString("\n")
		b.WriteString(e.note)
	}
	return b.String()
}

// excerpt renders the gutter, the source line and the caret row.
func (e *configErr) excerpt() string {
	cells := []rune(e.src[e.line-1])
	col := e.col - 1
	span := e.span
	if span < 1 {
		span = 1
	}

	start := 0
	if len(cells) > excerptWidth {
		start = col - excerptWidth/2
		if start < 0 {
			start = 0
		}
		if start+excerptWidth > len(cells) {
			start = len(cells) - excerptWidth
		}
	}
	end := len(cells)
	if end > start+excerptWidth {
		end = start + excerptWidth
	}

	caret := col - start
	if caret < 0 {
		caret = 0
	}
	width := span
	if caret+width > end-start {
		width = end - start - caret
	}
	if width < 1 {
		width = 1
	}

	return fmt.Sprintf("%3d | %s\n    | %s%s", e.line, string(cells[start:end]),
		strings.Repeat(" ", caret), strings.Repeat("^", width))
}

// nodeSpan underlines a node's text: a scalar's value, one caret otherwise.
func nodeSpan(node *yaml.Node) int {
	node = deref(node)
	if node == nil || node.Kind != yaml.ScalarNode || node.Value == "" {
		return 1
	}
	return len(node.Value)
}

func ParseConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	src := newConfigSource(path, data)

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s", path, cleanYAMLError(err))
	}

	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, src.at(nil, 0, "no targets defined")
		}
		root = root.Content[0]
	}
	root = deref(root)
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && root.Tag == "!!null") {
		return nil, src.at(nil, 0, "no targets defined")
	}
	if root.Kind != yaml.MappingNode {
		return nil, src.at(root, nodeSpan(root), "expected a mapping of target names")
	}
	if len(root.Content) == 0 {
		return nil, src.at(root, nodeSpan(root), "no targets defined")
	}

	cfg := &Config{Path: path}
	seen := make(map[string]bool)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := deref(root.Content[i]), deref(root.Content[i+1])
		if key.Kind != yaml.ScalarNode {
			return nil, src.at(key, 1, "target names must be strings")
		}
		name := key.Value
		if seen[name] {
			return nil, src.at(key, len(name), fmt.Sprintf("duplicate target %q", name))
		}
		seen[name] = true

		target, err := parseTarget(src, name, key, value)
		if err != nil {
			return nil, err
		}
		cfg.Targets = append(cfg.Targets, target)
	}

	return cfg, nil
}

// targetFields is the accepted rerun.yml target keys, in the order the docs
// list them. It mirrors the switch in parseTarget; TestTargetFieldsMatchParser
// fails when the two drift apart.
var targetFields = []string{"desc", "on", "except", "cmd", "url", "columns", "open", "selected", "delay", "cooldown", "grace"}

func parseTarget(src *configSource, name string, keyNode, node *yaml.Node) (Target, error) {
	node = deref(node)
	if node.Kind != yaml.MappingNode {
		return Target{}, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: expected a mapping of fields", name))
	}

	target := Target{Name: name, Delay: defaultDelay, Grace: rerun.DefaultGrace, Selected: true}
	seen := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := deref(node.Content[i])
		field := key.Value
		value := deref(node.Content[i+1])
		if seen[field] {
			return Target{}, src.at(key, len(field), fmt.Sprintf("target %q: duplicate field %q", name, field))
		}
		seen[field] = true

		switch field {
		case "desc":
			s, err := scalarField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			target.Desc = s

		case "on", "except":
			list, _, err := stringOrList(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "on" {
				target.On = list
			} else {
				target.Except = list
			}

		case "cmd", "url":
			s, err := scalarField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "cmd" {
				target.Cmd = s
			} else {
				target.URL = s
			}

		case "columns":
			list, nodes, err := stringOrList(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			cols := make([]Column, 0, len(list))
			for i, raw := range list {
				col, err := parseColumn(raw)
				if err != nil {
					return Target{}, src.at(nodes[i], len(raw), fmt.Sprintf("target %q: field %q: %s in %q", name, field, err, raw))
				}
				cols = append(cols, col)
			}
			target.Columns = cols

		case "open", "selected":
			b, err := boolField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "open" {
				target.Open = b
			} else {
				target.Selected = b
			}

		case "delay", "cooldown", "grace":
			d, err := durationField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			switch field {
			case "delay":
				target.Delay = d
			case "cooldown":
				target.Cooldown = d
			case "grace":
				target.Grace = d
			}

		default:
			e := src.at(key, len(field), fmt.Sprintf("target %q: unknown field %q", name, field))
			e.note = "fields: " + strings.Join(targetFields, ", ")
			return Target{}, e
		}
	}

	if strings.TrimSpace(target.Cmd) == "" {
		return Target{}, src.at(keyNode, len(name), fmt.Sprintf("target %q: cmd is required", name))
	}
	return target, nil
}

// deref follows `*name` to the anchored node, which may itself be an alias.
func deref(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}

func scalarField(src *configSource, name, field string, node *yaml.Node) (string, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: expected a string, got %s", name, field, yamlType(node)))
	}
	return node.Value, nil
}

func boolField(src *configSource, name, field string, node *yaml.Node) (bool, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: expected a boolean, got %s", name, field, yamlType(node)))
	}
	b, err := strconv.ParseBool(node.Value)
	if err != nil {
		return false, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: expected a boolean, got %q", name, field, node.Value))
	}
	return b, nil
}

// parseColumn splits a `name {.yq.expr}` column entry: the header is
// everything before the first `{`, the expression everything up to the last
// `}`.
func parseColumn(raw string) (Column, error) {
	open := strings.Index(raw, "{")
	if open < 0 {
		return Column{}, errors.New("expected a name and a {.yq.expr}")
	}
	close := strings.LastIndex(raw, "}")
	if close < open {
		return Column{}, errors.New("missing a closing }")
	}
	name := strings.TrimSpace(raw[:open])
	expr := strings.TrimSpace(raw[open+1 : close])
	if name == "" {
		return Column{}, errors.New("empty column name")
	}
	if expr == "" {
		return Column{}, errors.New("empty column expression")
	}
	return Column{Name: name, Expr: expr}, nil
}

// yamlType names a node's type for error messages, e.g. "int" for !!int.
func yamlType(node *yaml.Node) string {
	if node.Tag == "" {
		return "unknown"
	}
	return strings.TrimPrefix(node.Tag, "!!")
}

func stringOrList(src *configSource, name, field string, node *yaml.Node) ([]string, []*yaml.Node, error) {
	node = deref(node)
	switch node.Kind {
	case yaml.ScalarNode:
		// `on:` with no value decodes to a null scalar; that means "no paths".
		if node.Tag == "!!null" || strings.TrimSpace(node.Value) == "" {
			return nil, nil, nil
		}
		if node.Tag != "!!str" {
			return nil, nil, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: expected a string or a list of strings, got %s", name, field, yamlType(node)))
		}
		return []string{node.Value}, []*yaml.Node{node}, nil

	case yaml.SequenceNode:
		list := make([]string, 0, len(node.Content))
		nodes := make([]*yaml.Node, 0, len(node.Content))
		for _, item := range node.Content {
			item = deref(item)
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return nil, nil, src.at(item, nodeSpan(item), fmt.Sprintf("target %q: field %q: expected a list of strings, got %s", name, field, yamlType(item)))
			}
			list = append(list, item.Value)
			nodes = append(nodes, item)
		}
		return list, nodes, nil

	default:
		return nil, nil, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: expected a string or a list of strings", name, field))
	}
}

func durationField(src *configSource, name, field string, node *yaml.Node) (time.Duration, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return 0, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: expected a duration string, got %s", name, field, yamlType(node)))
	}
	d, err := time.ParseDuration(node.Value)
	if err != nil {
		return 0, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: invalid duration %q", name, field, node.Value))
	}
	if d < 0 {
		return 0, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: field %q: duration must not be negative, got %q", name, field, node.Value))
	}
	return d, nil
}

func cleanYAMLError(err error) string {
	return strings.TrimPrefix(err.Error(), "yaml: ")
}
