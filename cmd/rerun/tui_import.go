package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The footer names ctrl+s by what it writes: a new file, or the imports of an existing one.
const (
	importFooterNew    = "space toggle · a all/none · enter run · ctrl+s save rerun.yml · esc close"
	importFooterUpdate = "space toggle · a all/none · enter run · ctrl+s update rerun.yml imports · esc close"
)

// importFilterRow, importStatusRows and importScanRow are the picker's fixed lines around its list.
const (
	importFilterRows = 1
	importStatusRows = 1
	importScanRow    = "scan truncated"
)

// The three states of a row's box: none, some, or all of its checkable files checked.
const (
	checkNone = iota
	checkPartial
	checkAll
)

var checkBoxes = map[int]string{checkNone: "[ ]", checkPartial: "[■]", checkAll: "[x]"}

// importPicker is the import picker: the files found below the scan root, and which of them are checked.
type importPicker struct {
	open bool
	// dir is the scan root, shown in the title.
	dir string
	// rootFile is the rerun.yml that ctrl+s writes and enter runs beside.
	rootFile string
	// exists is set when rerun.yml is on disk, so ctrl+s updates it and the title names it.
	exists bool
	// schemas are the rerun.yml's own schemas, which its imports may name.
	schemas   map[string]groupDef
	cands     []importCandidate
	checked   []bool
	tree      *importNode
	truncated bool
	query     string
	cursor    int
	top       int
	// msg is the last failure, shown in the status row until the next key that changes the picker.
	msg string
}

// importNode is a folder in the picker's tree; kids keep the scan's order.
type importNode struct {
	group string
	name  string
	kids  []importEntry
}

// importEntry is one child of a folder: a file by its index in the candidates, or a subfolder.
type importEntry struct {
	cand int
	dir  *importNode
}

// importRow is one drawn line: the root, a folder, or a file.
type importRow struct {
	prefix string
	folder bool
	// group is a folder's path relative to the scan root, "" for the root.
	group string
	// cand is a file's index in the candidates.
	cand  int
	label string
}

// buildImportTree nests the candidates by folder, keeping the scan's order.
func buildImportTree(cands []importCandidate) *importNode {
	root := &importNode{}
	folders := map[string]*importNode{"": root}
	for i, c := range cands {
		parent := root
		if c.Group != "" {
			folder := ""
			for _, seg := range strings.Split(c.Group, "/") {
				if folder == "" {
					folder = seg
				} else {
					folder += "/" + seg
				}
				n := folders[folder]
				if n == nil {
					n = &importNode{group: folder, name: seg}
					folders[folder] = n
					parent.kids = append(parent.kids, importEntry{dir: n})
				}
				parent = n
			}
		}
		parent.kids = append(parent.kids, importEntry{cand: i})
	}
	return root
}

// initialChecks starts every checkable file checked, or only the imported ones when a rerun.yml exists.
func initialChecks(cands []importCandidate, imported map[string]bool) []bool {
	checked := make([]bool, len(cands))
	for i, c := range cands {
		checked[i] = c.Err == nil && (imported == nil || imported[c.Abs])
	}
	return checked
}

// newImportPicker opens the picker over cands. imported is nil when no rerun.yml exists yet.
func newImportPicker(rootFile string, cands []importCandidate, truncated, exists bool, schemas map[string]groupDef, imported map[string]bool) importPicker {
	return importPicker{
		open:      true,
		dir:       filepath.Dir(rootFile),
		rootFile:  rootFile,
		exists:    exists,
		schemas:   schemas,
		cands:     cands,
		checked:   initialChecks(cands, imported),
		tree:      buildImportTree(cands),
		truncated: truncated,
	}
}

// matches marks the files the query finds; every word must hit the path, as in the finder.
func (p *importPicker) matches() []bool {
	vis := make([]bool, len(p.cands))
	words := strings.Fields(finderMatchText(p.query))
	if len(words) == 0 {
		for i := range vis {
			vis[i] = true
		}
		return vis
	}
	paths := make([]string, len(p.cands))
	for i, c := range p.cands {
		paths[i] = finderMatchText(c.Path)
	}
	for i, h := range fuzzyAll(words, paths) {
		vis[i] = h.hits == len(words)
	}
	return vis
}

// rows lists the root, then every visible folder and file in tree order with its tree glyphs.
func (p *importPicker) rows() []importRow {
	if p.tree == nil {
		return nil
	}
	out := []importRow{{folder: true, label: "./"}}
	p.walk(p.tree.kids, "", p.matches(), &out)
	return out
}

// walk appends the visible entries of one folder; a folder shows only when something under it does.
func (p *importPicker) walk(kids []importEntry, prefix string, vis []bool, out *[]importRow) {
	var shown []importEntry
	for _, k := range kids {
		if k.dir != nil && subtreeVisible(k.dir, vis) || k.dir == nil && vis[k.cand] {
			shown = append(shown, k)
		}
	}
	for j, k := range shown {
		glyph, next := "├── ", prefix+"│   "
		if j == len(shown)-1 {
			glyph, next = "└── ", prefix+"    "
		}
		if k.dir != nil {
			*out = append(*out, importRow{prefix: prefix + glyph, folder: true, group: k.dir.group, label: k.dir.name + "/"})
			p.walk(k.dir.kids, next, vis, out)
			continue
		}
		*out = append(*out, importRow{prefix: prefix + glyph, cand: k.cand, label: path.Base(p.cands[k.cand].Path)})
	}
}

// subtreeVisible reports whether any file under the folder is visible.
func subtreeVisible(n *importNode, vis []bool) bool {
	for _, k := range n.kids {
		if k.dir != nil && subtreeVisible(k.dir, vis) || k.dir == nil && vis[k.cand] {
			return true
		}
	}
	return false
}

// under is the candidates below a folder, all of them for the root.
func (p *importPicker) under(group string) []int {
	var out []int
	for i, c := range p.cands {
		if group == "" || c.Group == group || strings.HasPrefix(c.Group, group+"/") {
			out = append(out, i)
		}
	}
	return out
}

// rowState is a row's box. A folder reads the files under it; files that failed to parse never count.
func (p *importPicker) rowState(r importRow) int {
	if !r.folder {
		if p.checked[r.cand] {
			return checkAll
		}
		return checkNone
	}
	total, on := 0, 0
	for _, i := range p.under(r.group) {
		if p.cands[i].Err != nil {
			continue
		}
		total++
		if p.checked[i] {
			on++
		}
	}
	switch {
	case on == 0:
		return checkNone
	case on == total:
		return checkAll
	default:
		return checkPartial
	}
}

// toggle flips a file, or sets a folder's whole subtree to the opposite of its state: partial and none check all.
func (p *importPicker) toggle(r importRow) {
	if !r.folder {
		c := p.cands[r.cand]
		if c.Err != nil {
			p.msg = c.Path + ": " + oneLine(c.Err.Error())
			return
		}
		p.checked[r.cand] = !p.checked[r.cand]
		return
	}
	want := p.rowState(r) != checkAll
	for _, i := range p.under(r.group) {
		if p.cands[i].Err == nil {
			p.checked[i] = want
		}
	}
}

// toggleAll checks every checkable file, or unchecks them all when they are all checked.
func (p *importPicker) toggleAll() {
	want := false
	for i, c := range p.cands {
		if c.Err == nil && !p.checked[i] {
			want = true
		}
	}
	for i, c := range p.cands {
		if c.Err == nil {
			p.checked[i] = want
		}
	}
}

// picked is the checked candidates in tree order.
func (p *importPicker) picked() []importCandidate {
	var out []importCandidate
	for i, c := range p.cands {
		if p.checked[i] {
			out = append(out, c)
		}
	}
	return out
}

// totals is the checked file count and the commands they hold.
func (p *importPicker) totals() (files, commands int) {
	for i, c := range p.cands {
		if p.checked[i] {
			files++
			commands += c.Count
		}
	}
	return files, commands
}

// countOf names a count with its noun, plural unless it is one.
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// importNoun is what a file's count counts, by its kind.
func importNoun(kind string) string {
	switch kind {
	case makeKind:
		return "target"
	case composeKind:
		return "service"
	}
	return "command"
}

// importNote is the text after a file's name: its count, its parse error, or its nested-imports warning.
func importNote(c importCandidate) string {
	if c.Err != nil {
		return oneLine(c.Err.Error())
	}
	note := countOf(c.Count, importNoun(c.Kind))
	if c.HasImports {
		note += " · ⚠ nested imports ignored"
	}
	return note
}

// shortDir is dir with the home folder as ~.
func shortDir(dir string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && (dir == home || strings.HasPrefix(dir, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

// openImports is I: the picker over the files below the rerun.yml's folder, with the ones it imports checked.
func (m *tuiModel) openImports() {
	rootFile := m.group.configPath
	if rootFile == "" {
		m.showNotice("rerun: no config to import files into", true)
		return
	}
	dir := filepath.Dir(rootFile)
	cands, truncated := scanImportable(dir, m.importOpts)
	if len(cands) == 0 {
		m.showNotice("no Makefile, compose or rerun.yml files below "+shortDir(dir), false)
		return
	}
	if !fileExists(rootFile) {
		m.imports = newImportPicker(rootFile, cands, truncated, false, nil, nil)
		return
	}
	cfg, err := ParseConfig(rootFile)
	if err != nil {
		m.showNotice("rerun: "+oneLine(err.Error()), true)
		return
	}
	schemas, err := rootSchemasOf(rootFile)
	if err != nil {
		m.showNotice("rerun: "+oneLine(err.Error()), true)
		return
	}
	imported := make(map[string]bool, len(cfg.Files))
	for _, f := range cfg.Files {
		imported[f] = true
	}
	m.imports = newImportPicker(rootFile, cands, truncated, true, schemas, imported)
}

// rootSchemasOf reads the schemas a rerun.yml defines, which its imports may refer to.
func rootSchemasOf(rootFile string) (map[string]groupDef, error) {
	data, err := os.ReadFile(rootFile)
	if err != nil {
		return nil, err
	}
	src, root, err := parseConfigBytes(rootFile, data)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(rootFile)
	if err != nil {
		return nil, err
	}
	file, err := parseFileBody(src, root, abs)
	if err != nil {
		return nil, err
	}
	return file.schemas, nil
}

// handleImportKey routes a key while the picker is open. Typing filters, so only space and the arrows act as commands.
func (m *tuiModel) handleImportKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.imports
	p.msg = ""
	switch barKeyName(msg) {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+[":
		m.imports = importPicker{}
		return m, nil
	case "up", "ctrl+p":
		p.cursor--
	case "down", "ctrl+n":
		p.cursor++
	case "space":
		if rows := p.rows(); p.cursor < len(rows) {
			p.toggle(rows[p.cursor])
		}
	case "a":
		// With a filter typed, a is a letter; otherwise it checks or unchecks everything.
		if p.query == "" {
			p.toggleAll()
		} else {
			m.importPaste(msg.Text)
		}
	case "enter", "ctrl+m":
		m.applyImports()
		return m, nil
	case "ctrl+s":
		m.saveImportPicks()
		return m, nil
	case "backspace", "ctrl+h":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.cursor, p.top = 0, 0
		}
	default:
		m.importPaste(msg.Text)
	}
	m.fitImports()
	return m, nil
}

// importPaste adds typed or pasted text to the filter, dropping control characters so a newline cannot split it.
func (m *tuiModel) importPaste(s string) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return
	}
	m.imports.query += s
	m.imports.cursor, m.imports.top = 0, 0
}

// importListRows is how many file rows fit between the filter and the status row.
func (m *tuiModel) importListRows(outerH int) int {
	innerH := max(1, outerH-m.styles.box.GetVerticalFrameSize())
	n := innerH - 3 - importFilterRows - importStatusRows
	if m.imports.truncated {
		n--
	}
	return max(1, n)
}

// fitImports clamps the cursor and the scroll window, called by the handlers so drawing changes nothing.
func (m *tuiModel) fitImports() {
	if !m.imports.open {
		return
	}
	p := &m.imports
	n := len(p.rows())
	p.cursor = min(max(p.cursor, 0), max(0, n-1))
	_, outerH := m.modalSize()
	capRows := m.importListRows(outerH)
	p.top = min(p.top, max(0, n-capRows))
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+capRows {
		p.top = p.cursor - capRows + 1
	}
}

// applyImports runs the checked files for this session: the config watcher applies them, and its reloads keep applying them. Nothing is written.
func (m *tuiModel) applyImports() {
	p := &m.imports
	picked := p.picked()
	if _, err := sessionConfig(p.rootFile, picked, p.schemas); err != nil {
		p.msg = oneLine(err.Error())
		return
	}
	m.group.session.Store(&importSession{rootFile: p.rootFile, picked: picked, schemas: p.schemas})
	m.group.nudgeReload()
	m.imports = importPicker{}
	m.keepNoticeUntil = m.now.Add(savedNoticeHold)
	m.showNotice("session runs "+countOf(len(picked), "file")+" · nothing saved", false)
}

// saveImportPicks writes the checked files as rerun.yml's imports and has the watcher load the saved file.
func (m *tuiModel) saveImportPicks() {
	p := &m.imports
	picked := p.picked()
	if len(picked) == 0 {
		p.msg = "nothing checked"
		return
	}
	if _, err := sessionConfig(p.rootFile, picked, p.schemas); err != nil {
		p.msg = oneLine(err.Error())
		return
	}
	if err := saveImports(p.rootFile, picked); err != nil {
		p.msg = oneLine(err.Error())
		return
	}
	m.group.session.Store(nil)
	m.group.nudgeReload()
	m.imports = importPicker{}
	m.keepNoticeUntil = m.now.Add(savedNoticeHold)
	m.showNotice("saved rerun.yml imports", false)
}

// importView draws the picker in the shared modal box.
func (m *tuiModel) importView() string {
	outerW, outerH := m.modalSize()
	innerW := m.modalInnerW(outerW)
	p := &m.imports
	rows := p.rows()
	capRows := m.importListRows(outerH)

	title := fmt.Sprintf("No rerun.yml in %s ── found %s", shortDir(p.dir), countOf(len(p.cands), "file"))
	footer := importFooterNew
	if p.exists {
		title = fmt.Sprintf("Import files ── found %s", countOf(len(p.cands), "file"))
		footer = importFooterUpdate
	}

	body := []string{m.styles.hint.Render("filter: ") + p.query + "_"}
	for i := p.top; i < len(rows) && i < p.top+capRows; i++ {
		body = append(body, m.importLine(rows[i], innerW, m.finderPaint(i == p.cursor)))
	}
	if len(rows) == 1 && p.query != "" {
		body = append(body, m.styles.hint.Render("no matches for \""+p.query+"\""))
	}
	for len(body) < 1+capRows {
		body = append(body, "")
	}
	if p.truncated {
		body = append(body, m.styles.hint.Render(importScanRow))
	}

	files, commands := p.totals()
	status := fmt.Sprintf("%d of %d files · %d commands", files, len(p.cands), commands)
	if p.msg != "" {
		status += " · " + p.msg
	}
	body = append(body, m.styles.hint.Render(status))
	return m.modalBox(m.styles.title.Render(title), footer, body, outerW, outerH)
}

// importLine is one row: the box, the tree glyphs and the name, and the file's note at the right edge.
func (m *tuiModel) importLine(r importRow, innerW int, paint func(lipgloss.Style) lipgloss.Style) string {
	left := checkBoxes[m.imports.rowState(r)] + " " + r.prefix + r.label
	right := ""
	if !r.folder {
		right = importNote(m.imports.cands[r.cand])
	}
	if room := innerW - lipgloss.Width(left) - 2; right != "" && lipgloss.Width(right) > room {
		right = ansi.Truncate(right, max(0, room), "…")
	}
	gap := max(0, innerW-lipgloss.Width(left)-lipgloss.Width(right))
	return paint(lipgloss.NewStyle()).Render(left+strings.Repeat(" ", gap)) +
		paint(m.styles.hint).Render(right)
}
