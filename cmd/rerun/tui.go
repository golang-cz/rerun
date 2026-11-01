package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/golang-cz/rerun"
)

// lineCap bounds each target's scrollback and the merged ALL stream. Phase B
// swaps this for golang-cz/ringbuf.
const lineCap = 10_000

// maxEventBatch is how many runner events one Update takes in at a time, so a
// chatty target cannot force a redraw per line.
const maxEventBatch = 256

// runTUI runs the targets behind the bubbletea TUI. configPath is the
// rerun.yml the run reloads itself from; "" turns the config watch off. It
// returns 0 after a graceful quit and 130 when a second signal forced the kill.
func runTUI(targets []Target, configPath string, signals <-chan os.Signal) int {
	signals, stopSignals := signalChannel(signals)
	defer stopSignals()

	model := newTUI(targets)
	model.group.configPath = configPath
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
	stateStarting
	stateBackoff
	stateExited
	stateStopped
)

// up reports whether a state means the target is running, getting ready to
// run, or waiting out a restart backoff.
func (s procState) up() bool {
	return s == stateRunning || s == stateStarting || s == stateBackoff
}

// tuiTarget is one row: its process state, its stream state, and its output.
type tuiTarget struct {
	spec   Target
	runner *targetRunner

	state     procState
	exitCode  int
	exitLabel string
	lastStart time.Time
	// backoffUntil is when a backoff-delayed start may run.
	backoffUntil time.Time

	muted  bool
	unseen bool
	opened bool

	// lastChange is the newest change batch the target reacted to; changeRows
	// is that set as the tree's rows, sorted once here rather than per frame.
	lastChange rerun.ChangeSet
	changeRows []changeRow

	// treePinned and treeOpen are the manual override of the change tree: the
	// cursor's own row expands on its own, a pinned row keeps its own state.
	treePinned bool
	treeOpen   bool

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

	// copySeq is the last copy's OSC 52 sequence and copyHint its message.
	// Both render only while m.now is before copyUntil, which is kept apart
	// from hintUntil so the m/s hint cannot resurrect a stale copy.
	copySeq   string
	copyHint  string
	copyUntil time.Time

	// selAnchor is the content index a frozen drag started on; nil means no
	// selection. selEnd is the drag row's current content index.
	selAnchor *int
	selEnd    int

	// merged is every line in arrival order; visible is the pane's
	// materialized view of it, which muting and soloing filter.
	merged      []mergedLine
	visible     []string
	mergedDirty bool

	// visibleRaw mirrors visible without target prefixes, so a copy yields the
	// target's own text. len(visibleRaw) always equals len(visible).
	visibleRaw []string

	// prefixW is the pane prefix column: the longest target name, so every
	// stream line starts its message in the same column.
	prefixW int

	vp      viewport.Model
	vpDirty bool

	width, height int
	now           time.Time
	// hintUntil bounds the ALL-row m/s hint; the 1s tick expires it.
	hintUntil time.Time

	// lastClick and lastClickAt track the previous row click, so a second
	// click within doubleClickWindow reads as a double-click.
	lastClick   int
	lastClickAt time.Time

	hover int

	layout tuiLayout
	styles tuiStyles
}

// tuiSink hands runner events to the model. A full channel puts backpressure
// on the child's pipe, which is what we want.
type tuiSink chan event

func (s tuiSink) emit(ev event) { s <- ev }

func newTUI(targets []Target) *tuiModel {
	events := make(chan event, 4096)
	group := newTargetGroup(targets, tuiSink(events), "")

	m := &tuiModel{
		group:     group,
		events:    events,
		stop:      make(chan struct{}),
		solo:      -1,
		follow:    true,
		now:       time.Now(),
		lastClick: -1,
		hover:     -1,
		styles:    newTUIStyles(),
		// Kept non-nil: copy code walks this list in parallel with visible.
		visibleRaw: []string{},
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
	for _, t := range m.targets {
		m.prefixW = max(m.prefixW, displayWidth(t.spec.Name))
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
// target is out. rerun's own lines and lines whose row is gone always show.
func (m *tuiModel) streamsTo(i int) bool {
	if i < 0 || i >= len(m.targets) {
		return true
	}
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
	m.refreshUnseen()
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
	m.refreshUnseen()
	m.group.setSoloGuard(true)
	m.mergedDirty = true
	m.vpDirty = true
}

// clearSolo drops the solo filter and restores the other rows' lines.
func (m *tuiModel) clearSolo() {
	if m.solo < 0 {
		return
	}
	m.solo = -1
	m.refreshUnseen() // the hidden rows' lines are back in the pane
	m.group.setSoloGuard(false)
	m.mergedDirty = true
	m.vpDirty = true
}

// refreshUnseen drops the badge from every row whose stream is in the pane
// again. Muted and solo-hidden rows keep theirs: their lines are still out.
func (m *tuiModel) refreshUnseen() {
	for i, t := range m.targets {
		if m.streamsTo(i) {
			t.unseen = false
		}
	}
}

// treeExpanded reports whether a row's change tree is shown. The cursor's row
// opens on its own; a manual toggle pins a row either way.
func (m *tuiModel) treeExpanded(i int) bool {
	t := m.targets[i]
	if t.treePinned {
		return t.treeOpen
	}
	return m.cursor == i+1
}

// toggleTree pins a row's change tree to the opposite of what it shows now.
func (m *tuiModel) toggleTree(i int) {
	t := m.targets[i]
	was := m.treeExpanded(i)
	t.treePinned = true
	t.treeOpen = !was
}

// treeCell is the row's change-tree indicator: `−` while its tree is open, `+`
// while a retained change set stays collapsed, and blank without one.
func (m *tuiModel) treeCell(i int) string {
	if len(m.targets[i].changeRows) == 0 {
		return " "
	}
	if m.treeExpanded(i) {
		return "−"
	}
	return "+"
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
		if t.state.up() {
			anyRunning = true
			break
		}
	}
	for i, t := range m.targets {
		switch {
		case anyRunning && t.state.up():
			m.stopTarget(i)
		case !anyRunning && !t.state.up():
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
	m.merged = appendCapped(m.merged, mergedLine{target: i, name: t.spec.Name, text: line})
	if m.streamsTo(i) {
		m.visible = appendCapped(m.visible, m.prefixed(mergedLine{target: i, name: t.spec.Name, text: line}))
		m.visibleRaw = appendCapped(m.visibleRaw, line)
		if !m.follow {
			m.pending++ // only lines the pane actually holds
		}
	} else {
		// Output the pane is not showing: muted, or hidden by another solo.
		t.unseen = true
		m.mergedDirty = true
	}
	m.vpDirty = true
}

// addNotice appends one of rerun's own lines to the pane: a config banner, not
// a target's output. The mute and solo filters never hide it.
func (m *tuiModel) addNotice(line string) {
	if line == "" {
		return
	}
	// A banner owns one pane row: past the width it would wrap and break the
	// frame's grid.
	if w := m.paneWidth(); w > 0 {
		line = truncate(line, w)
	}
	m.merged = appendCapped(m.merged, mergedLine{target: -1, text: line})
	m.visible = appendCapped(m.visible, line)
	m.visibleRaw = appendCapped(m.visibleRaw, line)
	if !m.follow {
		m.pending++
	}
	m.vpDirty = true
}

// paneWidth is the stream pane's width, or 0 before the first size message.
func (m *tuiModel) paneWidth() int {
	if m.width <= 0 {
		return 0
	}
	leftW, _ := m.layoutWidths()
	return m.width - leftW - 1
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
	switch ev.kind {
	case evReconcile:
		// Adopt the list first, so the banner lands in the new pane.
		if ev.cfg != nil {
			m.rebuildRows(ev.cfg)
		}
		m.addNotice(ev.line)
		return

	case evNotice:
		m.addNotice(ev.line)
		return
	}

	row := m.rowOf(ev.target)
	if row <= 0 {
		return
	}
	i := row - 1
	t := m.targets[i]

	switch ev.kind {
	case evOutput:
		m.addLine(i, ev.line)

	case evChanged:
		// Retain the whole batch: the row's change tree renders it and the
		// pane header counts it.
		t.lastChange = ev.change
		t.changeRows = m.changeRowsFor(t)
		plural := ""
		if ev.count > 1 {
			plural = "s"
		}
		m.addLine(i, fmt.Sprintf("# %d file%s changed (e.g. %s)", ev.count, plural, ev.first))

	case evStarting:
		// Queued behind the start stagger: the glyph stays running but the
		// uptime does not tick until evStarted names the real spawn time.
		t.state = stateStarting
		t.exitLabel = ""

	case evBackoff:
		t.state = stateBackoff
		t.backoffUntil = m.now.Add(ev.delay)
		m.addLine(i, fmt.Sprintf("# backoff %s (%s restart in %s)", ev.delay, ordinalWord(ev.ordinal), backoffWindow))

	case evStarted:
		t.state = stateRunning
		t.lastStart = ev.at
		t.exitCode = -1
		t.exitLabel = ""
		t.backoffUntil = time.Time{}
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

// rebuildRows adopts a reconciled target list. Rows are matched by name, so a
// row keeps its stream, its mute, and its change tree across a reload; only a
// name that just appeared gets a new row.
func (m *tuiModel) rebuildRows(c *configChange) {
	old := m.targets
	byName := make(map[string]*tuiTarget, len(old))
	for _, t := range old {
		byName[t.spec.Name] = t
	}

	// The cursor and the solo follow a target by name, never by index: a reload
	// can reorder, add, and drop rows underneath them.
	curName, soloName := "", ""
	if m.cursor > 0 && m.cursor <= len(old) {
		curName = old[m.cursor-1].spec.Name
	}
	if m.solo >= 0 && m.solo < len(old) {
		soloName = old[m.solo].spec.Name
	}

	rows := make([]*tuiTarget, 0, len(c.targets))
	index := make(map[string]int, len(c.targets))
	for i, spec := range c.targets {
		row, known := byName[spec.Name]
		if !known {
			row = &tuiTarget{runner: c.runners[i], state: stateRunning, exitCode: -1, lastStart: m.now}
			if i < len(c.stopped) && c.stopped[i] {
				row.state = stateOff // the group holds it down until space or r
			}
		}
		rootMoved := !slices.Equal(row.spec.On, spec.On)
		row.spec = spec
		if rootMoved {
			// The tree's paths are relative to the watch root, which moved.
			row.changeRows = m.changeRowsFor(row)
		}
		rows = append(rows, row)
		index[spec.Name] = i
	}

	// Stream lines carry a row index, so every retained line is remapped. A
	// line whose target left the config keeps its text and its prefix, and
	// loses only its row: it stops answering to mute and solo.
	for k := range m.merged {
		if m.merged[k].target < 0 {
			continue
		}
		m.merged[k].target = -1
		if j, ok := index[m.merged[k].name]; ok {
			m.merged[k].target = j
		}
	}

	m.targets = rows
	m.cursor = 0
	if j, ok := index[curName]; ok {
		m.cursor = j + 1
	}
	m.solo = -1
	if j, ok := index[soloName]; ok {
		m.solo = j
	}
	m.prefixW = 0
	for _, row := range rows {
		m.prefixW = max(m.prefixW, displayWidth(row.spec.Name))
	}
	m.hover = -1
	m.lastClick = -1
	m.mergedDirty = true
	m.vpDirty = true
}

// changeRowsFor turns a row's retained change set into tree rows: paths
// relative to the target's watch root, sorted, one fsnotify glyph each.
func (m *tuiModel) changeRowsFor(t *tuiTarget) []changeRow {
	if len(t.lastChange.Files) == 0 {
		return nil
	}
	rows := make([]changeRow, 0, len(t.lastChange.Files))
	for path := range t.lastChange.Files {
		rows = append(rows, changeRow{
			op:   opGlyph(t.lastChange.Ops[path]),
			path: relToWatch(t.spec.On, path),
		})
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].path < rows[b].path })
	return rows
}

// paneLines is the merged stream, rebuilt when muting or soloing changed
// what belongs in it.
func (m *tuiModel) paneLines() []string {
	if m.mergedDirty {
		m.rebuildMerged()
	}
	return m.visible
}

// mergedLine is one line of the arrival-ordered log the ALL pane filters. The
// name is the target's name at arrival, so a line whose row is gone keeps its
// prefix instead of jumping to whatever row took its index.
type mergedLine struct {
	target int
	name   string
	text   string
}

func (m *tuiModel) prefixed(line mergedLine) string {
	if line.name == "" {
		return line.text
	}
	return padRight(line.name, m.prefixW) + "  " + line.text
}

func (m *tuiModel) rebuildMerged() {
	m.visible = m.visible[:0]
	m.visibleRaw = m.visibleRaw[:0]
	for _, line := range m.merged {
		if !m.streamsTo(line.target) {
			continue
		}
		m.visible = appendCapped(m.visible, m.prefixed(line))
		m.visibleRaw = appendCapped(m.visibleRaw, line.text)
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
	case stateStarting:
		return "starting"
	case stateBackoff:
		left := t.backoffUntil.Sub(m.now)
		if left < 0 {
			left = 0
		}
		secs := int((left + time.Second - 1) / time.Second)
		return fmt.Sprintf("backoff %ds", secs)
	case stateExited:
		return t.exitLabel
	case stateStopped:
		return "stopped"
	}
	return "off"
}

// ordinalWord renders a count as 1st, 2nd, 3rd, ... for the backoff line.
func ordinalWord(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// glyph is the row's process state, backed by shape as well as color.
func (t *tuiTarget) glyph() string {
	switch t.state {
	case stateRunning, stateStarting:
		return "●"
	case stateExited, stateBackoff:
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
		case t.state == stateRunning || t.state == stateStarting:
			running++
		case (t.state == stateExited || t.state == stateBackoff) && t.exitCode == 0:
			ok++
		case t.state == stateExited || t.state == stateBackoff:
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

// osc52 wraps a payload in the OSC 52 clipboard sequence. Terminals that
// support it copy remotely, so it works over ssh; others ignore it.
func osc52(payload string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(payload)) + "\x1b\\"
}

// copyCap bounds one clipboard payload, so a huge selection cannot stall the
// terminal's paste buffer.
const copyCap = 64 << 10

// copyPayload joins whole raw lines for the clipboard, capped at copyCap
// bytes. Lines are kept from the top of the selection; the rest is dropped and
// reported as truncated. A single over-cap line copies as nothing.
func copyPayload(lines []string) (payload string, n int, truncated bool) {
	var b strings.Builder
	for _, line := range lines {
		add := len(line)
		if n > 0 {
			add++ // the separator newline
		}
		if b.Len()+add > copyCap {
			truncated = true
			break
		}
		if n > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		n++
	}
	if n == 0 {
		return "", 0, false
	}
	b.WriteByte('\n')
	return b.String(), n, truncated
}

// copyMessage names how many lines reached the clipboard.
func copyMessage(n int, truncated bool) string {
	s := fmt.Sprintf("copied %d lines", n)
	if n == 1 {
		s = "copied 1 line"
	}
	if truncated {
		s += " (truncated)"
	}
	return s
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
