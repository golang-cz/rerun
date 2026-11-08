package main

import (
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// hintDuration is how long a transient hint stays in the bottom line.
const hintDuration = 3 * time.Second

// noticeDuration is how long an info notice stays; errors wait for a key press.
const noticeDuration = 4 * time.Second

// doubleClickWindow is how close two row clicks must be to stop/start the row.
const doubleClickWindow = 400 * time.Millisecond

type (
	tickMsg      time.Time
	interruptMsg struct{}
	eventsMsg    struct{ events []event }
)

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// waitForEvents reads the next batch of runner events. Batching keeps a
// chatty target from forcing one redraw per line.
func waitForEvents(ch <-chan event, stop <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-ch:
			batch := []event{ev}
			for len(batch) < maxEventBatch {
				select {
				case ev := <-ch:
					batch = append(batch, ev)
				default:
					return eventsMsg{batch}
				}
			}
			return eventsMsg{batch}
		case <-stop:
			return interruptMsg{}
		}
	}
}

func (m *tuiModel) Init() tea.Cmd {
	// The background query is a msg, so the runtime reads the reply while the TUI runs.
	return tea.Batch(tickCmd(), waitForEvents(m.events, m.stop), tea.RequestBackgroundColor)
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clearSelection()
		// Re-slice the pane for the new width and re-clamp paneCol.
		m.vpDirty = true
		// A rule's cut and its expansion depend on the width, so the rows are redone too.
		m.dispDirty = true
		m.fitEvents()
		return m, nil

	case tea.BackgroundColorMsg:
		m.isDark = msg.IsDark()
		m.styles = newTUIStyles(m.isDark)
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		m.fitEvents()
		return m, tickCmd()

	case eventsMsg:
		m.applyEvents(msg.events)
		return m, waitForEvents(m.events, m.stop)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		return m.handlePaste(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case interruptMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m *tuiModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Any key ends a frozen selection; copy feedback survives its 3s window.
	m.clearSelection()

	// The quit confirm is routed first, so enter after q quits instead of closing the modal.
	if m.cfgErr != "" && !m.confirmQuit {
		switch msg.String() {
		case "esc", "ctrl+[", "enter", "ctrl+m":
			m.cfgErr = ""
			return m, nil
		case "ctrl+c":
			m.cfgErr = ""
		case "q":
			if m.inputMode != inputNone {
				return m, nil
			}
			m.cfgErr = ""
		default:
			return m, nil
		}
	}

	// ctrl+k opens the finder from the list, not over the bar, help or quit confirm.
	if msg.String() == "ctrl+k" && !m.finder.open && !m.eventsModal.open && m.inputMode == inputNone && !m.help && !m.confirmQuit {
		m.finder = finderState{open: true}
		return m, nil
	}
	if m.finder.open {
		return m.handleFinderKey(msg)
	}

	// The fs events modal owns every key while it is up, the way the finder does.
	if m.eventsModal.open {
		return m.handleEventsKey(msg)
	}

	// An open bar owns every key but Ctrl-C, so `q`, `?` and `/` are typed
	// into the query instead of running their commands.
	if m.inputMode != inputNone {
		return m.handleInputKey(msg)
	}
	m.dismissErrorNotice()

	if m.help {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			// Quit asks from the frame, so the help overlay closes into the confirm.
			m.help = false
			m.confirmQuit = true
		case "?":
			if m.helpAll {
				m.help = false
			}
			m.helpAll = true
		case "m":
			m.helpAll = true
		case "esc", "ctrl+[", "enter", "ctrl+m", "space":
			m.help = false
		}
		return m, nil
	}

	// A pending quit confirm consumes its own key: enter or a second ctrl+c
	// goes, esc/q dismiss, and every other key cancels then runs as usual.
	if m.confirmQuit {
		m.confirmQuit = false
		switch msg.String() {
		case "enter", "ctrl+m", "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			return m, nil
		}
	}

	// The bar owns its arrows, backspace and esc; any other key blurs it first.
	if m.barFocus {
		if m.handleBarNav(msg) {
			return m, nil
		}
		m.blurBar()
	}

	switch msg.String() {
	case "ctrl+c":
		m.confirmQuit = true

	case "q":
		m.confirmQuit = true

	case "?", "shift+?", "shift+/":
		m.help = true
		m.helpAll = false

	case "/", "v":
		m.openBar(inputFilter)

	case "ctrl+f":
		m.openBar(inputSearch)

	case "ctrl+g":
		m.searchJump(1)

	// The enhanced decoder spells the same key ctrl+shift+g.
	case "ctrl+shift+g":
		m.searchJump(-1)

	case "ctrl+l":
		m.clearKey()

	case "[", "shift+up":
		if m.listFocused() {
			m.jumpGroup(-1)
		}

	case "]", "shift+down":
		if m.listFocused() {
			m.jumpGroup(1)
		}

	case "c":
		m.toggleTable()

	case "n":
		m.searchJump(1)

	case "N", "shift+n":
		m.searchJump(-1)

	case "esc", "ctrl+[":
		switch {
		case m.drawerFocus:
			if m.feedOpened() != nil {
				m.feedOpen = 0
				m.feedTop = 0
			} else {
				m.drawerFocus = false
			}
		case m.paneFocus:
			m.paneFocus = false
		default:
			if !m.follow {
				m.goLive()
				break
			}
			before := m.scopeState()
			m.escList()
			if m.scopeState() == before && len(m.barChips()) > 0 {
				m.showHint("filters stay · backspace removes the last · click × to remove")
			}
		}

	case "up":
		m.stepSelection(-1)

	case "down":
		m.stepSelection(1)

	case "k":
		if m.drawerFocus {
			m.moveDrawer(-1)
		} else {
			m.moveCursor(-1)
		}

	case "j":
		if m.drawerFocus {
			m.moveDrawer(1)
		} else {
			m.moveCursor(1)
		}

	case "tab", "ctrl+i":
		if m.drawerOpen && len(m.feed) > 0 {
			m.setDrawerFocus(!m.drawerFocus)
		}

	case "left":
		if m.paneFocus {
			if m.arrowCollapse() {
				break
			}
			if m.paneCol > 0 {
				m.paneCol--
				m.vpDirty = true
			} else {
				m.paneFocus = false
			}
		} else if m.listFocused() {
			m.treeLeft()
		}

	case "right":
		if !m.paneFocus {
			if m.listFocused() && m.treeRight() {
				break
			}
			m.paneFocus = true
			m.drawerFocus = false
		} else if !m.arrowExpand() {
			m.paneCol++
			m.vpDirty = true
		}

	case "home":
		if m.paneFocus {
			m.vp.SetYOffset(0)
			m.paneCol = 0
			m.follow = false
			m.pending = 0
			m.vpDirty = true
		} else {
			m.cursor = 0
			m.liveSelect()
		}

	case "end":
		if m.paneFocus {
			m.follow = true
			m.pending = 0
			m.vp.GotoBottom()
		} else {
			m.cursor = max(0, m.listLen()-1)
			m.liveSelect()
		}

	case "enter", "ctrl+m":
		switch {
		case m.drawerFocus:
			m.openCursorBatch()
		case m.paneFocus:
			m.toggleCursorExpand()
		default:
			m.enterRow()
		}

	case "a":
		if m.paneFocus {
			m.pickCursorKey()
		}

	case "f":
		if m.paneFocus {
			m.pickCursorValue()
		}

	case "backspace":
		if chips := m.barChips(); m.paneFocus && len(chips) > 0 {
			m.removeBarChip(chips[len(chips)-1])
		}

	case "ctrl+s":
		m.saveColumns()

	case "space":
		// The pane owns the keys, so space jumps to the tail like end.
		if m.paneFocus {
			m.goLive()
			break
		}
		if r, ok := m.cursorRow(); ok {
			m.toggleRow(r)
		}

	case "s":
		m.stopRow()

	case "m":
		if r, ok := m.cursorRow(); ok {
			m.muteRow(r)
		}

	case "r":
		if r, ok := m.cursorRow(); ok {
			m.rebootRow(r)
		}

	case "t":
		m.toggleDrawer()

	case "e":
		m.openEvents()

	case "o":
		if i := m.cursorTarget(); i >= 0 {
			m.openTarget(i)
		}

	case "pgup":
		m.scrollUp(m.pageStep())

	case "pgdown":
		m.scrollDown(m.pageStep())

	case "ctrl+u":
		m.scrollUp(max(1, m.vp.Height()/2))

	case "ctrl+d":
		m.scrollDown(max(1, m.vp.Height()/2))
	}
	return m, nil
}

// handlePaste routes a paste where a keystroke would go; a closed input ignores it.
func (m *tuiModel) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	m.clearSelection()

	if m.cfgErr != "" && !m.confirmQuit {
		return m, nil
	}

	switch {
	case m.finder.open:
		m.finderPaste(msg.Content)
	case m.inputMode != inputNone:
		m.inputPaste(msg.Content)
	}
	return m, nil
}

// handleInputKey routes keys into the bar's open input; only Ctrl-C escapes it.
func (m *tuiModel) handleInputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+[":
		// Find drops the query; a line filter keeps its typed chip, as before.
		if m.inputMode == inputSearch {
			m.clearSearch()
		}
		m.leaveInput()
	case "ctrl+g":
		m.searchJump(1)
	// Mirror ctrl+g: the enhanced decoder keeps the shift distinct.
	case "ctrl+shift+g":
		m.searchJump(-1)
	case "enter", "ctrl+m":
		if m.inputMode == inputFilter && m.filterErr != "" {
			return m, nil
		}
		m.leaveInput()
	case "left":
		// An empty input hands the arrows to the chips, so they can be picked.
		if chips := m.barChips(); m.input == "" && len(chips) > 0 {
			m.inputMode = inputNone
			m.barSel = len(chips) - 1
		}
	case "backspace":
		if m.input != "" {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
			m.inputChanged()
			break
		}
		if chips := m.barChips(); len(chips) > 0 {
			m.removeBarChip(chips[len(chips)-1])
		}
	default:
		if msg.Text != "" {
			m.inputPaste(msg.Text)
			break
		}
		// v1 handed an alt combo over as key runes, so a printable one typed its letter.
		if msg.Mod.Contains(tea.ModAlt) && unicode.IsPrint(msg.Code) {
			m.inputPaste(string(msg.Code))
		}
	}
	return m, nil
}

// inputPaste types pasted text into the bar, exactly as the keys would.
func (m *tuiModel) inputPaste(s string) {
	if s == "" {
		return
	}
	m.input += s
	m.inputChanged()
}

// openInput shows the bar's input; find starts from its query, a text filter empty.
func (m *tuiModel) openInput(mode int) {
	m.inputMode = mode
	m.filterErr = ""
	m.draft = nil
	if mode == inputFilter {
		m.input = ""
		return
	}
	m.input = m.search
}

// inputChanged previews the typed text live; a yq query that does not parse is left out.
func (m *tuiModel) inputChanged() {
	if m.inputMode == inputFilter {
		m.draft, m.filterErr = nil, ""
		if m.input != "" {
			keep, err := compileFilter(m.input)
			if err != nil {
				m.filterErr = err.Error()
			} else {
				m.draft = keep
			}
		}
		m.chipsChanged()
		return
	}
	m.search = m.input
	m.recomputeSearch(true)
}

// leaveInput turns the typed text into a chip and blurs the bar; empty adds nothing.
func (m *tuiModel) leaveInput() {
	if m.inputMode == inputFilter && m.draft != nil && !m.hasFilter(m.input) {
		m.filters = append(m.filters, textFilter{query: m.input, keep: m.draft})
	}
	m.inputMode = inputNone
	m.input = ""
	m.draft = nil
	m.filterErr = ""
	m.blurBar()
	m.chipsChanged()
}

// hasFilter reports whether a text filter with this query is already a chip.
func (m *tuiModel) hasFilter(query string) bool {
	for _, f := range m.filters {
		if f.query == query {
			return true
		}
	}
	return false
}

// stepSelection is one ↑/↓ step, routed to the focused pane.
func (m *tuiModel) stepSelection(delta int) {
	switch {
	case m.paneFocus:
		m.moveCursorRow(delta)
	case m.drawerFocus:
		m.moveDrawer(delta)
	default:
		m.moveCursor(delta)
	}
}

func (m *tuiModel) moveCursor(delta int) {
	m.cursor = min(max(m.cursor+delta, 0), max(0, m.listLen()-1))
	m.liveSelect()
}

func (m *tuiModel) toggleTarget(i int) {
	if m.targets[i].state.up() {
		m.stopTarget(i)
		return
	}
	m.startTarget(i)
}

// Clears the pending count with the tail, so the ↓ N new marker goes too.
func (m *tuiModel) goLive() {
	m.follow = true
	m.pending = 0
	m.vp.GotoBottom()
}

// scrollUp freezes the tail: new lines collect behind the `↓ N new` marker.
// A gesture that cannot move the window (nothing above it) leaves the tail live.
func (m *tuiModel) scrollUp(n int) {
	m.clearSelection()
	before := m.vp.YOffset()
	m.vp.SetYOffset(before - n)
	if m.vp.YOffset() == before {
		return
	}
	if m.follow {
		m.pauseTail()
	}
}

// pauseTail freezes the pane on its rows, with the cursor left on the newest line.
func (m *tuiModel) pauseTail() {
	if m.follow && m.paneLen() > 0 {
		m.setPaneCursor(m.paneLen() - 1)
	}
	m.follow = false
	m.pending = 0
}

// pageStep is a full pane while it owns the keys, half a pane otherwise.
func (m *tuiModel) pageStep() int {
	if m.paneFocus {
		return max(1, m.vp.Height())
	}
	return max(1, m.vp.Height()/2)
}

// scrollDown resumes following once the pane is back at the bottom.
func (m *tuiModel) scrollDown(n int) {
	m.clearSelection()
	m.vp.SetYOffset(m.vp.YOffset() + n)
	if m.vp.AtBottom() {
		m.follow = true
		m.pending = 0
	}
}

func (m *tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.help || m.confirmQuit || m.cfgErr != "" || m.finder.open || m.eventsModal.open {
		return m, nil
	}
	e := msg.Mouse()

	switch msg.(type) {
	case tea.MouseWheelMsg:
		// The left column scrolls the target window and keeps the selection: clicks pick rows.
		// The divider and the log pane keep scrolling the stream.
		onList := e.X < m.layout.leftW
		switch e.Button {
		case tea.MouseWheelUp:
			if onList {
				m.scrollList(-1)
			} else {
				m.scrollUp(3)
			}
		case tea.MouseWheelDown:
			if onList {
				m.scrollList(1)
			} else {
				m.scrollDown(3)
			}
		}
		return m, nil

	case tea.MouseReleaseMsg:
		// Any release ends the drag; only a left one copies it. Otherwise a
		// right-button release would leave the anchor armed for a stray copy.
		if e.Button == tea.MouseLeft {
			m.copySelection()
		} else {
			m.clearSelection()
		}
		return m, nil

	case tea.MouseMotionMsg:
		// Motion only drags an existing selection: a bare hover never starts one.
		if m.selAnchor != nil {
			m.dragSelection(e.Y)
			return m, nil
		}
		row := m.rowAt(e.X, e.Y)
		// Only a row change alters the frame; wiggles inside one row must not.
		if row != m.hover {
			m.hover = row
		}
		return m, nil
	}

	if _, ok := msg.(tea.MouseClickMsg); !ok || e.Button != tea.MouseLeft {
		return m, nil
	}

	if m.layout.marker.contains(e.X, e.Y) {
		m.clearSelection()
		m.follow = true
		m.pending = 0
		m.vp.GotoBottom()
		return m, nil
	}

	// A press in the log pane anchors a selection instead of acting on rows.
	// The tail freezes on the first drag motion, so a plain click copies the
	// line and keeps the stream live. Past the content, the press does nothing.
	if m.inPaneHeader(e.X, e.Y) {
		m.clickHeader(e.X-m.layout.leftW-1, e.Y)
		return m, nil
	}
	if m.inPane(e.X, e.Y) {
		m.clickPane(e.X, e.Y)
		return m, nil
	}

	m.clearSelection()

	// The drawer never solos a target: a click focuses it and opens a batch's files.
	if e.X < m.layout.leftW {
		if drow, inDrawer := m.inDrawer(e.Y); inDrawer {
			m.clickDrawer(drow)
			return m, nil
		}
	}
	m.drawerFocus = false

	row := m.rowAt(e.X, e.Y)
	if row < 0 {
		return m, nil
	}
	r, _ := m.listRowAt(row)
	// The arrow cell folds a header; its name cell selects it as before.
	if r.header && e.X == r.indent() {
		m.toggleCollapse(row)
		return m, nil
	}

	m.cursor = row
	m.paneFocus = false
	m.liveSelect()
	// The second click of a pair starts or stops the row, keeping the selection.
	double := m.lastClick == row && time.Since(m.lastClickAt) < doubleClickWindow
	if double {
		m.toggleRow(r)
		m.lastClick = -1 // a fast third click must not read as another double
	} else {
		m.lastClick = row
	}
	m.lastClickAt = time.Now()
	return m, nil
}

// clearSelection drops the frozen selection highlight.
func (m *tuiModel) clearSelection() { m.selAnchor = nil }

// titleHeight is the pane's fixed header height, above the viewport.
func (m *tuiModel) titleHeight() int { return m.headerRows() }

// inPane reports whether a screen cell is inside the pane's viewport rows.
func (m *tuiModel) inPane(x, y int) bool {
	if x <= m.layout.leftW || x >= m.width {
		return false
	}
	return y >= m.titleHeight() && y < m.titleHeight()+m.vp.Height()
}

// paneRow resolves a screen cell to a content index in the pane's raw lines,
// or -1 when the cell is outside the pane or past the content.
func (m *tuiModel) paneRow(x, y int) int {
	if !m.inPane(x, y) {
		return -1
	}
	idx := m.vp.YOffset() + y - m.titleHeight()
	if idx < 0 || idx >= m.paneLen() {
		return -1
	}
	return idx
}

// paneRowAt clamps any pointer row into the pane's content, so a drag that
// leaves the viewport still selects the nearest line. The marker row is not
// content: it overwrites the line behind it, so it is not selectable either.
func (m *tuiModel) paneRowAt(y int) int {
	n := m.paneLen()
	if n == 0 {
		return -1
	}
	last := m.vp.Height() - 1
	if !m.follow && m.pending > 0 {
		last-- // the `↓ N new` marker owns this row
	}
	last = max(last, 0)
	row := min(max(y-m.titleHeight(), 0), last)
	idx := m.vp.YOffset() + row
	return min(max(idx, 0), n-1)
}

// dragSelection moves the frozen selection's end to the row under the pointer.
func (m *tuiModel) dragSelection(y int) {
	if m.selAnchor == nil {
		return
	}
	// The drag, not the press, freezes the tail: only a real selection gesture
	// stops the stream.
	if m.follow {
		m.pauseTail()
	}
	if idx := m.paneRowAt(y); idx >= 0 {
		m.selEnd = idx
	}
}

// copySelection sends the frozen selection to the clipboard as one OSC 52
// sequence and clears the highlight. A click without a drag copies one line.
func (m *tuiModel) copySelection() {
	if m.selAnchor == nil {
		return
	}
	anchor := *m.selAnchor
	m.selAnchor = nil

	lo, hi := anchor, m.selEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	lo = max(lo, 0)
	hi = min(hi, m.paneLen()-1)
	if lo > hi {
		return
	}

	raw := m.copyLines(lo, hi)
	if len(raw) == 0 {
		return
	}
	payload, n, truncated := copyPayload(raw)
	if payload == "" {
		// Non-empty selection with nothing copied means one line is over the
		// cap. Say so, and drop the previous sequence so it cannot re-fire.
		m.copySeq = ""
		m.copyHint = "line too long — not copied"
		m.copyUntil = m.now.Add(hintDuration)
		return
	}
	m.copySeq = osc52(payload)
	m.copyHint = copyMessage(n, truncated)
	m.copyUntil = m.now.Add(hintDuration)
}

// rowAt resolves a screen cell to a list row index, or -1 for none.
// The drawer is not target-scoped: no drawer line resolves to a row.
func (m *tuiModel) rowAt(x, y int) int {
	// The last screen row is the version line, never a list row.
	if y < 0 || y >= m.height-1 || x < 0 || x >= m.layout.leftW {
		return -1
	}
	if _, ok := m.inDrawer(y); ok {
		return -1
	}
	idx := m.layout.offset + y
	if idx < 0 || idx >= m.listLen() {
		return -1
	}
	return idx
}
