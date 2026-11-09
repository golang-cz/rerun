package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// maxNameW caps the name column; longer names get a middle ellipsis.
const maxNameW = 28

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
	chipHi       lipgloss.Style
	hit          lipgloss.Style
	errText      lipgloss.Style
	sel          lipgloss.Style
}

func newTUIStyles(isDark bool) tuiStyles {
	lightDark := lipgloss.LightDark(isDark)
	return tuiStyles{
		base:         lipgloss.NewStyle(),
		divider:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		dividerFocus: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		cursor:       lipgloss.NewStyle().Background(lightDark(lipgloss.Color("252"), lipgloss.Color("237"))),
		sel:          lipgloss.NewStyle().Reverse(true),
		hover:        lipgloss.NewStyle().Background(lightDark(lipgloss.Color("254"), lipgloss.Color("235"))),
		muted:        lipgloss.NewStyle().Faint(true),
		solo:         lipgloss.NewStyle().Background(lightDark(lipgloss.Color("253"), lipgloss.Color("234"))),
		all:          lipgloss.NewStyle().Bold(true),
		title:        lipgloss.NewStyle().Bold(true),
		marker:       lipgloss.NewStyle().Reverse(true),
		hint:         lipgloss.NewStyle().Faint(true),
		box:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		key:          lipgloss.NewStyle().Bold(true),
		paneCursor:   lipgloss.NewStyle().Reverse(true),
		chip:         lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		chipFind:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Underline(true),
		chipHi:       lipgloss.NewStyle().Faint(true),
		// Reverse, not underline: v2 wraps an underlined string per rune, which ansi.Cut miscuts.
		hit:     lipgloss.NewStyle().Reverse(true),
		errText: lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed)),
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
	marker rect
	// chips and heads are the header's clickable cells: chip × cells on the
	// filter bar, and table column names on the column header row.
	chips []chipHit
	heads []cellSpan
	// caretW is how many cells of the open input's visible text run before its
	// cursor; cursorX and cursorY are the frame cell that cursor sits on.
	caretW  int
	cursorX int
	cursorY int
}

func displayWidth(s string) int { return lipgloss.Width(s) }

// View draws the frame; alt screen, mouse reporting and the real terminal
// cursor for an open input are declared here.
func (m *tuiModel) View() tea.View {
	v := tea.NewView(m.frame())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// Blur clears an armed press whose mouse-up the focus loss can swallow.
	v.ReportFocus = true
	// v2 always asks for basic key disambiguation, which is all we need: the
	// decoder's own spellings (ctrl+shift+g, shift+n, shift+/) arrive whole.
	// Every extra flag stays off, so no release or repeat events we never read.
	v.KeyboardEnhancements = tea.KeyboardEnhancements{}
	// The config-error modal owns the keys over an open input, so it takes the caret too.
	if (m.finder.open || m.inputMode != inputNone) && m.cfgErr == "" {
		v.Cursor = tea.NewCursor(m.layout.cursorX, m.layout.cursorY)
	}
	return v
}

// frame is the drawn screen as one styled string.
func (m *tuiModel) frame() string {
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}
	m.layout = tuiLayout{marker: rect{x: -1, y: -1}}

	leftW, rightW := m.layoutWidths()
	m.layout.leftW = leftW
	m.layout.offset = m.listOffset()
	right := m.rightLines(rightW)

	divider, glyph := m.styles.divider, "│"
	if m.paneFocus {
		divider, glyph = m.styles.dividerFocus, "┃"
	}

	var b strings.Builder
	for y := 0; y < m.height; y++ {
		if m.listShown() {
			b.WriteString(m.leftLine(y, leftW))
			b.WriteString(divider.Render(glyph))
		}
		b.WriteString(right[y])
		if y < m.height-1 {
			b.WriteByte('\n')
		}
	}
	frame := b.String()
	if m.inputMode != inputNone {
		box := m.inputBoxView()
		frame = overlayAt(frame, box, m.layout.leftW+1, 0, m.width, m.height)
		m.layout.cursorX, m.layout.cursorY = m.inputCaret(m.layout.leftW+1, 0)
	}
	if m.finder.open {
		box := m.finderView()
		x, y := overlayOrigin(box, m.width, m.height)
		frame = overlayAt(frame, box, x, y, m.width, m.height)
		m.layout.cursorX, m.layout.cursorY = m.inputCaret(x, y)
	}
	if m.eventsModal.open {
		frame = overlayCenter(frame, m.eventsView(), m.width, m.height)
	}
	if m.original.open {
		frame = overlayCenter(frame, m.originalView(), m.width, m.height)
	}
	if m.help {
		frame = overlayCenter(frame, m.helpView(), m.width, m.height)
	}
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
	// v2 counts the border in Width, so the content gets the same cells as v1.
	return m.styles.box.Padding(1, 2).Width(56 + m.styles.box.GetHorizontalBorderSize()).Render(body)
}

// Only the body is cut, so the border and the close hint survive a small screen.
func (m *tuiModel) configErrView() string {
	w := min(80, max(24, m.width-4))
	body := strings.Split(lipgloss.NewStyle().Width(w-6).Render(m.cfgErr), "\n")
	maxH := max(8, m.height-2)
	box := func(lines []string) string {
		return m.styles.box.Padding(1, 2).Width(w + m.styles.box.GetHorizontalBorderSize()).Render(
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
// columns underneath keep updating while the modal is up.
func overlayCenter(frame, box string, width, height int) string {
	x, y := overlayOrigin(box, width, height)
	return overlayAt(frame, box, x, y, width, height)
}

// overlayOrigin is where overlayCenter drops a box of this rendered size: centered, never off screen.
func overlayOrigin(box string, width, height int) (int, int) {
	bw := lipgloss.Width(box)
	return max(0, (width-bw)/2), max(0, (height-len(strings.Split(box, "\n")))/2)
}

// overlayAt splices box into frame at (x, y), so an anchored box cannot drift with the content.
func overlayAt(frame, box string, x, y, width, height int) string {
	lines := strings.Split(frame, "\n")
	bw := lipgloss.Width(box)
	x = max(0, x)
	for i, bl := range strings.Split(box, "\n") {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		line := lines[row]
		lines[row] = ansi.Cut(line, 0, x) + bl + ansi.Cut(line, x+bw, width)
	}
	return strings.Join(lines, "\n")
}

// modalMinWidth is the narrowest a modal box shrinks to, terminal permitting.
const modalMinWidth = 40

// modalWidth is a modal box's outer width: 80% of the terminal, floored, never past it.
func modalWidth(termW int) int {
	return max(1, min(max(termW*4/5, min(modalMinWidth, termW)), termW))
}

// helpMaxW is a floor, not a cap: the help box is at least this wide when the terminal allows.
const helpMaxW = 80

// helpWidth is the help box's outer width: at least 80 columns when the terminal
// allows it, so no description is cut on an 80-column screen.
func helpWidth(termW int) int {
	return max(modalWidth(termW), min(termW, helpMaxW))
}

// modalInnerW is the box's content width, border and padding excluded.
func (m *tuiModel) modalInnerW(outerW int) int {
	return max(1, outerW-m.styles.box.GetHorizontalFrameSize())
}

// modalFit pads or truncates s to exactly width cells, so the box's rows stay aligned.
func modalFit(s string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(ansi.Truncate(s, width, "…"))
}

// modalBox draws a popup: a header row over a rule of the inner width, the body rows, and a
// faint footer dropped when the box is too short. outerH 0 sizes the box to its content,
// capped at two rows less than the terminal.
func (m *tuiModel) modalBox(header, footer string, body []string, outerW, outerH int) string {
	box := m.styles.box
	innerW := m.modalInnerW(outerW)
	if outerH <= 0 {
		outerH = box.GetVerticalFrameSize() + 2 + len(body)
		if footer != "" {
			outerH++
		}
		outerH = min(outerH, max(1, m.height-2))
	}
	innerH := max(1, outerH-box.GetVerticalFrameSize())
	lines := []string{
		modalFit(header, innerW),
		modalFit(m.styles.divider.Render(strings.Repeat("─", innerW)), innerW),
	}
	for _, l := range body {
		lines = append(lines, modalFit(l, innerW))
	}
	if footer != "" && len(lines) < innerH {
		lines = append(lines, modalFit(m.styles.hint.Render(footer), innerW))
	}
	lines = lines[:min(len(lines), innerH)]

	// v2's Width and Height are the whole block, border included.
	return box.Width(outerW).Height(outerH).Render(strings.Join(lines, "\n"))
}

// inputBoxW is the input box's outer width: the pane's width less two cells, floored at modalMinWidth.
func (m *tuiModel) inputBoxW() int {
	_, rightW := m.layoutWidths()
	return max(1, min(rightW, max(modalMinWidth, rightW-2)))
}

// inputPlaceholder is the faint hint an empty query box shows.
func (m *tuiModel) inputPlaceholder() string {
	return `level=error  "two words"  @saved  .yq`
}

// inputCount is the box header's right-aligned count: the picker size while
// the @ picker is open, else the kept/total line counts.
func (m *tuiModel) inputCount() string {
	if m.pickActive() {
		if n := len(m.pickMatches()); n == 1 {
			return "1 query"
		} else {
			return fmt.Sprintf("%d queries", n)
		}
	}
	kept, total := m.matchCounts()
	return fmt.Sprintf("%d/%d lines", kept, total)
}

// inputPrompt is the glyph and gap an input box draws before its text.
const inputPrompt = "❯ "

// inputCaret is the frame cell of an open input's caret: past the box's border,
// padding and prompt, right after the visible text on its first content row.
func (m *tuiModel) inputCaret(boxX, boxY int) (int, int) {
	box := m.styles.box
	innerX := boxX + box.GetBorderLeftSize() + box.GetPaddingLeft() + displayWidth(inputPrompt)
	innerY := boxY + box.GetBorderTopSize() + box.GetPaddingTop()
	return innerX + m.layout.caretW, innerY
}

// inputBoxView draws the query box: one header row with the input and the
// count, the @ picker's rows when it is open, then the keys footer. A parse
// error turns the input red and owns the footer.
func (m *tuiModel) inputBoxView() string {
	outerW := m.inputBoxW()
	innerW := m.modalInnerW(outerW)
	count := m.inputCount()
	head := m.styles.chip.Render(inputPrompt)
	m.layout.caretW = 0
	if m.input == "" {
		head += " " + m.styles.hint.Render(m.inputPlaceholder())
	} else {
		text, caret := m.inputWindow(max(0, innerW-2-displayWidth(count)-2))
		switch {
		case m.inputSel:
			head += m.styles.sel.Render(text)
		case m.queryErr != "":
			head += m.styles.errText.Render(text)
		default:
			head += text
		}
		m.layout.caretW = caret
	}
	// Reserve the count's cells on a narrow pane: the query trims first, the count stays whole.
	countW := displayWidth(count)
	head = truncate(head, max(0, innerW-countW-1))
	head += strings.Repeat(" ", max(1, innerW-displayWidth(head)-countW)) + m.styles.hint.Render(count)
	footer := "enter add pill · esc close"
	var body []string
	if m.barSel >= 0 && m.barSel < len(m.pills) {
		footer = m.pillContextFooter()
	}
	if m.pickActive() {
		body = m.pickLines(innerW)
		footer = "↑↓ move · enter add · esc close"
	}
	if m.queryErr != "" {
		footer = m.styles.errText.Render(m.queryErr + " · esc clears")
	}
	return m.modalBox(head, footer, body, outerW, 0)
}

// pickMaxRows caps the @ picker's list, so the box stays small.
const pickMaxRows = 8

// pickLines is the @ picker's body: one row per candidate in a window around
// the selection, the selected one reversed.
func (m *tuiModel) pickLines(innerW int) []string {
	ms := m.pickMatches()
	if len(ms) == 0 {
		return []string{"  " + m.styles.hint.Render("no saved queries in scope")}
	}
	// A reload can shrink the list under the selection; clamp before windowing.
	m.pickSel = min(m.pickSel, len(ms)-1)
	start := max(0, min(m.pickSel-pickMaxRows+1, len(ms)-pickMaxRows))
	shown := ms[start:min(len(ms), start+pickMaxRows)]
	nameW := 0
	for _, q := range shown {
		nameW = max(nameW, displayWidth(q.name))
	}
	rows := make([]string, 0, len(shown))
	for i, q := range shown {
		plain := "  @" + padRight(q.name, nameW) + " " + q.query
		if start+i == m.pickSel {
			rows = append(rows, m.styles.sel.Render(padRight(plain, innerW)))
			continue
		}
		rows = append(rows, "  "+m.styles.chip.Render("@"+padRight(q.name, nameW))+" "+m.styles.hint.Render(q.query))
	}
	return rows
}

// layoutWidths splits the screen between the two columns.
func (m *tuiModel) layoutWidths() (leftW, rightW int) {
	if m.leftMode == leftHidden {
		// -1 puts the pane's first cell in column 0, with no divider in front of it.
		return -1, max(1, m.width)
	}
	nameW := 1
	for _, r := range m.listRows() {
		w := r.indent()
		if r.header || r.more {
			w += displayWidth(r.label)
		} else {
			w += displayWidth(m.targets[r.target].spec.localName())
		}
		nameW = max(nameW, w)
	}
	if m.hasGroups() {
		nameW = max(nameW, displayWidth(groupLabel(ungrouped)))
	}
	nameW = min(nameW, maxNameW)
	leftW = nameW + m.nameMargin()
	// A wide screen keeps 24 cells, which fits the footer's `· ? help` cue beside
	// the version; a narrow one shrinks to the names, down to 16. Names-only keeps 16 at any width.
	if m.leftMode == leftNames || m.width < narrowW {
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

// leftMode is the left column's width: full, names only, or hidden for a full-width log.
type leftMode uint8

const (
	leftFull leftMode = iota
	leftNames
	leftHidden
)

// cycleLeft is `b`: full, then names, then hidden, then full again.
func (m *tuiModel) cycleLeft() {
	m.leftMode = (m.leftMode + 1) % 3
	// Hidden, the list cannot show its cursor, so the keys go to the pane.
	if !m.listShown() {
		m.paneFocus = true
	}
}

func (m *tuiModel) listShown() bool { return m.leftMode != leftHidden }

// nameMargin is the cells a row keeps beside its name: glyph, space, marker, and the status cell when shown.
func (m *tuiModel) nameMargin() int {
	if m.leftMode == leftFull {
		return statusW + 4
	}
	return 4
}

// nameWidth is the name cell's width for a given left column width.
func (m *tuiModel) nameWidth(leftW int) int { return max(0, leftW-m.nameMargin()) }

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
// fs events line and the version row at the bottom.
func (m *tuiModel) listHeight() int {
	return max(1, m.height-2)
}

// listOffset keeps the list's stored top row, and moves it only when the cursor
// leaves the window: just enough to put the cursor on the top or bottom row.
// A wheel parks the window until the cursor moves. The bottom lines never scroll with it.
func (m *tuiModel) listOffset() int {
	visible := m.listHeight()
	top := m.listTop
	if !m.listScrolled || m.cursor != m.listScrollAt {
		m.listScrolled = false
		switch {
		case m.cursor < top:
			top = m.cursor
		case m.cursor >= top+visible:
			top = m.cursor - visible + 1
		}
	}
	// Rows that shrank (a fold, a reload, a taller screen) never leave blank rows below the last one.
	top = min(max(top, 0), max(0, m.listLen()+len(m.missing)-visible))
	m.listTop = top
	return top
}

// scrollList moves the list window one row per wheel notch and leaves the cursor alone.
func (m *tuiModel) scrollList(delta int) {
	m.layout.offset = m.listOffset()
	m.listTop = m.layout.offset + delta
	m.listScrolled = true
	m.listScrollAt = m.cursor
	m.layout.offset = m.listOffset()
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
// last row, the fs events line the row above it, and the target list the rest.
func (m *tuiModel) leftLine(y, leftW int) string {
	// The list reserves the screen's very last row; the version line owns it.
	if y == m.height-1 {
		return m.versionCell(leftW)
	}
	if y == m.height-2 {
		return m.feedHintCell(leftW)
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
	if r.more {
		return m.moreLine(m.layout.offset+y, r, leftW)
	}
	return m.targetLine(m.layout.offset+y, r, leftW)
}

// moreLine draws a block's `(+) N hidden` row, which the cursor and the mouse can act on.
func (m *tuiModel) moreLine(row int, r listRow, leftW int) string {
	style := m.styles.muted
	if row == m.cursor {
		style = m.styles.cursor
	}
	return renderSegs([]seg{{text: strings.Repeat(" ", r.indent()) + truncate(r.label, max(0, leftW-r.indent()))}}, style, leftW)
}

// feedHintCell is the left column's fs events line: the newest visible
// batch's age, a red watch failure while its batch sits in the modal's window,
// and a 1s highlight when a batch lands. A click on it opens the fs events modal.
func (m *tuiModel) feedHintCell(leftW int) string {
	style := m.styles.hint
	text := "fs events · idle"
	if batches, dim := m.feedVisible(); !dim && len(batches) > 0 {
		b := batches[0]
		switch {
		case b.err != nil:
			text = fitWatchFailed(leftW)
			style = style.Foreground(lipgloss.Color(colorRed))
		default:
			text = "fs events · " + fmtDur(m.now.Sub(b.at)) + " ago"
		}
	}
	if m.now.Before(m.feedFlashUntil) {
		style = style.Reverse(true)
	}
	text = truncate(text, leftW)
	return style.Render(text) + strings.Repeat(" ", max(0, leftW-displayWidth(text)))
}

// fitWatchFailed is the hint's failure text, shortened until it fits leftW.
func fitWatchFailed(leftW int) string {
	for _, s := range []string{"fs events · ⚠ watch failed", "⚠ fs failed", "⚠ fs"} {
		if displayWidth(s) <= leftW {
			return s
		}
	}
	return truncate("⚠ fs", leftW)
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

// ellipsizeMiddle cuts s to w cells from its middle, keeping both ends with an
// ellipsis between. The head gets the extra cell, so `test-integration-postgres`
// at 20 keeps `test-integ` and `-postgres`.
func ellipsizeMiddle(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if displayWidth(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	headW := w / 2
	tailW := w - 1 - headW
	return padRight(s, headW) + "…" + tailCells(s, tailW)
}

// tailCells returns the last n cells of s, padded on the left when a wide rune
// would not fit whole.
func tailCells(s string, n int) string {
	rs := []rune(s)
	cells, start := 0, len(rs)
	for start > 0 {
		rw := displayWidth(string(rs[start-1]))
		if cells+rw > n {
			break
		}
		cells += rw
		start--
	}
	return strings.Repeat(" ", n-cells) + string(rs[start:])
}

// missingLine draws an absent optional import below the list; it is no list row, so the cursor skips it.
func (m *tuiModel) missingLine(mi missingImport, leftW int) string {
	return renderSegs([]seg{{text: truncate(mi.Group+" (not found)", leftW)}}, m.styles.muted, leftW)
}

func (m *tuiModel) targetLine(row int, r listRow, leftW int) string {
	t := m.targets[r.target]

	indent := r.indent()
	nameW := max(0, m.nameWidth(leftW)-indent)
	var segs []seg
	if indent > 0 {
		segs = append(segs, seg{text: strings.Repeat(" ", indent)})
	}
	segs = append(segs,
		seg{text: t.glyph(), color: stateColor(t)},
		seg{text: " "},
		seg{text: padRight(ellipsizeMiddle(t.spec.localName(), nameW), nameW), link: t.spec.URL},
		seg{text: marker(m.targetDirty(t))},
	)
	if m.leftMode == leftFull {
		segs = append(segs, seg{text: padRight(truncate(m.status(t), statusW), statusW), color: statusColor(t)})
	}
	style := m.rowStyle(row, r)
	if r.dim && row != m.cursor {
		style = m.styles.muted
	}
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
	if r.more {
		return m.styles.muted
	}
	// A focused pane dims every row it does not draw, so its own rows lead the eye.
	if m.paneFocus && !m.rowInPaneScope(r) {
		return m.styles.muted
	}
	switch {
	case row == m.cursor && (m.paneFocus || m.barFocus):
		if m.paneFocus {
			return m.styles.cursor // the in-scope cursor band keeps its brightness
		}
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

// rowInPaneScope reports whether the pane's focused scope draws this row's output.
// A focused pane with no scope draws every target, so nothing dims.
func (m *tuiModel) rowInPaneScope(r listRow) bool {
	members, ok := m.paneScope()
	if !ok {
		return true
	}
	if r.header {
		return m.schemaSel == r.schema
	}
	return slices.Contains(members, r.target)
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
// rows sit on top: the filter bar's chips and the table's column names. The
// bottom line carries the new-lines marker, else a hint.
func (m *tuiModel) rightLines(w int) []string {
	headH := m.headerRows()
	m.vp.SetWidth(w)
	m.vp.SetHeight(max(1, m.height-headH))
	m.syncContent()

	var header []string
	if m.barRows() == 1 {
		header = append(header, m.barLine(w))
	}
	if m.tableHeaderOn() {
		header = append(header, m.columnHeader(w))
	}

	out := strings.Split(m.vp.View(), "\n")

	if idx := m.paneCursorRow() - m.vp.YOffset(); m.paneFocus && m.paneLen() > 0 && idx >= 0 && idx < len(out) {
		out[idx] = m.styles.paneCursor.Render(padRight(out[idx], w))
	}

	// Selection is line-granular and styled before the bottom line's own
	// replacement, so the marker and hint never read as selected.
	if lo, hi, ok := m.selectionRange(); ok {
		for idx := lo; idx <= hi; idx++ {
			row := idx - m.vp.YOffset()
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
			m.layout.marker = rect{x: m.layout.leftW + 1, y: headH + m.vp.Height() - 1, w: w, h: 1}
		}
		again := m.runAgainHint()
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
		case m.now.Before(m.hintUntil):
			out[last] = m.bottomLine(w, m.hint, chip)
		case again != "":
			out[last] = m.bottomLine(w, again, chip)
		case !m.listShown() && m.watchFailed():
			// The hidden list has no left column to show the fs failure, so the bottom line does.
			out[last] = m.watchFailedLine(w, chip)
		case !m.follow:
			// A frozen pane with nothing else to say still names the state.
			out[last] = m.bottomLine(w, "", chip)
		case !m.listShown():
			// The hidden list has no left column to show its cue, so the bottom line does.
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
	var parts []string
	if !m.follow {
		parts = append(parts, "scrollback · esc live")
	}
	if !m.listShown() {
		// The hidden list has no left column to show its cue, so the chip carries it.
		parts = append(parts, "b list", "? help")
	}
	return strings.Join(parts, " · ")
}

// watchFailed reports whether the newest visible fs batch is a watch failure.
func (m *tuiModel) watchFailed() bool {
	batches, dim := m.feedVisible()
	return !dim && len(batches) > 0 && batches[0].err != nil
}

// watchFailedLine is the hidden list's bottom line: a red ⚠ fs, then the state chip.
func (m *tuiModel) watchFailedLine(w int, chip string) string {
	const warn = "⚠ fs"
	rest := ""
	if chip != "" {
		rest = truncate(" · "+chip, max(0, w-displayWidth(warn)))
	}
	red := m.styles.hint.Foreground(lipgloss.Color(colorRed))
	return lipgloss.NewStyle().Width(w).Render(red.Render(warn) + m.styles.hint.Render(rest))
}

// copyHintActive reports whether the copy feedback still owns the bottom line.
func (m *tuiModel) copyHintActive() bool {
	return m.copyHint != "" && m.now.Before(m.copyUntil)
}

// syncContent rebuilds the viewport only when its content changed, and keeps
// it pinned to the tail while following.
func (m *tuiModel) syncContent() {
	// A scroll-up drops the pin, and the padding it needed must go with it.
	if !m.follow && m.pinSeq != 0 {
		m.unpin()
		m.vpDirty = true
	}
	if m.vpDirty {
		lines := m.paintPills(m.paintLevels(m.sepLines(m.tableLines(m.paneLines()))))
		m.paneCol = m.clampPaneCol(lines)
		content := strings.Join(m.padForPin(m.markLines(m.panLines(lines))), "\n")
		if len(lines) == 0 {
			content = m.styles.hint.Render(m.emptyHint())
		}
		m.vp.SetContent(content)
		// SetContent only pulls YOffset back from past-the-end; a shrink that still
		// leaves it below the last row needs an explicit clamp, or the frozen pane
		// renders blank rows under the content.
		m.vp.SetYOffset(m.vp.YOffset())
		m.vpDirty = false
	}
	if m.follow {
		m.followTail()
	}
	m.placeCursor()
}

// paintPills marks every pill's spans in the pane's raw lines. Table rows
// reflow the text, so they keep no marks; the hit list still counts them.
func (m *tuiModel) paintPills(lines []string) []string {
	if len(m.pills) == 0 || m.tableOn() {
		return lines
	}
	var out []string
	for row, line := range lines {
		r := m.rowInfo(row)
		if r.sub >= 0 || m.visibleSep[r.raw].isSep() {
			continue
		}
		raw := m.visibleRaw[r.raw]
		spans := m.lineSpans(raw)
		if len(spans) == 0 {
			continue
		}
		if out == nil {
			out = append([]string(nil), lines...)
		}
		lead := displayWidth(line) - displayWidth(raw)
		for _, sp := range spans {
			line = paintCells(line, lead+displayWidth(raw[:sp[0]]), lead+displayWidth(raw[:sp[1]]), m.styles.hit)
		}
		out[row] = line
	}
	if out == nil {
		return lines
	}
	return out
}

// paintCells styles the cells [from, to) with st, keeping the rest as it was.
func paintCells(line string, from, to int, st lipgloss.Style) string {
	w := displayWidth(line)
	from, to = max(0, from), min(to, w)
	if to <= from {
		return line
	}
	return ansi.Cut(line, 0, from) + st.Render(ansi.Cut(line, from, to)) + ansi.Cut(line, to, w)
}

// clampPaneCol keeps the window where each line still ends inside it, a (+) cell taking markW cells.
func (m *tuiModel) clampPaneCol(lines []string) int {
	if m.paneCol <= 0 {
		return 0
	}
	reach := 0
	for row, line := range lines {
		avail := m.vp.Width()
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
		out[i] = sliceCells(line, m.paneCol, m.paneCol+m.vp.Width())
	}
	return out
}

// runAgainHint names r as the way to run a stopped or exited make row again.
func (m *tuiModel) runAgainHint() string {
	i := m.solo
	if i < 0 {
		i = m.cursorTarget()
	}
	if i < 0 || i >= len(m.targets) {
		return ""
	}
	t := m.targets[i]
	if t.spec.Kind != makeKind || (t.state != stateExited && t.state != stateStopped) {
		return ""
	}
	return "r run again · ctrl+l clear · ctrl+k go to"
}

// makeEmptyHint points at the finder before any target ran, since a Makefile's targets stay folded until found.
func (m *tuiModel) makeEmptyHint() string {
	for _, t := range m.targets {
		if t.spec.Kind == makeKind {
			if t.spec.ImportHide {
				return "ctrl+k go to a target · → unfold make/"
			}
			return "ctrl+k go to a target"
		}
	}
	return ""
}

func (m *tuiModel) emptyHint() string {
	if m.filterActive() {
		if m.solo >= 0 {
			return fmt.Sprintf("no lines in %s match the filters · backspace removes the last", m.targets[m.solo].spec.Name)
		}
		return "no lines match the filters · backspace removes the last"
	}
	// A solo row names its own state below; the make hint would hide it.
	if hint := m.makeEmptyHint(); hint != "" && !m.anyTargetRan() && m.solo < 0 {
		return hint
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

// anyTargetRan reports whether any target has started, or has exited or stopped after starting.
func (m *tuiModel) anyTargetRan() bool {
	for _, t := range m.targets {
		if t.runStarted || t.state == stateExited || t.state == stateStopped {
			return true
		}
	}
	return false
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
	{"space", "start or stop the row · pane: jump to live"},
	{"enter", "list: focus the pane, starting a stopped row or group"},
	{"s", "stop the row · pane: stop what the pane shows"},
	{"r", "run/reboot the row"},
	{"← →", "fold or unfold a group; → on a row focuses the log pane"},
	{"esc", "scrollback: back to live · else back out one level"},
	{"e", "fs events (last minute)"},
	{"enter → a f", "pane: → expand · ← collapse · a adds key, f filters value"},
	{"c t", "c copies the line or selection · t toggles the columns"},
	{"/ v ctrl+f", "query bar: enter adds a pill (level=error, -skip, .yq)"},
	{"n/N ctrl+g", "jump between the pills' hits"},
	{"pgup/pgdn", "scroll the pane; a full page while the pane is focused"},
	{"ctrl+l", "clear the target's log (old lines stay in scrollback)"},
	{"ctrl+s", "save unsaved columns to rerun.yml"},
	{"ctrl+k", "go to a target, or run a make target"},
	{"q", "quit (asks first)"},
}

// helpKeys is the help overlay's full view, in display order. It fits 30 rows.
var helpKeys = [][2]string{
	{"↑/↓ j/k", "move rows; ⇧↑⇧↓ select pane lines; home/end first/last"},
	{"←/→ enter", "group: fold or unfold · row: to its group · pane: focus, expand"},
	{"space", "start or stop · pane: jump to live · ▶ starts its auto targets"},
	{"s", "stop the row · pane: stop what the pane shows"},
	{"m r h", "mute (group: its targets) · r reboots · h shows idle targets"},
	{"e", "fs events (last minute)"},
	{"esc [ ] b", "list: esc backs out · [ ] ⇧↑⇧↓ groups · b list width"},
	{"ctrl+k", "go to a target, or run a make target"},
	{"o", "open the target's url · pane: original lines"},
	{"/ v ctrl+f", "query bar: pills AND; space toggles filter⇄mark · @ saved"},
	{"n/N ctrl+g", "jump pill hits · bar: ←/→ pick, x drop, enter edit, → expands @"},
	{"ctrl+l", "clear the log; old lines stay in scrollback"},
	{"c t", "c copies the line or selection · t toggles the columns table"},
	{"pgup/pgdn", "scroll the pane (ctrl+u/ctrl+d); a full page in focus"},
	{"enter → a f", "pane: expand/collapse the line · a add key, f filter value"},
	{"backspace ×", "pane: last pill · bar: ←/→ picks, backspace drops · click ×"},
	{"ctrl+s", "save unsaved columns to rerun.yml"},
	{"q ^C", "q asks to quit · enter or ^C again quits · esc cancels"},
}

// helpMouse is the help overlay's mouse notes and legend, shown under the full view.
var helpMouse = []string{
	"click a row scopes to it; double-click starts or stops it",
	"click a group scopes to its targets; the fs line opens the batches",
	"drag copies a range; shift-drag uses the terminal's selection",
	"⚙ building  ● starting  ● run  ▲ unhealthy  ■ failed",
	"✓ ok  ✗ exit  ○ stopped  · off",
}

// helpView draws the help overlay: the short key list, or the full one when helpAll is set.
// modalBox keeps it a popup, so the live frame stays visible around it.
func (m *tuiModel) helpView() string {
	keys, footer := helpShortKeys, "? m all keys  esc close"
	if m.helpAll {
		keys, footer = helpKeys, "esc close"
	}
	body := make([]string, 0, len(keys)+len(helpMouse)+1)
	for _, kv := range keys {
		body = append(body, m.styles.key.Render(padRight(kv[0], 12))+" "+kv[1])
	}
	if m.helpAll {
		for _, line := range helpMouse {
			body = append(body, m.styles.hint.Render(line))
		}
	}
	// Content-sized, capped at two rows less than the terminal so the full list
	// fits a 30-row screen; the header and the footer keep their rows, so the
	// body gives up its last rows first.
	outerH := m.styles.box.GetVerticalFrameSize() + 3 + len(body)
	outerH = min(outerH, max(1, m.height-2))
	body = body[:min(len(body), max(0, outerH-m.styles.box.GetVerticalFrameSize()-3))]
	return m.modalBox(m.styles.title.Render("rerun keys"), footer, body, helpWidth(m.width), outerH)
}
