package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

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

// feedRows caps the drawer's body; a longer feed scrolls.
const feedRows = 6

// restartFlash is how long a restarted target's row stays highlighted.
const restartFlash = time.Second

// configTarget is the pseudo-target a rerun.yml reload is listed under.
const configTarget = "config"

// feedHeader is the drawer's title line while the feed shows.
const feedHeader = "fs · last 1m"

// feedBatch is one burst of changes: its files merged by path, and every target
// the burst restarted. at is the first event's time, the join key.
type feedBatch struct {
	id      int
	at      time.Time
	files   map[string]byte
	targets []string
}

type drawerKind int

const (
	drawerSep   drawerKind = iota // the header line
	drawerBatch                   // one batch: `15:04:05  path +N  → targets`
	drawerDir                     // a directory header over 2+ files in the file list
	drawerFile                    // one file in the file list
)

// drawerRow is one drawn line of the fs drawer. id names the batch it belongs to.
type drawerRow struct {
	kind    drawerKind
	id      int
	label   string
	extra   int
	indent  int
	glyph   string
	at      time.Time
	targets []string
	dim     bool
}

// recordFeed folds one change batch into the feed. A change within feedJoin of the
// newest batch's first event joins it, so one save of one file is one line.
func (m *tuiModel) recordFeed(target string, cs rerun.ChangeSet, at time.Time) {
	if len(cs.Files) == 0 {
		return
	}
	if n := len(m.feed); n == 0 || absDuration(at.Sub(m.feed[n-1].at)) > feedJoin {
		m.feedSeq++
		m.feed = append(m.feed, feedBatch{id: m.feedSeq, at: at, files: map[string]byte{}})
	}
	b := &m.feed[len(m.feed)-1]
	for path := range cs.Files {
		key := m.feedPath(path)
		b.files[key] = mergeOp(b.files[key], opGlyph(cs.Ops[path]))
	}
	if !slices.Contains(b.targets, target) {
		b.targets = append(b.targets, target)
	}
	m.trimFeed()
}

// recordConfigFeed lists a rerun.yml reload under the batch holding that file,
// or under a batch of its own when no recent batch holds it.
func (m *tuiModel) recordConfigFeed(at time.Time) {
	if m.group.configPath == "" {
		return
	}
	key := m.feedPath(m.group.configPath)
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
	m.feed = append(m.feed, feedBatch{id: m.feedSeq, at: at, files: map[string]byte{key: '~'}, targets: []string{configTarget}})
	m.trimFeed()
}

func (m *tuiModel) trimFeed() {
	if over := len(m.feed) - feedCap; over > 0 {
		m.feed = m.feed[over:]
	}
}

// feedPath keys a changed file relative to the rerun.yml dir, so every target
// that watches it names it the same way.
func (m *tuiModel) feedPath(path string) string {
	clean := filepath.Clean(path)
	cfg := m.group.configPath
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

// setDrawerFocus moves the arrow keys between the target list and the drawer.
func (m *tuiModel) setDrawerFocus(on bool) {
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
	lines := feedBatchRows(batches, dim)
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

func feedBatchRows(batches []feedBatch, dim bool) []drawerRow {
	rows := make([]drawerRow, 0, len(batches))
	for _, b := range batches {
		rows = append(rows, drawerRow{
			kind:    drawerBatch,
			id:      b.id,
			label:   feedFirstFile(b),
			extra:   len(b.files) - 1,
			at:      b.at,
			targets: b.targets,
			dim:     dim,
		})
	}
	return rows
}

// feedFirstFile is the batch's first path in sort order, the one its line names.
func feedFirstFile(b feedBatch) string {
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		paths = append(paths, p)
	}
	return slices.Min(paths)
}

// feedFileRows lists a batch's files grouped by directory. A directory holding one
// file prints that file's full path; 2+ files get a header and indented names.
func feedFileRows(b feedBatch) []drawerRow {
	byDir := map[string][]string{}
	for p := range b.files {
		dir, _ := feedSplit(p)
		byDir[dir] = append(byDir[dir], p)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var rows []drawerRow
	for _, dir := range dirs {
		paths := byDir[dir]
		sort.Strings(paths)
		if len(paths) == 1 {
			rows = append(rows, feedFileRow(b, paths[0], paths[0], 0))
			continue
		}
		rows = append(rows, drawerRow{kind: drawerDir, id: b.id, label: feedDirLabel(dir)})
		for _, p := range paths {
			_, name := feedSplit(p)
			rows = append(rows, feedFileRow(b, p, name, 2))
		}
	}
	return rows
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
