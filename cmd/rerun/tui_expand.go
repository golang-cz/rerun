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
		block, _ := jsonBlock(m.visibleRaw[raw], m.prefixW+2)
		for _, r := range block {
			r.raw = raw
			m.addDisp(r)
		}
	}
}

func (m *tuiModel) addDisp(r dispRow) {
	m.disp = append(m.disp, r)
	m.dispText = append(m.dispText, r.text)
}

// toggleExpand opens or closes a JSON line's pretty-printed block. The state is
// keyed by the line's sequence number, so it survives later lines.
func (m *tuiModel) toggleExpand(raw int) {
	if !m.visibleObj[raw] {
		return
	}
	seq := m.visibleSeq[raw]
	if m.expanded[seq] {
		delete(m.expanded, seq)
	} else {
		m.expanded[seq] = true
	}
	m.dispDirty = true
	m.vpDirty = true
}

// bottomRow is the bottom-most visible content row, the one enter acts on.
func (m *tuiModel) bottomRow() (int, bool) {
	n := m.paneLen()
	if n == 0 {
		return 0, false
	}
	last := m.vp.YOffset + m.vp.Height - 1
	if !m.follow && m.pending > 0 {
		last-- // the `↓ N new` marker owns this row
	}
	last = min(last, n-1)
	return last, last >= 0
}

// expandCursor is enter on a focused pane: it toggles the bottom-most line.
func (m *tuiModel) expandCursor() {
	row, ok := m.bottomRow()
	if !ok {
		return
	}
	raw := m.rowInfo(row).raw
	if !m.visibleObj[raw] {
		m.showHint("the bottom line is not a JSON object")
		return
	}
	m.toggleExpand(raw)
}

// markLines draws the (+) or (−) cell at the pane's right edge on each JSON line.
func (m *tuiModel) markLines(lines []string) []string {
	w := m.vp.Width
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
		out[row] = padRight(lines[row], w-markW) + " " + glyph
	}
	return out
}

// clickPane handles a press in the viewport: the (+) cell, a key or value of an
// expansion row, or else an anchor for a copy of the line.
func (m *tuiModel) clickPane(x, y int) {
	row := m.paneRow(x, y)
	if row < 0 {
		return
	}
	px := x - m.layout.leftW - 1
	r := m.rowInfo(row)
	switch {
	case r.sub < 0 && r.obj && px >= m.vp.Width-markW:
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

// clickHeader acts on a header press at pane cell px: a chip's × removes that
// chip, a column name removes that column.
func (m *tuiModel) clickHeader(px, y int) {
	if m.titleRows() == 1 && y == 0 {
		for _, h := range m.layout.chips {
			if px == h.x {
				m.removeChip(h.idx)
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
