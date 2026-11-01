package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// lineCap bounds each target's scrollback and the merged ALL stream. Phase B
// swaps this for golang-cz/ringbuf.
const lineCap = 10_000

// maxEventBatch is how many runner events one Update takes in at a time, so a
// chatty target cannot force a redraw per line.
const maxEventBatch = 256

// runTUI runs the targets behind the bubbletea TUI. It returns 0 after a
// graceful quit and 130 when a second signal forced the kill.
func runTUI(targets []Target, signals <-chan os.Signal) int {
	signals, stopSignals := signalChannel(signals)
	defer stopSignals()

	model := newTUI(targets)
	model.group.start()

	program := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithFPS(30), // one frame per uptime tick is plenty; 60fps just redraws
		tea.WithoutSignalHandler(),
	)

	// A signal while the TUI owns the screen quits it gracefully, exactly
	// like q or Ctrl-C.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-signals:
				program.Send(interruptMsg{})
			case <-done:
				return
			}
		}
	}()

	_, err := program.Run()
	close(done)
	close(model.stop)

	// The runners keep reporting while they shut down; keep draining so none
	// of them blocks on a full event channel.
	drained := make(chan struct{})
	go func() {
		for {
			select {
			case <-model.events:
			case <-drained:
				return
			}
		}
	}()
	code := shutdownTargets(model.group, signals)
	close(drained)

	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", err)
		return 1
	}
	return code
}

// procState is the process state a row's glyph shows.
type procState int

const (
	stateOff procState = iota
	stateRunning
	stateExited
	stateStopped
)

// tuiTarget is one row: its process state, its stream state, and its output.
type tuiTarget struct {
	spec   Target
	runner *targetRunner

	state     procState
	exitCode  int
	exitLabel string
	lastStart time.Time

	muted  bool
	unseen bool
	opened bool

	lines []string
}

// tuiModel is the whole screen: the target list on the left, the stream on
// the right.
type tuiModel struct {
	group  *targetGroup
	events <-chan event
	// stop is closed once bubbletea has released the terminal, which retires
	// every pending event reader.
	stop chan struct{}

	targets []*tuiTarget

	cursor int // highlighted row: 0 = ALL, 1..n = target
	help   bool
	// solo is the soloed target index, or -1.
	solo int

	follow  bool
	pending int

	// merged is every line in arrival order; visible is the pane's
	// materialized view of it, which muting and soloing filter.
	merged      []mergedLine
	visible     []string
	mergedDirty bool

	vp      viewport.Model
	vpDirty bool

	width, height int
	now           time.Time
	// hintUntil bounds the ALL-row m/s hint; the 1s tick expires it.
	hintUntil time.Time

	hover       int
	lastClick   int
	lastClickAt time.Time

	layout tuiLayout
	styles tuiStyles
}

// tuiSink hands runner events to the model. A full channel puts backpressure
// on the child's pipe, which is what we want.
type tuiSink chan event

func (s tuiSink) emit(ev event) { s <- ev }

func newTUI(targets []Target) *tuiModel {
	events := make(chan event, 4096)
	group := newTargetGroup(targets, tuiSink(events))

	m := &tuiModel{
		group:     group,
		events:    events,
		stop:      make(chan struct{}),
		solo:      -1,
		follow:    true,
		now:       time.Now(),
		hover:     -1,
		lastClick: -1,
		styles:    newTUIStyles(),
	}
	m.vp = viewport.New(80, 20)

	for i, target := range targets {
		row := &tuiTarget{
			spec:      target,
			runner:    group.runners[i],
			state:     stateRunning,
			exitCode:  -1,
			lastStart: m.now,
		}
		if !target.Selected {
			row.state = stateOff
		}
		m.targets = append(m.targets, row)
	}
	return m
}

// rowOf maps a target name to its row index, or -1.
func (m *tuiModel) rowOf(name string) int {
	for i, t := range m.targets {
		if t.spec.Name == name {
			return i + 1
		}
	}
	return -1
}

// streamsTo reports whether the target's output belongs in the merged ALL
// stream: muted targets are out, and while one target is soloed every other
// target is out.
func (m *tuiModel) streamsTo(i int) bool {
	if m.targets[i].muted {
		return false
	}
	return m.solo < 0 || m.solo == i
}

// button is the row's stream button: blank, M, or S.
func (m *tuiModel) button(i int) string {
	switch {
	case m.solo == i:
		return "S"
	case m.solo >= 0:
		// Solo hides every other row's mute state, which is kept inside.
		return " "
	case m.targets[i].muted:
		return "M"
	}
	return " "
}

func (m *tuiModel) toggleMute(i int) {
	t := m.targets[i]
	if m.solo == i {
		return // muting the solo row is a no-op
	}
	t.muted = !t.muted
	if !t.muted {
		t.unseen = false
	}
	m.mergedDirty = true
	m.vpDirty = true
}

func (m *tuiModel) toggleSolo(i int) {
	if m.solo == i {
		m.clearSolo()
		return
	}
	m.solo = i
	m.targets[i].muted = false
	m.targets[i].unseen = false
	m.mergedDirty = true
	m.vpDirty = true
}

// clearSolo drops the solo filter and re-prefixes the restored stream.
func (m *tuiModel) clearSolo() {
	if m.solo < 0 {
		return
	}
	m.targets[m.solo].unseen = false // the row's own output is back in the pane
	m.solo = -1
	m.mergedDirty = true
	m.vpDirty = true
}

// showTargetHint flags that m/s need a row, not the ALL rollup.
func (m *tuiModel) showTargetHint() {
	m.hintUntil = m.now.Add(hintDuration)
}

func (m *tuiModel) stopTarget(i int) {
	t := m.targets[i]
	t.state = stateStopped
	t.exitLabel = ""
	t.runner.control(ctrlStop)
}

func (m *tuiModel) startTarget(i int) {
	t := m.targets[i]
	t.state = stateRunning
	t.lastStart = m.now
	t.runner.control(ctrlStart)
}

func (m *tuiModel) rebootTarget(i int) {
	t := m.targets[i]
	t.state = stateRunning
	t.lastStart = m.now
	t.exitLabel = ""
	t.runner.control(ctrlReboot)
}

// toggleAll stops every running target, or starts everything that is down.
func (m *tuiModel) toggleAll() {
	anyRunning := false
	for _, t := range m.targets {
		if t.state == stateRunning {
			anyRunning = true
			break
		}
	}
	for i, t := range m.targets {
		switch {
		case anyRunning && t.state == stateRunning:
			m.stopTarget(i)
		case !anyRunning && t.state != stateRunning:
			m.startTarget(i)
		}
	}
}

// rebootAll reboots every target that is selected in rerun.yml.
func (m *tuiModel) rebootAll() {
	for i, t := range m.targets {
		if t.spec.Selected {
			m.rebootTarget(i)
		}
	}
}

func (m *tuiModel) openTarget(i int) {
	t := m.targets[i]
	if t.spec.URL == "" {
		return
	}
	if err := openURL(t.spec.URL); err != nil {
		m.addLine(i, fmt.Sprintf("rerun: open %s: %v", t.spec.URL, err))
	}
}

// addLine appends one line to a target's stream and to the merged view when
// that target streams into it.
func (m *tuiModel) addLine(i int, line string) {
	t := m.targets[i]
	t.lines = appendCapped(t.lines, line)
	m.merged = appendCapped(m.merged, mergedLine{target: i, text: line})
	if m.streamsTo(i) {
		m.visible = appendCapped(m.visible, m.prefixed(i, line))
		if !m.follow {
			m.pending++ // only lines the pane actually holds
		}
	} else {
		m.mergedDirty = true
	}
	m.vpDirty = true
}

// appendCapped keeps the newest cap lines. The slice is re-sliced rather than
// shifted, so a line costs amortized O(1) and the backing array stays bounded.
func appendCapped[T any](lines []T, line T) []T {
	if len(lines) >= lineCap {
		lines = lines[len(lines)-lineCap+1:]
	}
	return append(lines, line)
}

func (m *tuiModel) applyEvents(events []event) {
	for _, ev := range events {
		m.applyEvent(ev)
	}
}

func (m *tuiModel) applyEvent(ev event) {
	row := m.rowOf(ev.target)
	if row <= 0 {
		return
	}
	i := row - 1
	t := m.targets[i]

	switch ev.kind {
	case evOutput:
		m.addLine(i, ev.line)
		// Output nobody can see gets the badge.
		if t.muted {
			t.unseen = true
		}

	case evChanged:
		plural := ""
		if ev.count > 1 {
			plural = "s"
		}
		m.addLine(i, fmt.Sprintf("# %d file%s changed (e.g. %s)", ev.count, plural, ev.first))

	case evStarted:
		t.state = stateRunning
		t.lastStart = ev.at
		t.exitCode = -1
		t.exitLabel = ""
		if t.spec.Open && t.spec.URL != "" && !t.opened {
			t.opened = true // once per session, never on a restart
			if err := openURL(t.spec.URL); err != nil {
				m.addLine(i, fmt.Sprintf("rerun: open %s: %v", t.spec.URL, err))
			}
		}

	case evExited:
		t.state = stateExited
		t.exitCode = ev.code
		t.exitLabel = exitLabel(ev.code, ev.err)
		t.unseen = true
		m.addLine(i, fmt.Sprintf("exited (%s) — waiting for changes", ev.status))

	case evWatcherErr:
		m.addLine(i, fmt.Sprintf("watcher error: %v", ev.err))

	case evRunnerErr:
		// Nothing is running after a spawn failure, so the row leaves the
		// running state the way an exit does.
		t.state = stateExited
		t.exitCode = -1
		t.exitLabel = "exit error"
		t.unseen = true
		m.addLine(i, fmt.Sprintf("rerun: %v", ev.err))

	case evCleared:
		// The TUI owns the screen; the clear bytes belong to the plain path.
	}
}

// paneLines is the merged stream, rebuilt when muting or soloing changed
// what belongs in it.
func (m *tuiModel) paneLines() []string {
	if m.mergedDirty {
		m.rebuildMerged()
	}
	return m.visible
}

// mergedLine is one line of the arrival-ordered log the ALL pane filters.
type mergedLine struct {
	target int
	text   string
}

func (m *tuiModel) prefixed(i int, text string) string {
	if m.solo >= 0 {
		return text // a soloed pane is one target, so the name prefix is noise
	}
	if name := m.targets[i].spec.Name; name != "" {
		return "[" + name + "] " + text
	}
	return text
}

func (m *tuiModel) rebuildMerged() {
	m.visible = m.visible[:0]
	for _, line := range m.merged {
		if !m.streamsTo(line.target) {
			continue
		}
		m.visible = appendCapped(m.visible, m.prefixed(line.target, line.text))
	}
	m.mergedDirty = false
}

// uptime renders a running target's age: 3s, 0m48s, 1m15s, 2h30m, 4d03h.
func uptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Minute:
		return fmt.Sprintf("0m%02ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

// status is the row's second column: uptime while running, what happened after.
func (m *tuiModel) status(t *tuiTarget) string {
	switch t.state {
	case stateRunning:
		return uptime(m.now.Sub(t.lastStart))
	case stateExited:
		return t.exitLabel
	case stateStopped:
		return "stopped"
	}
	return "off"
}

// glyph is the row's process state, backed by shape as well as color.
func (t *tuiTarget) glyph() string {
	switch t.state {
	case stateRunning:
		return "●"
	case stateExited:
		if t.exitCode == 0 {
			return "✓"
		}
		return "✗"
	case stateStopped:
		return "○"
	}
	return "·"
}

// rollupSegs is the ALL row's `3● 1✓ 1✗` count.
func (m *tuiModel) rollupSegs() []seg {
	var running, ok, bad int
	for _, t := range m.targets {
		switch {
		case t.state == stateRunning:
			running++
		case t.state == stateExited && t.exitCode == 0:
			ok++
		case t.state == stateExited:
			bad++
		}
	}

	var segs []seg
	add := func(n int, glyph string, color string) {
		if n == 0 {
			return
		}
		if len(segs) > 0 {
			segs = append(segs, seg{text: " "})
		}
		segs = append(segs, seg{text: fmt.Sprintf("%d", n), color: color}, seg{text: glyph, color: color})
	}
	add(running, "●", colorYellow)
	add(ok, "✓", colorGreen)
	add(bad, "✗", colorRed)

	if len(segs) == 0 {
		return []seg{{text: "—", color: colorGray}}
	}
	return segs
}

// anyUnseen reports whether any row hides unseen output.
func (m *tuiModel) anyUnseen() bool {
	for _, t := range m.targets {
		if t.unseen {
			return true
		}
	}
	return false
}

// osc8 wraps text in an OSC 8 hyperlink, which terminals that support it make
// clickable. Others render the text as-is.
func osc8(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// padRight truncates or pads plain text to exactly w display cells.
func padRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = truncate(s, w)
	for cells := displayWidth(s); cells < w; cells++ {
		s += " "
	}
	return s
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if displayWidth(s) <= w {
		return s
	}
	var b strings.Builder
	cells := 0
	for _, r := range s {
		rw := displayWidth(string(r))
		if cells+rw > w {
			break
		}
		b.WriteRune(r)
		cells += rw
	}
	return b.String()
}
