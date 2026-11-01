package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// hintDuration is how long the ALL-row m/s hint stays in the bottom line.
const hintDuration = 3 * time.Second

// doubleClickWindow is how close two row clicks must be to stop/start the row.
const doubleClickWindow = 400 * time.Millisecond

// allRowHint is the one transient message, shown when m/s hit the ALL rollup.
const allRowHint = "m/s need a target row - j/k to select"

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

	if m.help {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?", "esc", "enter", " ":
			m.help = false
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "?":
		m.help = true

	case "esc":
		m.clearSolo()

	case "up", "k":
		m.moveCursor(-1)

	case "down", "j":
		m.moveCursor(1)

	case "enter":
		if m.cursor == 0 {
			m.clearSolo()
		} else {
			m.toggleSolo(m.cursor - 1)
		}

	case " ":
		if m.cursor == 0 {
			m.toggleAll()
		} else {
			m.toggleTarget(m.cursor - 1)
		}

	case "m":
		if m.cursor > 0 {
			m.toggleMute(m.cursor - 1)
		} else {
			m.showTargetHint()
		}

	case "s":
		if m.cursor > 0 {
			m.toggleSolo(m.cursor - 1)
		} else {
			m.clearSolo()
			m.showTargetHint()
		}

	case "r":
		if m.cursor == 0 {
			m.rebootAll()
		} else {
			m.rebootTarget(m.cursor - 1)
		}

	case "t":
		if m.cursor > 0 {
			m.toggleTree(m.cursor - 1)
		}

	case "o":
		if m.cursor > 0 {
			m.openTarget(m.cursor - 1)
		}

	case "f":
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

func (m *tuiModel) moveCursor(delta int) {
	m.cursor = min(max(m.cursor+delta, 0), len(m.targets))
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
	if m.help {
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

	row := m.rowAt(msg.X, msg.Y)
	if row < 0 {
		return m, nil
	}

	switch {
	case msg.Shift:
		if row > 0 {
			m.toggleSolo(row - 1)
		}
	case row > 0 && msg.X == m.layout.leftW-1:
		m.toggleMute(row - 1)
	case row > 0 && msg.X == treeX(m.layout.leftW) && len(m.targets[row-1].changeRows) > 0:
		m.toggleTree(row - 1)
	default:
		m.cursor = row
		// The pair's first click already soloed the row; the second click
		// stops/starts it, and the solo stays.
		double := row > 0 && m.lastClick == row && time.Since(m.lastClickAt) < doubleClickWindow
		if double {
			m.toggleTarget(row - 1)
			m.lastClick = -1 // a fast third click must not read as another double
		} else {
			if row > 0 {
				m.toggleSolo(row - 1)
			} else {
				m.clearSolo()
			}
			m.lastClick = row
		}
		m.lastClickAt = time.Now()
	}
	return m, nil
}

// clearSelection drops the frozen selection highlight.
func (m *tuiModel) clearSelection() { m.selAnchor = nil }

// titleHeight is 1 when the cursor row's title takes the pane's first line.
func (m *tuiModel) titleHeight() int {
	if m.cursor > 0 {
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
	if idx < 0 || idx >= len(m.visibleRaw) {
		return -1
	}
	return idx
}

// paneRowAt clamps any pointer row into the pane's content, so a drag that
// leaves the viewport still selects the nearest line. The marker row is not
// content: it overwrites the line behind it, so it is not selectable either.
func (m *tuiModel) paneRowAt(y int) int {
	if len(m.visibleRaw) == 0 {
		return -1
	}
	last := m.vp.Height - 1
	if !m.follow && m.pending > 0 {
		last-- // the `↓ N new` marker owns this row
	}
	last = max(last, 0)
	row := min(max(y-m.titleHeight(), 0), last)
	idx := m.vp.YOffset + row
	return min(max(idx, 0), len(m.visibleRaw)-1)
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
	hi = min(hi, len(m.visibleRaw)-1)
	if lo > hi {
		return
	}

	payload, n, truncated := copyPayload(m.visibleRaw[lo : hi+1])
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

// rowAt resolves a screen cell to a row: 0 = ALL, 1..n = target, -1 = none. A
// change-tree line resolves to the target row it hangs under, so clicks and
// hovers never land on a file.
func (m *tuiModel) rowAt(x, y int) int {
	if y < 0 || y >= m.height || x < 0 || x >= m.layout.leftW {
		return -1
	}
	rows := m.visualRows()
	idx := m.layout.offset + y
	if idx < 0 || idx >= len(rows) {
		return -1
	}
	return rows[idx].target + 1
}
