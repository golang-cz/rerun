package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fsnotify/fsnotify"
)

// Herdr-style palette: the color leads, the glyph shape backs it up.
const (
	colorYellow = "3"
	colorGreen  = "2"
	colorRed    = "1"
	colorGray   = "8"
	colorSolo   = "6"
)

// Left column geometry: glyph, space, name, badge, status, space, button —
// nameW + 14 cells. The longest real status is `exit segv` and the longest
// uptime form is `4d03h`.
const statusW = 9

type tuiStyles struct {
	base    lipgloss.Style
	divider lipgloss.Style
	cursor  lipgloss.Style
	hover   lipgloss.Style
	muted   lipgloss.Style
	solo    lipgloss.Style
	all     lipgloss.Style
	title   lipgloss.Style
	marker  lipgloss.Style
	hint    lipgloss.Style
	box     lipgloss.Style
	key     lipgloss.Style
}

func newTUIStyles() tuiStyles {
	return tuiStyles{
		base:    lipgloss.NewStyle(),
		divider: lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		cursor:  lipgloss.NewStyle().Background(lipgloss.Color("237")),
		hover:   lipgloss.NewStyle().Background(lipgloss.Color("235")),
		muted:   lipgloss.NewStyle().Faint(true),
		solo:    lipgloss.NewStyle().Background(lipgloss.Color("234")),
		all:     lipgloss.NewStyle().Bold(true),
		title:   lipgloss.NewStyle().Bold(true),
		marker:  lipgloss.NewStyle().Reverse(true),
		hint:    lipgloss.NewStyle().Faint(true),
		box:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		key:     lipgloss.NewStyle().Bold(true),
	}
}

// seg is one styled piece of a row. color == "" keeps the row's own style.
type seg struct {
	text  string
	color string
	link  string
}

// rect is a cell area on screen, used for mouse hit testing.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// tuiLayout records what the last frame drew so mouse hits can be resolved.
type tuiLayout struct {
	leftW  int
	offset int
	marker rect
}

func displayWidth(s string) int { return lipgloss.Width(s) }

// View draws the frame: the target list, a divider, and the stream.
func (m *tuiModel) View() string {
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}
	m.layout = tuiLayout{marker: rect{x: -1, y: -1}}
	if m.help {
		return m.helpView()
	}

	leftW, rightW := m.layoutWidths()
	m.layout.leftW = leftW
	rows := m.visualRows()
	m.layout.offset = m.listOffset(rows)
	right := m.rightLines(rightW)

	var b strings.Builder
	for y := 0; y < m.height; y++ {
		b.WriteString(m.leftLine(y, leftW, rows))
		b.WriteString(m.styles.divider.Render("│"))
		b.WriteString(right[y])
		if y < m.height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// layoutWidths splits the screen between the two columns.
func (m *tuiModel) layoutWidths() (leftW, rightW int) {
	nameW := 4 // "ALL" plus the badge
	for _, t := range m.targets {
		nameW = max(nameW, displayWidth(t.spec.Name))
	}
	leftW = nameW + statusW + 6
	if limit := max(8, m.width/2); leftW > limit {
		leftW = limit
	}
	if limit := m.width - 2; leftW > limit {
		leftW = max(4, limit)
	}
	return leftW, max(1, m.width-leftW-1)
}

// nameWidth is the name cell's width for a given left column width.
func nameWidth(leftW int) int { return max(0, leftW-statusW-6) }

// treeX is the change-tree cell: one column of the name's trailing cells.
func treeX(leftW int) int { return nameWidth(leftW) + 3 }

// badge is the name column's trailing cell: a marker for hidden output, else
// a plain separator, so the status column never shifts.
func badge(unseen bool) string {
	if unseen {
		return "✱"
	}
	return " "
}

// listOffset scrolls the list so the cursor's row stays visible, and its
// change tree with it when both fit. The row itself wins when they do not.
func (m *tuiModel) listOffset(rows []visualRow) int {
	visible := max(1, m.height)
	start := visualIndex(rows, m.cursor)
	end := start
	for end+1 < len(rows) && start > 0 && rows[end+1].target == m.cursor-1 {
		end++
	}
	offset := 0
	if end >= visible {
		offset = end - visible + 1
	}
	if start < offset {
		offset = start
	}
	return min(offset, max(0, len(rows)-visible))
}

// visualIndex is the first screen line of a row (0 = ALL, 1..n = target), or 0
// when that row is gone.
func visualIndex(rows []visualRow, row int) int {
	want := row - 1
	for i, r := range rows {
		if r.target == want && r.file == "" && r.more == 0 {
			return i
		}
	}
	return 0
}

// visualRow is one line of the left column. A target row can be followed by
// its change tree, so a screen line is not a target index: rowAt maps back
// through this list.
type visualRow struct {
	target int    // the row's target index; -1 is the ALL rollup
	op     string // a change-tree row's fsnotify glyph
	file   string // a change-tree row's path, relative to the watch root
	more   int    // > 0 on the "+k more" row
}

// treeMax caps a row's change tree, so a batch of four hundred files cannot
// swallow the list. The rest is reported as one "+k more" row.
const treeMax = 5

// visualRows lays out the left column: the ALL rollup, then every target row
// with the expanded row's change tree beneath it.
func (m *tuiModel) visualRows() []visualRow {
	rows := make([]visualRow, 0, len(m.targets)+1)
	rows = append(rows, visualRow{target: -1})
	for i := range m.targets {
		rows = append(rows, visualRow{target: i})
		if m.treeExpanded(i) {
			rows = append(rows, m.treeRows(i)...)
		}
	}
	return rows
}

// treeRows is the change tree under one target row.
func (m *tuiModel) treeRows(i int) []visualRow {
	changes := m.targets[i].changeRows
	if len(changes) == 0 {
		return nil
	}
	shown := min(len(changes), treeMax)
	rows := make([]visualRow, 0, shown+1)
	for _, c := range changes[:shown] {
		rows = append(rows, visualRow{target: i, op: c.op, file: c.path})
	}
	if rest := len(changes) - shown; rest > 0 {
		rows = append(rows, visualRow{target: i, more: rest})
	}
	return rows
}

// changeRow is one file of a target's last change set, ready to render.
type changeRow struct {
	op   string
	path string
}

// opGlyph is the fsnotify op as one glyph: created, written or renamed, and
// removed. A set without op detail reads as a write.
func opGlyph(op fsnotify.Op) string {
	switch {
	case op.Has(fsnotify.Create):
		return "+"
	case op.Has(fsnotify.Write), op.Has(fsnotify.Rename):
		return "~"
	case op.Has(fsnotify.Remove):
		return "−"
	}
	return "~"
}

// relToWatch trims a changed path to the target's watch root, so the tree reads
// `cmd/rerun/tui.go` and not `./cmd/rerun/tui.go`. A path no root contains, as
// a glob root can leave, keeps its own form.
func relToWatch(roots []string, path string) string {
	if len(roots) == 0 {
		roots = []string{"./"}
	}
	for _, root := range roots {
		if strings.ContainsAny(root, "*?[") {
			continue // a glob root is expanded by the watcher, not here
		}
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		return rel
	}
	return filepath.Clean(path)
}

func (m *tuiModel) leftLine(y, leftW int, rows []visualRow) string {
	idx := m.layout.offset + y
	if idx < 0 || idx >= len(rows) {
		return strings.Repeat(" ", leftW)
	}
	row := rows[idx]
	switch {
	case row.target < 0:
		return m.allLine(leftW)
	case row.more > 0:
		return m.moreLine(row, leftW)
	case row.file != "":
		return m.treeLine(row, leftW)
	}
	return m.targetLine(row.target, leftW)
}

// allLine is the rollup row: `3● 1✓ 1✗`, with a badge for unseen output.
func (m *tuiModel) allLine(leftW int) string {
	segs := []seg{
		{text: " "},
		{text: " "},
		{text: padRight("ALL", nameWidth(leftW))},
		{text: badge(m.anyUnseen())},
	}
	segs = append(segs, m.rollupSegs()...)
	return renderSegs(segs, m.rowStyle(0), leftW)
}

func (m *tuiModel) targetLine(i, leftW int) string {
	t := m.targets[i]

	btn := m.button(i)
	btnColor := ""
	switch btn {
	case "M":
		btnColor = colorGray
	case "S":
		btnColor = colorSolo
	}

	segs := []seg{
		{text: t.glyph(), color: stateColor(t)},
		{text: " "},
		{text: padRight(t.spec.Name, nameWidth(leftW)), link: t.spec.URL},
		{text: badge(t.unseen)},
		{text: m.treeCell(i), color: colorGray},
		{text: padRight(m.status(t), statusW), color: statusColor(t)},
		{text: " "},
		{text: btn, color: btnColor},
	}
	return renderSegs(segs, m.rowStyle(i+1), leftW)
}

// treeLine is one change-tree row: its op glyph and path, indented under the
// target row it belongs to.
func (m *tuiModel) treeLine(row visualRow, leftW int) string {
	segs := []seg{
		{text: "  "},
		{text: row.op, color: opColor(row.op)},
		{text: " "},
		{text: row.file},
	}
	return renderSegs(segs, m.rowStyle(row.target+1), leftW)
}

// moreLine stands in for the rest of a capped change tree.
func (m *tuiModel) moreLine(row visualRow, leftW int) string {
	segs := []seg{
		{text: "    "},
		{text: fmt.Sprintf("+%d more", row.more), color: colorGray},
	}
	return renderSegs(segs, m.rowStyle(row.target+1), leftW)
}

// opColor ties a change-tree glyph to the row palette: a created file is
// green, a removed one red, a written one yellow.
func opColor(op string) string {
	switch op {
	case "+":
		return colorGreen
	case "−":
		return colorRed
	}
	return colorYellow
}

func (m *tuiModel) rowStyle(row int) lipgloss.Style {
	switch {
	case row == m.cursor:
		return m.styles.cursor
	case row == m.hover:
		return m.styles.hover
	case row > 0 && m.targets[row-1].muted:
		return m.styles.muted
	case row > 0 && m.solo == row-1:
		return m.styles.solo
	}
	return m.styles.base
}

func stateColor(t *tuiTarget) string {
	switch t.state {
	case stateRunning:
		return colorYellow
	case stateExited:
		if t.exitCode == 0 {
			return colorGreen
		}
		return colorRed
	}
	return colorGray
}

// statusColor keeps the uptime quiet and lets a red or green exit speak.
func statusColor(t *tuiTarget) string {
	if t.state == stateExited {
		return stateColor(t)
	}
	return ""
}

// renderSegs draws one row of styled segments in exactly width cells. Text
// past the edge is cut, never wrapped: a wrapped row breaks the frame's grid.
func renderSegs(segs []seg, base lipgloss.Style, width int) string {
	var b strings.Builder
	cells := 0
	for _, s := range segs {
		if s.text == "" || cells >= width {
			continue
		}
		style := base
		if s.color != "" {
			style = style.Foreground(lipgloss.Color(s.color))
		}
		text := truncate(s.text, width-cells)
		if text == "" {
			continue
		}
		if s.link != "" {
			text = osc8(s.link, text)
		}
		b.WriteString(style.Render(text))
		cells += displayWidth(text)
	}
	if cells < width {
		b.WriteString(base.Render(strings.Repeat(" ", width-cells)))
	}
	return b.String()
}

// rightLines is the stream pane, exactly m.height lines tall. The cursor row
// owns a title line at the top; the bottom line carries the new-lines marker,
// else the ALL-row hint.
func (m *tuiModel) rightLines(w int) []string {
	titleH := 0
	var title string
	if m.cursor > 0 {
		title = m.titleLine(w)
		titleH = 1
	}

	m.vp.Width = w
	m.vp.Height = max(1, m.height-titleH)
	m.syncContent()

	out := strings.Split(m.vp.View(), "\n")

	// Selection is line-granular and styled before the bottom line's own
	// replacement, so the marker and hint never read as selected.
	if lo, hi, ok := m.selectionRange(); ok {
		for idx := lo; idx <= hi; idx++ {
			row := idx - m.vp.YOffset
			if row < 0 || row >= len(out) {
				continue
			}
			out[row] = m.styles.marker.Render(out[row])
		}
	}

	if len(out) > 0 {
		last := len(out) - 1
		markerShown := !m.follow && m.pending > 0
		if markerShown {
			marker := fmt.Sprintf("↓ %d new", m.pending)
			m.layout.marker = rect{x: m.layout.leftW + 1, y: titleH + m.vp.Height - 1, w: w, h: 1}
			out[last] = lipgloss.NewStyle().Width(w).Render(m.styles.marker.Render(marker))
		}
		switch {
		case m.copyHintActive():
			// The OSC 52 sequence rides the hint line, so the renderer writes it
			// once and the terminal executes the copy.
			out[last] = lipgloss.NewStyle().Width(w).Render(m.styles.hint.Render(truncate(m.copyHint, w))) + m.copySeq
		case !markerShown && m.now.Before(m.hintUntil):
			out[last] = lipgloss.NewStyle().Width(w).Render(m.styles.hint.Render(truncate(allRowHint, w)))
		}
	}

	lines := make([]string, 0, m.height)
	if titleH == 1 {
		lines = append(lines, title)
	}
	lines = append(lines, out...)
	for len(lines) < m.height {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:m.height]
}

// selectionRange is the selected content range, or ok == false when nothing is
// selected.
func (m *tuiModel) selectionRange() (lo, hi int, ok bool) {
	if m.selAnchor == nil || len(m.visibleRaw) == 0 {
		return 0, 0, false
	}
	lo, hi = *m.selAnchor, m.selEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	lo = max(lo, 0)
	hi = min(hi, len(m.visibleRaw)-1)
	if lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// copyHintActive reports whether the copy feedback still owns the bottom line.
func (m *tuiModel) copyHintActive() bool {
	return m.copyHint != "" && m.now.Before(m.copyUntil)
}

// titleLine is the right pane's first line: how long the cursor row has been
// up, how many files its last change batch held, and its command. The url
// follows, linked, when the target has one.
func (m *tuiModel) titleLine(w int) string {
	t := m.targets[m.cursor-1]
	plural := ""
	if n := len(t.lastChange.Files); n != 1 {
		plural = "s"
	}
	head := fmt.Sprintf("%s · %d file%s · %s", m.status(t), len(t.lastChange.Files), plural, t.spec.Cmd)
	cmd := truncate(head, w)
	line := m.styles.title.Render(cmd)
	if t.spec.URL != "" {
		room := max(0, w-displayWidth(cmd)-2)
		if url := truncate(t.spec.URL, room); url != "" {
			line += m.styles.title.Render("  " + osc8(t.spec.URL, url))
		}
	}
	return lipgloss.NewStyle().Width(w).Render(line)
}

// syncContent rebuilds the viewport only when its content changed, and keeps
// it pinned to the tail while following.
func (m *tuiModel) syncContent() {
	if !m.vpDirty {
		if m.follow {
			m.vp.GotoBottom()
		}
		return
	}

	lines := m.paneLines()
	content := strings.Join(lines, "\n")
	if len(lines) == 0 {
		content = m.styles.hint.Render(m.emptyHint())
	}
	m.vp.SetContent(content)
	m.vpDirty = false
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *tuiModel) emptyHint() string {
	if m.solo >= 0 {
		return "solo: " + m.targets[m.solo].spec.Name
	}
	if m.allMuted() {
		return "every stream is muted — m unmutes the highlighted target"
	}
	return "no output yet — press ? for keys"
}

func (m *tuiModel) allMuted() bool {
	for _, t := range m.targets {
		if !t.muted {
			return false
		}
	}
	return len(m.targets) > 0
}

// helpKeys is the help overlay's content, in display order.
var helpKeys = [][2]string{
	{"↑/↓ j/k", "move between rows"},
	{"space", "start / stop (ALL row: every target)"},
	{"r", "reboot (grace applies; ALL row: every selected target)"},
	{"t", "expand the row's change tree"},
	{"m", "mute the target's stream (M)"},
	{"s enter", "solo the target's stream (S; again or esc to clear)"},
	{"esc", "clear solo"},
	{"o", "open the target's url"},
	{"G", "jump to live output"},
	{"f", "freeze / resume the pane"},
	{"pgup/pgdn", "scroll the pane (ctrl+u / ctrl+d)"},
	{"?", "close this help"},
	{"q ^C", "quit (a second ^C forces the kill)"},
}

func (m *tuiModel) helpView() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("rerun") + "\n\n")
	for _, kv := range helpKeys {
		b.WriteString(m.styles.key.Render(padRight(kv[0], 10)) + " " + kv[1] + "\n")
	}
	b.WriteString("\n")
	for _, line := range []string{
		"mouse: click a row to solo it (again or on ALL to clear),",
		"double-click to stop/start like space, click its button cell to",
		"mute, shift-click to solo.",
		"a click in the live pane freezes the tail, then anchors the copy.",
		"frozen copy: click or drag; OSC 52 works over ssh and tmux.",
		"shift-drag selects text with the terminal's own copy.",
		"the pane's top line is the cursor row's uptime, files, and cmd.",
		"the selected row's change tree lists its last changed files;",
		"t or a click on its +/− cell expands and collapses it.",
		"saving rerun.yml reloads it: the banner names what changed.",
	} {
		b.WriteString(m.styles.hint.Render(line) + "\n")
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		m.styles.box.Render(strings.TrimRight(b.String(), "\n")))
}
