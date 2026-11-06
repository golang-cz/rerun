package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
)

// Herdr-style palette: the color leads, the glyph shape backs it up.
const (
	colorYellow = "3"
	colorGreen  = "2"
	colorRed    = "1"
	colorGray   = "8"

	// Run-state colors, apart from the rule colors, so healthy and starting read differently.
	runGreen  = "46"
	runYellow = "220"
	runRed    = "196"
)

// Left column geometry: glyph, space, name, a two-cell marker, status —
// nameW + statusW + 4 cells. The status column fits `exit segv`, `4d03h`, and
// `backoff 10s` (11 cells).
const statusW = 11

// narrowW is the screen width under which the left column shrinks to its names.
const narrowW = 100

type tuiStyles struct {
	base         lipgloss.Style
	divider      lipgloss.Style
	dividerFocus lipgloss.Style
	cursor       lipgloss.Style
	hover        lipgloss.Style
	muted        lipgloss.Style
	solo         lipgloss.Style
	all          lipgloss.Style
	title        lipgloss.Style
	marker       lipgloss.Style
	hint         lipgloss.Style
	box          lipgloss.Style
	key          lipgloss.Style
	paneCursor   lipgloss.Style
	chip         lipgloss.Style
	chipFind     lipgloss.Style
}

func newTUIStyles() tuiStyles {
	return tuiStyles{
		base:         lipgloss.NewStyle(),
		divider:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		dividerFocus: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		cursor:       lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "252", Dark: "237"}),
		hover:        lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "254", Dark: "235"}),
		muted:        lipgloss.NewStyle().Faint(true),
		solo:         lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "253", Dark: "234"}),
		all:          lipgloss.NewStyle().Bold(true),
		title:        lipgloss.NewStyle().Bold(true),
		marker:       lipgloss.NewStyle().Reverse(true),
		hint:         lipgloss.NewStyle().Faint(true),
		box:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		key:          lipgloss.NewStyle().Bold(true),
		paneCursor:   lipgloss.NewStyle().Reverse(true),
		chip:         lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		chipFind:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Underline(true),
	}
}

// seg is one styled piece of a row. color == "" keeps the row's own style.
type seg struct {
	text  string
	color string
	link  string
	faint bool
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
	drawer []drawerRow
	marker rect
	// chips and heads are the header's clickable cells: chip × cells on the
	// filter bar, and table column names on the column header row.
	chips []chipHit
	heads []cellSpan
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
	m.layout.drawer = m.drawerRows()
	m.layout.offset = m.listOffset()
	right := m.rightLines(rightW)

	divider, glyph := m.styles.divider, "│"
	if m.paneFocus {
		divider, glyph = m.styles.dividerFocus, "┃"
	}

	var b strings.Builder
	for y := 0; y < m.height; y++ {
		b.WriteString(m.leftLine(y, leftW))
		b.WriteString(divider.Render(glyph))
		b.WriteString(right[y])
		if y < m.height-1 {
			b.WriteByte('\n')
		}
	}
	frame := b.String()
	if m.confirmQuit {
		return overlayCenter(frame, m.confirmQuitView(), m.width, m.height)
	}
	if m.cfgErr != "" {
		return overlayCenter(frame, m.configErrView(), m.width, m.height)
	}
	return frame
}

// confirmQuitView is the centered quit modal, drawn over the live frame.
func (m *tuiModel) confirmQuitView() string {
	quit := lipgloss.NewStyle().Background(lipgloss.Color("1")).Foreground(lipgloss.Color("15")).Bold(true).Render("[ Quit ]")
	cancel := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("[ Cancel ]")
	body := m.styles.title.Render("Quit rerun?") + "\n" +
		m.styles.hint.Render("Every target is terminated and every watcher stops.") + "\n"
	if m.dirty() {
		body += m.styles.title.Render("unsaved columns — ctrl+s to save") + "\n"
	}
	body += "\n" + quit + "  " + cancel + "\n\n" +
		m.styles.hint.Render("enter quits · esc cancels · ctrl+c again quits")
	// Width fits the body line on one row; 44 would wrap and split the sentence.
	return m.styles.box.Padding(1, 2).Width(56).Render(body)
}

// Only the body is cut, so the border and the close hint survive a small screen.
func (m *tuiModel) configErrView() string {
	w := min(80, max(24, m.width-4))
	body := strings.Split(lipgloss.NewStyle().Width(w-6).Render(m.cfgErr), "\n")
	maxH := max(8, m.height-2)
	box := func(lines []string) string {
		return m.styles.box.Padding(1, 2).Width(w).Render(
			m.styles.title.Render("rerun.yml not reloaded") + "\n\n" + strings.Join(lines, "\n") + "\n\n" +
				m.styles.hint.Render("old config still running · esc close"))
	}
	full := len(body)
	for len(body) > 1 && strings.Count(box(body), "\n")+1 > maxH {
		body = body[:len(body)-1]
	}
	if len(body) < full {
		body[len(body)-1] = "…"
	}
	return box(body)
}

// printable drops controls that would drive the terminal; newlines stay only when keepNL is set.
func printable(s string, keepNL bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && keepNL:
			return r
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		}
		return r
	}, s)
}

// overlayCenter splices box into frame at the canvas center, so the stream and
// drawer underneath keep updating while the modal is up.
func overlayCenter(frame, box string, width, height int) string {
	lines := strings.Split(frame, "\n")
	boxLines := strings.Split(box, "\n")
	bw := lipgloss.Width(box)
	x := max(0, (width-bw)/2)
	y := max(0, (height-len(boxLines))/2)
	for i, bl := range boxLines {
		row := y + i
		if row >= len(lines) {
			break
		}
		line := lines[row]
		lines[row] = ansi.Cut(line, 0, x) + bl + ansi.Cut(line, x+bw, width)
	}
	return strings.Join(lines, "\n")
}

// layoutWidths splits the screen between the two columns.
func (m *tuiModel) layoutWidths() (leftW, rightW int) {
	nameW := 1
	for _, r := range m.listRows() {
		w := 2 * r.depth // each indent level sits in front of the name
		if r.header {
			w += displayWidth(r.label)
		} else {
			w += displayWidth(m.targets[r.target].spec.localName())
		}
		nameW = max(nameW, w)
	}
	if m.hasGroups() {
		nameW = max(nameW, displayWidth(groupLabel(ungrouped)))
	}
	leftW = nameW + statusW + 4
	// A wide screen keeps 24 cells, which fits the footer's `· ? help` cue beside
	// the version; a narrow one shrinks to the names, down to 16.
	if m.width < narrowW {
		leftW = max(16, leftW)
	} else {
		leftW = max(24, leftW)
	}
	if limit := max(8, m.width/2); leftW > limit {
		leftW = limit
	}
	if limit := m.width - 2; leftW > limit {
		leftW = max(4, limit)
	}
	return leftW, max(1, m.width-leftW-1)
}

// nameWidth is the name cell's width for a given left column width.
func nameWidth(leftW int) int { return max(0, leftW-statusW-4) }

// marker is the two cells after a name: `*` when its columns await ctrl+s, else blank,
// so the status never shifts.
func marker(dirty bool) string {
	if dirty {
		return "* "
	}
	return "  "
}

// listLen counts the list's screen lines: schema headers and targets.
func (m *tuiModel) listLen() int { return len(m.listRows()) }

// listHeight is how many screen rows the target list has: everything above the
// drawer, which itself sits above a blank divider and the version line.
func (m *tuiModel) listHeight() int {
	reserved := len(m.layout.drawer)
	if reserved > 0 {
		reserved++ // the blank line between the drawer and the version line
	}
	return max(1, m.height-1-reserved)
}

// listOffset keeps the list's stored top row, and moves it only when the cursor
// leaves the window: just enough to put the cursor on the top or bottom row.
// The drawer never scrolls with it.
func (m *tuiModel) listOffset() int {
	visible := m.listHeight()
	top := m.listTop
	switch {
	case m.cursor < top:
		top = m.cursor
	case m.cursor >= top+visible:
		top = m.cursor - visible + 1
	}
	// Rows that shrank (a fold, a reload, a taller screen) never leave blank rows below the last one.
	top = min(max(top, 0), max(0, m.listLen()+len(m.missing)-visible))
	m.listTop = top
	return top
}

// inDrawer reports whether a screen row is inside the drawer, and which of its
// lines it is. The drawer sits between the list and the version line.
func (m *tuiModel) inDrawer(y int) (drawerRow, bool) {
	n := len(m.layout.drawer)
	if n == 0 || y < m.height-2-n || y >= m.height-2 {
		return drawerRow{}, false
	}
	return m.layout.drawer[y-(m.height-2-n)], true
}

// versionCell draws the left column's bottom line: the version text and the
// help cue, hint styled and left aligned, with plain padding out to the
// divider column. A column too narrow for the cue keeps the bare version.
func (m *tuiModel) versionCell(leftW int) string {
	cue := ""
	switch {
	case leftW >= 16:
		cue = " · ? help"
	case leftW >= 8:
		cue = " · ?"
	}
	version := m.versionText()
	if displayWidth(version)+displayWidth(cue) > leftW {
		version = strings.TrimPrefix(version, "rerun · ") // a narrow column keeps the version, not the name
	}
	text := strings.TrimRight(ellipsize(version, leftW-displayWidth(cue)), " ") + cue
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

// leftLine draws one screen line of the left column: the version line owns the
// last row, the fs drawer the rows above it, and the target list the rest.
func (m *tuiModel) leftLine(y, leftW int) string {
	// The list reserves the screen's very last row; the version line owns it.
	if y == m.height-1 {
		return m.versionCell(leftW)
	}
	// The blank divider keeps the drawer's last line off the version line.
	if len(m.layout.drawer) > 0 && y == m.height-2 {
		return strings.Repeat(" ", leftW)
	}
	if row, ok := m.inDrawer(y); ok {
		return m.drawerLine(row, leftW)
	}
	r, ok := m.listRowAt(m.layout.offset + y)
	if !ok {
		if i := m.layout.offset + y - m.listLen(); i >= 0 && i < len(m.missing) {
			return m.missingLine(m.missing[i], leftW)
		}
		return strings.Repeat(" ", leftW)
	}
	if r.header {
		return m.schemaLine(m.layout.offset+y, r, leftW)
	}
	return m.targetLine(m.layout.offset+y, r, leftW)
}

// drawerLine draws one line of the fs drawer: its header, a feed batch, or a
// file list's directory and file lines.
func (m *tuiModel) drawerLine(row drawerRow, leftW int) string {
	base := m.styles.base
	if row.dim {
		base = m.styles.muted
	}
	switch row.kind {
	case drawerSep:
		label := truncate(row.label+" ", leftW)
		return m.styles.divider.Render(label + strings.Repeat("─", max(0, leftW-displayWidth(label))))

	case drawerBatch:
		if row.cursor && m.drawerFocus {
			base = m.styles.cursor
		}
		return batchLine(row, leftW, base)

	case drawerDir:
		return renderSegs([]seg{{text: row.label}}, m.styles.all, leftW)

	case drawerFile:
		segs := []seg{
			{text: strings.Repeat(" ", row.indent)},
			{text: row.glyph, color: opColor(row.glyph)},
			{text: " " + row.label},
		}
		return renderSegs(segs, base, leftW)
	}
	return strings.Repeat(" ", leftW)
}

// batchLine draws a feed batch as `  5s  path +N  → targets`. The arrow is reserved
// first; the path takes the rest and cuts from its left, so the file name survives.
func batchLine(row drawerRow, leftW int, base lipgloss.Style) string {
	age := strings.Repeat(" ", max(0, feedAgeW-displayWidth(row.age))) + row.age
	path := row.label
	if row.extra > 0 {
		path += fmt.Sprintf(" +%d", row.extra)
	}
	room := leftW - feedAgeW - 2
	arrow := targetsArrow(row.targets, room, displayWidth(path))
	return renderSegs([]seg{
		{text: age, faint: true},
		{text: "  "},
		{text: ellipsizeLeft(path, room-displayWidth(arrow))},
		{text: arrow, color: colorGray},
	}, base, leftW)
}

// targetsArrow is the batch's `→ targets` cell. It names every target when the path
// keeps minPath cells beside it, else the first target, else how many.
func targetsArrow(targets []string, room, pathW int) string {
	if len(targets) == 0 {
		return ""
	}
	full := "  → " + strings.Join(targets, ", ")
	first := "  → " + targets[0]
	count := fmt.Sprintf("  → %d targets", len(targets))
	budget := room - min(pathW, 12)
	switch {
	case displayWidth(full) <= budget:
		return full
	case budget >= displayWidth(first)+2: // room for the first name and a cut mark
		return ellipsize(full, budget)
	case displayWidth(first) <= room:
		return first
	}
	return truncate(count, room)
}

// ellipsize cuts s to w cells, ending a cut string with an ellipsis.
func ellipsize(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if displayWidth(s) <= w {
		return s
	}
	return truncate(s, w-1) + "…"
}

// ellipsizeLeft cuts s to w cells from its left, with an ellipsis at the cut, so a
// path keeps its end.
func ellipsizeLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if displayWidth(s) <= w {
		return s
	}
	return "…" + ansi.TruncateLeft(s, displayWidth(s)-(w-1), "")
}

// missingLine draws an absent optional import below the list; it is no list row, so the cursor skips it.
func (m *tuiModel) missingLine(mi missingImport, leftW int) string {
	return renderSegs([]seg{{text: truncate(mi.Group+" (not found)", leftW)}}, m.styles.muted, leftW)
}

func (m *tuiModel) targetLine(row int, r listRow, leftW int) string {
	t := m.targets[r.target]

	indent := 2 * r.depth
	var segs []seg
	if indent > 0 {
		segs = append(segs, seg{text: strings.Repeat(" ", indent)})
	}
	segs = append(segs,
		seg{text: t.glyph(), color: stateColor(t)},
		seg{text: " "},
		seg{text: padRight(t.spec.localName(), max(0, nameWidth(leftW)-indent)), link: t.spec.URL},
		seg{text: marker(m.targetDirty(t))},
		seg{text: padRight(truncate(m.status(t), statusW), statusW), color: statusColor(t)},
	)
	style := m.rowStyle(row, r)
	if t.flashing(m.now) {
		style = style.Reverse(true)
	}
	return renderSegs(segs, style, leftW)
}

// opColor ties a feed file glyph to the row palette: a created file is
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

func (m *tuiModel) rowStyle(row int, r listRow) lipgloss.Style {
	switch {
	case row == m.cursor && (m.drawerFocus || m.paneFocus || m.barFocus):
		return m.styles.muted // the cursor steps back while another region owns the keys
	case row == m.cursor:
		return m.styles.cursor
	case row == m.hover:
		return m.styles.hover
	case r.header && m.schemaSel == r.schema:
		return m.styles.solo
	case r.header:
		return m.styles.base
	case m.targets[r.target].muted:
		return m.styles.muted
	case m.solo == r.target:
		return m.styles.solo
	}
	return m.styles.base
}

func stateColor(t *tuiTarget) string {
	if _, c := t.healthMark(); c != "" {
		return c
	}
	switch t.state {
	case stateRunning:
		return runGreen
	case stateStarting:
		return colorGray
	case stateExited, stateBackoff:
		if t.exitCode == 0 {
			return runGreen
		}
		return runRed
	}
	return colorGray
}

// statusColor keeps the uptime quiet and lets a red or green exit speak.
func statusColor(t *tuiTarget) string {
	if t.building {
		return runYellow
	}
	if t.buildFailed {
		return runRed
	}
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
		if s.faint {
			style = style.Faint(true)
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

// rightLines is the stream pane, exactly m.height lines tall. The fixed header
// rows sit on top: the title row (cursor row's state, or the chips) and the
// table's column names. The bottom line carries the new-lines marker, else a hint.
func (m *tuiModel) rightLines(w int) []string {
	headH := m.headerRows()
	m.vp.Width = w
	m.vp.Height = max(1, m.height-headH)
	m.syncContent()

	var header []string
	if m.barRows() == 1 {
		header = append(header, m.barLine(w))
	}
	if m.titleRows() == 1 {
		header = append(header, m.titleLine(w))
	}
	if m.tableHeaderOn() {
		header = append(header, m.columnHeader(w))
	}

	out := strings.Split(m.vp.View(), "\n")

	if idx := m.paneCursorRow() - m.vp.YOffset; m.paneFocus && m.paneLen() > 0 && idx >= 0 && idx < len(out) {
		out[idx] = m.styles.paneCursor.Render(padRight(out[idx], w))
	}

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
			// Keep the click target live even when an overlaid line hides the marker.
			m.layout.marker = rect{x: m.layout.leftW + 1, y: headH + m.vp.Height - 1, w: w, h: 1}
		}
		// The bottom row is one line, so this switch is its precedence order.
		switch {
		case m.copyHintActive():
			// The OSC 52 sequence rides the hint line, so the renderer writes it
			// once and the terminal executes the copy.
			out[last] = lipgloss.NewStyle().Width(w).Render(m.styles.hint.Render(m.bottomText(w, m.copyHint, chip))) + m.copySeq
		case m.noticeLive():
			// A notice outranks the marker: a hidden error reads as no error.
			// The notice names the mute itself, so the state chip waits.
			out[last] = m.bottomLine(w, m.notice, "")
		case m.dirty():
			out[last] = m.bottomLine(w, unsavedNotice, chip)
		case markerShown:
			marker := fmt.Sprintf("↓ %d new", m.pending)
			out[last] = lipgloss.NewStyle().Width(w).Render(m.markerLine(w, marker, chip))
		case m.search != "":
			out[last] = m.bottomLine(w, m.searchLine(), chip)
		case m.filterOn():
			out[last] = m.bottomLine(w, m.filterLine(), chip)
		case m.now.Before(m.hintUntil):
			out[last] = m.bottomLine(w, m.hint, chip)
		case !m.follow:
			// A frozen pane with nothing else to say still names the state.
			out[last] = m.bottomLine(w, "", chip)
		}
	}

	lines := make([]string, 0, m.height)
	lines = append(lines, header...)
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

// stateChip is the bottom line's frozen suffix. Scope needs no chip: the list
// selection already shows it.
func (m *tuiModel) stateChip() string {
	if m.follow {
		return ""
	}
	return "scrollback · esc live"
}

// searchLine is the find counter.
func (m *tuiModel) searchLine() string {
	return "find " + m.search + " · " + m.searchStatus()
}

// filterLine is the filtered line count.
func (m *tuiModel) filterLine() string {
	return m.filterStatus()
}

// copyHintActive reports whether the copy feedback still owns the bottom line.
func (m *tuiModel) copyHintActive() bool {
	return m.copyHint != "" && m.now.Before(m.copyUntil)
}

// titleLine is the right pane's title row for a target row: its state, how many
// files its last change batch held, and its command. The state replaces the
// uptime whenever the row is not running; an exited row leads with its exit
// glyph. The url follows, linked, when the target has one.
func (m *tuiModel) titleLine(w int) string {
	var segs []seg
	if i := m.cursorTarget(); i >= 0 {
		t := m.targets[i]
		if t.state == stateExited {
			segs = append(segs, seg{text: t.glyph() + " ", color: stateColor(t)})
		}
		if word := m.streamWord(i); word != "" {
			segs = append(segs, seg{text: word + " · "})
		}
		if _, err := t.columns(); err != nil {
			segs = append(segs, seg{text: ellipsize(oneLine(err.Error()), 40) + " · ", color: colorRed})
		}
		parts := []string{m.status(t)}
		if t.state == stateRunning && !t.lastStart.IsZero() {
			parts = []string{"running", "since " + t.lastStart.Format("15:04:05")}
			if t.lastBuild > 0 {
				parts = append(parts, "built "+buildDur(t.lastBuild))
			}
		}
		parts = append(parts, fileCount(len(t.lastChange.Files)), oneLine(t.spec.Cmd))
		segs = append(segs, seg{text: strings.Join(parts, " · ")})
		if t.spec.URL != "" {
			segs = append(segs, seg{text: "  "}, seg{text: t.spec.URL, link: t.spec.URL})
		}
	}
	return renderSegs(segs, m.titleStyle(), w)
}

// titleStyle is bold only while the pane owns the keys.
func (m *tuiModel) titleStyle() lipgloss.Style {
	if m.paneFocus {
		return m.styles.title
	}
	return m.styles.base
}

// syncContent rebuilds the viewport only when its content changed, and keeps
// it pinned to the tail while following.
func (m *tuiModel) syncContent() {
	// A scroll-up drops the pin, and the padding it needed must go with it.
	if !m.follow && m.pinSeq != 0 {
		m.pinSeq = 0
		m.vpDirty = true
	}
	if m.vpDirty {
		lines := m.paintLevels(m.sepLines(m.tableLines(m.paneLines())))
		m.paneCol = m.clampPaneCol(lines)
		content := strings.Join(m.padForPin(m.markLines(m.panLines(lines))), "\n")
		if len(lines) == 0 {
			content = m.styles.hint.Render(m.emptyHint())
		}
		m.vp.SetContent(content)
		// SetContent only pulls YOffset back from past-the-end; a shrink that still
		// leaves it below the last row needs an explicit clamp, or the frozen pane
		// renders blank rows under the content.
		m.vp.SetYOffset(m.vp.YOffset)
		m.vpDirty = false
	}
	if m.follow {
		m.followTail()
	}
	m.placeCursor()
}

// clampPaneCol keeps the window where each line still ends inside it, a (+) cell taking markW cells.
func (m *tuiModel) clampPaneCol(lines []string) int {
	if m.paneCol <= 0 {
		return 0
	}
	reach := 0
	for row, line := range lines {
		avail := m.vp.Width
		if r := m.rowInfo(row); r.sub < 0 && r.obj && avail >= markW {
			avail -= markW
		}
		reach = max(reach, displayWidth(line)-avail)
	}
	return min(m.paneCol, max(0, reach))
}

// panLines slices each pane line to the horizontal window paneCol opens.
func (m *tuiModel) panLines(lines []string) []string {
	if m.paneCol <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = sliceCells(line, m.paneCol, m.paneCol+m.vp.Width)
	}
	return out
}

func (m *tuiModel) emptyHint() string {
	if m.filterActive() {
		if m.solo >= 0 {
			return fmt.Sprintf("no lines in %s match the filters · backspace removes the last", m.targets[m.solo].spec.Name)
		}
		return "no lines match the filters · backspace removes the last"
	}
	if m.solo >= 0 {
		t := m.targets[m.solo]
		if t.building {
			return t.spec.Name + ": building… output appears when the run starts"
		}
		return t.spec.Name + ": no output yet"
	}
	if m.schemaSel != "" {
		return "group: " + groupLabel(m.schemaSel)
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

// helpShortKeys is the help overlay's first view: the keys used most.
var helpShortKeys = [][2]string{
	{"j/k ↑/↓", "move between rows; the row under the cursor is selected"},
	{"space", "start or stop the row"},
	{"r", "reboot the row"},
	{"← →", "fold or unfold a group; → on a row focuses the log pane"},
	{"esc", "scrollback: back to live · else back out: row to its group, group to all"},
	{"tab", "focus the fs drawer"},
	{"enter a f", "pane: expand the line; a adds its key, f filters its value"},
	{"c", "toggle the columns table"},
	{"/", "filter the pane; each filter is a chip (leading . is yq)"},
	{"ctrl+f", "find in the pane (n/N jump)"},
	{"ctrl+s", "save unsaved columns to rerun.yml"},
	{"q", "quit (asks first)"},
}

// helpKeys is the help overlay's full view, in display order. It fits 30 rows.
var helpKeys = [][2]string{
	{"↑/↓ j/k", "move between rows, selecting each; enter focuses the log pane"},
	{"←/→", "on a group: ← folds, → unfolds; on a row: ← goes to its group, → focuses the log pane"},
	{"home/end", "first/last row; pane top-left / live tail when focused"},
	{"space", "start / stop (group row: its targets)"},
	{"r", "reboot (grace applies; group row: its selected targets)"},
	{"t", "toggle the fs drawer (the change feed, seven lines at most)"},
	{"tab", "focus the fs drawer (j/k move, enter lists a batch's files, esc closes)"},
	{"m", "mute the target's stream (group row: its targets)"},
	{"esc [ ]", "list: esc backs out · [ ] ⇧↑⇧↓ jump groups"},
	{"o", "open the target's url"},
	{"ctrl+f", "find in the pane (n/N next/prev; chip ×, esc keeps it)"},
	{"/ v", "filter the lines (enter adds a chip; leading . is yq)"},
	{"c", "toggle the columns table"},
	{"pgup/pgdn", "scroll the pane (ctrl+u / ctrl+d)"},
	{"enter a f", "pane: expand the cursor line; a adds its key, f filters its value"},
	{"backspace ×", "pane: last chip · bar: ←/→ picks, backspace drops · click ×"},
	{"ctrl+s", "save unsaved columns to rerun.yml"},
	{"q ^C", "q asks to quit · enter or ^C again quits · esc cancels"},
}

// helpMouse is the help overlay's mouse notes and legend, shown under the full view.
var helpMouse = []string{
	"click a row scopes to it; double-click starts or stops it",
	"click a group scopes to its targets; click a batch lists its files",
	"drag copies a range; shift-drag uses the terminal's selection",
	"⚙ building  ● starting (gray)  ● run  ▲ unhealthy  ■ failed",
	"✓ ok  ✗ exit  ○ stopped  · off",
}

// helpView draws the help overlay: the short key list, or the full one when helpAll is set.
func (m *tuiModel) helpView() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("rerun keys") + "\n")
	keys, footer := helpShortKeys, "? or m: all keys · esc closes"
	if m.helpAll {
		keys, footer = helpKeys, "esc closes"
	}
	for _, kv := range keys {
		b.WriteString(m.styles.key.Render(padRight(kv[0], 12)) + " " + kv[1] + "\n")
	}
	if m.helpAll {
		b.WriteString("\n")
		for _, line := range helpMouse {
			b.WriteString(m.styles.hint.Render(line) + "\n")
		}
	}
	b.WriteString("\n" + m.styles.hint.Render(footer))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		m.styles.box.Render(b.String()))
}
