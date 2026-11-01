package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

// Left column geometry: glyph, space, name, badge, tree, status, space,
// button — nameW + statusW + 6 cells. The status column fits `exit segv`,
// `4d03h`, and `backoff 10s` (11 cells).
const statusW = 11

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

// treeX is the ledger's +/− cell: one column of the name's trailing cells.
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
// reboot ledger with it when both fit. The row itself wins when they do not.
func (m *tuiModel) listOffset(rows []visualRow) int {
	// The version line's row is never part of the list.
	visible := max(1, m.height-1)
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
		if r.target == want && r.kind == treeNone {
			return i
		}
	}
	return 0
}

// visualRow is one line of the left column. A target row can be followed by
// the cursor row's reboot ledger, so a screen line is not a target index:
// rowAt maps back through this list.
type visualRow struct {
	target int      // the row's target index; -1 is the ALL rollup
	kind   treeKind // which ledger line this is, treeNone on a target row
	at     time.Time
	count  int    // an entry header's or a pending line's file count
	op     string // a ledger file row's fsnotify glyph
	file   string // a ledger file row's path, relative to the watch root
}

// treeKind is which line of a row's reboot ledger a visual row draws.
type treeKind int

const (
	treeNone    treeKind = iota
	treeEntry            // `↺ 14:32:07 · 3 files`
	treeFile             // `+ pkg/api/types.gen.go`
	treePending          // `· 3 files recorded, not rebooted (stopped)`
)

// treeFiles caps one compact ledger entry's file lines. The header count
// carries the total, so a large batch never grows a `+k more` row.
const treeFiles = 2

// visualRows lays out the left column: the ALL rollup, then every target row
// with its reboot ledger beneath it when that row is expanded.
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

// treeRows is the compact reboot ledger under one target row: the batch a
// stopped target never used, then the newest spawn as a header and at most
// treeFiles file lines. A row with nothing to show grows no rows at all.
func (m *tuiModel) treeRows(i int) []visualRow {
	t := m.targets[i]
	if !t.hasContent() {
		return nil
	}
	var rows []visualRow
	if t.pendingStopped() {
		rows = append(rows, visualRow{target: i, kind: treePending, count: len(t.pending)})
	}
	if len(t.ledger) == 0 {
		return rows
	}
	e := t.ledger[0]
	rows = append(rows, visualRow{target: i, kind: treeEntry, at: e.at, count: len(e.files)})
	shown := min(len(e.files), treeFiles)
	for _, f := range e.files[:shown] {
		rows = append(rows, visualRow{
			target: i,
			kind:   treeFile,
			op:     opText(f.op),
			file:   relToWatch(t.spec.On, f.path),
		})
	}
	return rows
}

// versionCell draws the left column's bottom line: the version text and the
// help cue, hint styled and left aligned, with plain padding out to the
// divider column. A column too narrow for the cue keeps the bare version.
func (m *tuiModel) versionCell(leftW int) string {
	cue := 0
	if leftW >= 8 {
		cue = 4 // " · ?" always fits: the version text truncates to make room
	}
	text := truncate(m.versionText(), leftW-cue)
	if cue > 0 {
		text += " · ?"
	}
	return m.styles.hint.Render(text) + strings.Repeat(" ", max(0, leftW-displayWidth(text)))
}

// versionText names the binary: the release tag, the dev build with its age,
// or the plain dev fallback. A missing or unparseable buildTime drops the age.
func (m *tuiModel) versionText() string {
	switch {
	case strings.HasPrefix(m.version, "v"):
		return "rerun · " + m.version
	case strings.HasPrefix(m.version, "dev-"):
		if age, ok := buildAge(m.buildTime, m.now); ok {
			return fmt.Sprintf("rerun · %s (%s)", m.version, age)
		}
		return "rerun · " + m.version
	}
	return "rerun · dev"
}

// buildAge renders how long ago the binary was built. A build time in the
// future reads as `just now`, the way a clock skew should.
func buildAge(buildTime string, now time.Time) (string, bool) {
	built, err := time.Parse(time.RFC3339, buildTime)
	if err != nil {
		return "", false
	}
	d := now.Sub(built)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now", true
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes())), true
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours())), true
	}
	return fmt.Sprintf("%dd ago", int(d.Hours())/24), true
}

// opGlyph is the fsnotify op as one glyph: created, written or renamed, and
// removed. A set without op detail reads as a write.
func opGlyph(op fsnotify.Op) byte {
	switch {
	case op.Has(fsnotify.Create):
		return '+'
	case op.Has(fsnotify.Write), op.Has(fsnotify.Rename):
		return '~'
	case op.Has(fsnotify.Remove):
		return '-'
	}
	return '~'
}

// opText renders a change glyph. A removal is a minus sign, not a hyphen.
func opText(op byte) string {
	if op == '-' {
		return "−"
	}
	return string(op)
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
	// The list reserves the screen's very last row; the version line owns it.
	if y == m.height-1 {
		return m.versionCell(leftW)
	}
	idx := m.layout.offset + y
	if idx < 0 || idx >= len(rows) {
		return strings.Repeat(" ", leftW)
	}
	row := rows[idx]
	switch row.kind {
	case treeEntry:
		return m.entryLine(row, leftW)
	case treeFile:
		return m.treeLine(row, leftW)
	case treePending:
		return m.pendingLine(row, leftW)
	case treeNone:
		if row.target < 0 {
			return m.allLine(leftW)
		}
		return m.targetLine(row.target, leftW)
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

// entryLine is one ledger entry's header: when the spawn ran and how many
// files triggered it.
func (m *tuiModel) entryLine(row visualRow, leftW int) string {
	segs := []seg{
		{text: "  "},
		{text: "↺ " + row.at.Format("15:04:05") + " · " + fileCount(row.count), color: colorGray},
	}
	return renderSegs(segs, m.rowStyle(row.target+1), leftW)
}

// treeLine is one changed file of a ledger entry: its op glyph and path,
// indented under the entry header it belongs to.
func (m *tuiModel) treeLine(row visualRow, leftW int) string {
	segs := []seg{
		{text: "    "},
		{text: row.op, color: opColor(row.op)},
		{text: " "},
		{text: row.file},
	}
	return renderSegs(segs, m.rowStyle(row.target+1), leftW)
}

// pendingLine warns that a stopped or group-held target recorded changes no
// spawn used, so the files stay pending until a manual start.
func (m *tuiModel) pendingLine(row visualRow, leftW int) string {
	word := "stopped"
	if m.targets[row.target].state == stateOff {
		word = "off"
	}
	segs := []seg{
		{text: "  "},
		{text: "· " + fileCount(row.count) + " recorded, not rebooted (" + word + ")"},
	}
	return renderSegs(segs, m.rowStyle(row.target+1).Faint(true), leftW)
}

// opColor ties a ledger file glyph to the row palette: a created file is
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
	case stateRunning, stateStarting:
		return colorYellow
	case stateExited, stateBackoff:
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
		// The state chip never shows while a bar owns the line.
		chip := ""
		if m.inputMode == inputNone {
			chip = m.stateChip()
		}
		markerShown := !m.follow && m.pending > 0
		if markerShown {
			marker := fmt.Sprintf("↓ %d new", m.pending)
			m.layout.marker = rect{x: m.layout.leftW + 1, y: titleH + m.vp.Height - 1, w: w, h: 1}
			out[last] = lipgloss.NewStyle().Width(w).Render(m.markerLine(w, marker, chip))
		}
		// One line, six claimants, in precedence order: an open bar, the copy
		// hint, the new-lines marker (already drawn), the committed search
		// counter, the filter count, then the m/s hint.
		switch {
		case m.inputMode == inputSearch:
			out[last] = m.bottomLine(w, m.searchStatus(true), "")
		case m.inputMode == inputFilter && m.filterErr != "":
			out[last] = m.bottomLine(w, "yq: "+m.filterErr, "")
		case m.inputMode == inputFilter:
			out[last] = m.bottomLine(w, m.filterStatus()+"  "+m.input, "")
		case m.copyHintActive():
			// The OSC 52 sequence rides the hint line, so the renderer writes it
			// once and the terminal executes the copy.
			out[last] = lipgloss.NewStyle().Width(w).Render(m.styles.hint.Render(m.bottomText(w, m.copyHint, chip))) + m.copySeq
		case markerShown:
			// The marker owns the line and was drawn above.
		case m.search != "":
			out[last] = m.bottomLine(w, m.searchLine(), chip)
		case m.filter != "":
			out[last] = m.bottomLine(w, m.filterLine(), chip)
		case m.now.Before(m.hintUntil):
			out[last] = m.bottomLine(w, m.hint, chip)
		case !m.follow:
			// A frozen pane with nothing else to say still names the state.
			out[last] = m.bottomLine(w, "", chip)
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

// selectionRange is the selected display-row range, or ok == false when
// nothing is selected.
func (m *tuiModel) selectionRange() (lo, hi int, ok bool) {
	m.ensurePane()
	n := m.paneLen()
	if m.selAnchor == nil || n == 0 {
		return 0, 0, false
	}
	lo, hi = *m.selAnchor, m.selEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	lo = max(lo, 0)
	hi = min(hi, n-1)
	if lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// bottomLine draws plain text faint across the pane's width, with the state
// chip appended after it.
func (m *tuiModel) bottomLine(w int, text string, chip string) string {
	return lipgloss.NewStyle().Width(w).Render(m.styles.hint.Render(m.bottomText(w, text, chip)))
}

// bottomText joins text and the chip, truncating text so the chip survives at
// narrow widths.
func (m *tuiModel) bottomText(w int, text string, chip string) string {
	if chip == "" {
		return truncate(text, w)
	}
	if text == "" {
		return truncate(chip, w)
	}
	suffix := " · " + chip
	if displayWidth(suffix) >= w {
		return truncate(chip, w)
	}
	return truncate(text, w-displayWidth(suffix)) + suffix
}

// markerLine draws the new-lines marker, with the state chip after it in the
// hint style.
func (m *tuiModel) markerLine(w int, marker string, chip string) string {
	if chip == "" {
		return m.styles.marker.Render(truncate(marker, w))
	}
	suffix := " · " + chip
	if displayWidth(suffix) >= w {
		return m.styles.hint.Render(truncate(chip, w))
	}
	return m.styles.marker.Render(truncate(marker, w-displayWidth(suffix))) + m.styles.hint.Render(suffix)
}

// stateChip is the bottom line's frozen/solo suffix. Solo alone does not claim
// an idle line, so following output keeps its newest row visible.
func (m *tuiModel) stateChip() string {
	parts := make([]string, 0, 2)
	if !m.follow {
		parts = append(parts, "frozen (f/G)")
	}
	if m.solo >= 0 {
		parts = append(parts, "solo "+m.targets[m.solo].spec.Name)
	}
	return strings.Join(parts, " · ")
}

// searchLine is the committed search counter.
func (m *tuiModel) searchLine() string {
	return "/" + m.search + " · " + m.searchStatus(false) + " · esc"
}

// filterLine is the committed filter counter, query included.
func (m *tuiModel) filterLine() string {
	return m.filter + " · " + m.filterStatus() + " · esc"
}

// copyHintActive reports whether the copy feedback still owns the bottom line.
func (m *tuiModel) copyHintActive() bool {
	return m.copyHint != "" && m.now.Before(m.copyUntil)
}

// fileCount renders `1 file` or `3 files`.
func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// titleLine is the right pane's first line: the cursor row's state, how many
// files its last change batch held, and its command. The state replaces the
// uptime whenever the row is not running; an exited row leads with its exit
// glyph. The url follows, linked, when the target has one.
func (m *tuiModel) titleLine(w int) string {
	t := m.targets[m.cursor-1]

	segs := make([]seg, 0, 5)
	if t.state == stateExited {
		segs = append(segs, seg{text: t.glyph() + " ", color: stateColor(t)})
	}
	segs = append(segs, seg{text: fmt.Sprintf("%s · %s · %s", m.status(t), fileCount(len(t.lastChange.Files)), t.spec.Cmd)})
	if t.spec.URL != "" {
		segs = append(segs, seg{text: "  "}, seg{text: t.spec.URL, link: t.spec.URL})
	}
	return renderSegs(segs, m.styles.title, w)
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

	lines := m.tableLines(m.paneLines())
	content := strings.Join(lines, "\n")
	if len(lines) == 0 {
		content = m.styles.hint.Render(m.emptyHint())
	}
	m.vp.SetContent(content)
	// SetContent only pulls YOffset back from past-the-end; a shrink that still
	// leaves it below the last row needs an explicit clamp, or the frozen pane
	// renders blank rows under the content.
	m.vp.SetYOffset(m.vp.YOffset)
	m.vpDirty = false
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *tuiModel) emptyHint() string {
	if m.solo >= 0 {
		return "solo: " + m.targets[m.solo].spec.Name
	}
	if m.filter != "" {
		return "no lines match the filter · esc clears"
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
	{"t", "collapse / expand the cursor row's reboot ledger"},
	{"m", "mute the target's stream (M)"},
	{"s enter", "solo the target's stream (S; again or esc to clear)"},
	{"esc", "clear search, then filter, then solo"},
	{"o", "open the target's url"},
	{"G", "jump to live output"},
	{"f", "freeze / resume the pane"},
	{"/", "search the pane (n/N next/prev, esc clears)"},
	{"v", "filter (esc clears; leading . is yq)"},
	{"c", "toggle the columns table"},
	{"pgup/pgdn", "scroll the pane (ctrl+u / ctrl+d)"},
	{"?", "close this help"},
	{"^C", "quit (q closes help)"},
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
		"double-click to stop/start like space, click its button cell to mute.",
		"click a pane line to copy it; drag copies a range and freezes the tail",
		"(f, G, or ↓ N new resume). shift-drag uses the terminal selection.",
		"space starts/stops — it does not page; pgup/pgdn/ctrl-u/d page.",
		"the pane's top line is the cursor row's uptime, files, and cmd.",
		"each row's reboot ledger shows its newest spawn and its files; all",
		"rows start expanded, t or the row's +/− cell collapses that row.",
		"a filter starting with . runs a yq expression per JSON line.",
		"saving rerun.yml reloads it: the banner names what changed.",
		"● run  ○ stopped  ✓ ok  ✗ exit  · off  ✱ unseen  ⟳ starting/backoff",
	} {
		b.WriteString(m.styles.hint.Render(line) + "\n")
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		m.styles.box.Render(strings.TrimRight(b.String(), "\n")))
}
