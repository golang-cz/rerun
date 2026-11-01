package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// doubleClickWindow is how close two row clicks must be to focus the pane.
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
		m.setFocus(0)

	case "up", "k":
		m.moveCursor(-1)

	case "down", "j":
		m.moveCursor(1)

	case "enter":
		m.setFocus(m.cursor)

	case " ":
		if m.cursor == 0 {
			m.toggleAll()
		} else {
			m.toggleTarget(m.cursor - 1)
		}

	case "m":
		if m.cursor > 0 {
			m.toggleMute(m.cursor - 1)
		}

	case "s":
		if m.cursor > 0 {
			m.toggleSolo(m.cursor - 1)
		}

	case "r":
		if m.cursor == 0 {
			m.rebootAll()
		} else {
			m.rebootTarget(m.cursor - 1)
		}

	case "o":
		if m.cursor > 0 {
			m.openTarget(m.cursor - 1)
		}

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
	if m.targets[i].state == stateRunning {
		m.stopTarget(i)
		return
	}
	m.startTarget(i)
}

// scrollUp freezes the tail: new lines collect behind the `↓ N new` marker.
func (m *tuiModel) scrollUp(n int) {
	if m.follow {
		m.follow = false
		m.pending = 0
	}
	m.vp.SetYOffset(m.vp.YOffset - n)
}

// scrollDown resumes following once the pane is back at the bottom.
func (m *tuiModel) scrollDown(n int) {
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

	if msg.Action == tea.MouseActionMotion {
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
		m.follow = true
		m.pending = 0
		m.vp.GotoBottom()
		return m, nil
	}

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
	default:
		if m.lastClick == row && time.Since(m.lastClickAt) < doubleClickWindow {
			m.setFocus(row)
		}
		m.cursor = row
		m.lastClick = row
		m.lastClickAt = time.Now()
	}
	return m, nil
}

// rowAt resolves a screen cell to a row: 0 = ALL, 1..n = target, -1 = none.
func (m *tuiModel) rowAt(x, y int) int {
	if y < 0 || y >= m.height || x < 0 || x >= m.layout.leftW {
		return -1
	}
	row := m.layout.offset + y
	if row > len(m.targets) {
		return -1
	}
	return row
}
