package main

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// drawerKind is which line of the fs drawer a row draws.
type drawerKind int

const (
	drawerSep    drawerKind = iota // `fs ·────────`
	drawerFolder                   // `~ pkg/api/   2`
	drawerFile                     // `+ types.gen.go  4m`
)

// drawerRow is one drawn line of the fs drawer: a folder with its recursive
// changed-file count, or a file with its op and age. path is the folder a
// folder line toggles; a file line carries its parent's path.
type drawerRow struct {
	kind  drawerKind
	path  string
	depth int
	glyph string
	label string
	count int
	at    time.Time
	op    string
}

// treeNode is one folder of a root's folded subtree. dirs holds its changed
// sub-directories, files the changed files directly under it, count the
// recursive changed-file count.
type treeNode struct {
	name  string
	path  string
	dirs  map[string]*treeNode
	files []treeFileEntry
	count int
}

// treeFileEntry is one changed file directly under a folder.
type treeFileEntry struct {
	name string
	at   time.Time
	op   byte
}

// treeRoot is one root the drawer can draw: its prefix, its folded subtree,
// the newest change under it, and every folder path it holds, so expansion
// state is pruned when a folder disappears.
type treeRoot struct {
	prefix string
	newest time.Time
	node   *treeNode
	all    []string
}

// cleanTreePath normalizes a watcher path to the form the roots match against.
func cleanTreePath(path string) string {
	p := filepath.ToSlash(filepath.Clean(path))
	return strings.TrimPrefix(p, "./")
}

// globRoot is the literal directory prefix of an on: glob: everything up to
// the first wildcard, cut back to the last separator. A glob with no wildcard
// names a directory outright. `schema/**.ridl` -> `schema/`, `pkg/api` ->
// `pkg/api/`, `*.go` -> the current directory ("").
func globRoot(glob string) string {
	g := filepath.ToSlash(filepath.Clean(glob))
	if g == "." || g == "" || g == "/" {
		return ""
	}
	i := strings.IndexAny(g, "*?[")
	if i < 0 {
		return g + "/"
	}
	g = g[:i]
	if strings.HasSuffix(g, "/") {
		return g
	}
	if j := strings.LastIndex(g, "/"); j >= 0 {
		return g[:j+1]
	}
	return ""
}

// matchRoot reports whether a path sits under a root prefix. The empty root is
// the current directory, which matches everything not claimed by a longer root.
func matchRoot(root, path string) bool {
	return root == "" || strings.HasPrefix(path, root)
}

// dirPrefix is a path's own directory with its trailing slash, the fallback
// root for a change no on: glob covers.
func dirPrefix(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i+1]
	}
	return ""
}

// rootLabel is a root's drawn name; the current-directory root reads `./`.
func rootLabel(prefix string) string {
	if prefix == "" {
		return "./"
	}
	return prefix
}

func newTreeNode(name, path string) *treeNode {
	return &treeNode{name: name, path: path, dirs: map[string]*treeNode{}}
}

// addTreeFile hangs one changed file under its root at its relative path.
func addTreeFile(root *treeNode, prefix, path string, tf treeFile) {
	rel := path
	if prefix != "" {
		rel = strings.TrimPrefix(path, prefix)
	}
	rel = strings.TrimPrefix(rel, "./")
	comps := strings.Split(rel, "/")
	node := root
	for _, c := range comps[:len(comps)-1] {
		if c == "" || c == "." {
			continue
		}
		child := node.dirs[c]
		if child == nil {
			child = newTreeNode(c+"/", node.path+c+"/")
			node.dirs[c] = child
		}
		node = child
	}
	name := comps[len(comps)-1]
	if name == "" || name == "." {
		return
	}
	node.files = append(node.files, treeFileEntry{name: name, at: tf.at, op: tf.op})
}

// finalizeNode fills a subtree's recursive count and newest change time, and
// returns every folder path it holds.
func finalizeNode(n *treeNode) (time.Time, []string) {
	newest := time.Time{}
	paths := []string{n.path}
	n.count = len(n.files)
	for _, f := range n.files {
		if f.at.After(newest) {
			newest = f.at
		}
	}
	for _, d := range n.dirs {
		childNewest, childPaths := finalizeNode(d)
		n.count += d.count
		if childNewest.After(newest) {
			newest = childNewest
		}
		paths = append(paths, childPaths...)
	}
	return newest, paths
}

// rootPrefixes is every on: glob's root across all targets, in yml
// first-appearance order, deduped. Roots are advice, not a filter: a change no
// root covers still gets a root of its own.
func (m *tuiModel) rootPrefixes() []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range m.targets {
		for _, glob := range t.spec.On {
			p := globRoot(glob)
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// treeRoots folds the drawer's changed files under the watched roots, in yml
// order. A path no root covers gets its own dirname as a root. A root with no
// changed file is dropped, so the drawer shows only what moved.
func (m *tuiModel) treeRoots() []treeRoot {
	if len(m.treeFiles) == 0 {
		return nil
	}
	prefixes := m.rootPrefixes()
	paths := make([]string, 0, len(m.treeFiles))
	for p := range m.treeFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	order := make([]string, 0, len(prefixes))
	seen := make(map[string]bool, len(prefixes))
	for _, p := range prefixes {
		if !seen[p] {
			seen[p] = true
			order = append(order, p)
		}
	}
	byRoot := make(map[string][]string, len(order))
	for _, path := range paths {
		best, found := "", false
		for _, r := range prefixes {
			if matchRoot(r, path) && (!found || len(r) > len(best)) {
				best, found = r, true
			}
		}
		if !found {
			best = dirPrefix(path)
			if !seen[best] {
				seen[best] = true
				order = append(order, best)
			}
		}
		byRoot[best] = append(byRoot[best], path)
	}

	roots := make([]treeRoot, 0, len(order))
	for _, prefix := range order {
		ps := byRoot[prefix]
		if len(ps) == 0 {
			continue
		}
		node := newTreeNode(rootLabel(prefix), prefix)
		for _, path := range ps {
			addTreeFile(node, prefix, path, m.treeFiles[path])
		}
		newest, all := finalizeNode(node)
		roots = append(roots, treeRoot{prefix: prefix, newest: newest, node: node, all: all})
	}
	m.pruneTreeExpansions(roots)
	return roots
}

// pruneTreeExpansions drops the expansion flag of every folder the tree no
// longer holds, so a path pushed out of the cap forgets it, and a folder that
// reappears later cannot inherit stale state.
func (m *tuiModel) pruneTreeExpansions(roots []treeRoot) {
	if len(m.treeExpanded) == 0 {
		return
	}
	live := make(map[string]bool, len(roots))
	for _, r := range roots {
		for _, p := range r.all {
			live[p] = true
		}
	}
	for p := range m.treeExpanded {
		if !live[p] {
			delete(m.treeExpanded, p)
		}
	}
}

// isTreeExpanded reports whether a folder path shows its children.
func (m *tuiModel) isTreeExpanded(path string) bool { return m.treeExpanded[path] }

// toggleTreeFolder folds a folder's children in or out, in place.
func (m *tuiModel) toggleTreeFolder(path string) {
	if m.treeExpanded[path] {
		delete(m.treeExpanded, path)
		return
	}
	m.treeExpanded[path] = true
}

// folderGlyph is the folder's state: expanded, collapsed with changed
// sub-dirs, or a leaf whose children are only files.
func folderGlyph(n *treeNode, expanded bool) string {
	switch {
	case expanded:
		return "−"
	case len(n.dirs) > 0:
		return "+"
	}
	return "~"
}

// drawerMax caps the fs drawer's drawn lines, its `fs ·` separator included.
// Roots past the cap are simply not drawn; the change data stays in the tree.
const drawerMax = 8

// drawerRows lays out the fs drawer as a change tree: the separator, then one
// line per root, a folder's children folding out beneath it when expanded. The
// newest roots win the cap; children past it do not render.
func (m *tuiModel) drawerRows() []drawerRow {
	if !m.drawerOpen {
		return nil
	}
	limit := min(drawerMax, max(0, m.height-1)/2)
	if limit < 2 {
		return nil // the separator alone would claim the whole drawer
	}
	roots := m.treeRoots()
	if len(roots) == 0 {
		return nil
	}
	blocks := make([][]drawerRow, len(roots))
	for i, r := range roots {
		blocks[i] = m.nodeRows(r.node, 0)
	}

	// The newest roots win the cap; the survivors keep the roots' own order.
	order := make([]int, len(roots))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return roots[order[a]].newest.After(roots[order[b]].newest) })
	keep := make([]bool, len(roots))
	budget := limit - 1
	for _, i := range order {
		if budget <= 0 {
			break
		}
		if len(blocks[i]) <= budget {
			keep[i] = true
			budget -= len(blocks[i])
			continue
		}
		blocks[i] = blocks[i][:budget]
		keep[i] = true
		budget = 0
	}

	rows := make([]drawerRow, 0, limit)
	rows = append(rows, drawerRow{kind: drawerSep})
	for i := range roots {
		if keep[i] {
			rows = append(rows, blocks[i]...)
		}
	}
	return rows
}

// nodeRows renders one folder and, when it is expanded, its sub-dirs first and
// then its files, both alphabetically.
func (m *tuiModel) nodeRows(n *treeNode, depth int) []drawerRow {
	expanded := m.isTreeExpanded(n.path)
	rows := []drawerRow{{
		kind:  drawerFolder,
		path:  n.path,
		depth: depth,
		glyph: folderGlyph(n, expanded),
		label: n.name,
		count: n.count,
	}}
	if !expanded {
		return rows
	}
	dirs := make([]string, 0, len(n.dirs))
	for name := range n.dirs {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	for _, name := range dirs {
		rows = append(rows, m.nodeRows(n.dirs[name], depth+1)...)
	}
	files := append([]treeFileEntry(nil), n.files...)
	sort.Slice(files, func(a, b int) bool { return files[a].name < files[b].name })
	for _, f := range files {
		rows = append(rows, drawerRow{kind: drawerFile, path: n.path, depth: depth + 1, op: opText(f.op), label: f.name, at: f.at})
	}
	return rows
}
