package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	// importScanBudget stops a scan of a slow or huge tree; the picker lists what it found with a truncated row.
	importScanBudget = 2 * time.Second
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
	// Budget caps the scan's time; zero means importScanBudget.
	Budget time.Duration
	// now replaces time.Now in tests.
	now func() time.Time
}

// importScan is a finished scan.
type importScan struct {
	Found []importCandidate
	// Truncated reports that the depth, file or time cap left out more.
	Truncated bool
	// Skips are the rows naming git worktrees left out because another checkout of the same repo is listed.
	Skips []string
}

// scanImportable lists the Makefiles, compose files and rerun files under root in tree order.
// truncated reports that the depth, file or time cap left out more.
func scanImportable(root string, opts importScanOpts) (found []importCandidate, truncated bool) {
	r := scanImportableFull(root, opts)
	return r.Found, r.Truncated
}

// scanImportableFull is scanImportable plus the worktree skip rows.
func scanImportableFull(root string, opts importScanOpts) importScan {
	abs, err := filepath.Abs(root)
	if err != nil {
		return importScan{}
	}
	s := &importScanner{root: abs, noIgnore: opts.NoGitignore, now: opts.now}
	if s.now == nil {
		s.now = time.Now
	}
	budget := opts.Budget
	if budget == 0 {
		budget = importScanBudget
	}
	s.deadline = s.now().Add(budget)

	var ignores ignoreStack
	if !opts.NoGitignore {
		ignores = ancestorIgnores(abs)
	}
	s.rootCheckout = s.checkoutOfRoot()
	top := s.build(abs, "", 0, ignores)
	s.chooseCheckouts()
	s.emit(top)
	return importScan{Found: s.found, Truncated: s.truncated, Skips: s.skipRows()}
}

// importDir is one scanned folder; its entries and ignores are read once and reused for listing.
type importDir struct {
	path, group string
	depth       int
	entries     []os.DirEntry
	ignores     ignoreStack
	checkout    *gitCheckout
	// deferred checkouts are not entered until chooseCheckouts picks which ones to keep.
	deferred bool
	skipped  bool
	kids     []*importDir
}

type importScanner struct {
	root     string
	noIgnore bool
	now      func() time.Time
	deadline time.Time

	found     []importCandidate
	truncated bool

	rootCheckout *gitCheckout
	// deferredDirs are the checkouts met in the first pass, in tree order.
	deferredDirs []*importDir
	// keepers maps a repo to the one checkout dir the first pass chose; nil while that pass runs.
	keepers map[string]string
	kept    map[string]bool
	skips   map[string]int
	names   map[string]string
	skipIDs []string
}

func (s *importScanner) expired() bool {
	if s.now().Before(s.deadline) {
		return false
	}
	s.truncated = true
	return true
}

// build reads dir and its folders down to the depth cap. Checkouts below the root wait for chooseCheckouts.
func (s *importScanner) build(dir, group string, depth int, ignores ignoreStack) *importDir {
	if !s.noIgnore {
		ignores = ignores.with(dir)
	}
	n := &importDir{path: dir, group: group, depth: depth, ignores: ignores}
	n.entries, _ = os.ReadDir(dir)
	if depth > 0 && hasEntry(n.entries, ".git") {
		n.checkout = checkoutAt(dir)
	}
	if n.checkout != nil && s.keepers == nil {
		n.deferred = true
		s.deferredDirs = append(s.deferredDirs, n)
		return n
	}
	s.buildKids(n)
	return n
}

func (s *importScanner) buildKids(n *importDir) {
	for _, e := range n.entries {
		if !e.IsDir() || importScanSkipDirs[e.Name()] {
			continue
		}
		if s.expired() {
			return
		}
		sub := filepath.Join(n.path, e.Name())
		if n.ignores.ignored(sub, true) {
			continue
		}
		if n.depth+1 > importScanMaxDepth {
			// A folder past the depth cap only counts as truncated if a candidate sits somewhere under it.
			budget := importProbeMaxDirs
			if s.holdsCandidate(sub, n.ignores, &budget) {
				s.truncated = true
			}
			continue
		}
		kid := s.build(sub, relSlash(s.root, sub), n.depth+1, n.ignores)
		if kid.checkout != nil && s.keepers != nil && !s.keepNested(kid) {
			continue
		}
		n.kids = append(n.kids, kid)
	}
}

// keepNested decides a checkout met inside a kept one, where the first pass did not look.
func (s *importScanner) keepNested(kid *importDir) bool {
	id := kid.checkout.id
	keep := !s.kept[id]
	if k, known := s.keepers[id]; known {
		keep = k == kid.path
	}
	if keep {
		s.kept[id] = true
		return true
	}
	s.skip(kid)
	return false
}

func (s *importScanner) skip(kid *importDir) {
	id := kid.checkout.id
	if s.skips == nil {
		s.skips, s.names = map[string]int{}, map[string]string{}
	}
	if s.skips[id] == 0 {
		s.skipIDs = append(s.skipIDs, id)
	}
	s.skips[id]++
	s.names[id] = kid.checkout.name
	kid.skipped = true
}

// chooseCheckouts keeps one checkout per repo: the one holding the scan root, else the main checkout, else the first in tree order.
// It then enters the kept ones, which the first pass left unread.
func (s *importScanner) chooseCheckouts() {
	s.keepers, s.kept = map[string]string{}, map[string]bool{}
	if rc := s.rootCheckout; rc != nil {
		s.keepers[rc.id] = rc.dir
		s.kept[rc.id] = true
	}
	keeperIsMain := map[string]bool{}
	for _, d := range s.deferredDirs {
		id := d.checkout.id
		if rc := s.rootCheckout; rc != nil && rc.id == id {
			continue
		}
		if _, ok := s.keepers[id]; !ok || (d.checkout.main && !keeperIsMain[id]) {
			s.keepers[id], keeperIsMain[id] = d.path, d.checkout.main
		}
	}
	for _, d := range s.deferredDirs {
		if s.keepers[d.checkout.id] != d.path {
			s.skip(d)
			continue
		}
		s.kept[d.checkout.id] = true
		if !s.expired() {
			s.buildKids(d)
		}
	}
}

func (s *importScanner) skipRows() []string {
	var rows []string
	for _, id := range s.skipIDs {
		n, noun := s.skips[id], "worktrees"
		if n == 1 {
			noun = "worktree"
		}
		rows = append(rows, fmt.Sprintf("%d other %s of %s skipped", n, noun, s.names[id]))
	}
	return rows
}

// emit lists n's own candidates, then its folders' in tree order. It returns false once the file cap is hit.
func (s *importScanner) emit(n *importDir) bool {
	if n.skipped {
		return true
	}
	for _, file := range s.dirFiles(n.path, n.entries, n.ignores) {
		if len(s.found) >= importScanMaxFiles {
			s.truncated = true
			return false
		}
		s.found = append(s.found, parseCandidate(file, s.root, n.group))
	}
	for _, kid := range n.kids {
		if !s.emit(kid) {
			return false
		}
	}
	return true
}

// holdsCandidate reports whether dir or a folder below it has a candidate, stopping at the first; budget caps the folders it opens.
func (s *importScanner) holdsCandidate(dir string, ignores ignoreStack, budget *int) bool {
	if *budget <= 0 || s.expired() {
		return false
	}
	*budget--
	if !s.noIgnore {
		ignores = ignores.with(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	if len(s.dirFiles(dir, entries, ignores)) > 0 {
		return true
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
func (s *importScanner) dirFiles(dir string, entries []os.DirEntry, ignores ignoreStack) []string {
	if dir != s.root {
		if rr := firstNamed(dir, entries, []string{ConfigFileName}); rr != "" && !ignores.ignored(rr, false) {
			return []string{rr}
		}
	}
	var out []string
	for _, f := range []string{firstNamed(dir, entries, makeNames), firstNamed(dir, entries, composeNames)} {
		if f != "" && !ignores.ignored(f, false) {
			out = append(out, f)
		}
	}
	return out
}

func hasEntry(entries []os.DirEntry, name string) bool {
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == name })
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
