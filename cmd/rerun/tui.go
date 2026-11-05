package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/golang-cz/rerun"
	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// lineCap bounds each target's scrollback and the merged stream. Phase B
// swaps this for golang-cz/ringbuf.
const lineCap = 10_000

// buildLogCap bounds one target's build output, so a noisy build cannot evict service lines.
const buildLogCap = 2000

// maxEventBatch is how many runner events one Update takes in at a time, so a
// chatty target cannot force a redraw per line.
const maxEventBatch = 256

// runTUI runs the targets behind the bubbletea TUI. configPath is the
// rerun.yml the run reloads itself from; "" turns the config watch off. It
// returns 0 after a graceful quit and 130 when a second signal forced the kill.
func runTUI(targets []Target, configPath string, signals <-chan os.Signal) int {
	signals, stopSignals := signalChannel(signals)
	defer stopSignals()
	// Runs after shutdownTargets; bubbletea restores before children are killed.
	tty := saveTTY()
	defer tty.restore()

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

// Bottom-line input modes. inputNone means no bar is open.
const (
	inputNone = iota
	inputSearch
	inputFilter
)

// procState is the process state a row's glyph shows.
type procState int

const (
	stateOff procState = iota
	stateRunning
	stateStarting
	stateBackoff
	stateExited
	stateStopped
	// stateOnChange is an onchange target before its first file change.
	stateOnChange
	// stateManual is a manual target before its first r.
	stateManual
)

// up reports whether a state means the target is running, getting ready to
// run, or waiting out a restart backoff.
func (s procState) up() bool {
	return s == stateRunning || s == stateStarting || s == stateBackoff
}

// initialState is the state a row is born with: off when the config does not
// select it, and the start mode's own word until its first run.
func initialState(spec Target) procState {
	switch {
	case !spec.Selected:
		return stateOff
	case spec.Start == startOnChange:
		return stateOnChange
	case spec.Start == startManual:
		return stateManual
	}
	return stateRunning
}

// tuiTarget is one row: its process state, its stream state, and its output.
type tuiTarget struct {
	spec   Target
	runner *targetRunner

	state     procState
	exitCode  int
	exitLabel string
	lastStart time.Time
	// runStarted is set by a real start, so an exit without one shows no duration.
	runStarted bool
	// backoffUntil is when a backoff-delayed start may run.
	backoffUntil time.Time

	muted  bool
	unseen bool
	opened bool

	// lastChange is the newest change batch the target reacted to, which the
	// pane's title line counts.
	lastChange rerun.ChangeSet
	// flashUntil ends the row highlight a restart leaves behind.
	flashUntil time.Time

	// restartBatch is the batch that restarted the target, cleared when its spawn lands.
	restartBatch int
	// building and buildStart track a build in flight, which the status column counts.
	building    bool
	buildStart  time.Time
	buildFailed bool
	// buildCancelled marks the build log of a build that was dropped, not finished.
	buildCancelled bool
	// health is the live run's probe state; lastRule is the restart rule it may still complete.
	health   healthState
	lastRule *restartRule

	// colSpec is the spec the cached column expressions were parsed from, so
	// a spec swap reparses exactly once. colBad marks a spec whose expression
	// does not compile, which keeps every one of its rows raw.
	colSpec  []Column
	colExprs []*yqlib.ExpressionNode
	colBad   bool
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

	cursor  int // highlighted list row, an index into listRows
	help    bool
	helpAll bool // the help overlay shows every key, not the short list
	// confirmQuit is the `q` modal: the frame keeps running behind it, and
	// only enter quits.
	confirmQuit bool
	cfgErr      string

	// paneFocus puts the arrow keys on the log pane: up/down scroll it,
	// left/right pan its columns. False means the left list owns them.
	paneFocus bool
	paneCol   int // horizontal column offset of the pane

	// drawerOpen shows the fs drawer under the list. One global toggle, open
	// by default, so every target's spawns are on screen without a per-row
	// expansion.
	drawerOpen bool

	// feed is the drawer's change batches, oldest first; feedOpen lists one batch's files (0 for none).
	feed        []feedBatch
	feedSeq     int
	feedCursor  int
	feedOpen    int
	feedTop     int
	drawerFocus bool

	// version and buildTime are the ldflags stamp, rendered on the left
	// column's bottom line; buildTime is empty in a dev binary.
	version   string
	buildTime string
	// solo is the soloed target index, or -1.
	solo int

	follow  bool
	pending int
	// paneCurRaw is the cursor's raw line, paneCurSub its expansion row (-1 for the line).
	paneCurRaw, paneCurSub int
	// revealCur scrolls the pane to the cursor next frame, revealTop to its top.
	revealCur, revealTop bool
	// hintedExpand is set once the first expansion's footer hint has shown.
	hintedExpand bool
	// keepNoticeUntil holds the saved-columns notice against a config reload notice.
	keepNoticeUntil time.Time

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

	// visibleIdx mirrors visible with each line's target row. len(visibleIdx)
	// always equals len(visible).
	visibleIdx []int

	// visibleCells caches each visible line's table form, so a chatty stream
	// evaluates a line once and a frame only pads cached cells. Cleared on
	// every visibility rebuild; "not computed" is a zero cellCache.
	visibleCells []cellCache
	// cellEvals counts the lines evaluated for the table, which the tests read.
	cellEvals int
	colSample colSampler

	// visibleSeq and visibleObj mirror visible: each line's sequence number, which
	// keys its expansion, and whether it is a JSON object.
	visibleSeq []int
	visibleObj []bool
	visibleSep []sepLine
	// visibleLvl mirrors visible with each line's JSON level.
	visibleLvl []levelHit
	// cellBySeq keeps each line's table form across rebuilds, so a scope switch re-decodes nothing.
	cellBySeq map[int]*cellEntry
	// blockCache keeps each expanded line's YAML rows, so redrawing one costs no re-render.
	blockCache map[blockKey][]dispRow
	nextSeq    int
	// expanded is the set of sequence numbers shown pretty-printed.
	expanded map[int]bool
	// disp is the pane's rows once an expansion or filter is active: each shown
	// line, then its expansion rows. dispText mirrors disp for paneLines.
	disp      []dispRow
	dispText  []string
	dispDirty bool
	// chips are the value filters clicked into the pane; every chip ANDs with the rest.
	chips []pickChip
	// edits are the unsaved column picks, one per schema or target node.
	edits []colEdit

	// tableFlip is c's raw/table flip for the current selection.
	tableFlip bool

	// schemaSel is the group whose members the pane merges, or "" for none.
	schemaSel string

	// barFocus routes keys to the filter bar; barSel is its focused chip, or -1.
	barFocus bool
	barSel   int

	// pinSeq is the restart rule the pane holds at its top, 0 for none.
	pinSeq int
	// buildView is the target whose build log the pane shows alone, or -1 for none.
	buildView int
	// buildLog is each target's build output by name, capped apart from merged so builds never evict service lines.
	buildLog map[string][]mergedLine

	// inputMode is the bar's open input and input its text; an open input owns every key.
	inputMode int
	input     string

	// search is the query; while inputSearch is open it tracks input live.
	// searchMatches holds the pane rows it hits, searchRow the current hit.
	search        string
	searchMatches []int
	searchRow     int

	// filters AND together; draft is the open input's predicate, nil when empty or invalid.
	filters      []textFilter
	draft        func(string) bool
	filterErr    string
	paneSel      []int
	paneSelDirty bool

	// prefixW is the pane prefix column: the longest target name, so every
	// stream line starts its message in the same column.
	prefixW int

	vp      viewport.Model
	vpDirty bool

	width, height int
	now           time.Time
	// hint is the transient bottom-line message, live until hintUntil; the 1s
	// tick expires it.
	hint      string
	hintUntil time.Time
	// notice is the bottom-line message: info fades at noticeUntil, errors wait for a key.
	notice      string
	noticeErr   bool
	noticeUntil time.Time

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
		group:      group,
		events:     events,
		stop:       make(chan struct{}),
		version:    version,
		buildTime:  buildTime,
		solo:       -1,
		buildView:  -1,
		buildLog:   map[string][]mergedLine{},
		follow:     true,
		drawerOpen: true,
		now:        time.Now(),
		lastClick:  -1,
		hover:      -1,
		searchRow:  -1,
		barSel:     -1,
		paneCurSub: -1,
		styles:     newTUIStyles(),
		// Kept non-nil: copy code walks these lists in parallel with visible.
		visibleRaw:   []string{},
		visibleIdx:   []int{},
		visibleCells: []cellCache{},
		visibleSep:   []sepLine{},
		cellBySeq:    map[int]*cellEntry{},
		blockCache:   map[blockKey][]dispRow{},
		expanded:     map[int]bool{},
	}
	m.vp = viewport.New(80, 20)

	for i, target := range targets {
		row := &tuiTarget{
			spec:      target,
			runner:    group.runners[i],
			state:     initialState(target),
			exitCode:  -1,
			lastStart: m.now,
		}
		m.targets = append(m.targets, row)
	}
	groupTargets(m.targets)
	for _, t := range m.targets {
		m.prefixW = max(m.prefixW, displayWidth(t.spec.Name))
	}
	return m
}

// targetIndex maps a target name to its index in m.targets, or -1.
func (m *tuiModel) targetIndex(name string) int {
	for i, t := range m.targets {
		if t.spec.Name == name {
			return i
		}
	}
	return -1
}

// streamsTo reports whether the target's output belongs in the merged stream:
// muted targets are out, and a solo or schema selection drops the rest.
// rerun's own lines and lines whose row is gone always show.
func (m *tuiModel) streamsTo(i int) bool {
	if i < 0 || i >= len(m.targets) {
		return true
	}
	if m.targets[i].muted {
		return false
	}
	switch {
	case m.solo >= 0:
		return m.solo == i
	case m.schemaSel != "":
		return groupOf(m.targets[i]) == m.schemaSel
	}
	return true
}

// streamWord names a muted target's stream for the pane's title line.
func (m *tuiModel) streamWord(i int) string {
	if m.targets[i].muted {
		return "muted"
	}
	return ""
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
	m.recomputeSearch(false)
	if t.muted {
		m.showNotice("muted "+t.spec.Name, false)
	} else {
		m.showNotice("unmuted "+t.spec.Name, false)
	}
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

// toggleDrawer shows or hides the fs drawer under the target list, closing any
// open file list and dropping its focus when it hides.
func (m *tuiModel) toggleDrawer() {
	m.drawerOpen = !m.drawerOpen
	if !m.drawerOpen {
		m.drawerFocus = false
		m.feedOpen = 0
		m.feedTop = 0
	}
}

// showHint posts a transient message to the pane's bottom line.
func (m *tuiModel) showHint(text string) {
	m.hint = text
	m.hintUntil = m.now.Add(hintDuration)
}

// cancelBuild drops a running build's flags; a build log left over from it reads cancelled.
func (m *tuiModel) cancelBuild(i int) {
	t := m.targets[i]
	t.buildCancelled = t.building
	m.setBuildState(i, false, t.buildFailed)
}

func (m *tuiModel) stopTarget(i int) {
	t := m.targets[i]
	t.restartBatch = 0
	m.cancelBuild(i)
	t.state = stateStopped
	t.exitLabel = ""
	t.runner.control(ctrlStop)
}

func (m *tuiModel) startTarget(i int) {
	t := m.targets[i]
	t.restartBatch = 0
	m.cancelBuild(i)
	t.state = stateRunning
	t.lastStart = m.now
	t.runner.control(ctrlStart)
}

func (m *tuiModel) rebootTarget(i int) {
	t := m.targets[i]
	t.restartBatch = 0
	m.cancelBuild(i)
	t.state = stateRunning
	t.lastStart = m.now
	t.exitLabel = ""
	t.runner.control(ctrlReboot)
}

func (m *tuiModel) openTarget(i int) {
	t := m.targets[i]
	if t.spec.URL == "" {
		return
	}
	if err := openURL(t.spec.URL); err != nil {
		m.showNotice(fmt.Sprintf("rerun: open %s: %v", t.spec.URL, err), true)
	}
}

// addLine appends one line to a target's stream and to the merged view when
// that target streams into it.
func (m *tuiModel) addLine(i int, line string) {
	m.addRow(i, line, sepLine{})
}

// refreshRule rewrites a rule's stored raw text, so copy and search match what it now draws.
func (m *tuiModel) refreshRule(rr *restartRule) {
	raw := rr.label(1 << 16)
	for k := range m.merged {
		if m.merged[k].sep.restart == rr {
			m.merged[k].text = raw
		}
	}
	for k := range m.visibleSep {
		if m.visibleSep[k].restart == rr {
			m.visible[k] = raw
			m.visibleRaw[k] = raw
		}
	}
}

func (m *tuiModel) addRule(i int, s sepLine) {
	m.addRow(i, s.raw(), s)
}

func (m *tuiModel) addRow(i int, line string, sep sepLine) {
	t := m.targets[i]
	m.nextSeq++
	ml := mergedLine{target: i, name: t.spec.Name, text: line, seq: m.nextSeq, obj: isJSONObject(line), sep: sep}
	if ml.obj {
		ml.lvl = jsonLevel(line)
	}
	m.merged = appendCapped(m.merged, ml)
	if len(m.cellBySeq) > 2*lineCap {
		m.pruneCells()
	}
	if m.shows(i) {
		m.showLine(ml)
		if !m.follow {
			m.pending++ // only lines the pane actually holds
		}
	} else {
		// Output the pane is not showing: muted, hidden by another solo, or a build log is scoped.
		t.unseen = true
		m.mergedDirty = true
	}
	m.vpDirty = true
}

func (m *tuiModel) addBuildLine(i int, line string) {
	m.addBuildRow(i, line, sepLine{})
}

func (m *tuiModel) addBuildRule(i int, s sepLine) {
	m.addBuildRow(i, s.raw(), s)
}

func (m *tuiModel) addBuildRow(i int, line string, sep sepLine) {
	name := m.targets[i].spec.Name
	m.nextSeq++
	ml := mergedLine{target: i, name: name, text: line, seq: m.nextSeq, obj: isJSONObject(line), sep: sep}
	m.buildLog[name] = appendLimit(m.buildLog[name], ml, buildLogCap)
	if m.buildView == i {
		m.showLine(ml)
		if !m.follow {
			m.pending++
		}
	}
	m.vpDirty = true
}

func (m *tuiModel) shows(i int) bool {
	return m.buildView < 0 && m.streamsTo(i)
}

func (m *tuiModel) showLine(ml mergedLine) {
	m.visible = appendCapped(m.visible, m.prefixed(ml))
	m.visibleRaw = appendCapped(m.visibleRaw, ml.text)
	m.visibleIdx = appendCapped(m.visibleIdx, ml.target)
	m.visibleCells = appendCapped(m.visibleCells, cellCache{})
	m.visibleSeq = appendCapped(m.visibleSeq, ml.seq)
	m.visibleObj = appendCapped(m.visibleObj, ml.obj)
	m.visibleSep = appendCapped(m.visibleSep, ml.sep)
	m.visibleLvl = appendCapped(m.visibleLvl, ml.lvl)
	m.dispDirty = true
	if m.filterActive() {
		m.paneSelDirty = true
	}
}

// showNotice puts rerun's own message on the bottom line; newest wins.
func (m *tuiModel) showNotice(text string, isErr bool) {
	if text == "" {
		return
	}
	m.notice = text
	m.noticeErr = isErr
	m.noticeUntil = time.Time{}
	if !isErr {
		m.noticeUntil = m.now.Add(noticeDuration)
	}
}

// noticeLive reports whether the bottom line should show the notice now.
func (m *tuiModel) noticeLive() bool {
	return m.notice != "" && (m.noticeErr || m.now.Before(m.noticeUntil))
}

// dismissErrorNotice clears an error on a key press; info fades on its timer.
func (m *tuiModel) dismissErrorNotice() {
	if m.noticeErr {
		m.notice = ""
		m.noticeErr = false
	}
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
func appendCapped[T any](lines []T, line T) []T { return appendLimit(lines, line, lineCap) }

func appendLimit[T any](lines []T, line T, limit int) []T {
	if len(lines) >= limit {
		lines = lines[len(lines)-limit+1:]
	}
	return append(lines, line)
}

func (m *tuiModel) applyEvents(events []event) {
	for _, ev := range events {
		m.applyEvent(ev)
	}
	if m.search != "" {
		m.recomputeSearch(false)
	}
}

func (m *tuiModel) applyEvent(ev event) {
	switch ev.kind {
	case evConfigErr:
		m.showNotice(ev.line, true)
		if ev.err != nil {
			m.cfgErr = printable(ev.err.Error(), true)
		}
		return

	case evConfigOK:
		m.cfgErr = ""
		return

	case evReconcile:
		m.cfgErr = ""
		// Adopt the list first, so the banner lands in the new pane.
		if ev.cfg != nil {
			m.rebuildRows(ev.cfg)
		}
		m.recordConfigFeed(eventTime(ev, m.now))
		// Our own save triggers this reload; its banner would replace the save notice at once.
		if !m.now.Before(m.keepNoticeUntil) {
			m.showNotice(ev.line, false)
		}
		return

	case evNotice:
		m.showNotice(ev.line, ev.err != nil)
		return
	}

	i := m.targetIndex(ev.target)
	if i < 0 {
		return
	}
	t := m.targets[i]

	switch ev.kind {
	case evOutput:
		m.addLine(i, ev.line)
		m.captureURL(t, ev.line)

	case evChanged:
		// The batch writes no pane line; the pane header and the feed show it.
		at := eventTime(ev, m.now)
		t.lastChange = ev.change
		t.restartBatch = 0
		if id := m.recordFeed(ev.change, at); ev.restarts {
			t.restartBatch = id
		}

	case evStarting:
		// Queued behind the start stagger: the glyph stays running but the
		// uptime does not tick until evStarted names the real spawn time.
		t.state = stateStarting
		t.exitLabel = ""

	case evBackoff:
		// The row's `backoff Ns` status owns this; no pane line.
		t.state = stateBackoff
		t.backoffUntil = m.now.Add(ev.delay)

	case evBuildStarted:
		t.buildStart = eventTime(ev, m.now)
		t.buildCancelled = false
		m.setBuildState(i, true, false)
		m.addBuildRule(i, buildStartRule(t.spec.Name, t.buildStart, ev.files))
		// The new build's rule pins to the top, with the earlier builds kept above it.
		if m.follow && m.buildView == i {
			m.pinSeq = m.nextSeq
		}

	case evBuildOutput:
		// A line from a build that a newer one has replaced belongs to no shown build.
		if ev.buildAt.Equal(t.buildStart) {
			m.addBuildLine(i, printable(ev.line, false))
		}

	case evBuildOK:
		t.buildCancelled = false
		if t.exitLabel == "build failed" {
			t.exitLabel = ""
		}
		m.setBuildState(i, false, false)
		m.addBuildRule(i, buildEndRule(t.spec.Name, true, ev.dur))

	case evBuildCancelled:
		m.cancelBuild(i)

	case evBuildFailed:
		// The output was streamed as evBuildOutput; the service keeps only the failure rule.
		m.setBuildState(i, false, true)
		t.restartBatch = 0
		m.addRule(i, buildFailedRule(t, ev))
		m.addBuildRule(i, buildEndRule(t.spec.Name, false, ev.dur))
		if !(t.runStarted && t.state.up()) {
			t.state = stateExited
			t.exitCode = -1
			t.exitLabel = "build failed"
			t.lastRule = nil
			t.unseen = true
		}

	case evHealth:
		// A probe from an earlier run says nothing about the live one.
		if ev.runAt.Before(t.lastStart) {
			break
		}
		t.health = ev.health
		if rr := t.lastRule; ev.health == healthHealthy && rr != nil && rr.healthy == 0 && rr.at.Equal(ev.runAt) {
			rr.healthy = ev.dur
			m.refreshRule(rr)
		}

	case evStarted:
		t.state = stateRunning
		t.lastStart = ev.at
		t.runStarted = true
		t.exitCode = -1
		t.exitLabel = ""
		t.backoffUntil = time.Time{}
		t.health = ""
		if t.spec.Health != "" {
			t.health = healthStarting
		}
		if ev.restart {
			rr := &restartRule{name: t.spec.Name, at: eventTime(ev, m.now), files: ev.files, built: ev.buildDur, upFor: ev.prevUp}
			t.lastRule = rr
			m.addRule(i, sepLine{restart: rr})
			if m.follow && m.solo == i && m.schemaSel == "" {
				m.pinSeq = m.nextSeq
			}
		} else {
			t.lastRule = nil
			label := fmt.Sprintf("▶ %s started %s", t.spec.Name, eventTime(ev, m.now).Format("15:04:05"))
			if ev.buildDur > 0 {
				label += " · built " + shortDur(ev.buildDur)
			}
			m.addRule(i, sepLine{label: label})
		}
		if t.restartBatch != 0 {
			m.feedRestart(t, eventTime(ev, m.now))
		}
		if t.spec.Open && t.spec.URL != "" && !t.opened {
			t.opened = true // once per session, never on a restart
			if err := openURL(t.spec.URL); err != nil {
				m.showNotice(fmt.Sprintf("rerun: open %s: %v", t.spec.URL, err), true)
			}
		}

	case evExited:
		// A swap's exit draws nothing; the ↻ rule at the next start stands for it.
		if ev.restart {
			return
		}
		t.lastRule = nil
		t.state = stateExited
		t.exitCode = ev.code
		t.exitLabel = exitLabel(ev.code, ev.err)
		t.unseen = true
		m.addRule(i, exitRule(t, ev, m.now))
		t.runStarted = false

	case evWatcherErr:
		m.showNotice(fmt.Sprintf("rerun: %s: watcher error: %v", t.spec.Name, ev.err), true)

	case evRunnerErr:
		// Nothing is running after a spawn failure, so the row leaves the
		// running state the way an exit does.
		t.state = stateExited
		t.restartBatch = 0
		t.exitCode = -1
		t.exitLabel = "exit error"
		t.unseen = true
		m.showNotice(fmt.Sprintf("rerun: %s: %v", t.spec.Name, ev.err), true)

	case evLoop:
		m.showNotice(fmt.Sprintf("rerun: loop suspected — %s paused after %d× re-triggered by %s; add it to except, or change another file to resume", t.spec.Name, loopCycles, ev.line), true)

	case evCleared:
		// The TUI owns the screen; the clear bytes belong to the plain path.
	}
}

// rebuildRows adopts a reconciled target list. Rows are matched by name, so a
// row keeps its stream and its mute across a reload; only a name that just
// appeared gets a new row.
func (m *tuiModel) rebuildRows(c *configChange) {
	old := m.targets
	buildName := ""
	if m.buildView >= 0 && m.buildView < len(old) {
		buildName = old[m.buildView].spec.Name
	}
	byName := make(map[string]*tuiTarget, len(old))
	for _, t := range old {
		byName[t.spec.Name] = t
	}

	// The cursor and selection follow their row by name: a reload reorders rows.
	cursorKey := ""
	if r, ok := m.cursorRow(); ok {
		cursorKey = m.rowKey(r)
	}
	soloName := ""
	if m.solo >= 0 && m.solo < len(old) {
		soloName = old[m.solo].spec.Name
	}
	schema := m.schemaSel

	rows := make([]*tuiTarget, 0, len(c.targets))
	for i, spec := range c.targets {
		row, known := byName[spec.Name]
		if !known {
			row = &tuiTarget{runner: c.runners[i], state: initialState(spec), exitCode: -1, lastStart: m.now}
			if i < len(c.stopped) && c.stopped[i] && row.state == stateRunning {
				row.state = stateOff // the group holds it down until space or r
			}
		}
		row.spec = spec
		// A row that never ran takes its spec's new start mode.
		if row.state == stateOnChange || row.state == stateManual {
			row.state = initialState(spec)
		}
		rows = append(rows, row)
	}
	groupTargets(rows)
	index := make(map[string]int, len(rows))
	for i, row := range rows {
		index[row.spec.Name] = i
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
	m.applyEdits()
	m.solo = -1
	if j, ok := index[soloName]; ok {
		m.solo = j
	}
	m.buildView = -1
	if j, ok := index[buildName]; ok && buildName != "" {
		m.buildView = j
	}
	for name := range m.buildLog {
		if _, ok := index[name]; !ok {
			delete(m.buildLog, name)
		}
	}
	m.schemaSel = ""
	if schema != "" && m.hasGroups() && len(m.schemaMembers(schema)) > 0 {
		m.schemaSel = schema
	}
	m.cursor = min(m.cursor, max(0, m.listLen()-1))
	for i, r := range m.listRows() {
		if m.rowKey(r) == cursorKey {
			m.cursor = i
			break
		}
	}
	m.prefixW = 0
	for _, row := range rows {
		m.prefixW = max(m.prefixW, displayWidth(row.spec.Name))
	}
	m.hover = -1
	m.lastClick = -1
	m.mergedDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
}

// eventTime is the event's own stamp, or the frame clock for one that carries none.
func eventTime(ev event, now time.Time) time.Time {
	if ev.at.IsZero() {
		return now
	}
	return ev.at
}

// paneLines is the merged stream, rebuilt when muting, soloing, filtering, or
// expanding changed what the pane shows.
func (m *tuiModel) paneLines() []string {
	m.ensurePane()
	if m.paneDirect() {
		return m.visible
	}
	return m.dispText
}

// ensurePane materializes visible, paneSel and the display rows before a frame
// or a pointer gesture reads them.
func (m *tuiModel) ensurePane() {
	if m.mergedDirty {
		m.rebuildMerged()
	}
	if m.paneSelDirty {
		m.rebuildPaneSel()
	}
	if m.dispDirty && !m.paneDirect() {
		m.rebuildDisp()
	}
}

// paneDirect reports whether the pane shows visible lines one to one. Filters
// and expansions take the display-row path instead.
func (m *tuiModel) paneDirect() bool { return !m.filterActive() && len(m.expanded) == 0 }

// paneLen is how many rows the pane shows.
func (m *tuiModel) paneLen() int {
	m.ensurePane()
	if m.paneDirect() {
		return len(m.visibleRaw)
	}
	return len(m.disp)
}

// paneRowRaw maps a display row to its raw line's index in visibleRaw.
func (m *tuiModel) paneRowRaw(row int) int {
	m.ensurePane()
	if m.paneDirect() {
		return row
	}
	return m.disp[row].raw
}

// filterOn reports whether a text filter or an open draft narrows the pane.
func (m *tuiModel) filterOn() bool { return len(m.filters) > 0 || m.draft != nil }

// filterActive reports whether any text filter or value chip narrows the pane.
func (m *tuiModel) filterActive() bool { return m.filterOn() || len(m.chips) > 0 }

// rebuildPaneSel re-derives the visible subset for the current filters. The
// filter expressions are compiled once per edit, never per line.
func (m *tuiModel) rebuildPaneSel() {
	m.paneSelDirty = false
	m.dispDirty = true
	m.paneSel = m.paneSel[:0]
	if !m.filterActive() {
		return
	}
	keep := m.lineKeeper()
	for i := range m.visible {
		// Run rules are not output, so a filter must not swallow the boundaries.
		if m.visibleSep[i].isSep() || keep(m.visibleRaw[i]) {
			m.paneSel = append(m.paneSel, i)
		}
	}
}

// lineKeeper is the per-line predicate for the text filters, the open draft,
// and every value chip.
func (m *tuiModel) lineKeeper() func(string) bool {
	keeps := make([]func(string) bool, 0, len(m.filters)+1)
	for _, f := range m.filters {
		keeps = append(keeps, f.keep)
	}
	if m.draft != nil {
		keeps = append(keeps, m.draft)
	}
	chips := m.chips
	return func(line string) bool {
		for _, keep := range keeps {
			if !keep(line) {
				return false
			}
		}
		for _, c := range chips {
			if !yqMatches(c.prog, line) {
				return false
			}
		}
		return true
	}
}

// textFilter is a committed text filter chip: its query and its compiled predicate.
type textFilter struct {
	query string
	keep  func(string) bool
}

// compileFilter builds the per-line predicate: a yq expression when the query
// starts with ".", else a case-insensitive substring. A yq query that does not
// parse returns the error, so it never reaches the pane.
func compileFilter(query string) (func(string) bool, error) {
	if strings.HasPrefix(query, ".") {
		expr, err := yqParse(query)
		if err != nil {
			return nil, err
		}
		return func(line string) bool { return yqMatches(expr, line) }, nil
	}
	needle := strings.ToLower(query)
	return func(line string) bool {
		return strings.Contains(strings.ToLower(line), needle)
	}, nil
}

// recomputeSearch re-sweeps the pane's raw lines for the query. One Contains
// pass over at most lineCap lines, run on edits and arrivals, never per frame.
// reset parks the hit at the first match in view; otherwise the current hit is
// kept when it still matches.
func (m *tuiModel) recomputeSearch(reset bool) {
	m.ensurePane()
	m.searchMatches = m.searchMatches[:0]
	if m.search == "" {
		m.searchRow = -1
		return
	}
	q := strings.ToLower(m.search)
	n := m.paneLen()
	for row := 0; row < n; row++ {
		if m.rowInfo(row).sub >= 0 {
			continue
		}
		raw := m.paneRowRaw(row)
		// Run rules are not output, so a search never lands on one.
		if m.visibleSep[raw].isSep() {
			continue
		}
		if strings.Contains(strings.ToLower(m.visibleRaw[raw]), q) {
			m.searchMatches = append(m.searchMatches, row)
		}
	}
	if !reset {
		for _, r := range m.searchMatches {
			if r == m.searchRow {
				return
			}
		}
	}
	m.searchRow = -1
	for _, r := range m.searchMatches {
		if r >= m.vp.YOffset {
			m.searchRow = r
			break
		}
	}
}

// clearSearch drops the query and its matches.
func (m *tuiModel) clearSearch() {
	m.search = ""
	m.searchMatches = m.searchMatches[:0]
	m.searchRow = -1
}

// searchJump moves to the next (dir > 0) or previous match and centers it,
// wrapping at the ends. A search with no matches is a no-op.
func (m *tuiModel) searchJump(dir int) {
	if m.search == "" || len(m.searchMatches) == 0 {
		return
	}
	if dir < 0 {
		prev := m.searchMatches[len(m.searchMatches)-1]
		for i := len(m.searchMatches) - 1; i >= 0; i-- {
			if m.searchMatches[i] < m.searchRow {
				prev = m.searchMatches[i]
				break
			}
		}
		m.searchRow = prev
	} else {
		next := m.searchMatches[0]
		for _, r := range m.searchMatches {
			if r > m.searchRow {
				next = r
				break
			}
		}
		m.searchRow = next
	}
	m.centerOn(m.searchRow)
}

// centerOn scrolls a row to the middle of the pane and freezes the tail, the
// way a manual scroll does.
func (m *tuiModel) centerOn(row int) {
	m.clearSelection()
	m.follow = false
	m.pending = 0
	lines := m.paneLines()
	maxOff := max(0, len(lines)-m.vp.Height)
	m.vp.SetYOffset(min(max(row-m.vp.Height/2, 0), maxOff))
}

// searchStatus is the `12/47` counter.
func (m *tuiModel) searchStatus() string {
	pos := 0
	for i, r := range m.searchMatches {
		if r == m.searchRow {
			pos = i + 1
			break
		}
	}
	return fmt.Sprintf("%d/%d", pos, len(m.searchMatches))
}

// filterStatus is the bottom line's filtered row count.
func (m *tuiModel) filterStatus() string {
	return fmt.Sprintf("%d lines", m.paneLen())
}

// mergedLine is one line of the arrival-ordered log the pane filters. The
// name is the target's name at arrival, so a line whose row is gone keeps its
// prefix instead of jumping to whatever row took its index.
type mergedLine struct {
	target int
	name   string
	text   string
	seq    int
	obj    bool
	sep    sepLine
	// lvl is the JSON level behind the line, zero for plain text.
	lvl levelHit
}

func (m *tuiModel) prefixed(line mergedLine) string {
	if line.name == "" || line.sep.isSep() {
		return line.text
	}
	return padRight(line.name, m.prefixW) + "  " + line.text
}

func (m *tuiModel) rebuildMerged() {
	m.visible = m.visible[:0]
	m.visibleRaw = m.visibleRaw[:0]
	m.visibleIdx = m.visibleIdx[:0]
	m.visibleCells = m.visibleCells[:0]
	m.visibleSeq = m.visibleSeq[:0]
	m.visibleObj = m.visibleObj[:0]
	m.visibleSep = m.visibleSep[:0]
	m.visibleLvl = m.visibleLvl[:0]
	if m.buildView >= 0 {
		for _, line := range m.buildLog[m.targets[m.buildView].spec.Name] {
			line.target = m.buildView
			m.showLine(line)
		}
	} else {
		for _, line := range m.merged {
			if m.streamsTo(line.target) {
				m.showLine(line)
			}
		}
	}
	m.mergedDirty = false
	m.paneSelDirty = true // paneSel indexes into the rebuilt visible
	m.dispDirty = true
}

// Drops entries for lines that scrolled out, so streaming cannot grow the map.
func (m *tuiModel) pruneCells() {
	live := make(map[int]*cellEntry, len(m.merged))
	keep := func(line mergedLine) {
		if e, ok := m.cellBySeq[line.seq]; ok {
			live[line.seq] = e
		}
	}
	for _, line := range m.merged {
		keep(line)
	}
	for _, lines := range m.buildLog {
		for _, line := range lines {
			keep(line)
		}
	}
	m.cellBySeq = live
}

// uptime renders a running target's age as a clock: 0:07, 2:03, 1:02:03, then 4d03h.
func uptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d / time.Second)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d:%02d", secs/60, secs%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%d:%02d:%02d", secs/3600, secs/60%60, secs%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

// status is the row's second column: uptime while running, what happened after.
func (m *tuiModel) status(t *tuiTarget) string {
	if t.building {
		return "⚙ " + shortDur(max(0, m.now.Sub(t.buildStart)))
	}
	if t.buildFailed {
		return "✗ build"
	}
	if t.state.up() && t.health == healthUnhealthy {
		return "unhealthy"
	}
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
	case stateOnChange:
		return "waiting"
	case stateManual:
		return "manual"
	}
	return "off"
}

// glyph is the row's process state, backed by shape as well as color.
// healthMark is the list mark a probe shows on a live run, or "" when there is none.
func (t *tuiTarget) healthMark() (glyph, color string) {
	if t.health == "" {
		return "", ""
	}
	if t.state == stateExited {
		return "■", runRed
	}
	if !t.state.up() {
		return "", ""
	}
	switch t.health {
	case healthStarting:
		return "●", colorGray
	case healthHealthy:
		return "●", runGreen
	case healthUnhealthy:
		return "▲", runRed
	}
	return "", ""
}

func (t *tuiTarget) glyph() string {
	if g, _ := t.healthMark(); g != "" {
		return g
	}
	switch t.state {
	case stateRunning:
		return "●"
	case stateStarting:
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
		// Native selections copy plain text; drop the pane's color codes.
		line = ansi.Strip(line)
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

// sliceCells cuts s to display cells [from, to): the pane's horizontal window.
// Escape sequences have no width and are kept whole, so a cut never splits one.
func sliceCells(s string, from, to int) string {
	if from < 0 {
		from = 0
	}
	if to <= from {
		return ""
	}
	return ansi.Cut(s, from, to)
}

// truncate cuts s to at most w display cells, keeping escape sequences whole.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "")
}
