package main

import (
	"slices"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

// finderMinRows is the match count the popup shows on any terminal tall enough for it.
const finderMinRows = 10

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
	enter   func(m *tuiModel)
}

// groupDesc is a group's description; groups have none until config supplies one.
func groupDesc(schema string) string { return "" }

// finderDesc collapses a description onto one line, so a newline cannot break the popup.
func finderDesc(s string) string { return strings.Join(strings.Fields(s), " ") }

// finderCandidates lists every target and group in list order, including rows under collapsed groups.
func (m *tuiModel) finderCandidates() []finderItem {
	rows := m.allRows()
	out := make([]finderItem, 0, len(rows))
	for i, r := range rows {
		if r.header {
			out = append(out, finderItem{kind: "group", name: groupLabel(r.schema), desc: finderDesc(groupDesc(r.schema)),
				enter: func(m *tuiModel) { m.finderJump(i) }})
			continue
		}
		t := m.targets[r.target].spec
		out = append(out, finderItem{kind: "target", name: t.Name, desc: finderDesc(t.Desc),
			enter: func(m *tuiModel) { m.finderJump(i) }})
	}
	return out
}

// finderResults ranks candidates: name matches first, then desc-only matches, each by score.
func (m *tuiModel) finderResults(query string) []finderItem {
	cands := m.finderCandidates()
	if query == "" {
		return cands
	}
	names := make([]string, len(cands))
	descs := make([]string, len(cands))
	for i, c := range cands {
		names[i], descs[i] = c.name, c.desc
	}
	var out []finderItem
	named := make([]bool, len(cands))
	for _, f := range fuzzy.Find(query, names) {
		c := cands[f.Index]
		c.matched = f.MatchedIndexes
		out = append(out, c)
		named[f.Index] = true
	}
	for _, f := range fuzzy.Find(query, descs) {
		if !named[f.Index] {
			out = append(out, cands[f.Index])
		}
	}
	return out
}

// finderWidth is the popup's outer width, border included: half the screen or 100 cells, never wider than the screen.
func (m *tuiModel) finderWidth() int {
	w := max(m.width/2, min(100, m.width-8))
	return max(1, min(w, m.width-2))
}

// finderVisibleRows is how many matches fit: half the screen, and the popup adds six lines around them.
func (m *tuiModel) finderVisibleRows() int {
	return max(1, min(max(finderMinRows, m.height/2), m.height-6))
}

// handleFinderKey routes keys while the finder is open; enter jumps, esc and ctrl+k close.
func (m *tuiModel) handleFinderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	res := m.finderResults(m.finder.query)
	n := min(len(res), m.finderVisibleRows())
	m.finder.sel = min(m.finder.sel, max(0, n-1))
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+k":
		m.finder = finderState{}
	case "enter":
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
	case "backspace":
		if r := []rune(m.finder.query); len(r) > 0 {
			m.finder.query = string(r[:len(r)-1])
			m.finder.sel = 0
		}
	default:
		switch msg.Type {
		case tea.KeyRunes:
			if msg.Alt {
				break
			}
			if q := strings.Map(dropControl, string(msg.Runes)); q != "" {
				m.finder.query += q
				m.finder.sel = 0
			}
		case tea.KeySpace:
			m.finder.query += " "
			m.finder.sel = 0
		}
	}
	return m, nil
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
	m.paneFocus = false
	m.setDrawerFocus(false)
	rows := m.allRows()
	m.revealRow(rows, idx)
	m.cursor = m.indexOfKey(m.rowKey(rows[idx]))
	if r, ok := m.cursorRow(); ok {
		m.applyRowScope(r)
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

// finderView draws the popup: the query line, then the ranked matches.
func (m *tuiModel) finderView() string {
	outer := m.finderWidth()
	w := max(1, outer-4) // border and padding take four cells
	res := m.finderResults(m.finder.query)
	row := lipgloss.NewStyle().MaxWidth(w)
	bold := lipgloss.NewStyle().Bold(true)

	var b strings.Builder
	b.WriteString(row.Render(m.styles.title.Render("find target or group")) + "\n")
	b.WriteString(row.Render("> "+finderTail(m.finder.query, w-3)+"_") + "\n")
	if len(res) == 0 {
		b.WriteString(row.Render(m.styles.hint.Render("no match")) + "\n")
	}
	for i, c := range res[:min(len(res), m.finderVisibleRows())] {
		var name strings.Builder
		for j, r := range []rune(c.name) {
			if slices.Contains(c.matched, j) {
				name.WriteString(bold.Render(string(r)))
			} else {
				name.WriteRune(r)
			}
		}
		if c.kind == "group" {
			name.WriteString("/")
		}
		prefix := "  "
		if i == m.finder.sel {
			prefix = "› "
		}
		line := prefix + name.String()
		if room := w - lipgloss.Width(line) - 2; c.desc != "" && room > 0 {
			line += "  " + m.styles.hint.Render(ansi.Truncate(c.desc, room, "…"))
		}
		b.WriteString(row.Render(line) + "\n")
	}
	b.WriteString("\n" + row.Render(m.styles.hint.Render("↑/↓ move · enter jump · esc closes")))
	lines := strings.Split(m.styles.box.Width(outer-2).Render(b.String()), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "")
	}
	return strings.Join(lines, "\n")
}
