package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
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

// makeKind is the Target.Kind of a Makefile target.
const makeKind = "make"

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
	// Queries are the effective saved queries: the group's, overlaid with the target's own.
	Queries map[string]string
	// ownQueries are the target's `queries:` entries alone, and groupQueries the
	// ones its group handed down. groupQueryCols are the group's columns, which
	// the group's queries resolve their field names against.
	ownQueries     map[string]string
	groupQueries   map[string]string
	groupQueryCols []Column
	// Schema names the top-level `schemas:` entry the target uses, or "".
	Schema   string
	Delay    time.Duration
	Cooldown time.Duration
	Grace    time.Duration
	// Start is when the target first runs: startAuto at session start,
	// startOnChange at the first file change, startManual on r only.
	Start startMode
	// startSet is set when the target's own or its group's `start:` fixed Start,
	// so an import's start does not override it.
	startSet bool
	// NoGitignore turns off .gitignore filtering for this target.
	NoGitignore bool
	// Selected is false for `selected: false` targets: they exist in the
	// list but start off until they are started by hand.
	Selected bool
	// Open asks rerun to open URL in the browser on the first start.
	Open bool
	// Muted is the `muted:` value, or nil when unset so the group's value can fill in.
	Muted *bool
	// GroupHide is set when the target's group is `hide: true`: its list header starts folded.
	GroupHide bool
	// ImportHide is set when the target's import is `hide: true`: the import section folds, not its sub-group.
	ImportHide bool
	// AutoHide lists the target only while it runs, starts, restarts or fails; see tui_list.go.
	AutoHide bool
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

// mutedAtStart is the stream state a row starts in: its `muted:` value, else unmuted.
func (t Target) mutedAtStart() bool { return t.Muted != nil && *t.Muted }

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
	// MakeOnly is set when no rerun.yml exists and the Makefile in its dir is the whole config.
	MakeOnly bool
	// Queries are the top-level saved queries, shared by every pane scope.
	Queries map[string]string
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
	path, err := findConfig(startDir)
	if err != nil {
		if !errors.Is(err, ErrConfigNotFound) {
			return nil, err
		}
		start, absErr := filepath.Abs(startDir)
		if absErr != nil {
			return nil, absErr
		}
		if mk := implicitMakefile(start); mk != "" {
			return makeOnlyConfig(filepath.Join(start, ConfigFileName), mk)
		}
		return nil, &configNotFoundError{dir: startDir}
	}
	return ParseConfig(path)
}

// findConfig is the nearest rerun.yml at or above startDir.
func findConfig(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, ConfigFileName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
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
	return "", &configNotFoundError{dir: startDir}
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
	if errors.Is(err, fs.ErrNotExist) {
		// A reload after rerun.yml is deleted keeps the Makefile running, not an error.
		if mk := implicitMakefile(filepath.Dir(abs)); mk != "" {
			return makeOnlyConfig(abs, mk)
		}
	}
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
	cfg := &Config{Path: path, Targets: file.targets, Files: []string{abs}, Queries: file.queries}
	if file.legacy {
		cfg.Notices = append(cfg.Notices, fmt.Sprintf("%s %s", filepath.Base(abs), migrateNoticeText))
	}
	if err := cfg.parseImports(src, mappingValue(root, importsKey), abs, file.schemas); err != nil {
		return nil, err
	}
	if err := cfg.validateQueries(); err != nil {
		return nil, err
	}

	if len(cfg.Targets) == 0 && len(cfg.Missing) == 0 {
		return nil, src.at(root, nodeSpan(root), "no targets defined")
	}
	return cfg, nil
}

// makeOnlyConfig is the config for a dir with a Makefile and no rerun.yml. path is where the rerun.yml would be, so a later one reloads.
func makeOnlyConfig(path, mk string) (*Config, error) {
	cfg := &Config{Path: path, Files: []string{path}, MakeOnly: true}
	if err := cfg.parseImports(&configSource{path: path}, nil, path, nil); err != nil {
		return nil, err
	}
	if len(cfg.Targets) == 0 {
		return nil, fmt.Errorf("%s: no targets defined; add a rule or write rerun.yml", mk)
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
	schemas map[string]groupDef
	queries map[string]string
	// legacy is set for a file whose targets sit at the top level: no `targets:` key, at least one top-level target.
	legacy bool
}

// groupDef is one `groups:` entry: its columns, and the `muted:`, `hide:` and `start:` its members start with.
type groupDef struct {
	columns []Column
	queries map[string]string
	muted   *bool
	hide    bool
	// hideSet tells `hide: false` apart from no hide key, which Makefile groups read differently.
	hideSet bool
	// autoHide and autoHideSet are the group's `auto-hide:`, which hides idle members from the list.
	autoHide    bool
	autoHideSet bool
	// start and startSet carry the group's `start:` default to its members.
	start    startMode
	startSet bool
}

// parseFileBody reads a file's groups and targets in the form the file writes:
// under `targets:`, or, for a file with no such key, at the top level.
func parseFileBody(src *configSource, root *yaml.Node, file string) (parsedFile, error) {
	schemas, err := parseSchemas(src, root)
	if err != nil {
		return parsedFile{}, err
	}
	out := parsedFile{schemas: schemas}
	if _, block := mappingEntry(root, queriesKey); block != nil {
		out.queries, err = parseQueries(src, queriesKey, block, nil)
		if err != nil {
			return parsedFile{}, err
		}
	}

	if key, block := mappingEntry(root, targetsKey); block != nil {
		// A legacy target named `targets` reads as the new form's block, so catch it first.
		if looksLikeTarget(block) {
			return parsedFile{}, src.at(key, len(key.Value), errTargetsReserved)
		}
		if err := checkTopLevel(src, root); err != nil {
			return parsedFile{}, err
		}
		if block.Kind == yaml.ScalarNode && block.Tag == "!!null" {
			return out, nil
		}
		if block.Kind != yaml.MappingNode {
			return parsedFile{}, src.at(block, nodeSpan(block), "targets: expected a mapping of target names")
		}
		out.targets, err = parseTargets(src, block, schemas, file, false)
		return out, err
	}

	out.legacy = hasTopLevelTargets(root)
	out.targets, err = parseTargets(src, root, schemas, file, true)
	return out, err
}

// checkTopLevel rejects any top-level key that is not rerun's own, so a typo
// surfaces instead of turning into a silently ignored target.
func checkTopLevel(src *configSource, root *yaml.Node) error {
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := deref(root.Content[i])
		if key.Kind != yaml.ScalarNode {
			return src.at(key, 1, "top-level keys must be strings")
		}
		switch key.Value {
		case targetsKey, importsKey, groupsBlock.block, schemasBlock.block, queriesKey:
			continue
		}
		e := src.at(key, len(key.Value), fmt.Sprintf("unknown top-level key %q", key.Value))
		e.note = "top-level keys: imports, groups, schemas, targets, queries"
		return e
	}
	return nil
}

// looksLikeTarget tells a legacy target named `targets` apart from the new form's block.
func looksLikeTarget(node *yaml.Node) bool {
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := deref(node.Content[i]), deref(node.Content[i+1])
		if key.Kind == yaml.ScalarNode && slices.Contains(targetFields, key.Value) && value.Kind != yaml.MappingNode {
			return true
		}
	}
	return false
}

// hasTopLevelTargets reports whether a file without `targets:` holds an actual target at the top level.
func hasTopLevelTargets(root *yaml.Node) bool {
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := deref(root.Content[i])
		if key.Kind != yaml.ScalarNode {
			return true
		}
		switch key.Value {
		case importsKey, groupsBlock.block, schemasBlock.block, queriesKey:
			continue
		}
		return true
	}
	return false
}

// parseTargets reads the target entries of a mapping. In a legacy file the same
// mapping also holds the reserved keys, which are skipped.
func parseTargets(src *configSource, block *yaml.Node, schemas map[string]groupDef, file string, legacy bool) ([]Target, error) {
	var out []Target
	seen := make(map[string]bool)
	for i := 0; i+1 < len(block.Content); i += 2 {
		key, value := deref(block.Content[i]), deref(block.Content[i+1])
		if key.Kind != yaml.ScalarNode {
			return nil, src.at(key, 1, "target names must be strings")
		}
		name := key.Value
		if seen[name] {
			return nil, src.at(key, len(name), fmt.Sprintf("duplicate target %q", name))
		}
		seen[name] = true
		if legacy {
			if name == importsKey && value.Kind != yaml.SequenceNode && !(value.Kind == yaml.ScalarNode && value.Tag == "!!null") {
				return nil, src.at(key, len(name), "`imports` is reserved; rename the target")
			}
			if name == groupsBlock.block || name == schemasBlock.block || name == importsKey || name == queriesKey {
				continue
			}
		}

		target, err := parseTarget(src, name, key, value, schemas)
		if err != nil {
			return nil, err
		}
		target.File = file
		target.Dir = filepath.Dir(file)
		if target.Schema != "" {
			target.SchemaFile = file
			target.SchemaLocal = target.Schema
		}
		out = append(out, target)
	}
	return out, nil
}

// mappingEntry returns the key and value nodes of key in a mapping, or nils.
func mappingEntry(root *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := deref(root.Content[i]); k.Kind == yaml.ScalarNode && k.Value == key {
			return k, deref(root.Content[i+1])
		}
	}
	return nil, nil
}

// mappingValue is the value node of key in a mapping, or nil.
func mappingValue(root *yaml.Node, key string) *yaml.Node {
	_, value := mappingEntry(root, key)
	return value
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

// targetsKey is the reserved top-level key holding the targets, the new form.
const targetsKey = "targets"

// queriesKey is the reserved key holding saved query pills, at the top level,
// in a group, and in a target.
const queriesKey = "queries"

// parseQueries reads a `queries:` mapping of name -> pill string. Every value
// must parse with cols, the columns its scope resolves field names against.
func parseQueries(src *configSource, owner string, node *yaml.Node, cols []Column) (map[string]string, error) {
	node = deref(node)
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, src.at(node, nodeSpan(node), fmt.Sprintf("%s: expected a mapping of query names", owner))
	}
	out := make(map[string]string)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := deref(node.Content[i]), deref(node.Content[i+1])
		if key.Kind != yaml.ScalarNode {
			return nil, src.at(key, 1, owner+": query names must be strings")
		}
		name := key.Value
		if _, ok := out[name]; ok {
			return nil, src.at(key, len(name), fmt.Sprintf("%s: duplicate query %q", owner, name))
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, src.at(value, nodeSpan(value), fmt.Sprintf("%s: query %q: expected a string, got %s", owner, name, yamlType(value)))
		}
		if _, err := parsePills(value.Value, cols); err != nil {
			return nil, src.at(value, nodeSpan(value), fmt.Sprintf("%s: query %q: %v", owner, name, err))
		}
		out[name] = value.Value
	}
	return out, nil
}

// mergeQueries overlays b on a; the more specific b wins a name clash.
func mergeQueries(a, b map[string]string) map[string]string {
	if len(a) == 0 {
		return b
	}
	out := maps.Clone(a)
	for k, v := range b {
		out[k] = v
	}
	return out
}

// queryRef is one saved query and the columns its field names resolve against.
type queryRef struct {
	query string
	cols  []Column
}

// maxQueryDepth caps a chain of saved queries referring to saved queries.
const maxQueryDepth = 8

// validateQueries resolves every saved query's @refs in its effective scope at
// load: an unknown name, a cycle, or a chain past maxQueryDepth is a config
// error, not an inert pill that matches every line.
func (cfg *Config) validateQueries() error {
	global := func(name string) (queryRef, bool) {
		q, ok := cfg.Queries[name]
		return queryRef{q, nil}, ok
	}
	check := func(owner string, names map[string]string, lookup func(string) (queryRef, bool)) error {
		for _, name := range slices.Sorted(maps.Keys(names)) {
			if err := checkQueryRef(lookup, nil, name); err != nil {
				return fmt.Errorf("%s: %s: query %q: %v", cfg.Path, owner, name, err)
			}
		}
		return nil
	}
	if err := check(queriesKey, cfg.Queries, global); err != nil {
		return err
	}
	for _, t := range cfg.Targets {
		lookup := func(name string) (queryRef, bool) {
			if q, ok := t.ownQueries[name]; ok {
				return queryRef{q, t.Columns}, true
			}
			if q, ok := t.groupQueries[name]; ok {
				return queryRef{q, t.groupQueryCols}, true
			}
			return global(name)
		}
		if err := check(fmt.Sprintf("target %q", t.Name), t.Queries, lookup); err != nil {
			return err
		}
	}
	return nil
}

// checkQueryRef walks one saved query's @refs; stack holds the chain so a
// cycle names itself instead of recursing forever.
func checkQueryRef(lookup func(string) (queryRef, bool), stack []string, name string) error {
	ref, ok := lookup(name)
	if !ok {
		return fmt.Errorf("unknown query @%s", name)
	}
	if slices.Contains(stack, name) {
		return fmt.Errorf("@%s refers to itself", name)
	}
	if len(stack) > maxQueryDepth {
		return fmt.Errorf("@%s nests saved queries past %d levels", name, maxQueryDepth)
	}
	pills, err := parsePills(ref.query, ref.cols)
	if err != nil {
		return err
	}
	for _, p := range pills {
		if p.saved == "" {
			continue
		}
		if err := checkQueryRef(lookup, append(stack, name), p.saved); err != nil {
			return err
		}
	}
	return nil
}

// migrateNoticeText is the footer notice a file in the legacy form shows.
const migrateNoticeText = "uses top-level targets; run rerun -migrate"

// errTargetsReserved rejects a legacy target named `targets`, which reads as the new form's block.
const errTargetsReserved = "targets is reserved; rename the target or move targets under targets:"

// schemaFields is the accepted keys inside a group entry.
var schemaFields = []string{"columns", "queries", "muted", "hide", "auto-hide", "start"}

// parseSchemas reads the top-level `groups:` or `schemas:` block into name ->
// group. The two spellings cannot both appear. A null block is an empty one,
// and a group with no columns, null or `{}`, is valid.
func parseSchemas(src *configSource, root *yaml.Node) (map[string]groupDef, error) {
	schemas := make(map[string]groupDef)
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
			def, err := parseSchema(src, kw, name, value)
			if err != nil {
				return nil, err
			}
			schemas[name] = def
		}
	}
	return schemas, nil
}

func parseSchema(src *configSource, kw groupBlock, name string, node *yaml.Node) (groupDef, error) {
	node = deref(node)
	owner := fmt.Sprintf("%s %q", kw.field, name)
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return groupDef{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return groupDef{}, src.at(node, nodeSpan(node), fmt.Sprintf("%s: expected a mapping of fields", owner))
	}

	var def groupDef
	var queriesNode *yaml.Node
	seen := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := deref(node.Content[i])
		field := key.Value
		if seen[field] {
			return groupDef{}, src.at(key, len(field), fmt.Sprintf("%s: duplicate field %q", owner, field))
		}
		seen[field] = true

		switch field {
		case "queries":
			// Validated after the loop, so a later columns: still resolves its fields.
			queriesNode = node.Content[i+1]

		case "columns":
			parsed, err := parseColumnList(src, owner, field, deref(node.Content[i+1]))
			if err != nil {
				return groupDef{}, err
			}
			def.columns = parsed

		case "muted":
			b, err := boolField(src, name, field, deref(node.Content[i+1]))
			if err != nil {
				return groupDef{}, err
			}
			def.muted = &b

		case "hide":
			b, err := boolField(src, name, field, deref(node.Content[i+1]))
			if err != nil {
				return groupDef{}, err
			}
			def.hide, def.hideSet = b, true

		case "auto-hide":
			b, err := boolField(src, name, field, deref(node.Content[i+1]))
			if err != nil {
				return groupDef{}, err
			}
			def.autoHide, def.autoHideSet = b, true

		case "start":
			m, err := parseStartMode(src, owner, field, deref(node.Content[i+1]))
			if err != nil {
				return groupDef{}, err
			}
			def.start, def.startSet = m, true

		default:
			e := src.at(key, len(field), fmt.Sprintf("%s: unknown field %q", owner, field))
			e.note = "fields: " + strings.Join(schemaFields, ", ")
			return groupDef{}, e
		}
	}

	if queriesNode != nil {
		var err error
		def.queries, err = parseQueries(src, fmt.Sprintf("%s: field %q", owner, queriesKey), queriesNode, def.columns)
		if err != nil {
			return groupDef{}, err
		}
	}
	return def, nil
}

// targetFields is the accepted rerun.yml target keys, in the order the docs
// list them. It mirrors the switch in parseTarget; TestTargetFieldsAllAccepted
// fails when the two drift apart. `schema` parses too but is not listed.
var targetFields = []string{"desc", "on", "except", "run", "cmd", "build", "health", "url", "columns", "queries", "group", "muted", "hide", "open", "selected", "start", "delay", "cooldown", "grace"}

func parseTarget(src *configSource, name string, keyNode, node *yaml.Node, schemas map[string]groupDef) (Target, error) {
	node = deref(node)
	if node.Kind != yaml.MappingNode {
		return Target{}, src.at(node, nodeSpan(node), fmt.Sprintf("target %q: expected a mapping of fields", name))
	}

	owner := fmt.Sprintf("target %q", name)
	target := Target{Name: name, Delay: defaultDelay, Grace: rerun.DefaultGrace, Selected: true}
	seen := make(map[string]bool)
	var hide *bool
	var queriesNode *yaml.Node
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

		case "queries":
			// Validated after the group merge, so the effective columns resolve its fields.
			queriesNode = value

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
			m, err := parseStartMode(src, owner, field, value)
			if err != nil {
				return Target{}, err
			}
			target.Start, target.startSet = m, true

		case "muted":
			b, err := boolField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			target.Muted = &b

		case "hide":
			b, err := boolField(src, name, field, value)
			if err != nil {
				return Target{}, err
			}
			hide = &b

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

	// Own muted beats own hide, and both beat the group's muted and hide.
	if target.Muted == nil && hide != nil {
		target.Muted = hide
	}
	if target.Schema != "" {
		group := schemas[target.Schema]
		if !seen["columns"] {
			target.Columns = slices.Clone(group.columns)
		}
		target.groupQueries = group.queries
		target.groupQueryCols = group.columns
		target.Queries = group.queries
		if target.Muted == nil {
			target.Muted = group.muted
		}
		if target.Muted == nil && group.hide {
			muted := true
			target.Muted = &muted
		}
		target.GroupHide = group.hide
		if group.autoHideSet {
			target.AutoHide = group.autoHide
		}
		if !target.startSet && group.startSet {
			target.Start, target.startSet = group.start, true
		}
	}
	if queriesNode != nil {
		own, err := parseQueries(src, fmt.Sprintf("%s: field %q", owner, queriesKey), queriesNode, target.Columns)
		if err != nil {
			return Target{}, err
		}
		target.ownQueries = own
		target.Queries = mergeQueries(target.Queries, own)
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

// parseStartMode reads a `start:` value: auto, onchange, or manual. owner names
// the block for the error, such as `target "api"` or `group "backend"`.
func parseStartMode(src *configSource, owner, field string, node *yaml.Node) (startMode, error) {
	node = deref(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return startAuto, src.at(node, nodeSpan(node), fmt.Sprintf("%s: field %q: expected a string, got %s", owner, field, yamlType(node)))
	}
	switch node.Value {
	case "auto":
		return startAuto, nil
	case "onchange":
		return startOnChange, nil
	case "manual":
		return startManual, nil
	default:
		return startAuto, src.at(node, nodeSpan(node), fmt.Sprintf("%s: field %q: expected auto, onchange, or manual, got %q", owner, field, node.Value))
	}
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
