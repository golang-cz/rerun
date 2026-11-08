package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// watchTitle is the modal's header, naming what it shows.
const watchTitle = "what changed"

// watchFooter is the modal's key line.
const watchFooter = "j k move · → files · ← back · esc close"

// watchEmpty is the modal's placeholder while no batch sits in the window.
const watchEmpty = "no changes in the last minute"

// watchState is the `w` what-changed modal: the last minute's change batches read
// over the live frame. cursor and openID name batches, never indexes, so a batch
// that arrives while the modal is open cannot move the cursor off its own row.
type watchState struct {
	open   bool
	cursor int // the batch the cursor sits on
	openID int // the batch whose file list is expanded, 0 for none
	top    int // the first body row drawn
}

// openWatch opens the modal on the newest visible batch.
func (m *tuiModel) openWatch() {
	batches, _ := m.feedVisible()
	id := 0
	if len(batches) > 0 {
		id = batches[0].id
	}
	m.watch = watchState{open: true, cursor: id}
	m.fitWatch()
}

// watchSize is the modal's outer box: the finder's width, 80% of the terminal's height.
func (m *tuiModel) watchSize() (int, int) { return m.finderSize() }

// watchBodyRows is how many body rows fit between the header, the rule and the footer.
func (m *tuiModel) watchBodyRows() int {
	_, outerH := m.watchSize()
	innerH := max(1, outerH-m.styles.box.GetVerticalFrameSize())
	return max(0, innerH-2-1)
}

// watchRow is one modal body row: a batch, or one line of the open batch's file list.
type watchRow struct {
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

// watchCursorID is the cursor's batch id, the newest when its own batch has aged out.
func (m *tuiModel) watchCursorID(batches []feedBatch) int {
	for _, b := range batches {
		if b.id == m.watch.cursor {
			return b.id
		}
	}
	if len(batches) > 0 {
		return batches[0].id
	}
	return 0
}

// watchRows lays out the body: the visible batches newest first, each followed by
// its file list when it is the open one. The file rows come from the drawer's own
// feedFileRows, so both list a batch's files the same way.
func (m *tuiModel) watchRows(batches []feedBatch, dim bool) []watchRow {
	cur := m.watchCursorID(batches)
	rows := make([]watchRow, 0, len(batches))
	for i := range batches {
		b := &batches[i]
		rows = append(rows, watchRow{id: b.id, batch: b, sel: b.id == cur, dim: dim})
		if b.id != m.watch.openID {
			continue
		}
		for _, r := range feedFileRows(*b) {
			rows = append(rows, watchRow{
				id: b.id, dir: r.kind == drawerDir, file: r.kind == drawerFile,
				glyph: r.glyph, indent: r.indent, label: r.label,
			})
		}
	}
	return rows
}

// moveWatch moves the cursor between batches, skipping an open batch's file rows.
func (m *tuiModel) moveWatch(delta int) {
	batches, _ := m.feedVisible()
	if len(batches) == 0 {
		return
	}
	i := min(max(batchIndex(batches, m.watchCursorID(batches))+delta, 0), len(batches)-1)
	m.watch.cursor = batches[i].id
}

// expandWatch opens the cursor batch's file list below it.
func (m *tuiModel) expandWatch() {
	batches, _ := m.feedVisible()
	if len(batches) == 0 {
		return
	}
	m.watch.openID = m.watchCursorID(batches)
}

// collapseWatch closes the open file list.
func (m *tuiModel) collapseWatch() { m.watch.openID = 0 }

// handleWatchKey routes keys while the modal is open; esc closes it.
func (m *tuiModel) handleWatchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.watch = watchState{}
	case "j", "down":
		m.moveWatch(1)
	case "k", "up":
		m.moveWatch(-1)
	case "right", "enter":
		m.expandWatch()
	case "left":
		m.collapseWatch()
	}
	m.fitWatch()
	return m, nil
}

// fitWatch clamps the modal's scroll window to its cursor. The handlers call it,
// never the render, so drawing the same state twice changes nothing.
func (m *tuiModel) fitWatch() {
	if !m.watch.open {
		return
	}
	batches, dim := m.feedVisible()
	rows := m.watchRows(batches, dim)
	sel := 0
	for i, r := range rows {
		if r.sel {
			sel = i
			break
		}
	}
	m.watch.top = scrollTop(m.watch.top, len(rows), m.watchBodyRows(), sel)
}

// watchView draws the modal: the batches, the open batch's files, and the key line.
func (m *tuiModel) watchView() string {
	outerW, outerH := m.watchSize()
	innerW := m.modalInnerW(outerW)
	batches, dim := m.feedVisible()
	rows := m.watchRows(batches, dim)
	body := m.watchBodyRows()

	// The body fills its rows, so the footer lands on the box's last inner row.
	lines := make([]string, 0, body)
	if len(rows) == 0 {
		lines = append(lines, m.styles.hint.Render(watchEmpty))
	}
	for i := m.watch.top; i < len(rows) && len(lines) < body; i++ {
		lines = append(lines, m.watchLine(rows[i], innerW))
	}
	for len(lines) < body {
		lines = append(lines, "")
	}
	return m.modalBox(m.watchHeader(len(batches), innerW), watchFooter, lines, outerW, outerH)
}

// watchHeader is the modal's title row: what it shows on the left, the window and
// the batch count on the right.
func (m *tuiModel) watchHeader(batches, innerW int) string {
	left, right := watchTitle, "last 1m · "+batchCount(batches)
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

// watchLine draws one body row, in the drawer's own vocabulary: an absolute time,
// the batch's ops, its first file, the targets it restarted, or the error text.
func (m *tuiModel) watchLine(w watchRow, innerW int) string {
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
	label := watchBatchLabel(b)
	segs := []seg{
		{text: at, faint: true},
		{text: "  " + watchOps(b)},
		{text: "  " + label},
	}
	if arrow := targetsArrow(b.targets, innerW, displayWidth(label)); arrow != "" {
		segs = append(segs, seg{text: arrow, color: colorGray})
	}
	return renderSegs(segs, base, innerW)
}

// watchOps is a batch's op summary, `+2 ~1 −3`, in glyph order.
func watchOps(b *feedBatch) string {
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

// watchBatchLabel is a batch's first file in sort order, plus its `+N` remainder.
func watchBatchLabel(b *feedBatch) string {
	label := feedFirstFile(*b)
	if n := len(b.files) - 1; n > 0 {
		label += fmt.Sprintf(" +%d", n)
	}
	return label
}
