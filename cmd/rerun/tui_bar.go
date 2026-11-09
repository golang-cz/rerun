package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"
)

// pillLabel is a pill as drawn: filter solid in [..], highlight dim in ⟨..⟩;
// the brackets, never color alone, name the mode. The × closes it on a click.
func pillLabel(p pill) string {
	if p.kind == pillHighlight {
		return "⟨" + p.raw + " ×⟩"
	}
	return "[" + p.raw + " ×]"
}

func (m *tuiModel) barRows() int {
	if m.barFocus || len(m.pills) > 0 {
		return 1
	}
	return 0
}

// barLine draws the query bar's one row: the pills, a key hint, and the pill
// status right-aligned, with each pill's × cell recorded for clicks. Tight
// rows give way in order: pills elide from the left behind a '…', the status
// ellipsizes, and the key hint — the affordance — is never cut.
func (m *tuiModel) barLine(w int) string {
	m.layout.chips = m.layout.chips[:0]
	texts := make([]string, len(m.pills))
	for i, p := range m.pills {
		texts[i] = pillLabel(p)
	}
	status := ""
	if len(m.pills) > 0 {
		status = m.pillStatus()
	}
	hint := m.barHint()
	pillsW := func(hidden int) int {
		n := 0
		if hidden > 0 {
			n = 2 // "… "
		}
		for _, t := range texts[hidden:] {
			n += displayWidth(t) + 1
		}
		return n
	}
	need := func(hidden int, hint, status string) int {
		n := pillsW(hidden)
		if hint != "" {
			n += displayWidth(hint) + 1
		}
		if status != "" {
			n += displayWidth(status) + 1
		}
		return n
	}
	// The generic key hint goes before any pill does; pills then elide from
	// the left. The focused pill must stay on screen, or the arrows would
	// point at nothing.
	if need(0, hint, status) > w {
		hint = ""
	}
	hidden := 0
	for hidden < len(texts)-1 && need(hidden, hint, status) > w {
		if m.barFocus && m.barSel == hidden {
			break
		}
		hidden++
	}
	if need(hidden, hint, status) > w && status != "" {
		// The status is the trailing affordance: it ellipsizes, it never cuts mid-pill.
		status = ellipsize(status, max(0, w-pillsW(hidden)-displayWidth(hint)-2))
	}
	var parts []string
	x := 0
	if hidden > 0 {
		parts = append(parts, m.styles.hint.Render("…"))
		x += 2
	}
	for i := hidden; i < len(m.pills); i++ {
		text := texts[i]
		style := m.styles.chip
		if m.pills[i].kind == pillHighlight {
			style = m.styles.chipHi
		}
		if m.barFocus && m.barSel == i {
			style = style.Reverse(true)
		}
		cross := strings.LastIndex(text, "×")
		m.layout.chips = append(m.layout.chips, chipHit{x: x + displayWidth(text[:cross]), idx: i})
		parts = append(parts, style.Render(text))
		x += displayWidth(text) + 1
	}
	line := strings.Join(parts, " ")
	if hint != "" {
		line += " " + m.styles.hint.Render(hint)
	}
	if status != "" {
		gap := max(1, w-displayWidth(line)-displayWidth(status))
		line += strings.Repeat(" ", gap) + m.styles.hint.Render(status)
	}
	return padRight(line, w)
}

// barHint is the bar row's key line. A selected pill's keys move to the
// input box's footer, which has the room for them.
func (m *tuiModel) barHint() string {
	if len(m.pills) == 0 {
		return "/ query · @ saved"
	}
	return "/ add · ← backspace remove · space toggles"
}

// pillContextFooter is the input box's key line while a pill is selected;
// → expand only shows on a saved [@name] pill.
func (m *tuiModel) pillContextFooter() string {
	h := "enter edit · x remove · space filter⇄highlight"
	if m.pills[m.barSel].saved != "" {
		h += " · → expand"
	}
	return h + " · esc back"
}

// removePill drops the pill at idx, then fixes the bar's focus.
func (m *tuiModel) removePill(idx int) {
	if idx < 0 || idx >= len(m.pills) {
		return
	}
	m.pills = slices.Delete(m.pills, idx, idx+1)
	m.pillsChanged()
}

// pillsChanged refreshes everything a pill edit touches: the pane subset, the
// hit list, and the bar's focus.
func (m *tuiModel) pillsChanged() {
	m.paneSelDirty = true
	m.dispDirty = true
	m.vpDirty = true
	m.clampBar()
	m.recomputeSearch(true)
}

// Focus must not outlive the last pill it could point at.
func (m *tuiModel) clampBar() {
	if m.barSel >= len(m.pills) {
		m.barSel = len(m.pills) - 1
	}
}

// openBar focuses the query bar with its input open; paneFocus stays set underneath.
func (m *tuiModel) openBar() {
	m.barFocus = true
	m.barSel = -1
	m.inputMode = inputQuery
	m.input = ""
	m.inputCursor = 0
	m.inputSel = false
	m.editKindOn = false
	m.draft, m.draftOn, m.queryErr = pillSet{}, false, ""
}

// closeBar commits a valid typed input, then blurs the bar; the pills stay.
// The input commits even while a pill is selected. An input whose saved query
// does not resolve keeps the bar open with the error named; esc then clears it.
func (m *tuiModel) closeBar() {
	if m.input != "" && m.queryErr == "" {
		if m.pickActive() {
			if !m.pickCommit() {
				return
			}
		} else if !m.commitText() {
			return
		}
	}
	m.inputMode = inputNone
	m.input = ""
	m.inputCursor = 0
	m.inputSel = false
	m.editKindOn = false
	m.draft, m.draftOn, m.queryErr = pillSet{}, false, ""
	m.barFocus = false
	m.barSel = -1
	m.pillsChanged()
}

// pickCommit is esc or blur with the @ picker open: an exactly typed @name
// commits as a saved pill, a name no candidate matches names the error and
// keeps the bar open, and anything else just closes.
func (m *tuiModel) pickCommit() bool {
	if _, ok := m.scopeQuery(strings.TrimPrefix(m.input, "@")); ok {
		return m.commitText()
	}
	if m.input != "@" && len(m.pickMatches()) == 0 {
		m.queryErr = "unknown query " + m.input
		return false
	}
	return true
}

// commitText parses the input into pills and appends them, restoring the kind
// of a pill under edit. A parse or resolve error is named and reported false.
func (m *tuiModel) commitText() bool {
	pills, err := parsePills(m.input, m.barCols())
	if err != nil {
		m.queryErr = err.Error()
		return false
	}
	if err := m.resolveSaved(pills); err != nil {
		m.queryErr = err.Error()
		return false
	}
	if m.editKindOn {
		for i := range pills {
			pills[i].kind = m.editKind
		}
	}
	m.pills = append(m.pills, pills...)
	return true
}

// commitInput is enter in the bar: on a picked saved query it adds it, on a
// selected pill it moves the text back into the input, else it parses the
// input into pills. A parse error keeps the input red and the bar open.
func (m *tuiModel) commitInput() {
	if m.barSel >= 0 {
		p := m.pills[m.barSel]
		m.pills = slices.Delete(m.pills, m.barSel, m.barSel+1)
		text := p.raw
		if m.input != "" {
			text += " " + m.input
		}
		m.barSel = -1
		// The kind survives the edit: re-committing restores filter vs highlight.
		m.editKind, m.editKindOn = p.kind, true
		m.inputSet(text, len([]rune(p.raw)))
		m.pillsChanged()
		return
	}
	if m.pickActive() {
		m.pickSelected()
		return
	}
	if m.queryErr != "" {
		return
	}
	if m.input != "" && !m.commitText() {
		return
	}
	// The input is consumed; closeBar must not commit it a second time.
	m.input = ""
	m.inputCursor = 0
	m.closeBar()
}

// inputChanged re-parses the typed text live: a valid input previews as a
// draft, an invalid one only shows its error; the pane never blanks on it. A
// draft's @refs resolve here too, and the hit list follows the changed rows.
func (m *tuiModel) inputChanged() {
	m.draft, m.draftOn, m.queryErr = pillSet{}, false, ""
	m.pickSel = 0
	if m.input != "" {
		if set, err := compilePills(m.input, m.barCols()); err != nil {
			// The @ picker's partial names never turn the input red.
			if !m.pickActive() {
				m.queryErr = err.Error()
			}
		} else if m.resolveSaved(set.pills) == nil {
			// An unresolvable @name stays undrafted; enter or esc names the error.
			m.draft, m.draftOn = set, true
		}
	}
	m.paneSelDirty = true
	m.dispDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
}

// barCols is the columns the bar's field pills resolve their names against.
func (m *tuiModel) barCols() []Column { return m.headerColumns() }

// pickActive reports whether the bar input is picking a saved query: '@'
// leads the input and no pill is selected.
func (m *tuiModel) pickActive() bool {
	return m.inputMode != inputNone && m.barSel < 0 && strings.HasPrefix(m.input, "@")
}

// pickMatches is the @ picker's candidate list, narrowed by the text after '@'.
func (m *tuiModel) pickMatches() []savedQuery {
	qs := m.scopeQueries()
	needle := strings.TrimPrefix(m.input, "@")
	if needle == "" {
		return qs
	}
	names := make([]string, len(qs))
	for i, q := range qs {
		names[i] = q.name
	}
	var out []savedQuery
	for _, f := range fuzzy.Find(needle, names) {
		out = append(out, qs[f.Index])
	}
	return out
}

// barKeyName is the key's name; String() prefers Text, which drops the alt
// modifier that select-all rides on.
func barKeyName(msg tea.KeyPressMsg) string {
	if msg.Mod.Contains(tea.ModAlt) && msg.Text != "" {
		return msg.Keystroke()
	}
	return msg.String()
}

// savedQuery is one queries: entry: its name, its pill string, and the
// columns of the scope that defined it, which its field names resolve against.
type savedQuery struct {
	name  string
	query string
	cols  []Column
}

// scopeQueries lists the pane scope's saved queries, most specific first:
// every scope target's own entries, then their groups', then the global ones.
// A name clash keeps the most specific.
func (m *tuiModel) scopeQueries() []savedQuery {
	var out []savedQuery
	seen := map[string]bool{}
	add := func(qs map[string]string, cols []Column) {
		for _, n := range slices.Sorted(maps.Keys(qs)) {
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, savedQuery{name: n, query: qs[n], cols: cols})
		}
	}
	members := m.scopeMembers()
	for _, i := range members {
		spec := m.targets[i].spec
		own := spec.ownQueries
		if own == nil && spec.groupQueries == nil {
			own = spec.Queries // a hand-built target holds only the flattened set
		}
		add(own, spec.Columns)
	}
	for _, i := range members {
		spec := m.targets[i].spec
		add(spec.groupQueries, spec.groupQueryCols)
	}
	add(m.queries, nil)
	return out
}

// scopeQuery finds a saved query by name in the pane's scope.
func (m *tuiModel) scopeQuery(name string) (savedQuery, bool) {
	for _, q := range m.scopeQueries() {
		if q.name == name {
			return q, true
		}
	}
	return savedQuery{}, false
}

// resolveSaved attaches every saved pill's parsed pills; an unknown name is an
// error, and a cycle past the depth cap is one too, never an inert pill.
func (m *tuiModel) resolveSaved(pills []pill) error {
	return m.resolveSavedDepth(pills, 0)
}

func (m *tuiModel) resolveSavedDepth(pills []pill, depth int) error {
	if depth > maxQueryDepth {
		return fmt.Errorf("saved queries nest past %d levels; a cycle?", maxQueryDepth)
	}
	for i := range pills {
		p := &pills[i]
		if p.saved == "" {
			continue
		}
		q, ok := m.scopeQuery(p.saved)
		if !ok {
			return fmt.Errorf("unknown query @%s", p.saved)
		}
		sub, err := parsePills(q.query, q.cols)
		if err != nil {
			return fmt.Errorf("@%s: %v", p.saved, err)
		}
		p.sub = sub
		if err := m.resolveSavedDepth(p.sub, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// pickSelected is enter in the @ picker: the selected query becomes one
// [@name] pill, and the input clears for the next one.
func (m *tuiModel) pickSelected() {
	ms := m.pickMatches()
	if len(ms) == 0 || m.pickSel >= len(ms) {
		return
	}
	name := ms[m.pickSel].name
	p := []pill{{raw: "@" + name, saved: name, kind: pillFilter}}
	if err := m.resolveSaved(p); err != nil {
		m.queryErr = err.Error()
		return
	}
	m.pills = append(m.pills, p[0])
	m.inputSet("", 0)
	m.pillsChanged()
}

// expandSaved replaces a [@name] pill with its parsed pills in place; they
// keep the parent's kind, so expanding a highlight never starts hiding lines.
func (m *tuiModel) expandSaved(idx int) {
	if idx < 0 || idx >= len(m.pills) || m.pills[idx].saved == "" {
		return
	}
	if m.pills[idx].neg {
		// Expanding would drop the negation, so the saved pill stays whole.
		m.showHint("negated @query cannot expand")
		return
	}
	subs := slices.Clone(m.pills[idx].sub)
	for i := range subs {
		subs[i].kind = m.pills[idx].kind
	}
	m.pills = slices.Replace(m.pills, idx, idx+1, subs...)
	m.pillsChanged()
}

func (m *tuiModel) jumpGroup(dir int) {
	rows := m.listRows()
	for i := m.cursor + dir; i >= 0 && i < len(rows); i += dir {
		if rows[i].header {
			m.cursor = i
			m.applyRowScope(rows[i])
			return
		}
	}
}

// scopeState is what esc can change in the list; a comparison tells whether it did.
type scopeState struct {
	solo   int
	sel    string
	cursor int
}

func (m *tuiModel) scopeState() scopeState {
	return scopeState{solo: m.solo, sel: m.schemaSel, cursor: m.cursor}
}

// listFocused reports whether the list, not the pane, owns the keys.
func (m *tuiModel) listFocused() bool {
	return !m.paneFocus && m.listShown()
}

// escList backs the scope out one level; pills are never touched.
func (m *tuiModel) escList() {
	r, ok := m.cursorRow()
	if !ok || (r.header && r.depth == 0) || !m.hasGroups() {
		m.clearScope()
		return
	}
	i := m.enclosingHeader(m.cursor)
	if i < 0 {
		m.clearScope()
		return
	}
	m.cursor = i
	hr, _ := m.listRowAt(i)
	m.applyRowScope(hr)
}

// handleBarKey routes keys while the query bar is open: the input owns the
// text keys, and the pills take the arrows when the input is empty or the
// cursor is at 0. Only Ctrl-C escapes the bar.
func (m *tuiModel) handleBarKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := barKeyName(msg)
	if m.pickActive() {
		switch key {
		case "up", "ctrl+p":
			m.pickSel = max(0, m.pickSel-1)
			return m, nil
		case "down", "ctrl+n":
			m.pickSel = min(max(0, len(m.pickMatches())-1), m.pickSel+1)
			return m, nil
		}
	}
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+[":
		m.closeBar()
	case "ctrl+g":
		m.inputSel = false
		m.searchJump(1)
	// Mirror ctrl+g: the enhanced decoder keeps the shift distinct.
	case "ctrl+shift+g":
		m.inputSel = false
		m.searchJump(-1)
	case "enter", "ctrl+m":
		m.commitInput()
	case "left":
		switch {
		case m.barSel > 0:
			m.barSel--
		case m.barSel == 0:
		case len(m.pills) > 0 && (m.input == "" || m.inputPos() == 0):
			m.barSel = len(m.pills) - 1
		default:
			m.inputMoveRune(-1)
		}
	case "right":
		switch {
		case m.barSel >= 0 && m.pills[m.barSel].saved != "":
			m.expandSaved(m.barSel)
		case m.barSel >= 0 && m.barSel < len(m.pills)-1:
			m.barSel++
		case m.barSel >= 0:
			m.barSel = -1
		case m.input == "" && len(m.pills) > 0:
			m.barSel = 0
		default:
			m.inputMoveRune(1)
		}
	case "alt+left", "alt+b":
		m.inputMoveWord(-1)
	case "alt+right", "alt+f":
		m.inputMoveWord(1)
	case "home", "ctrl+a":
		m.inputMoveTo(0)
	case "end", "ctrl+e":
		m.inputMoveTo(len([]rune(m.input)))
	case "alt+i":
		m.inputSelectAll()
	case "x":
		// On a selected pill x removes it; otherwise it is just a letter.
		if m.barSel >= 0 {
			m.removePill(m.barSel)
			break
		}
		m.inputInsert("x")
	case " ", "space":
		if m.barSel >= 0 {
			p := &m.pills[m.barSel]
			p.kind = pillHighlight - p.kind // the two kinds are 0 and 1
			m.pillsChanged()
			break
		}
		m.inputInsert(" ")
	// ctrl+h is how the enhanced decoder spells backspace (kitty disambiguation).
	case "backspace", "ctrl+h":
		switch {
		case m.barSel >= 0:
			m.removePill(m.barSel)
		case m.inputSel || m.input != "":
			m.inputDeleteBefore()
		case len(m.pills) > 0:
			m.removePill(len(m.pills) - 1)
		}
	case "delete", "ctrl+d":
		if m.barSel >= 0 {
			m.removePill(m.barSel)
			break
		}
		m.inputDeleteAfter()
	case "ctrl+w", "alt+backspace":
		m.inputDeleteWordBefore()
	case "ctrl+u":
		m.inputDeleteToStart()
	default:
		if msg.Text != "" {
			m.barSel = -1
			m.inputInsert(msg.Text)
			break
		}
		// v1 handed an alt combo over as key runes, so a printable one typed its letter.
		if msg.Mod.Contains(tea.ModAlt) && unicode.IsPrint(msg.Code) {
			m.barSel = -1
			m.inputInsert(string(msg.Code))
		}
	}
	return m, nil
}
