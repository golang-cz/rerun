package main

// markW is the pane cells the (+) and (−) cell takes: a space, then the glyph.
const markW = 4

// dispRow is one pane row: a shown line (sub -1) or a row of its expansion.
type dispRow struct {
	raw   int
	sub   int
	text  string
	obj   bool
	open  bool
	field *jsonField
}

// rowInfo is the pane row at row, with the raw line it belongs to.
func (m *tuiModel) rowInfo(row int) dispRow {
	m.ensurePane()
	if m.paneDirect() {
		return dispRow{raw: row, sub: -1, obj: m.visibleObj[row]}
	}
	return m.disp[row]
}

// rebuildDisp lays out the shown lines, each followed by its expansion when it
// is open. The expansion is indented under the message column.
func (m *tuiModel) rebuildDisp() {
	m.dispDirty = false
	m.disp = m.disp[:0]
	m.dispText = m.dispText[:0]
	shown := len(m.visible)
	if m.filterActive() {
		shown = len(m.paneSel)
	}
	for k := 0; k < shown; k++ {
		raw := k
		if m.filterActive() {
			raw = m.paneSel[k]
		}
		open := m.expanded[m.visibleSeq[raw]]
		m.addDisp(dispRow{raw: raw, sub: -1, text: m.visible[raw], obj: m.visibleObj[raw], open: open})
		if !open {
			continue
		}
		for _, r := range m.expansionRows(raw) {
			r.raw = raw
			m.addDisp(r)
		}
	}
}

type blockKey struct{ seq, base int }

// Expansions are few at a time, so a small cache covers every redraw.
const blockCacheCap = 64

func (m *tuiModel) expansionRows(raw int) []dispRow {
	if !m.visibleObj[raw] {
		return m.sepRows(raw)
	}
	k := blockKey{m.visibleSeq[raw], displayWidth(m.visible[raw]) - displayWidth(m.visibleRaw[raw])}
	if rows, ok := m.blockCache[k]; ok {
		return rows
	}
	rows, _ := jsonBlock(m.visibleRaw[raw], k.base)
	if len(m.blockCache) >= blockCacheCap {
		m.blockCache = map[blockKey][]dispRow{}
	}
	m.blockCache[k] = rows
	return rows
}

// sepRows is a rule's expansion: its build rows, or the command a cut-short started rule hides.
func (m *tuiModel) sepRows(raw int) []dispRow {
	s := m.visibleSep[raw]
	if !s.expandable(m.vp.Width()) {
		return nil
	}
	lines := []string{s.full}
	if s.build != nil {
		lines = s.build.expansion()
	}
	rows := make([]dispRow, len(lines))
	for k, line := range lines {
		rows[k] = dispRow{sub: k, text: spaces(4) + line}
	}
	return rows
}

// expandable reports whether a shown line opens: a JSON object, or a rule with something under it.
func (m *tuiModel) expandable(raw int) bool {
	return m.visibleObj[raw] || m.visibleSep[raw].expandable(m.vp.Width())
}

func (m *tuiModel) addDisp(r dispRow) {
	m.disp = append(m.disp, r)
	m.dispText = append(m.dispText, r.text)
}

// Keyed by seq so an expansion survives a re-filter; opening pauses the tail.
func (m *tuiModel) toggleExpand(raw int) {
	if !m.expandable(raw) {
		return
	}
	seq := m.visibleSeq[raw]
	opening := !m.expanded[seq]
	if opening {
		m.pauseTail()
		m.expanded[seq] = true
		if m.visibleObj[raw] && !m.hintedExpand {
			m.hintedExpand = true
			m.showHint("click a key (a) to add a column · click a value (f) to filter")
		}
	} else {
		delete(m.expanded, seq)
	}
	m.dispDirty = true
	m.vpDirty = true
	m.ensurePane()
	m.setPaneCursor(m.lineRow(raw))
	m.revealCur, m.revealTop = true, opening
}

// paneCursorRow is the display row under the cursor, the newest row while the tail follows.
func (m *tuiModel) paneCursorRow() int {
	n := m.paneLen()
	if n == 0 {
		return 0
	}
	if m.follow {
		return n - 1
	}
	if m.paneDirect() {
		return min(max(m.paneCurRaw, 0), n-1)
	}
	for row, r := range m.disp {
		if r.raw == m.paneCurRaw && r.sub == m.paneCurSub {
			return row
		}
	}
	return m.lineRow(m.paneCurRaw)
}

// setPaneCursor moves the cursor to display row row.
func (m *tuiModel) setPaneCursor(row int) {
	r := m.rowInfo(row)
	m.paneCurRaw, m.paneCurSub = r.raw, r.sub
}

// lineRow is the display row of raw's line, or the next shown line when a filter hides it.
func (m *tuiModel) lineRow(raw int) int {
	if m.paneDirect() {
		return raw
	}
	for row, r := range m.disp {
		if r.sub < 0 && r.raw >= raw {
			return row
		}
	}
	return max(0, len(m.disp)-1)
}

// paneRows is the content rows the viewport shows, less the marker row.
func (m *tuiModel) paneRows() int {
	h := m.vp.Height()
	if !m.follow && m.pending > 0 {
		h--
	}
	return max(h, 1)
}

// moveCursorRow moves the cursor by delta rows, pausing the tail as a scroll does.
func (m *tuiModel) moveCursorRow(delta int) {
	n := m.paneLen()
	if n == 0 {
		return
	}
	row := m.paneCursorRow()
	next := min(max(row+delta, 0), n-1)
	if next == row {
		return
	}
	m.pauseTail()
	m.setPaneCursor(next)
	m.revealCur, m.revealTop = true, false
}

// placeCursor scrolls to a moved cursor, else keeps a paused cursor inside the viewport.
func (m *tuiModel) placeCursor() {
	n := m.paneLen()
	if n == 0 {
		return
	}
	if m.follow {
		m.revealCur, m.revealTop = false, false
		m.setPaneCursor(n - 1)
		return
	}
	row := m.paneCursorRow()
	h := m.paneRows()
	if m.revealCur {
		switch {
		case m.revealTop:
			m.vp.SetYOffset(row)
		case row < m.vp.YOffset():
			m.vp.SetYOffset(row)
		case row >= m.vp.YOffset()+h:
			m.vp.SetYOffset(row - h + 1)
		}
		m.revealCur, m.revealTop = false, false
	}
	switch {
	case row < m.vp.YOffset():
		m.setPaneCursor(min(m.vp.YOffset(), n-1))
	case row >= m.vp.YOffset()+h:
		m.setPaneCursor(min(m.vp.YOffset()+h-1, n-1))
	}
}

func (m *tuiModel) toggleCursorExpand() {
	if m.paneLen() == 0 {
		return
	}
	m.toggleExpand(m.rowInfo(m.paneCursorRow()).raw)
}

// arrowExpand only opens: a right arrow on an open line must keep panning, not collapse it.
func (m *tuiModel) arrowExpand() bool {
	r, ok := m.cursorLine()
	if !ok || !m.expandable(r.raw) {
		return false
	}
	if !m.expanded[m.visibleSeq[r.raw]] {
		m.toggleExpand(r.raw)
	}
	return true
}

// arrowCollapse closes the cursor's expansion and puts the cursor on its line.
func (m *tuiModel) arrowCollapse() bool {
	if m.paneLen() == 0 {
		return false
	}
	r := m.rowInfo(m.paneCursorRow())
	if !m.expanded[m.visibleSeq[r.raw]] {
		return false
	}
	delete(m.expanded, m.visibleSeq[r.raw])
	m.dispDirty = true
	m.vpDirty = true
	m.ensurePane()
	m.setPaneCursor(m.lineRow(r.raw))
	return true
}

// cursorLine is the shown line under the cursor, false while it sits on an expansion row.
func (m *tuiModel) cursorLine() (dispRow, bool) {
	if m.paneLen() == 0 {
		return dispRow{}, false
	}
	r := m.rowInfo(m.paneCursorRow())
	return r, r.sub < 0
}

// cursorField is the expansion row under the cursor, or nil on a shown line.
func (m *tuiModel) cursorField() *jsonField {
	if m.paneLen() == 0 {
		return nil
	}
	return m.rowInfo(m.paneCursorRow()).field
}

// pickCursorKey adds the cursor's key as a column, the same pick as a click on it.
func (m *tuiModel) pickCursorKey() {
	f := m.cursorField()
	if f == nil || f.key == "" {
		m.showHint("move the cursor onto a key in an expanded line")
		return
	}
	m.pickKey(f)
}

// pickCursorValue adds the cursor's scalar as a chip, the same pick as a click on it.
func (m *tuiModel) pickCursorValue() {
	f := m.cursorField()
	if f == nil || f.lit == "" {
		m.showHint("move the cursor onto a scalar value in an expanded line")
		return
	}
	m.pickValue(f)
}

// markLines draws the (+) or (−) cell at the pane's right edge on each JSON line.
// The line's content gives way to the cell with an ellipsis, never under it.
func (m *tuiModel) markLines(lines []string) []string {
	w := m.vp.Width()
	if w < markW {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	for row := range lines {
		r := m.rowInfo(row)
		if r.sub >= 0 || !r.obj {
			continue
		}
		glyph := "(+)"
		if r.open {
			glyph = "(−)"
		}
		content := padRight(ellipsize(lines[row], w-markW), w-markW)
		out[row] = content + " " + glyph
	}
	return out
}

// clickPane handles a press in the viewport: the (+) cell, a key or value of an
// expansion row, or else an anchor for a copy of the line.
func (m *tuiModel) clickPane(x, y int) {
	// A press anywhere in the pane takes the keys, even past the content.
	m.paneFocus = true
	row := m.paneRow(x, y)
	if row < 0 {
		return
	}
	px := x - m.layout.leftW - 1
	r := m.rowInfo(row)
	switch {
	case r.sub < 0 && r.obj && px >= m.vp.Width()-markW:
		m.toggleExpand(r.raw)
	case r.sub < 0 && !r.obj && m.visibleSep[r.raw].hit(px+m.paneCol, m.vp.Width()):
		m.toggleExpand(r.raw)
	case r.field != nil && inSpan(r.field.keyX, px+m.paneCol):
		m.pickKey(r.field)
	case r.field != nil && r.field.lit != "" && inSpan(r.field.valX, px+m.paneCol):
		m.pickValue(r.field)
	default:
		anchor := row
		m.selAnchor = &anchor
		m.selEnd = row
	}
}

func inSpan(span [2]int, x int) bool {
	return span[1] > span[0] && x >= span[0] && x < span[1]
}

// inPaneHeader reports whether a press lands on the pane's fixed header rows.
func (m *tuiModel) inPaneHeader(x, y int) bool {
	return x > m.layout.leftW && x < m.width && y >= 0 && y < m.titleHeight()
}

// clickHeader acts on a header press at pane cell px: a chip's or a column's × removes it.
func (m *tuiModel) clickHeader(px, y int) {
	if y < m.barRows() {
		chips := m.barChips()
		for _, h := range m.layout.chips {
			if px == h.x && h.idx < len(chips) {
				m.removeBarChip(chips[h.idx])
				return
			}
		}
		return
	}
	if !m.tableHeaderOn() {
		return
	}
	for _, h := range m.layout.heads {
		if inSpan([2]int{h.x0, h.x1}, px+m.paneCol) {
			m.removeColumn(h.col)
			return
		}
	}
}

// copyLines is the raw text of the selected display rows, one per line, skipping
// expansion rows so a copy never repeats a line.
func (m *tuiModel) copyLines(lo, hi int) []string {
	raw := make([]string, 0, hi-lo+1)
	for row := lo; row <= hi; row++ {
		r := m.rowInfo(row)
		if r.sub >= 0 {
			continue
		}
		raw = append(raw, m.visibleRaw[r.raw])
	}
	return raw
}
