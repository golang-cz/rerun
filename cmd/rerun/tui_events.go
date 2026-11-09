package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// eventsTitle is the modal's header, naming what it shows.
const eventsTitle = "fs events"

// The footer names only the arrow the cursor batch can use right now.
const (
	eventsFooterExpand   = "j k move · → expand · c copy all · esc close"
	eventsFooterCollapse = "j k move · ← collapse · c copy all · esc close"
)

// eventsLabelCol is where a batch row's label starts: the time and two spaces.
const eventsLabelCol = 10

// eventsServicesCapPct keeps a long path from pushing the services off the modal.
const eventsServicesCapPct = 60

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
	// copiedN and copiedUntil show the `c` confirmation in the footer for 2s.
	copiedN     int
	copiedUntil time.Time
}

// openEvents opens the modal on the newest visible batch.
func (m *tuiModel) openEvents() {
	batches, _ := m.feedVisible()
	merged := mergeEventsBatches(batches)
	id := 0
	if len(merged) > 0 {
		id = merged[0].id
	}
	m.eventsModal = eventsState{open: true, cursor: id}
	m.fitEvents()
}

// eventsBodyRows is how many body rows fit between the header, the rule and the footer.
func (m *tuiModel) eventsBodyRows() int {
	_, outerH := m.modalSize()
	innerH := max(1, outerH-m.styles.box.GetVerticalFrameSize())
	return max(0, innerH-2-1)
}

// eventsRow is one modal body row: a batch, one line of the open batch's file
// list, or a wrapped services continuation under a file row.
type eventsRow struct {
	id     int
	batch  *feedBatch // nil on a file-list or continuation row
	dir    bool
	file   bool
	cont   bool
	glyph  string
	indent int
	label  string
	svc    string // the right-aligned services text, "· " included
	svcCol int    // where the services start, shared by every row of this kind
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

// mergeEventsBatches is the modal's batch list: error batches out (they are the
// red line, never rows), and a run of batches over the same file set folded
// into its newest, which keeps the newest time and gains every target.
func mergeEventsBatches(batches []feedBatch) []feedBatch {
	out := make([]feedBatch, 0, len(batches))
	for _, b := range batches {
		if b.err != nil {
			continue
		}
		if n := len(out); n > 0 && sameFiles(out[n-1], b) {
			out[n-1].targets = unionStrings(out[n-1].targets, b.targets)
			svc := make(map[string][]string, len(out[n-1].svc)) // the row's copy, never the feed's map
			for k, names := range out[n-1].svc {
				svc[k] = names
			}
			for k, names := range b.svc {
				svc[k] = unionStrings(svc[k], names)
			}
			out[n-1].svc = svc
			continue
		}
		out = append(out, b)
	}
	return out
}

// sameFiles reports whether two batches hold the same set of changed paths.
func sameFiles(x, y feedBatch) bool {
	if len(x.files) != len(y.files) {
		return false
	}
	for p := range x.files {
		if _, ok := y.files[p]; !ok {
			return false
		}
	}
	return true
}

// unionStrings appends the strings of b that a does not already hold.
func unionStrings(a, b []string) []string {
	for _, s := range b {
		if !slices.Contains(a, s) {
			a = append(a, s)
		}
	}
	return a
}

// latestWatchErr is the newest visible batch's watcher error, nil when none failed.
func latestWatchErr(batches []feedBatch) error {
	for _, b := range batches {
		if b.err != nil {
			return b.err
		}
	}
	return nil
}

// eventsRows lays out the body: the visible batches newest first, each followed by
// its file list when it is the open one. innerW decides where a file row's
// services wrap, since every continuation is a row of its own.
func (m *tuiModel) eventsRows(batches []feedBatch, dim bool, innerW int) []eventsRow {
	merged := mergeEventsBatches(batches)
	cur := m.eventsCursorID(merged)
	capCol := innerW * eventsServicesCapPct / 100
	batchCol := eventsBatchServicesCol(merged, capCol)
	rows := make([]eventsRow, 0, len(merged))
	for i := range merged {
		b := &merged[i]
		row := eventsRow{id: b.id, batch: b, sel: b.id == cur, dim: dim, svcCol: batchCol}
		if names := fitServices(b.targets, innerW-batchCol-2); names != "" {
			row.svc = "↻ " + names
		}
		rows = append(rows, row)
		if b.id != m.eventsModal.openID {
			continue
		}
		fileRows := feedFileRows(*b)
		fileCol := eventsFilesServicesCol(fileRows, capCol)
		for _, r := range fileRows {
			if r.dir {
				rows = append(rows, eventsRow{id: b.id, dir: true, label: r.label, dim: dim})
				continue
			}
			svcs := m.fileServices(b, r.path)
			if len(svcs) == 0 {
				label := ellipsizeMiddle(r.label, max(1, innerW-r.indent-2))
				rows = append(rows, eventsRow{id: b.id, file: true, glyph: r.glyph, indent: r.indent, label: label, dim: dim})
				continue
			}
			// The path takes a middle ellipsis so the services keep their column.
			label := ellipsizeMiddle(r.label, max(1, fileCol-r.indent-4))
			first, rest := packServices(svcs, max(1, innerW-fileCol-2))
			rows = append(rows, eventsRow{id: b.id, file: true, glyph: r.glyph, indent: r.indent, label: label,
				svc: "↻ " + first, svcCol: fileCol, dim: dim})
			for len(rest) > 0 {
				var chunk string
				chunk, rest = packServices(rest, max(1, innerW-fileCol))
				rows = append(rows, eventsRow{id: b.id, cont: true, svc: chunk, svcCol: fileCol, dim: dim})
			}
		}
	}
	return rows
}

// eventsBatchServicesCol is where the batch rows' services start: one gap after
// the longest batch label, never past the cap.
func eventsBatchServicesCol(merged []feedBatch, capCol int) int {
	longest := 0
	for i := range merged {
		longest = max(longest, displayWidth(eventsBatchLabel(&merged[i])))
	}
	return min(eventsLabelCol+longest+2, capCol)
}

// eventsFilesServicesCol is where an expanded batch's file rows put their services:
// one gap after the longest path, never past the cap.
func eventsFilesServicesCol(rows []feedFile, capCol int) int {
	longest := 0
	for _, r := range rows {
		if !r.dir {
			longest = max(longest, r.indent+2+displayWidth(r.label))
		}
	}
	return min(longest+2, capCol)
}

// packServices takes as many leading names as fit the budget, at least one.
func packServices(names []string, budget int) (string, []string) {
	w, i := 0, 0
	for i < len(names) {
		add := displayWidth(names[i])
		if i > 0 {
			add += 2 // the ", " join
		}
		if i > 0 && w+add > budget {
			break
		}
		w += add
		i++
	}
	return strings.Join(names[:i], ", "), names[i:]
}

// fitServices shortens a service list to `a, b +N more` until it fits the budget.
func fitServices(names []string, budget int) string {
	if len(names) == 0 || budget < 1 {
		return ""
	}
	full := strings.Join(names, ", ")
	if displayWidth(full) <= budget {
		return full
	}
	for k := len(names) - 1; k >= 1; k-- {
		cand := strings.Join(names[:k], ", ") + fmt.Sprintf(" +%d more", len(names)-k)
		if displayWidth(cand) <= budget {
			return cand
		}
	}
	if cand := fmt.Sprintf("+%d more", len(names)); displayWidth(cand) <= budget {
		return cand
	}
	return ellipsize(full, budget)
}

// fileServices is the file's recorded owner targets, the targets whose
// watcher delivered the change. An exited target gets a ✗ after its name.
func (m *tuiModel) fileServices(b *feedBatch, key string) []string {
	var out []string
	for _, name := range b.svc[key] {
		if name != configTarget {
			if i := m.targetIndex(name); i >= 0 && m.targets[i].state == stateExited {
				name += " ✗"
			}
		}
		out = append(out, name)
	}
	return out
}

// moveEvents moves the cursor between batches, skipping an open batch's file rows.
func (m *tuiModel) moveEvents(delta int) {
	batches, _ := m.feedVisible()
	merged := mergeEventsBatches(batches)
	if len(merged) == 0 {
		return
	}
	i := min(max(batchIndex(merged, m.eventsCursorID(merged))+delta, 0), len(merged)-1)
	m.eventsModal.cursor = merged[i].id
}

// expandEvents opens the cursor batch's file list below it.
func (m *tuiModel) expandEvents() {
	batches, _ := m.feedVisible()
	merged := mergeEventsBatches(batches)
	if len(merged) == 0 {
		return
	}
	m.eventsModal.openID = m.eventsCursorID(merged)
}

// collapseEvents closes the open file list.
func (m *tuiModel) collapseEvents() { m.eventsModal.openID = 0 }

// handleEventsKey routes keys while the modal is open; esc closes it.
func (m *tuiModel) handleEventsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+[":
		m.eventsModal = eventsState{}
	case "j", "down":
		m.moveEvents(1)
	case "k", "up":
		m.moveEvents(-1)
	case "right", "enter", "ctrl+m":
		m.expandEvents()
	case "left":
		m.collapseEvents()
	case "c":
		text, n := m.eventsCopyText()
		if text != "" {
			m.eventsModal.copiedN = n
			m.eventsModal.copiedUntil = m.now.Add(2 * time.Second)
			m.fitEvents()
			return m, copyToClipboard(text)
		}
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
	outerW, _ := m.modalSize()
	rows := m.eventsRows(batches, dim, m.modalInnerW(outerW))
	body := m.eventsBodyRows()
	if latestWatchErr(batches) != nil {
		body-- // the red error line owns the body's first row
	}
	sel := 0
	for i, r := range rows {
		if r.sel {
			sel = i
			break
		}
	}
	m.eventsModal.top = scrollTop(m.eventsModal.top, len(rows), body, sel)
}

// eventsView draws the modal: the batches, the open batch's files, and the key line.
func (m *tuiModel) eventsView() string {
	outerW, outerH := m.modalSize()
	innerW := m.modalInnerW(outerW)
	batches, dim := m.feedVisible()
	merged := mergeEventsBatches(batches)
	rows := m.eventsRows(batches, dim, innerW)
	body := m.eventsBodyRows()

	// The body fills its rows, so the footer lands on the box's last inner row.
	lines := make([]string, 0, body)
	if err := latestWatchErr(batches); err != nil {
		lines = append(lines, renderSegs([]seg{
			{text: "⚠ watch failed: " + printable(err.Error(), false), color: colorRed},
		}, m.styles.base, innerW))
		body--
	}
	if len(rows) == 0 && len(lines) == 0 {
		lines = append(lines, m.styles.hint.Render(eventsEmpty))
	}
	for i := m.eventsModal.top; i < len(rows) && len(lines) < body; i++ {
		lines = append(lines, m.eventsLine(rows[i], innerW))
	}
	for len(lines) < body {
		lines = append(lines, "")
	}
	footer := eventsFooterExpand
	if m.eventsModal.openID != 0 && m.eventsModal.openID == m.eventsCursorID(merged) {
		footer = eventsFooterCollapse
	}
	if m.now.Before(m.eventsModal.copiedUntil) {
		footer = "copied " + batchCount(m.eventsModal.copiedN)
	}
	return m.modalBox(m.eventsHeader(len(merged), innerW), footer, lines, outerW, outerH)
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

// eventsLine draws one body row: a batch as `time  path +N` with its services
// right-aligned behind one middle dot, or a file row of the open batch.
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
	case w.cont:
		return renderSegs([]seg{
			{text: strings.Repeat(" ", w.svcCol)},
			{text: w.svc, color: colorGray},
		}, base, innerW)
	case w.file:
		segs := []seg{
			{text: strings.Repeat(" ", w.indent)},
			{text: w.glyph, color: opColor(w.glyph)},
			{text: " " + w.label},
		}
		if w.svc != "" {
			gap := max(2, w.svcCol-(w.indent+2+displayWidth(w.label)))
			segs = append(segs, seg{text: strings.Repeat(" ", gap)}, seg{text: w.svc, color: colorGray})
		}
		return renderSegs(segs, base, innerW)
	}

	b := w.batch
	at := b.at.Local().Format("15:04:05")
	label := eventsBatchLabel(b)
	segs := []seg{{text: at, faint: true}}
	if w.svc == "" {
		segs = append(segs, seg{text: "  " + ellipsizeMiddle(label, max(1, innerW-eventsLabelCol-2))})
		return renderSegs(segs, base, innerW)
	}
	label = ellipsizeMiddle(label, max(1, w.svcCol-eventsLabelCol-2))
	gap := max(2, w.svcCol-eventsLabelCol-displayWidth(label))
	segs = append(segs,
		seg{text: "  " + label},
		seg{text: strings.Repeat(" ", gap)},
		seg{text: w.svc, color: colorGray},
	)
	return renderSegs(segs, base, innerW)
}

// eventsBatchLabel is a batch's first file in sort order, plus its `+N` remainder.
func eventsBatchLabel(b *feedBatch) string {
	label := feedFirstFile(*b)
	if n := len(b.files) - 1; n > 0 {
		label += fmt.Sprintf(" +%d", n)
	}
	return label
}

// eventsCopyText is the `c` payload: every visible batch expanded, one line per
// batch and per file, each naming its services. It returns the batch count too.
func (m *tuiModel) eventsCopyText() (string, int) {
	batches, _ := m.feedVisible()
	merged := mergeEventsBatches(batches)
	var out strings.Builder
	if err := latestWatchErr(batches); err != nil {
		fmt.Fprintf(&out, "⚠ watch failed: %s\n", printable(err.Error(), false))
	}
	for i := range merged {
		b := &merged[i]
		line := b.at.Local().Format("15:04:05") + "  " + eventsBatchLabel(b)
		if len(b.targets) > 0 {
			line += "  · " + strings.Join(b.targets, ", ")
		}
		out.WriteString(line + "\n")
		for _, r := range feedFileRows(*b) {
			if r.dir {
				out.WriteString(r.label + "\n")
				continue
			}
			file := strings.Repeat(" ", r.indent) + r.glyph + " " + r.label
			if svcs := m.fileServices(b, r.path); len(svcs) > 0 {
				file += "  · " + strings.Join(svcs, ", ")
			}
			out.WriteString(file + "\n")
		}
	}
	return out.String(), len(merged)
}
