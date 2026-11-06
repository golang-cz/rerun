package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// Column is one table-mode column: a display header and the yq expression, or
// template of expressions, that extracts its value from a JSON output line.
type Column struct {
	Name string
	Expr string
	// Template is the raw body after the header when it is more than one
	// placeholder or has literal text; Expr is empty then.
	Template string
}

// body is the column's text after its header, in the form the config file uses.
func (c Column) body() string {
	if c.Template != "" {
		return c.Template
	}
	return "{" + c.Expr + "}"
}

// startMode is when a target first runs. The zero value is startAuto, so a
// Target literal that names no mode behaves exactly like `start: auto`.
type startMode string

const (
	startAuto     startMode = ""
	startOnChange startMode = "onchange"
	startManual   startMode = "manual"
)

type Target struct {
	Name   string
	Desc   string
	On     []string
	Except []string
	// Cmd is the `run:` command, or the older `cmd:` spelling.
	Cmd string
	// Build, when set, runs before each (re)start and gates the swap to a new run.
	Build string
	// Health is an http(s):// URL, a tcp://host:port, or a shell command.
	Health string
	URL    string
	// Columns are the effective columns: the target's own, else its schema's.
	Columns []Column
	// ownColumns is set when the target writes its own `columns:` instead of inheriting.
	ownColumns bool
	// Schema names the top-level `schemas:` entry the target uses, or "".
	Schema   string
	Delay    time.Duration
	Cooldown time.Duration
	Grace    time.Duration
	// Start is when the target first runs: startAuto at session start,
	// startOnChange at the first file change, startManual on r only.
	Start startMode
	// NoGitignore turns off .gitignore filtering for this target.
	NoGitignore bool
	// Selected is false for `selected: false` targets: they exist in the
	// list but start off until they are started by hand.
	Selected bool
	// Open asks rerun to open URL in the browser on the first start.
	Open bool
	// Dir is the defining file's directory: the base for run, build, health and on:.
	Dir string
	// Kind is "compose" for a docker-compose service, or "" for a process.
	Kind string
	// Compose is the service a compose target runs; nil for a process.
	Compose *composeTarget
	// File is the rerun.yml that defines the target; column writes go there.
	File string
	// Import is the import group the target came from, or "" for the root file.
	Import string
	// Sub is the sub-group a list shows inside an import: the imported file's group or a compose profile, prefixed with the import name; "" for none.
	Sub string
	// GroupImport is the import a root target's group names, so it lists in that import's section.
	GroupImport string
	// SchemaFile and SchemaLocal locate Schema's definition: its file and its name in that file.
	SchemaFile  string
	SchemaLocal string
}

// section is the import whose list section the target lists under, or "".
func (t Target) section() string {
	if t.Import != "" {
		return t.Import
	}
	return t.GroupImport
}

// localName is the target's name as its own file writes it, without the import prefix.
func (t Target) localName() string {
	if t.Import == "" {
		return t.Name
	}
	return strings.TrimPrefix(t.Name, t.Import+"/")
}

// missingImport is an optional import whose file does not exist yet.
type missingImport struct {
	Group string
	Path  string
}

type Config struct {
	Path    string
	Targets []Target
	// Files is every rerun.yml read, the root first.
	Files []string
	// Missing lists the optional imports whose file is absent.
	Missing []missingImport
	// Notices are warnings that do not stop the config from loading.
	Notices []string
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

// ParseConfig reads a rerun.yml and every file its imports pull in.
func ParseConfig(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src, root, err := parseConfigBytes(path, data)
	if err != nil {
		return nil, err
	}

	file, err := parseFileBody(src, root, abs)
	if err != nil {
		return nil, err
	}
	cfg := &Config{Path: path, Targets: file.targets, Files: []string{abs}}
	if imports := mappingValue(root, importsKey); imports != nil {
		if err := cfg.parseImports(src, imports, abs, file.schemas); err != nil {
			return nil, err
		}
	}

	if len(cfg.Targets) == 0 && len(cfg.Missing) == 0 {
		return nil, src.at(root, nodeSpan(root), "no targets defined")
	}
	return cfg, nil
}

// parseConfigBytes parses one rerun.yml and returns its top-level mapping.
func parseConfigBytes(path string, data []byte) (*configSource, *yaml.Node, error) {
	src := newConfigSource(path, data)

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %s", path, cleanYAMLError(err))
	}

	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, nil, src.at(nil, 0, "no targets defined")
		}
		root = root.Content[0]
	}
	root = deref(root)
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && root.Tag == "!!null") {
		return nil, nil, src.at(nil, 0, "no targets defined")
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, src.at(root, nodeSpan(root), "expected a mapping of target names")
	}
	if len(root.Content) == 0 {
		return nil, nil, src.at(root, nodeSpan(root), "no targets defined")
	}
	return src, root, nil
}

// parsedFile is one file's own targets and groups, before imports are merged in.
type parsedFile struct {
	targets []Target
	schemas map[string][]Column
}

// parseFileBody reads a file's groups and targets under the names the file writes.
func parseFileBody(src *configSource, root *yaml.Node, file string) (parsedFile, error) {
	schemas, err := parseSchemas(src, root)
	if err != nil {
		return parsedFile{}, err
	}

	var out parsedFile
	out.schemas = schemas
	seen := make(map[string]bool)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := deref(root.Content[i]), deref(root.Content[i+1])
		if key.Kind != yaml.ScalarNode {
			return parsedFile{}, src.at(key, 1, "target names must be strings")
		}
		name := key.Value
		if seen[name] {
			return parsedFile{}, src.at(key, len(name), fmt.Sprintf("duplicate target %q", name))
		}
		seen[name] = true
		if name == importsKey && value.Kind != yaml.SequenceNode && !(value.Kind == yaml.ScalarNode && value.Tag == "!!null") {
			return parsedFile{}, src.at(key, len(name), "`imports` is reserved; rename the target")
		}
		if name == groupsBlock.block || name == schemasBlock.block || name == importsKey {
			continue
		}

		target, err := parseTarget(src, name, key, value, schemas)
		if err != nil {
			return parsedFile{}, err
		}
		target.File = file
		target.Dir = filepath.Dir(file)
		if target.Schema != "" {
			target.SchemaFile = file
			target.SchemaLocal = target.Schema
		}
		out.targets = append(out.targets, target)
	}
	return out, nil
}

// mappingValue is the value node of key in a mapping, or nil.
func mappingValue(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := deref(root.Content[i]); k.Kind == yaml.ScalarNode && k.Value == key {
			return deref(root.Content[i+1])
		}
	}
	return nil
}

// groupBlock is one spelling of the top-level group block and of the target key
// that names a group. `groups`/`group` is primary; `schemas`/`schema` still parses.
type groupBlock struct {
	block string
	field string
}

var (
	groupsBlock  = groupBlock{block: "groups", field: "group"}
	schemasBlock = groupBlock{block: "schemas", field: "schema"}
)

// schemaFields is the accepted keys inside a group entry.
var schemaFields = []string{"columns"}

// parseSchemas reads the top-level `groups:` or `schemas:` block into name ->
// columns. The two spellings cannot both appear. A null block is an empty one,
// and a group with no columns, null or `{}`, is valid.
func parseSchemas(src *configSource, root *yaml.Node) (map[string][]Column, error) {
	schemas := make(map[string][]Column)
	var kw groupBlock
	found := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := deref(root.Content[i])
		if key.Kind != yaml.ScalarNode || (key.Value != groupsBlock.block && key.Value != schemasBlock.block) {
			continue
		}
		if found {
			if key.Value != kw.block {
				return nil, src.at(key, len(key.Value), fmt.Sprintf("both %q and %q are set; use one", kw.block, key.Value))
			}
			return nil, src.at(key, len(key.Value), fmt.Sprintf("duplicate top-level key %q", key.Value))
		}
		found = true
		kw = groupsBlock
		if key.Value == schemasBlock.block {
			kw = schemasBlock
		}

		block := deref(root.Content[i+1])
		if block.Kind == yaml.ScalarNode && block.Tag == "!!null" {
			continue
		}
		if block.Kind != yaml.MappingNode {
			return nil, src.at(block, nodeSpan(block), fmt.Sprintf("%s: expected a mapping of %s names", kw.block, kw.field))
		}
		for j := 0; j+1 < len(block.Content); j += 2 {
			nameNode, value := deref(block.Content[j]), deref(block.Content[j+1])
			if nameNode.Kind != yaml.ScalarNode {
				return nil, src.at(nameNode, 1, kw.field+" names must be strings")
			}
			name := nameNode.Value
			if _, ok := schemas[name]; ok {
				return nil, src.at(nameNode, len(name), fmt.Sprintf("duplicate %s %q", kw.field, name))
			}
			cols, err := parseSchema(src, kw, name, value)
			if err != nil {
				return nil, err
			}
			schemas[name] = cols
		}
	}
	return schemas, nil
}

func parseSchema(src *configSource, kw groupBlock, name string, node *yaml.Node) ([]Column, error) {
	node = deref(node)
	owner := fmt.Sprintf("%s %q", kw.field, name)
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, src.at(node, nodeSpan(node), fmt.Sprintf("%s: expected a mapping of fields", owner))
	}

	var cols []Column
	seen := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := deref(node.Content[i])
		field := key.Value
		if seen[field] {
			return nil, src.at(key, len(field), fmt.Sprintf("%s: duplicate field %q", owner, field))
		}
		seen[field] = true

		switch field {
		case "columns":
			parsed, err := parseColumnList(src, owner, field, deref(node.Content[i+1]))
			if err != nil {
				return nil, err
			}
			cols = parsed

		default:
			e := src.at(key, len(field), fmt.Sprintf("%s: unknown field %q", owner, field))
			e.note = "fields: " + strings.Join(schemaFields, ", ")
			return nil, e
		}
	}

	return cols, nil
}

// targetFields is the accepted rerun.yml target keys, in the order the docs
// list them. It mirrors the switch in parseTarget; TestTargetFieldsAllAccepted
// fails when the two drift apart. `schema` parses too but is not listed.
var targetFields = []string{"desc", "on", "except", "run", "cmd", "build", "health", "url", "columns", "group", "open", "selected", "start", "delay", "cooldown", "grace"}

func parseTarget(src *configSource, name string, keyNode, node *yaml.Node, schemas map[string][]Column) (Target, error) {
	node = deref(node)
	if node.Kind != yaml.MappingNode {
		return Target{}, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: expected a mapping of fields", name))
	}

	owner := fmt.Sprintf("target %q", name)
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
		if seen["run"] && seen["cmd"] {
			return Target{}, src.at(key, len(field), fmt.Sprintf("target %q: both \"run\" and \"cmd\" are set; use one", name))
		}
		if seen["group"] && seen["schema"] {
			return Target{}, src.at(key, len(field), fmt.Sprintf("target %q: both \"group\" and \"schema\" are set; use one", name))
		}

		switch field {
		case "desc":
			s, err := scalarField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			target.Desc = s

		case "on", "except":
			list, _, err := stringOrList(src, owner, field, value)
			if err != nil {
				return Target{}, err
			}
			if field == "on" {
				target.On = list
			} else {
				target.Except = list
			}

		case "run", "cmd", "build", "health", "url":
			s, err := scalarField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			switch field {
			case "run", "cmd":
				target.Cmd = s
			case "build":
				target.Build = s
			case "health":
				target.Health = s
			default:
				target.URL = s
			}

		case "columns":
			cols, err := parseColumnList(src, owner, field, value)
			if err != nil {
				return Target{}, err
			}
			target.Columns = cols
			target.ownColumns = true

		case "group", "schema":
			s, err := scalarField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			if _, ok := schemas[s]; !ok {
				return Target{}, src.at(value, nodeSpan(value), fmt.Sprintf("target %q: field %q: unknown %s %q", name, field, field, s))
			}
			target.Schema = s

		case "start":
			s, err := scalarField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			switch s {
			case "auto":
				target.Start = startAuto
			case "onchange":
				target.Start = startOnChange
			case "manual":
				target.Start = startManual
			default:
				return Target{}, src.at(value, nodeSpan(value), fmt.Sprintf("target %q: field %q: expected auto, onchange, or manual, got %q", name, field, s))
			}

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

	if target.Schema != "" && !seen["columns"] {
		target.Columns = slices.Clone(schemas[target.Schema])
	}
	if strings.TrimSpace(target.Cmd) == "" {
		return Target{}, src.at(keyNode, len(name), fmt.Sprintf("target %q: run is required", name))
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
// everything before the first `{`. The rest is a template of literal text and
// `{expr}` placeholders; a body that is one placeholder keeps only its expression.
func parseColumn(raw string) (Column, error) {
	open := strings.Index(raw, "{")
	if open < 0 {
		return Column{}, errors.New("expected a name and a {.yq.expr}")
	}
	name := strings.TrimSpace(raw[:open])
	if name == "" {
		return Column{}, errors.New("empty column name")
	}
	body := strings.TrimSpace(raw[open:])
	segs, err := templateSegments(body)
	if err != nil {
		return Column{}, err
	}
	for _, s := range segs {
		if s.expr && s.text == "" {
			return Column{}, errors.New("empty column expression")
		}
	}
	if len(segs) == 1 {
		return Column{Name: name, Expr: segs[0].text}, nil
	}
	return Column{Name: name, Template: body}, nil
}

// templateSeg is one run of a column template: literal text, or a {expr} placeholder.
type templateSeg struct {
	text string
	expr bool
}

// templateSegments splits a column template into literal runs and placeholders.
// Braces nest, because yq expressions may build objects, and a quoted string
// inside a placeholder may hold braces.
func templateSegments(s string) ([]templateSeg, error) {
	var segs []templateSeg
	lit := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '}' {
			return nil, errors.New("unexpected }")
		}
		if s[i] != '{' {
			continue
		}
		end, err := closingBrace(s, i)
		if err != nil {
			return nil, err
		}
		if i > lit {
			segs = append(segs, templateSeg{text: s[lit:i]})
		}
		segs = append(segs, templateSeg{text: strings.TrimSpace(s[i+1 : end]), expr: true})
		lit = end + 1
		i = end
	}
	if lit < len(s) {
		segs = append(segs, templateSeg{text: s[lit:]})
	}
	return segs, nil
}

// closingBrace returns the index of the } that closes the { at open.
func closingBrace(s string, open int) (int, error) {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, errors.New("missing a closing }")
}

// yamlType names a node's type for error messages, e.g. "int" for !!int.
func yamlType(node *yaml.Node) string {
	if node.Tag == "" {
		return "unknown"
	}
	return strings.TrimPrefix(node.Tag, "!!")
}

// parseColumnList reads a `columns:` value, a list of `name {.yq.expr}` entries.
func parseColumnList(src *configSource, owner, field string, node *yaml.Node) ([]Column, error) {
	list, nodes, err := stringOrList(src, owner, field, node)
	if err != nil {
		return nil, err
	}
	cols := make([]Column, 0, len(list))
	for i, raw := range list {
		col, err := parseColumn(raw)
		if err != nil {
			return nil, src.at(nodes[i], len(raw), fmt.Sprintf("%s: field %q: %s in %q", owner, field, err, raw))
		}
		cols = append(cols, col)
	}
	return cols, nil
}

// stringOrList reads a string or a list of strings. owner names the entry in
// error messages, e.g. `target "api"`.
func stringOrList(src *configSource, owner, field string, node *yaml.Node) ([]string, []*yaml.Node, error) {
	node = deref(node)
	switch node.Kind {
	case yaml.ScalarNode:
		// `on:` with no value decodes to a null scalar; that means "no paths".
		if node.Tag == "!!null" || strings.TrimSpace(node.Value) == "" {
			return nil, nil, nil
		}
		if node.Tag != "!!str" {
			return nil, nil, src.at(node, nodeSpan(node), fmt.Sprintf("%s: field %q: expected a string or a list of strings, got %s", owner, field, yamlType(node)))
		}
		return []string{node.Value}, []*yaml.Node{node}, nil

	case yaml.SequenceNode:
		list := make([]string, 0, len(node.Content))
		nodes := make([]*yaml.Node, 0, len(node.Content))
		for _, item := range node.Content {
			item = deref(item)
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return nil, nil, src.at(item, nodeSpan(item), fmt.Sprintf("%s: field %q: expected a list of strings, got %s", owner, field, yamlType(item)))
			}
			list = append(list, item.Value)
			nodes = append(nodes, item)
		}
		return list, nodes, nil

	default:
		return nil, nil, src.at(node, nodeSpan(node), fmt.Sprintf("%s: field %q: expected a string or a list of strings", owner, field))
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
