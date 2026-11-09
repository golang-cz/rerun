package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

const (
	// finderChromeRows are the input, rule and footer lines around the match rows.
	finderChromeRows = 3
	// finderMinRows is the fewest match rows worth keeping when the footer is dropped to make room.
	finderMinRows = 3
)

// finderState is the Ctrl+K popup: a fuzzy query over every target and group.
type finderState struct {
	open  bool
	query string
	sel   int
}

// finderItem is one candidate: what it is, the text it matches on, and what Enter does.
type finderItem struct {
	kind    string // "target" or "group"
	name    string
	desc    string
	matched []int // rune indexes of name that the query matched
	// make marks a Makefile target, whose enter runs it instead of jumping.
	make   bool
	target int // the index into m.targets; -1 for a group
	enter  func(m *tuiModel)
}

// finderRuns is whether enter starts the row's command: a make target that is not up.
func (m *tuiModel) finderRuns(c finderItem) bool {
	return c.make && c.target >= 0 && !m.targets[c.target].state.up()
}

// finderDesc collapses a description onto one line, so a newline cannot break the popup.
func finderDesc(s string) string { return strings.Join(strings.Fields(s), " ") }

// finderCandidates lists every target and group in list order, including rows under collapsed groups.
func (m *tuiModel) finderCandidates() []finderItem {
	rows := m.allRows()
	out := make([]finderItem, 0, len(rows))
	for i, r := range rows {
		if r.header {
			out = append(out, finderItem{kind: "group", name: groupLabel(r.schema), desc: "", target: -1,
				enter: func(m *tuiModel) { m.finderJump(i) }})
			continue
		}
		t := m.targets[r.target].spec
		item := finderItem{kind: "target", name: t.Name, desc: finderDesc(t.Desc), make: t.Kind == makeKind,
			target: r.target, enter: func(m *tuiModel) { m.finderJump(i) }}
		if item.make {
			item.enter = func(m *tuiModel) { m.finderRun(i) }
		}
		out = append(out, item)
	}
	return out
}

// fuzzyHit is one entry's result over all query words: summed score, merged indexes, words matched.
type fuzzyHit struct {
	score, hits int
	matched     []int
}

// fuzzyAll runs one fuzzy pass per word; an entry counts only when every word hits it.
func fuzzyAll(words, data []string) []fuzzyHit {
	hits := make([]fuzzyHit, len(data))
	for n, w := range words {
		for _, f := range fuzzy.Find(w, data) {
			h := &hits[f.Index]
			if h.hits != n {
				continue
			}
			h.hits++
			h.score += f.Score
			h.matched = append(h.matched, f.MatchedIndexes...)
		}
		// Stop once no entry has matched every word so far; nothing can match later.
		if !slices.ContainsFunc(hits, func(h fuzzyHit) bool { return h.hits == n+1 }) {
			break
		}
	}
	for i := range hits {
		if hits[i].hits == len(words) {
			slices.Sort(hits[i].matched)
			hits[i].matched = slices.Compact(hits[i].matched)
		}
	}
	return hits
}

// finderResults ranks candidates: name matches first, then desc-only matches, each by score.
func (m *tuiModel) finderResults(query string) []finderItem {
	cands := m.finderCandidates()
	words := strings.Fields(query)
	if len(words) == 0 {
		return cands
	}
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = c.name
	}
	nameHits := fuzzyAll(words, names)

	// Desc is only searched for candidates the name pass missed, so a name match never repeats.
	var descIdx []int
	var descs []string
	for i, h := range nameHits {
		if h.hits != len(words) {
			descIdx = append(descIdx, i)
			descs = append(descs, cands[i].desc)
		}
	}
	descHits := fuzzyAll(words, descs)

	type scored struct {
		item  finderItem
		score int
	}
	var named, desc []scored
	for i, h := range nameHits {
		if h.hits == len(words) {
			c := cands[i]
			c.matched = h.matched
			named = append(named, scored{c, h.score})
		}
	}
	for j, h := range descHits {
		if h.hits == len(words) {
			desc = append(desc, scored{cands[descIdx[j]], h.score})
		}
	}
	byScore := func(a, b scored) int { return cmp.Compare(b.score, a.score) }
	slices.SortStableFunc(named, byScore)
	slices.SortStableFunc(desc, byScore)

	out := make([]finderItem, 0, len(named)+len(desc))
	for _, s := range append(named, desc...) {
		out = append(out, s.item)
	}
	return out
}

// finderInnerH is the box height inside its border; it depends only on the terminal.
func (m *tuiModel) finderInnerH() int {
	_, h := m.modalSize()
	return max(0, h-m.styles.box.GetVerticalFrameSize())
}

// finderHasFooter drops the footer on boxes too short to keep finderMinRows match rows under it.
func (m *tuiModel) finderHasFooter() bool {
	return m.finderInnerH()-finderChromeRows >= finderMinRows
}

// finderListRows is how many match rows fill the box; the size depends only on the terminal, never the matches.
func (m *tuiModel) finderListRows() int {
	chrome := finderChromeRows
	if !m.finderHasFooter() {
		chrome--
	}
	return max(0, m.finderInnerH()-chrome)
}

// handleFinderKey routes keys while the finder is open; enter jumps, esc and ctrl+k close.
func (m *tuiModel) handleFinderKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	res := m.finderResults(m.finder.query)
	n := min(len(res), m.finderListRows())
	m.finder.sel = min(m.finder.sel, max(0, n-1))
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+[", "ctrl+k":
		m.finder = finderState{}
	case "enter", "ctrl+m":
		// With no match, enter keeps the popup and its query open.
		if m.finder.sel >= n {
			return m, nil
		}
		res[m.finder.sel].enter(m)
		m.finder = finderState{}
	case "up", "ctrl+p":
		m.finder.sel = max(0, m.finder.sel-1)
	case "down", "ctrl+n":
		m.finder.sel = min(max(0, n-1), m.finder.sel+1)
	// ctrl+h is how the enhanced decoder spells backspace (kitty disambiguation).
	case "backspace", "ctrl+h":
		if r := []rune(m.finder.query); len(r) > 0 {
			m.finder.query = string(r[:len(r)-1])
			m.finder.sel = 0
		}
	default:
		m.finderPaste(msg.Text)
		// v1 handed an alt combo over as key runes, so a printable one typed its letter.
		if msg.Text == "" && msg.Mod.Contains(tea.ModAlt) && unicode.IsPrint(msg.Code) {
			m.finderPaste(string(msg.Code))
		}
	}
	return m, nil
}

// finderPaste drops control characters, so a pasted newline cannot split the query.
func (m *tuiModel) finderPaste(s string) {
	if q := strings.Map(dropControl, s); q != "" {
		m.finder.query += q
		m.finder.sel = 0
	}
}

// dropControl removes control characters, so a pasted newline cannot split the query.
func dropControl(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}

// finderJump unfolds the rows hiding allRows()[idx] and the group itself when it is one, then moves the cursor to it.
func (m *tuiModel) finderJump(idx int) {
	m.listScrolled = false
	rows := m.allRows()
	m.revealRow(rows, idx)
	// Solo is never hidden, so the target lists without revealing its group's hidden members.
	if r := rows[idx]; !r.header {
		m.applyScope(r.target, "")
	}
	m.cursor = m.indexOfKey(m.rowKey(rows[idx]))
	m.cursorSet = true
	if r, ok := m.cursorRow(); ok {
		m.applyRowScope(r)
	}
	// The jump lands in the pane, where the target's log is.
	m.focusPane()
}

// finderRun is enter on a make row: it jumps there, then starts it unless it is already up.
func (m *tuiModel) finderRun(idx int) {
	m.finderJump(idx)
	if r := m.allRows()[idx]; !m.targets[r.target].state.up() {
		m.startTarget(r.target)
	}
}

// revealRow unfolds the headers above rows[idx] that hide it, and the header itself when it is one.
func (m *tuiModel) revealRow(rows []listRow, idx int) {
	depth := rows[idx].depth
	if rows[idx].header {
		delete(m.collapsed, rows[idx].schema)
	}
	for i := idx - 1; i >= 0 && depth > 0; i-- {
		if r := rows[i]; r.header && r.depth < depth {
			depth = r.depth
			delete(m.collapsed, r.schema)
		}
	}
}

// finderTail is the longest suffix of s that fits width cells, so the typed end stays visible.
func finderTail(s string, width int) string {
	r := []rune(s)
	used := 0
	i := len(r)
	for ; i > 0; i-- {
		used += lipgloss.Width(string(r[i-1]))
		if used > width {
			break
		}
	}
	return string(r[i:])
}

// finderView draws the popup at a fixed size: input and count, a rule, the ranked matches, then the footer.
func (m *tuiModel) finderView() string {
	outerW, outerH := m.modalSize()
	innerW := m.modalInnerW(outerW)
	res := m.finderResults(m.finder.query)
	rows := m.finderListRows()
	nameW := finderNameWidth(res[:min(len(res), rows)], innerW)

	n := 0
	if len(res) > 0 {
		n = min(m.finder.sel, len(res)-1) + 1
	}
	count := fmt.Sprintf("%d/%d", n, len(res))

	input := m.styles.chip.Render(inputPrompt)
	m.layout.caretW = 0
	if m.finder.query == "" {
		input += " " + m.styles.hint.Render("go to target or run…")
	} else {
		tail := finderTail(m.finder.query, innerW-2-lipgloss.Width(count)-2)
		input += tail
		m.layout.caretW = lipgloss.Width(tail)
	}
	gap := max(1, innerW-lipgloss.Width(input)-lipgloss.Width(count))
	input += strings.Repeat(" ", gap) + m.styles.hint.Render(count)

	body := make([]string, 0, rows)
	for i := range rows {
		switch {
		case i == 0 && len(res) == 0:
			body = append(body, "  "+m.styles.hint.Render("no matches for \""+m.finder.query+"\""))
		case i < len(res):
			body = append(body, m.finderLine(res[i], i == m.finder.sel, nameW, innerW))
		default:
			body = append(body, "")
		}
	}
	footer := ""
	if m.finderHasFooter() {
		footer = "↑↓ move  enter go to  esc close"
		if m.finder.sel < len(res) && m.finderRuns(res[m.finder.sel]) {
			footer = "↑↓ move  enter run  esc close"
		}
		if len(res) == 0 {
			footer = "esc close"
		}
	}
	return m.modalBox(input, footer, body, outerW, outerH)
}

// finderNameWidth is the name column: the longest shown name, capped at 55% of the row and floored at 12.
func finderNameWidth(shown []finderItem, innerW int) int {
	longest := 0
	for _, c := range shown {
		n := lipgloss.Width(c.name)
		if c.kind == "group" {
			n++
		}
		longest = max(longest, n)
	}
	return min(max(12, min(longest, innerW*55/100)), max(1, innerW-2))
}

// finderLine is one match row: a marker, the name cell, then the desc column when it fits.
func (m *tuiModel) finderLine(c finderItem, selected bool, nameW, innerW int) string {
	paint := m.finderPaint(selected)
	marker := paint(lipgloss.NewStyle()).Render("  ")
	if selected {
		marker = paint(m.styles.chip).Render("▌") + paint(lipgloss.NewStyle()).Render(" ")
	}
	glyph := "  "
	if m.finderRuns(c) {
		glyph = "▶ "
	}
	row := marker + paint(lipgloss.NewStyle()).Render(glyph) + m.finderName(c, selected, nameW)
	if descW := innerW - 6 - nameW; descW >= 8 && c.desc != "" {
		row += paint(lipgloss.NewStyle()).Render("  ") + paint(m.styles.hint).Render(ansi.Truncate(c.desc, descW, "…"))
	}
	return row + paint(lipgloss.NewStyle()).Render(strings.Repeat(" ", max(0, innerW-lipgloss.Width(row))))
}

// finderName renders the name cell at exactly nameW cells: dim path prefix, own name, and matches underlined.
func (m *tuiModel) finderName(c finderItem, selected bool, nameW int) string {
	paint := m.finderPaint(selected)
	label := c.name
	if c.kind == "group" {
		label = strings.TrimSuffix(label, "/")
	}
	prefix := 0
	if i := strings.LastIndex(label, "/"); i >= 0 {
		prefix = utf8.RuneCountInString(label[:i+1])
	}
	var b strings.Builder
	for j, r := range []rune(label) {
		st := paint(lipgloss.NewStyle())
		switch {
		case j < prefix:
			st = paint(m.styles.hint)
		case selected:
			st = st.Bold(true)
		}
		if slices.Contains(c.matched, j) {
			st = m.styles.chipFind.Inherit(st)
		}
		b.WriteString(st.Render(string(r)))
	}
	if c.kind == "group" {
		b.WriteString(paint(m.styles.hint).Render("/"))
	}
	name := b.String()
	if w := lipgloss.Width(name); w > nameW {
		// Cut from the left so the own name, the last segment, stays visible.
		name = paint(m.styles.hint).Render("…") + ansi.TruncateLeft(name, w-nameW+1, "")
	}
	return name + paint(lipgloss.NewStyle()).Render(strings.Repeat(" ", max(0, nameW-lipgloss.Width(name))))
}

// finderPaint gives a style the selected row's background, so the cursor band has no gaps.
func (m *tuiModel) finderPaint(selected bool) func(lipgloss.Style) lipgloss.Style {
	return func(s lipgloss.Style) lipgloss.Style {
		if selected {
			return s.Inherit(m.styles.cursor)
		}
		return s
	}
}
