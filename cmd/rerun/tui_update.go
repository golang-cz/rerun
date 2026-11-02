package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	return tea.Batch(tickCmd(), waitForEvents(m.events, m.stop))
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clearSelection()
		// Re-slice the pane for the new width and re-clamp paneCol.
		m.vpDirty = true
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case eventsMsg:
		m.applyEvents(msg.events)
		return m, waitForEvents(m.events, m.stop)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case interruptMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m *tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key ends a frozen selection; copy feedback survives its 3s window.
	m.clearSelection()
	m.dismissErrorNotice()

	// An open bar owns every key but Ctrl-C, so `q`, `?` and `/` are typed
	// into the query instead of running their commands.
	if m.inputMode != inputNone {
		return m.handleInputKey(msg)
	}

	if m.help {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q", "?", "esc", "enter", " ":
			m.help = false
		}
		return m, nil
	}

	// A pending quit confirm consumes its own key: enter or a second ctrl+c
	// goes, esc/q dismiss, and every other key cancels then runs as usual.
	if m.confirmQuit {
		m.confirmQuit = false
		switch msg.String() {
		case "enter", "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			return m, nil
		}
	}

	switch msg.String() {
	case "ctrl+c":
		m.confirmQuit = true

	case "q":
		m.confirmQuit = true

	case "?":
		m.help = true

	case "/":
		m.openInput(inputSearch)

	case "v":
		m.openInput(inputFilter)

	case "c":
		m.toggleTable()

	case "n":
		m.searchJump(1)

	case "N":
		m.searchJump(-1)

	case "esc":
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
		case m.search != "":
			m.clearSearch()
		case m.filter != "":
			m.clearFilter()
		default:
			m.clearScope()
		}

	case "up":
		switch {
		case m.paneFocus:
			m.scrollUp(1)
		case m.drawerFocus:
			m.moveDrawer(-1)
		default:
			m.moveCursor(-1)
		}

	case "down":
		switch {
		case m.paneFocus:
			m.scrollDown(1)
		case m.drawerFocus:
			m.moveDrawer(1)
		default:
			m.moveCursor(1)
		}

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

	case "tab":
		if m.drawerOpen && len(m.feed) > 0 {
			m.setDrawerFocus(!m.drawerFocus)
		}

	case "left":
		if m.paneFocus {
			if m.paneCol > 0 {
				m.paneCol--
				m.vpDirty = true
			} else {
				m.paneFocus = false
			}
		}

	case "right":
		if !m.paneFocus {
			m.paneFocus = true
			m.drawerFocus = false
		} else {
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
		}

	case "end":
		if m.paneFocus {
			m.follow = true
			m.pending = 0
			m.vp.GotoBottom()
		} else {
			m.cursor = max(0, m.listLen()-1)
		}

	case "enter":
		if m.drawerFocus {
			m.openCursorBatch()
		} else if r, ok := m.cursorRow(); ok {
			m.selectRow(r)
		}

	case " ":
		if r, ok := m.cursorRow(); ok {
			m.toggleRow(r)
		}

	case "m":
		if r, ok := m.cursorRow(); ok {
			m.muteRow(r)
		}

	case "s":
		if r, ok := m.cursorRow(); ok {
			m.selectRow(r)
		}

	case "r":
		if r, ok := m.cursorRow(); ok {
			m.rebootRow(r)
		}

	case "t":
		m.toggleDrawer()

	case "o":
		if i := m.cursorTarget(); i >= 0 {
			m.openTarget(i)
		}

	case "p":
		m.toggleFreeze()

	case "G":
		m.follow = true
		m.pending = 0
		m.vp.GotoBottom()

	case "pgup", "ctrl+u":
		m.scrollUp(max(1, m.vp.Height/2))

	case "pgdown", "ctrl+d":
		m.scrollDown(max(1, m.vp.Height/2))
	}
	return m, nil
}

// handleInputKey routes keys into the open bottom-line bar. Only Ctrl-C
// escapes it; esc cancels the whole query, enter commits it.
func (m *tuiModel) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.cancelInput()
	case "enter":
		m.commitInput()
	case "backspace":
		if m.input != "" {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
			m.inputChanged()
		}
	default:
		switch msg.Type {
		case tea.KeyRunes:
			m.input += string(msg.Runes)
			m.inputChanged()
		case tea.KeySpace:
			m.input += " "
			m.inputChanged()
		}
	}
	return m, nil
}

// openInput shows a bar, seeded with the query already in force so `/` and `v`
// reopen for editing rather than starting over. The committed filter is
// stashed so an edit can be cancelled without destroying it.
func (m *tuiModel) openInput(mode int) {
	m.inputMode = mode
	if mode == inputFilter {
		m.input = m.filter
		m.filterStash = m.filter
		return
	}
	m.input = m.search
}

// inputChanged applies the in-progress text live to the pane. A yq expression
// that does not parse is reported and suspends the filter instead of matching
// nothing, so a typo does not blank the pane.
func (m *tuiModel) inputChanged() {
	if m.inputMode == inputFilter {
		m.filter = m.input
		m.filterErr = filterParseError(m.filter)
		m.paneSelDirty = true
		m.vpDirty = true
		m.recomputeSearch(true)
		return
	}
	m.search = m.input
	m.recomputeSearch(true)
}

// filterParseError reports why a yq query does not compile, or "" when the
// query is a plain substring or a valid expression.
func filterParseError(query string) string {
	if !strings.HasPrefix(query, ".") {
		return ""
	}
	if _, err := yqParse(query); err != nil {
		return err.Error()
	}
	return ""
}

// commitInput closes the bar, keeping a non-empty query live. A filter with a
// parse error keeps the bar open so the error stays visible.
func (m *tuiModel) commitInput() {
	mode := m.inputMode
	if mode == inputFilter && m.filterErr != "" {
		return
	}
	m.inputMode = inputNone
	m.input = ""
	if mode == inputFilter {
		if m.filter == "" {
			m.clearFilter()
		}
		return
	}
	if m.search == "" {
		m.clearSearch()
	}
}

// cancelInput closes the bar. A filter edit reverts to the committed filter;
// a search is dropped entirely.
func (m *tuiModel) cancelInput() {
	mode := m.inputMode
	m.inputMode = inputNone
	m.input = ""
	if mode == inputFilter {
		m.filter = m.filterStash
		m.filterErr = ""
		m.paneSelDirty = true
		m.vpDirty = true
		m.recomputeSearch(true)
		return
	}
	m.clearSearch()
}

func (m *tuiModel) moveCursor(delta int) {
	m.cursor = min(max(m.cursor+delta, 0), max(0, m.listLen()-1))
}

func (m *tuiModel) toggleTarget(i int) {
	if m.targets[i].state.up() {
		m.stopTarget(i)
		return
	}
	m.startTarget(i)
}

// toggleFreeze stops the pane where it is, or resumes the live tail.
func (m *tuiModel) toggleFreeze() {
	if m.follow {
		m.follow = false
		m.pending = 0
		return
	}
	m.vp.GotoBottom()
	m.follow = true
	m.pending = 0
}

// scrollUp freezes the tail: new lines collect behind the `↓ N new` marker.
// A gesture that cannot move the window (nothing above it) leaves the tail live.
func (m *tuiModel) scrollUp(n int) {
	m.clearSelection()
	before := m.vp.YOffset
	m.vp.SetYOffset(before - n)
	if m.vp.YOffset == before {
		return
	}
	if m.follow {
		m.follow = false
		m.pending = 0
	}
}

// scrollDown resumes following once the pane is back at the bottom.
func (m *tuiModel) scrollDown(n int) {
	m.clearSelection()
	m.vp.SetYOffset(m.vp.YOffset + n)
	if m.vp.AtBottom() {
		m.follow = true
		m.pending = 0
	}
}

func (m *tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.help || m.confirmQuit {
		return m, nil
	}

	if msg.Action == tea.MouseActionPress || msg.Action == tea.MouseActionMotion {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scrollUp(3)
			return m, nil
		case tea.MouseButtonWheelDown:
			m.scrollDown(3)
			return m, nil
		}
	}

	if msg.Action == tea.MouseActionRelease {
		// Any release ends the drag; only a left one copies it. Otherwise a
		// right-button release would leave the anchor armed for a stray copy.
		if msg.Button == tea.MouseButtonLeft {
			m.copySelection()
		} else {
			m.clearSelection()
		}
		return m, nil
	}

	if msg.Action == tea.MouseActionMotion {
		// Motion only drags an existing selection: a bare hover never starts one.
		if m.selAnchor != nil {
			m.dragSelection(msg.Y)
			return m, nil
		}
		row := m.rowAt(msg.X, msg.Y)
		// Only a row change alters the frame; wiggles inside one row must not.
		if row != m.hover {
			m.hover = row
		}
		return m, nil
	}

	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	if m.layout.marker.contains(msg.X, msg.Y) {
		m.clearSelection()
		m.follow = true
		m.pending = 0
		m.vp.GotoBottom()
		return m, nil
	}

	// A press in the log pane anchors a selection instead of acting on rows.
	// The tail freezes on the first drag motion, so a plain click copies the
	// line and keeps the stream live. Past the content, the press does nothing.
	if m.inPane(msg.X, msg.Y) {
		if idx := m.paneRow(msg.X, msg.Y); idx >= 0 {
			anchor := idx
			m.selAnchor = &anchor
			m.selEnd = idx
		}
		return m, nil
	}

	m.clearSelection()

	// The drawer never solos a target: a click focuses it and opens a batch's files.
	if msg.X < m.layout.leftW {
		if drow, inDrawer := m.inDrawer(msg.Y); inDrawer {
			m.clickDrawer(drow)
			return m, nil
		}
	}
	m.drawerFocus = false

	row := m.rowAt(msg.X, msg.Y)
	if row < 0 {
		return m, nil
	}
	r, _ := m.listRowAt(row)

	switch {
	case msg.Shift:
		m.selectRow(r)
	case msg.X == m.layout.leftW-1:
		m.muteRow(r)
	default:
		m.cursor = row
		// The second click of a pair starts or stops the row, keeping the selection.
		double := m.lastClick == row && time.Since(m.lastClickAt) < doubleClickWindow
		if double {
			m.toggleRow(r)
			m.lastClick = -1 // a fast third click must not read as another double
		} else {
			m.selectRow(r)
			m.lastClick = row
		}
		m.lastClickAt = time.Now()
	}
	return m, nil
}

// clearSelection drops the frozen selection highlight.
func (m *tuiModel) clearSelection() { m.selAnchor = nil }

func (m *tuiModel) titleHeight() int {
	if m.cursorTarget() >= 0 {
		return 1
	}
	return 0
}

// inPane reports whether a screen cell is inside the pane's viewport rows.
func (m *tuiModel) inPane(x, y int) bool {
	if x <= m.layout.leftW || x >= m.width {
		return false
	}
	return y >= m.titleHeight() && y < m.titleHeight()+m.vp.Height
}

// paneRow resolves a screen cell to a content index in the pane's raw lines,
// or -1 when the cell is outside the pane or past the content.
func (m *tuiModel) paneRow(x, y int) int {
	if !m.inPane(x, y) {
		return -1
	}
	idx := m.vp.YOffset + y - m.titleHeight()
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
	last := m.vp.Height - 1
	if !m.follow && m.pending > 0 {
		last-- // the `↓ N new` marker owns this row
	}
	last = max(last, 0)
	row := min(max(y-m.titleHeight(), 0), last)
	idx := m.vp.YOffset + row
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
		m.follow = false
		m.pending = 0
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

	raw := make([]string, 0, hi-lo+1)
	for row := lo; row <= hi; row++ {
		raw = append(raw, m.visibleRaw[m.paneRowRaw(row)])
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
