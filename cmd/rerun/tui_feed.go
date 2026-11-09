package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/golang-cz/rerun"
)

// feedWindow is how long a batch stays in the fs events modal.
const feedWindow = time.Minute

// feedJoin is how far a change may sit from its batch's first event and still join it.
const feedJoin = 150 * time.Millisecond

// configJoin is how far back a config reload looks for the batch that holds rerun.yml.
const configJoin = 2 * time.Second

// feedCap bounds the stored batches; the window drops them sooner.
const feedCap = 100

// restartFlash is how long a restarted target's row stays highlighted.
const restartFlash = time.Second

// feedHintFlash is how long the fs events line stays highlighted after a batch lands.
const feedHintFlash = time.Second

// configTarget is the pseudo-target a rerun.yml reload is listed under.
const configTarget = "config"

// feedBatch is one burst of changes: its files merged by path, the paths that are
// directories created in it, and every target the burst restarted. at is the first
// event's time, the join key.
type feedBatch struct {
	id      int
	at      time.Time
	files   map[string]byte
	dirs    map[string]bool
	targets []string
	// svc records each file's owner targets as their changes landed, so the
	// modal never re-matches watch globs after the fact.
	svc map[string][]string
	// err is the watcher error the batch's ChangeSet carried, so the modal can
	// show what went wrong instead of files.
	err error
}

// feedFile is one line of a batch's file list: a directory header over the
// files under it, or one file. path is the file's feed key.
type feedFile struct {
	dir    bool
	label  string
	indent int
	glyph  string
	path   string
}

// recordFeed folds one change batch into the feed and returns the batch's id, or 0
// for an empty batch. A change within feedJoin of the newest batch's first event
// joins it, so one save of one file is one line. Files and a watcher error never
// share a batch: the modal draws one or the other.
func (m *tuiModel) recordFeed(cs rerun.ChangeSet, at time.Time, owner Target) int {
	if len(cs.Files) == 0 && cs.Error == nil {
		return 0
	}
	// A mixed ChangeSet becomes two batches, so its error cannot swallow its files.
	if cs.Error != nil && len(cs.Files) > 0 {
		clean := cs
		clean.Error = nil
		m.recordFeed(clean, at, owner)
		cs.Files, cs.Ops = nil, nil
	}
	isErr := cs.Error != nil
	if n := len(m.feed); n == 0 || absDuration(at.Sub(m.feed[n-1].at)) > feedJoin || (m.feed[n-1].err != nil) != isErr {
		m.feedSeq++
		m.feed = append(m.feed, feedBatch{id: m.feedSeq, at: at, files: map[string]byte{}, dirs: map[string]bool{}, svc: map[string][]string{}})
		m.feedFlashUntil = at.Add(feedHintFlash)
	}
	b := &m.feed[len(m.feed)-1]
	if cs.Error != nil {
		b.err = cs.Error // a later clean change in the same batch must not clear it
	}
	for path := range cs.Files {
		key := m.feedPath(path, owner)
		if owner.Name != "" {
			b.svc[key] = unionStrings(b.svc[key], []string{owner.Name})
		}
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
		b.svc[key] = unionStrings(b.svc[key], []string{configTarget})
		return
	}
	m.feedSeq++
	m.feed = append(m.feed, feedBatch{
		id: m.feedSeq, at: at, files: map[string]byte{key: '~'}, dirs: map[string]bool{},
		targets: []string{configTarget}, svc: map[string][]string{key: {configTarget}},
	})
	m.feedFlashUntil = at.Add(feedHintFlash)
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

// feedVisible is the batches the modal lists, newest first. When every batch has
// aged out, the newest one stays, dimmed, so the modal never goes blank.
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

// batchIndex is the position of the named batch in the visible list, the newest
// (0) when no batch carries that id.
func batchIndex(batches []feedBatch, id int) int {
	for i, b := range batches {
		if b.id == id {
			return i
		}
	}
	return 0
}

// scrollTop keeps a scroll window inside n lines and the cursor's line visible. A
// negative cursor means no cursor, as in the file list.
func scrollTop(top, n, body, cursor int) int {
	if cursor >= 0 {
		if cursor < top {
			top = cursor
		}
		if cursor >= top+body {
			top = cursor - body + 1
		}
	}
	return min(max(top, 0), max(0, n-body))
}

// feedFirstFile is the batch's first path in sort order, the one its line names.
// A created directory keeps its trailing slash.
func feedFirstFile(b feedBatch) string {
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return "" // an error batch carries no file to name
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
func feedFileRows(b feedBatch) []feedFile {
	type group struct {
		key  string
		rows []feedFile
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
		rows := []feedFile{{dir: true, label: d + "/"}}
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
		var rows []feedFile
		if len(paths) == 1 {
			rows = append(rows, feedFileRow(b, paths[0], paths[0]+suffixDir(b, paths[0]), 0))
		} else {
			rows = append(rows, feedFile{dir: true, label: feedDirLabel(dir)})
			for _, p := range paths {
				_, name := feedSplit(p)
				rows = append(rows, feedFileRow(b, p, name+suffixDir(b, p), 2))
			}
		}
		groups = append(groups, group{key: dir, rows: rows})
	}

	slices.SortFunc(groups, func(x, y group) int { return strings.Compare(x.key, y.key) })
	var out []feedFile
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

func feedFileRow(b feedBatch, path, label string, indent int) feedFile {
	return feedFile{label: label, indent: indent, glyph: opText(b.files[path]), path: path}
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
