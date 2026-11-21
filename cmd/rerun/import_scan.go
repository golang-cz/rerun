package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/denormal/go-gitignore"
	"gopkg.in/yaml.v3"
)

// rerunKind is the importCandidate.Kind of a rerun.yml file.
const rerunKind = "rerun"

// Caps keep a huge checkout from opening a picker that takes seconds to draw.
const (
	importScanMaxDepth = 4
	importScanMaxFiles = 200
	// importProbeMaxDirs bounds the look past the depth cap for a candidate.
	importProbeMaxDirs = 1000
)

// importScanSkipDirs are never descended into; they hold vendored or generated files, not the app's own.
var importScanSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// importCandidate is one file the import picker lists.
type importCandidate struct {
	// Path is slash-separated and relative to the scan root, e.g. "api/Makefile".
	Path string
	Abs  string
	// Group is the folder relative to the scan root, "" for a file at the root.
	Group string
	// Kind is makeKind, composeKind or rerunKind.
	Kind string
	// Count is the targets, services or commands in the file; zero when Err is set.
	Count int
	Err   error
	// HasImports is set for a rerun.yml with its own imports:, which rerun ignores past one level.
	HasImports bool
}

// importScanOpts holds the scan's flags.
type importScanOpts struct {
	// NoGitignore lists files .gitignore hides, as -no-gitignore does for watching.
	NoGitignore bool
}

// scanImportable lists the Makefiles, compose files and rerun files under root in tree order.
// truncated reports that the depth or file cap left out more.
func scanImportable(root string, opts importScanOpts) (found []importCandidate, truncated bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}
	s := &importScanner{root: abs, noIgnore: opts.NoGitignore}
	var ignores ignoreStack
	if !opts.NoGitignore {
		ignores = ancestorIgnores(abs)
	}
	s.walk(abs, "", 0, ignores)
	return s.found, s.truncated
}

type importScanner struct {
	root      string
	noIgnore  bool
	found     []importCandidate
	truncated bool
}

// walk adds dir's own candidates, then recurses into its folders in name order. It returns false once the file cap is hit.
func (s *importScanner) walk(dir, group string, depth int, ignores ignoreStack) bool {
	if !s.noIgnore {
		ignores = ignores.with(dir)
	}
	for _, file := range s.dirFiles(dir, ignores) {
		if len(s.found) >= importScanMaxFiles {
			s.truncated = true
			return false
		}
		s.found = append(s.found, parseCandidate(file, s.root, group))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() || importScanSkipDirs[e.Name()] {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if ignores.ignored(sub, true) {
			continue
		}
		if depth+1 > importScanMaxDepth {
			// A folder past the depth cap only counts as truncated if a candidate sits somewhere under it.
			budget := importProbeMaxDirs
			if s.holdsCandidate(sub, ignores, &budget) {
				s.truncated = true
			}
			continue
		}
		if !s.walk(sub, relSlash(s.root, sub), depth+1, ignores) {
			return false
		}
	}
	return true
}

// holdsCandidate reports whether dir or a folder below it has a candidate, stopping at the first; budget caps the folders it opens.
func (s *importScanner) holdsCandidate(dir string, ignores ignoreStack, budget *int) bool {
	if *budget <= 0 {
		return false
	}
	*budget--
	if !s.noIgnore {
		ignores = ignores.with(dir)
	}
	if len(s.dirFiles(dir, ignores)) > 0 {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || importScanSkipDirs[e.Name()] {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if !ignores.ignored(sub, true) && s.holdsCandidate(sub, ignores, budget) {
			return true
		}
	}
	return false
}

// dirFiles are the candidates directly in dir. A subfolder's rerun.yml covers its sibling Makefile and compose file; the root's rerun.yml is never listed.
func (s *importScanner) dirFiles(dir string, ignores ignoreStack) []string {
	if dir != s.root {
		rr := filepath.Join(dir, ConfigFileName)
		if fileExists(rr) && !ignores.ignored(rr, false) {
			return []string{rr}
		}
	}
	var out []string
	for _, f := range []string{implicitMakefile(dir), implicitCompose(dir)} {
		if f != "" && !ignores.ignored(f, false) {
			out = append(out, f)
		}
	}
	return out
}

// parseCandidate reads one file's kind and its count, or its parse error.
func parseCandidate(abs, root, group string) importCandidate {
	c := importCandidate{Path: relSlash(root, abs), Abs: abs, Group: group}
	data, err := os.ReadFile(abs)
	if err != nil {
		c.Err = err
		return c
	}
	switch {
	case filepath.Base(abs) == ConfigFileName:
		c.Kind = rerunKind
		c.Count, c.HasImports, c.Err = countRerunFile(abs, data)
	case isMakefile(abs):
		c.Kind = makeKind
		rules, err := parseMakefile(bytes.NewReader(data))
		c.Count, c.Err = len(rules), err
	default:
		c.Kind = composeKind
		c.Count, c.Err = countComposeServices(data)
	}
	return c
}

// countRerunFile counts a rerun.yml's targets and reports whether it has its own imports.
func countRerunFile(abs string, data []byte) (int, bool, error) {
	src, root, err := parseConfigBytes(abs, data)
	if err != nil {
		return 0, false, err
	}
	file, err := parseFileBody(src, root, abs)
	if err != nil {
		return 0, false, err
	}
	imports := mappingValue(root, importsKey)
	hasImports := imports != nil && !(imports.Kind == yaml.ScalarNode && imports.Tag == "!!null")
	return len(file.targets), hasImports, nil
}

// countComposeServices counts a compose file's services from its YAML; docker is not run, so profiles are not applied.
func countComposeServices(data []byte) (int, error) {
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return 0, err
	}
	return len(doc.Services), nil
}

// relSlash is path relative to base, slash-separated.
func relSlash(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// ignoreLayer is one directory's .gitignore, matched against paths below base.
type ignoreLayer struct {
	base string
	gi   gitignore.GitIgnore
}

// ignoreStack holds the .gitignore files that apply to a directory, shallowest first.
type ignoreStack []ignoreLayer

// with adds dir's .gitignore to the stack, when it has one.
func (st ignoreStack) with(dir string) ignoreStack {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return st
	}
	return append(slices.Clip(st), ignoreLayer{base: dir, gi: gitignore.New(bytes.NewReader(data), dir, nil)})
}

// ignored follows git: the deepest .gitignore with a matching pattern decides.
func (st ignoreStack) ignored(path string, isDir bool) bool {
	for i := len(st) - 1; i >= 0; i-- {
		rel, err := filepath.Rel(st[i].base, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if m := st[i].gi.Relative(rel, isDir); m != nil {
			return m.Ignore()
		}
	}
	return false
}

// ancestorIgnores loads the .gitignore files from the enclosing repo's root down to root's parent, since git applies them to root too.
func ancestorIgnores(root string) ignoreStack {
	top := repoTop(root)
	if top == "" || top == root {
		return nil
	}
	var dirs []string
	for d := filepath.Dir(root); ; d = filepath.Dir(d) {
		dirs = append([]string{d}, dirs...)
		if d == top {
			break
		}
	}
	var st ignoreStack
	for _, d := range dirs {
		st = st.with(d)
	}
	return st
}

// repoTop is the nearest directory at or above dir that holds a .git, or "" outside a repo.
func repoTop(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		} else if !errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// anyBelowRoot reports whether a candidate sits in a subfolder of the scan root.
func anyBelowRoot(cands []importCandidate) bool {
	return slices.ContainsFunc(cands, func(c importCandidate) bool { return c.Group != "" })
}
