package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Herdr-style palette: the color leads, the glyph shape backs it up.
const (
	colorYellow = "3"
	colorGreen  = "2"
	colorRed    = "1"
	colorGray   = "8"
	colorSolo   = "6"
)

// Left column geometry: glyph, space, name, space, status, space, button.
const statusW = 12

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
	m.layout.offset = m.listOffset()
	right := m.rightLines(rightW)

	var b strings.Builder
	for y := 0; y < m.height; y++ {
		b.WriteString(m.leftLine(y, leftW))
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
	nameW := 8 // "ALL" plus the badge
	for _, t := range m.targets {
		nameW = max(nameW, displayWidth(t.spec.Name)+2)
	}
	leftW = nameW + statusW + 5
	if limit := max(8, m.width/2); leftW > limit {
		leftW = limit
	}
	if limit := m.width - 2; leftW > limit {
		leftW = max(4, limit)
	}
	return leftW, max(1, m.width-leftW-1)
}

// nameWidth is the name cell's width for a given left column width.
func nameWidth(leftW int) int { return max(0, leftW-statusW-5) }

// listOffset scrolls the list so the highlighted row stays visible.
func (m *tuiModel) listOffset() int {
	visible := max(1, m.height-1) // the footer is the command line, not a row
	rows := len(m.targets) + 1
	offset := 0
	if m.cursor >= visible {
		offset = m.cursor - visible + 1
	}
	return min(offset, max(0, rows-visible))
}

func (m *tuiModel) leftLine(y, leftW int) string {
	if y == m.height-1 {
		return m.cmdLine(leftW)
	}
	row := m.layout.offset + y
	switch {
	case row == 0:
		return m.allLine(leftW)
	case row > len(m.targets):
		return strings.Repeat(" ", leftW)
	}
	return m.targetLine(row, leftW)
}

// cmdLine is the left column's footer: the cursor row's command, dim, or blank
// on the ALL rollup.
func (m *tuiModel) cmdLine(leftW int) string {
	if m.cursor == 0 {
		return strings.Repeat(" ", leftW)
	}
	return m.styles.hint.Render(padRight("$ "+m.targets[m.cursor-1].spec.Cmd, leftW))
}

// allLine is the rollup row: `3● 1✓ 1✗`, with a badge for unseen output.
func (m *tuiModel) allLine(leftW int) string {
	segs := []seg{
		{text: " "},
		{text: " "},
		{text: nameCell("ALL", m.anyUnseen(), nameWidth(leftW))},
		{text: " "},
	}
	segs = append(segs, m.rollupSegs()...)
	return renderSegs(segs, m.rowStyle(0), leftW)
}

func (m *tuiModel) targetLine(row, leftW int) string {
	i := row - 1
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
		{text: nameCell(t.spec.Name, t.unseen, nameWidth(leftW)), link: t.spec.URL},
		{text: " "},
		{text: padRight(m.status(t), statusW), color: statusColor(t)},
		{text: " "},
		{text: btn, color: btnColor},
	}
	return renderSegs(segs, m.rowStyle(row), leftW)
}

// nameCell pads a target name to w cells, keeping two cells for the badge so
// rows do not shift when it appears.
func nameCell(name string, unseen bool, w int) string {
	if w <= 0 {
		return ""
	}
	if w <= 2 {
		return padRight(name, w)
	}
	badge := " "
	if unseen {
		badge = "✱"
	}
	return padRight(name, w-2) + " " + badge
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

// rightLines is the stream pane, exactly m.height lines tall. Its bottom line
// carries the new-lines marker, else the ALL-row hint.
func (m *tuiModel) rightLines(w int) []string {
	m.vp.Width = w
	m.vp.Height = max(1, m.height)
	m.syncContent()

	out := strings.Split(m.vp.View(), "\n")
	if len(out) > 0 {
		switch {
		case !m.follow && m.pending > 0:
			marker := fmt.Sprintf("↓ %d new", m.pending)
			out[len(out)-1] = lipgloss.NewStyle().Width(w).Render(m.styles.marker.Render(marker))
			m.layout.marker = rect{x: m.layout.leftW + 1, y: m.vp.Height - 1, w: w, h: 1}
		case m.now.Before(m.hintUntil):
			out[len(out)-1] = lipgloss.NewStyle().Width(w).Render(m.styles.hint.Render(truncate(allRowHint, w)))
		}
	}

	for len(out) < m.height {
		out = append(out, strings.Repeat(" ", w))
	}
	return out[:m.height]
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
	{"m", "mute the target's stream (M)"},
	{"s enter", "solo the target's stream (S; again or esc to clear)"},
	{"esc", "clear solo"},
	{"o", "open the target's url"},
	{"G", "jump to live output"},
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
		"its button cell to mute, shift-click to solo.",
		"shift-drag selects text with the terminal's own copy.",
	} {
		b.WriteString(m.styles.hint.Render(line) + "\n")
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		m.styles.box.Render(strings.TrimRight(b.String(), "\n")))
}
