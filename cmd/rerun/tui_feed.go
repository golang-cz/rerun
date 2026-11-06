package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/golang-cz/rerun"
)

// feedWindow is how long a batch stays in the fs drawer.
const feedWindow = time.Minute

// feedJoin is how far a change may sit from its batch's first event and still join it.
const feedJoin = 150 * time.Millisecond

// configJoin is how far back a config reload looks for the batch that holds rerun.yml.
const configJoin = 2 * time.Second

// feedCap bounds the stored batches; the window drops them sooner.
const feedCap = 100

// feedRows caps the drawer's body at six rows, so the drawer is seven lines with its header.
const feedRows = 6

// restartFlash is how long a restarted target's row stays highlighted.
const restartFlash = time.Second

// configTarget is the pseudo-target a rerun.yml reload is listed under.
const configTarget = "config"

// feedHeader is the drawer's title line while the feed shows.
const feedHeader = "fs · last 1m"

// feedAgeW is the width of a batch's right-aligned age column.
const feedAgeW = 3

// feedBatch is one burst of changes: its files merged by path, the paths that are
// directories created in it, and every target the burst restarted. at is the first
// event's time, the join key.
type feedBatch struct {
	id      int
	at      time.Time
	files   map[string]byte
	dirs    map[string]bool
	targets []string
}

type drawerKind int

const (
	drawerSep   drawerKind = iota // the header line
	drawerBatch                   // one batch: `  5s  path +N  → targets`
	drawerDir                     // a directory header over the files under it
	drawerFile                    // one file in the file list
)

// drawerRow is one drawn line of the fs drawer. id names the batch it belongs to;
// cursor marks the batch the drawer's cursor is on.
type drawerRow struct {
	kind    drawerKind
	id      int
	label   string
	extra   int
	indent  int
	glyph   string
	age     string
	targets []string
	dim     bool
	cursor  bool
}

// recordFeed folds one change batch into the feed and returns the batch's id, or 0
// for an empty batch. A change within feedJoin of the newest batch's first event
// joins it, so one save of one file is one line.
func (m *tuiModel) recordFeed(cs rerun.ChangeSet, at time.Time, owner Target) int {
	if len(cs.Files) == 0 {
		return 0
	}
	if n := len(m.feed); n == 0 || absDuration(at.Sub(m.feed[n-1].at)) > feedJoin {
		m.feedSeq++
		m.feed = append(m.feed, feedBatch{id: m.feedSeq, at: at, files: map[string]byte{}, dirs: map[string]bool{}})
	}
	b := &m.feed[len(m.feed)-1]
	for path := range cs.Files {
		key := m.feedPath(path, owner)
		b.files[key] = mergeOp(b.files[key], opGlyph(cs.Ops[path]))
		if cs.Ops[path].Has(fsnotify.Create) && isDirPath(path) {
			b.dirs[key] = true
		}
	}
	// A watcher may report a created directory's files without the directory itself.
	ancestors := map[string]bool{}
	for other := range b.files {
		for dir := other; ; {
			i := strings.LastIndex(dir, "/")
			if i < 0 {
				break
			}
			dir = dir[:i]
			ancestors[dir] = true
		}
	}
	for key, op := range b.files {
		if op == '+' && ancestors[key] {
			b.dirs[key] = true
		}
	}
	m.trimFeed()
	return b.id
}

// isDirPath reports whether path names a directory on disk right now.
func isDirPath(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// feedAge is how long ago a batch started, as the drawer shows it: 5s, 42s, 1m, 2h.
func feedAge(d time.Duration) string {
	secs := int(max(d, 0) / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	}
	return fmt.Sprintf("%dh", secs/3600)
}

// feedRestart names the target in its change's batch once the spawn lands.
func (m *tuiModel) feedRestart(t *tuiTarget, at time.Time) {
	id := t.restartBatch
	t.restartBatch = 0
	t.flashUntil = at.Add(restartFlash)
	for i := range m.feed {
		if m.feed[i].id == id && !slices.Contains(m.feed[i].targets, t.spec.Name) {
			m.feed[i].targets = append(m.feed[i].targets, t.spec.Name)
		}
	}
}

// recordConfigFeed lists a rerun.yml reload under the batch holding that file,
// or under a batch of its own when no recent batch holds it.
func (m *tuiModel) recordConfigFeed(at time.Time) {
	if m.group.configPath == "" {
		return
	}
	key := m.feedPath(m.group.configPath, Target{})
	for i := range m.feed {
		b := &m.feed[i]
		if _, ok := b.files[key]; !ok || absDuration(at.Sub(b.at)) > configJoin {
			continue
		}
		if !slices.Contains(b.targets, configTarget) {
			b.targets = append(b.targets, configTarget)
		}
		return
	}
	m.feedSeq++
	m.feed = append(m.feed, feedBatch{id: m.feedSeq, at: at, files: map[string]byte{key: '~'}, dirs: map[string]bool{}, targets: []string{configTarget}})
	m.trimFeed()
}

func (m *tuiModel) trimFeed() {
	if over := len(m.feed) - feedCap; over > 0 {
		m.feed = m.feed[over:]
	}
}

// feedPath keys a changed file relative to its owner's dir, with an import's
// section name in front, so a target's files never read as `../` headers.
func (m *tuiModel) feedPath(path string, owner Target) string {
	clean := filepath.Clean(path)
	cfg := m.group.configPath
	if owner.Dir != "" {
		cfg = filepath.Join(owner.Dir, ConfigFileName)
	}
	if cfg == "" {
		return filepath.ToSlash(clean)
	}
	base, err := filepath.Abs(filepath.Dir(cfg))
	if err != nil {
		return filepath.ToSlash(clean)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return filepath.ToSlash(clean)
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return filepath.ToSlash(clean)
	}
	if owner.Import != "" {
		return owner.Import + "/" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(rel)
}

// mergeOp keeps a created file created when a later write lands in the same batch.
func mergeOp(old, next byte) byte {
	if old == '+' && next == '~' {
		return old
	}
	return next
}

// feedVisible is the batches the drawer lists, newest first. When every batch has
// aged out, the newest one stays, dimmed, so the drawer never goes blank.
func (m *tuiModel) feedVisible() ([]feedBatch, bool) {
	var out []feedBatch
	for i := len(m.feed) - 1; i >= 0; i-- {
		if m.now.Sub(m.feed[i].at) <= feedWindow {
			out = append(out, m.feed[i])
		}
	}
	if len(out) == 0 && len(m.feed) > 0 {
		return []feedBatch{m.feed[len(m.feed)-1]}, true
	}
	return out, false
}

// feedCursorIndex is the cursor's batch within the visible list, the newest when
// the cursor names a batch that has aged out.
func (m *tuiModel) feedCursorIndex(batches []feedBatch) int {
	for i, b := range batches {
		if b.id == m.feedCursor {
			return i
		}
	}
	return 0
}

// feedOpened is the batch whose file list is open, nil when the feed shows.
func (m *tuiModel) feedOpened() *feedBatch {
	for i := range m.feed {
		if m.feed[i].id == m.feedOpen {
			return &m.feed[i]
		}
	}
	return nil
}

func (m *tuiModel) openFeed(id int) {
	m.feedOpen = id
	m.feedCursor = id
	m.feedTop = 0
}

// setDrawerFocus moves the arrow keys between the target list and the drawer. The
// drawer's cursor starts on the newest batch each time it gains focus.
func (m *tuiModel) setDrawerFocus(on bool) {
	if on && !m.drawerFocus {
		m.feedTop = 0
		if n := len(m.feed); n > 0 {
			m.feedCursor = m.feed[n-1].id
		}
	}
	m.drawerFocus = on
	if on {
		m.paneFocus = false
	}
}

// moveDrawer moves the feed cursor, or scrolls an open file list.
func (m *tuiModel) moveDrawer(delta int) {
	if m.feedOpened() != nil {
		m.feedTop += delta
		return
	}
	batches, _ := m.feedVisible()
	if len(batches) == 0 {
		return
	}
	i := min(max(m.feedCursorIndex(batches)+delta, 0), len(batches)-1)
	m.feedCursor = batches[i].id
}

// openCursorBatch opens the file list of the batch under the feed cursor.
func (m *tuiModel) openCursorBatch() {
	if m.feedOpened() != nil {
		return
	}
	batches, _ := m.feedVisible()
	if len(batches) == 0 {
		return
	}
	m.openFeed(batches[m.feedCursorIndex(batches)].id)
}

// clickDrawer focuses the drawer; a batch line opens its file list, and the header
// closes an open list.
func (m *tuiModel) clickDrawer(row drawerRow) {
	m.setDrawerFocus(true)
	switch row.kind {
	case drawerBatch:
		m.openFeed(row.id)
	case drawerSep:
		m.feedOpen = 0
		m.feedTop = 0
	}
}

// drawerRows lays out the fs drawer: a header, then the feed's batches or the open
// batch's file list, scrolled to the window the drawer has room for.
func (m *tuiModel) drawerRows() []drawerRow {
	if !m.drawerOpen {
		return nil
	}
	limit := min(feedRows+1, max(0, m.height-1)/2)
	if limit < 2 {
		return nil // the header alone would claim the whole drawer
	}
	body := limit - 1

	if open := m.feedOpened(); open != nil {
		lines := feedFileRows(*open)
		m.fitFeed(len(lines), body, -1)
		header := drawerRow{kind: drawerSep, label: fmt.Sprintf("fs · %s · %s", open.at.Format("15:04:05"), fileCount(len(open.files)))}
		return drawerWindow(header, lines, m.feedTop, body)
	}

	batches, dim := m.feedVisible()
	if len(batches) == 0 {
		return nil
	}
	lines := feedBatchRows(batches, dim, m.feedCursor, m.now)
	m.fitFeed(len(lines), body, m.feedCursorIndex(batches))
	return drawerWindow(drawerRow{kind: drawerSep, label: feedHeader}, lines, m.feedTop, body)
}

// fitFeed keeps the scroll window inside the lines and, in feed mode, keeps the
// cursor's line visible. A negative cursor means the file list, which has no cursor.
func (m *tuiModel) fitFeed(n, body, cursor int) {
	if cursor >= 0 {
		if cursor < m.feedTop {
			m.feedTop = cursor
		}
		if cursor >= m.feedTop+body {
			m.feedTop = cursor - body + 1
		}
	}
	m.feedTop = min(max(m.feedTop, 0), max(0, n-body))
}

func drawerWindow(header drawerRow, lines []drawerRow, top, body int) []drawerRow {
	end := min(top+body, len(lines))
	return append([]drawerRow{header}, lines[top:end]...)
}

func feedBatchRows(batches []feedBatch, dim bool, cursor int, now time.Time) []drawerRow {
	rows := make([]drawerRow, 0, len(batches))
	for _, b := range batches {
		rows = append(rows, drawerRow{
			kind:    drawerBatch,
			id:      b.id,
			label:   feedFirstFile(b),
			extra:   len(b.files) - 1,
			age:     feedAge(now.Sub(b.at)),
			targets: b.targets,
			dim:     dim,
			cursor:  b.id == cursor,
		})
	}
	return rows
}

// feedFirstFile is the batch's first path in sort order, the one its line names.
// A created directory keeps its trailing slash.
func feedFirstFile(b feedBatch) string {
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		paths = append(paths, p)
	}
	first := slices.Min(paths)
	if b.dirs[first] {
		return first + "/"
	}
	return first
}

// feedFileRows lists a batch's files grouped by directory. A created directory
// heads the files reported under it; other directories group their files, one
// file printing its full path and 2+ files getting a header and indented names.
func feedFileRows(b feedBatch) []drawerRow {
	type group struct {
		key  string
		rows []drawerRow
	}
	var groups []group
	owned := map[string][]string{} // created directory -> the files reported under it
	var loose []string
	for p := range b.files {
		if d := ownerDir(b, p); d != "" {
			owned[d] = append(owned[d], p)
		} else {
			loose = append(loose, p)
		}
	}
	for d, paths := range owned {
		sort.Strings(paths)
		rows := []drawerRow{{kind: drawerDir, id: b.id, label: d + "/"}}
		for _, p := range paths {
			rows = append(rows, feedFileRow(b, p, strings.TrimPrefix(p, d+"/")+suffixDir(b, p), 2))
		}
		groups = append(groups, group{key: d, rows: rows})
	}

	byDir := map[string][]string{}
	for _, p := range loose {
		if _, heads := owned[p]; heads {
			continue // the created directory's own group lists it
		}
		dir, _ := feedSplit(p)
		byDir[dir] = append(byDir[dir], p)
	}
	for dir, paths := range byDir {
		sort.Strings(paths)
		var rows []drawerRow
		if len(paths) == 1 {
			rows = append(rows, feedFileRow(b, paths[0], paths[0]+suffixDir(b, paths[0]), 0))
		} else {
			rows = append(rows, drawerRow{kind: drawerDir, id: b.id, label: feedDirLabel(dir)})
			for _, p := range paths {
				_, name := feedSplit(p)
				rows = append(rows, feedFileRow(b, p, name+suffixDir(b, p), 2))
			}
		}
		groups = append(groups, group{key: dir, rows: rows})
	}

	slices.SortFunc(groups, func(x, y group) int { return strings.Compare(x.key, y.key) })
	var out []drawerRow
	for _, g := range groups {
		out = append(out, g.rows...)
	}
	return out
}

// ownerDir is the longest created directory in the batch that holds p, or "".
func ownerDir(b feedBatch, p string) string {
	best := ""
	for d := range b.dirs {
		if strings.HasPrefix(p, d+"/") && len(d) > len(best) && b.files[d] == '+' {
			best = d
		}
	}
	return best
}

// suffixDir is a trailing slash for a created directory, else nothing.
func suffixDir(b feedBatch, p string) string {
	if b.dirs[p] {
		return "/"
	}
	return ""
}

func feedFileRow(b feedBatch, path, label string, indent int) drawerRow {
	return drawerRow{kind: drawerFile, id: b.id, label: label, indent: indent, glyph: opText(b.files[path])}
}

// feedSplit cuts a feed path into its directory (no trailing slash) and name.
func feedSplit(p string) (string, string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

func feedDirLabel(dir string) string {
	if dir == "" {
		return "./"
	}
	return dir + "/"
}

// absDuration is |d|, the gap between two events regardless of their order.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// flashing reports whether a restarted target's row is still highlighted.
func (t *tuiTarget) flashing(now time.Time) bool { return now.Before(t.flashUntil) }
