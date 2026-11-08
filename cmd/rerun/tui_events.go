package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// eventsTitle is the modal's header, naming what it shows.
const eventsTitle = "fs events"

// eventsFooter is the modal's key line.
const eventsFooter = "j k move · → files · ← back · esc close"

// eventsEmpty is the modal's placeholder while no batch sits in the window.
const eventsEmpty = "no fs events in the last minute"

// eventsState is the `e` fs events modal: the last minute's change batches read
// over the live frame. cursor and openID name batches, never indexes, so a batch
// that arrives while the modal is open cannot move the cursor off its own row.
type eventsState struct {
	open   bool
	cursor int // the batch the cursor sits on
	openID int // the batch whose file list is expanded, 0 for none
	top    int // the first body row drawn
}

// openEvents opens the modal on the newest visible batch.
func (m *tuiModel) openEvents() {
	batches, _ := m.feedVisible()
	id := 0
	if len(batches) > 0 {
		id = batches[0].id
	}
	m.eventsModal = eventsState{open: true, cursor: id}
	m.fitEvents()
}

// eventsSize is the modal's outer box: the finder's width, 80% of the terminal's height.
func (m *tuiModel) eventsSize() (int, int) { return m.finderSize() }

// eventsBodyRows is how many body rows fit between the header, the rule and the footer.
func (m *tuiModel) eventsBodyRows() int {
	_, outerH := m.eventsSize()
	innerH := max(1, outerH-m.styles.box.GetVerticalFrameSize())
	return max(0, innerH-2-1)
}

// eventsRow is one modal body row: a batch, or one line of the open batch's file list.
type eventsRow struct {
	id     int
	batch  *feedBatch // nil on a file-list row
	dir    bool
	file   bool
	glyph  string
	indent int
	label  string
	dim    bool
	sel    bool
}

// eventsCursorID is the cursor's batch id, the newest when its own batch has aged out.
func (m *tuiModel) eventsCursorID(batches []feedBatch) int {
	for _, b := range batches {
		if b.id == m.eventsModal.cursor {
			return b.id
		}
	}
	if len(batches) > 0 {
		return batches[0].id
	}
	return 0
}

// eventsRows lays out the body: the visible batches newest first, each followed by
// its file list when it is the open one. The file rows come from the drawer's own
// feedFileRows, so both list a batch's files the same way.
func (m *tuiModel) eventsRows(batches []feedBatch, dim bool) []eventsRow {
	cur := m.eventsCursorID(batches)
	rows := make([]eventsRow, 0, len(batches))
	for i := range batches {
		b := &batches[i]
		rows = append(rows, eventsRow{id: b.id, batch: b, sel: b.id == cur, dim: dim})
		if b.id != m.eventsModal.openID {
			continue
		}
		for _, r := range feedFileRows(*b) {
			rows = append(rows, eventsRow{
				id: b.id, dir: r.kind == drawerDir, file: r.kind == drawerFile,
				glyph: r.glyph, indent: r.indent, label: r.label,
			})
		}
	}
	return rows
}

// moveEvents moves the cursor between batches, skipping an open batch's file rows.
func (m *tuiModel) moveEvents(delta int) {
	batches, _ := m.feedVisible()
	if len(batches) == 0 {
		return
	}
	i := min(max(batchIndex(batches, m.eventsCursorID(batches))+delta, 0), len(batches)-1)
	m.eventsModal.cursor = batches[i].id
}

// expandEvents opens the cursor batch's file list below it.
func (m *tuiModel) expandEvents() {
	batches, _ := m.feedVisible()
	if len(batches) == 0 {
		return
	}
	m.eventsModal.openID = m.eventsCursorID(batches)
}

// collapseEvents closes the open file list.
func (m *tuiModel) collapseEvents() { m.eventsModal.openID = 0 }

// handleEventsKey routes keys while the modal is open; esc closes it.
func (m *tuiModel) handleEventsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.eventsModal = eventsState{}
	case "j", "down":
		m.moveEvents(1)
	case "k", "up":
		m.moveEvents(-1)
	case "right", "enter":
		m.expandEvents()
	case "left":
		m.collapseEvents()
	}
	m.fitEvents()
	return m, nil
}

// fitEvents clamps the modal's scroll window to its cursor. The handlers call it,
// never the render, so drawing the same state twice changes nothing.
func (m *tuiModel) fitEvents() {
	if !m.eventsModal.open {
		return
	}
	batches, dim := m.feedVisible()
	rows := m.eventsRows(batches, dim)
	sel := 0
	for i, r := range rows {
		if r.sel {
			sel = i
			break
		}
	}
	m.eventsModal.top = scrollTop(m.eventsModal.top, len(rows), m.eventsBodyRows(), sel)
}

// eventsView draws the modal: the batches, the open batch's files, and the key line.
func (m *tuiModel) eventsView() string {
	outerW, outerH := m.eventsSize()
	innerW := m.modalInnerW(outerW)
	batches, dim := m.feedVisible()
	rows := m.eventsRows(batches, dim)
	body := m.eventsBodyRows()

	// The body fills its rows, so the footer lands on the box's last inner row.
	lines := make([]string, 0, body)
	if len(rows) == 0 {
		lines = append(lines, m.styles.hint.Render(eventsEmpty))
	}
	for i := m.eventsModal.top; i < len(rows) && len(lines) < body; i++ {
		lines = append(lines, m.eventsLine(rows[i], innerW))
	}
	for len(lines) < body {
		lines = append(lines, "")
	}
	return m.modalBox(m.eventsHeader(len(batches), innerW), eventsFooter, lines, outerW, outerH)
}

// eventsHeader is the modal's title row: what it shows on the left, the window and
// the batch count on the right.
func (m *tuiModel) eventsHeader(batches, innerW int) string {
	left, right := eventsTitle, "last 1m · "+batchCount(batches)
	gap := strings.Repeat(" ", max(1, innerW-displayWidth(left)-displayWidth(right)))
	return m.styles.title.Render(left) + gap + m.styles.hint.Render(right)
}

// batchCount names a batch count, so the header never reads `1 batches`.
func batchCount(n int) string {
	if n == 1 {
		return "1 batch"
	}
	return fmt.Sprintf("%d batches", n)
}

// eventsLine draws one body row, in the drawer's own vocabulary: an absolute time,
// the batch's ops, its first file, the targets it restarted, or the error text.
func (m *tuiModel) eventsLine(w eventsRow, innerW int) string {
	base := m.styles.base
	if w.sel {
		base = m.styles.cursor
	} else if w.dim {
		base = m.styles.muted
	}
	switch {
	case w.dir:
		return renderSegs([]seg{{text: w.label}}, m.styles.all, innerW)
	case w.file:
		return renderSegs([]seg{
			{text: strings.Repeat(" ", w.indent)},
			{text: w.glyph, color: opColor(w.glyph)},
			{text: " " + w.label},
		}, base, innerW)
	}

	b := w.batch
	at := b.at.Local().Format("15:04:05")
	if b.err != nil {
		return renderSegs([]seg{
			{text: at, faint: true},
			{text: "  "},
			{text: printable(b.err.Error(), false), color: colorRed},
		}, base, innerW)
	}
	label := eventsBatchLabel(b)
	segs := []seg{
		{text: at, faint: true},
		{text: "  " + eventsOps(b)},
		{text: "  " + label},
	}
	if arrow := targetsArrow(b.targets, innerW, displayWidth(label)); arrow != "" {
		segs = append(segs, seg{text: arrow, color: colorGray})
	}
	return renderSegs(segs, base, innerW)
}

// eventsOps is a batch's op summary, `+2 ~1 −3`, in glyph order.
func eventsOps(b *feedBatch) string {
	var parts []string
	for _, glyph := range []byte{'+', '~', '-'} {
		n := 0
		for _, op := range b.files {
			if op == glyph {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, opText(glyph)+strconv.Itoa(n))
		}
	}
	return strings.Join(parts, " ")
}

// eventsBatchLabel is a batch's first file in sort order, plus its `+N` remainder.
func eventsBatchLabel(b *feedBatch) string {
	label := feedFirstFile(*b)
	if n := len(b.files) - 1; n > 0 {
		label += fmt.Sprintf(" +%d", n)
	}
	return label
}
