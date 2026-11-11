package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/golang-cz/rerun"
	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// Output under a build rule is capped at the tail a failed build keeps.
const buildLogCap = buildTailLines

// maxEventBatch is how many runner events one Update takes in at a time, so a
// chatty target cannot force a redraw per line.
const maxEventBatch = 256

// runTUI runs the targets behind the bubbletea TUI. cfg is the rerun.yml the
// run reloads itself from. It returns 0 after a graceful quit and 130 when a
// second signal forced the kill.
func runTUI(targets []Target, cfg *Config, signals <-chan os.Signal) int {
	signals, stopSignals := signalChannel(signals)
	defer stopSignals()
	// Runs after shutdownTargets; bubbletea restores before children are killed.
	tty := saveTTY()
	defer tty.restore()

	model := newConfigTUI(targets, cfg)
	for _, n := range cfg.Notices {
		model.group.sink.emit(event{kind: evNotice, line: "── " + oneLine(n) + " ──"})
	}
	model.group.start()

	program := tea.NewProgram(model,
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
	inputQuery
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
	// Gray until the first evStarted, so a fresh row never reads as running.
	return stateStarting
}

// tuiTarget is one row: its process state, its stream state, and its output.
type tuiTarget struct {
	spec   Target
	runner *targetRunner

	state     procState
	exitCode  int
	exitLabel string
	// exitedAt is when the last run exited, which the fold window counts from.
	exitedAt  time.Time
	lastStart time.Time
	// runStarted is set by a real start, so an exit without one shows no duration.
	runStarted bool
	// backoffUntil is when a backoff-delayed start may run.
	backoffUntil time.Time

	muted bool
	// dropped is d's state: the stream is muted and stores no service output.
	dropped bool
	// lines is the target's own scrollback, oldest first, bounded by its keep:; the pane merges these by seq.
	lines []mergedLine

	// lastChange is the newest change batch the target reacted to, which the
	// pane's title line counts.
	lastChange rerun.ChangeSet
	// restartedAt is when a file-triggered restart landed, which the green name tint counts from.
	restartedAt time.Time

	// restartBatch is the batch that restarted the target, cleared when its spawn lands.
	restartBatch int
	// building and buildStart track a build in flight, which the build cell shows.
	building    bool
	buildStart  time.Time
	buildFailed bool
	// buildOKAt is when the last build passed, which the build cell's ✓ counts from.
	buildOKAt time.Time
	// loopFile names the file a core rebuild loop keeps re-triggering; empty when not looping.
	loopFile string
	// buildRule is the target's latest build, whose rule and output the next build event updates.
	buildRule *buildRec
	// buildOwner is the target whose row shows this target's shared build; empty when it owns its own.
	buildOwner string
	// health is the live run's probe state; lastRule is the restart rule it may still complete.
	health   healthState
	lastRule *restartRule

	// colSpec is the spec the cached column programs were compiled from, so
	// a spec swap recompiles exactly once. colErr is the first column that does
	// not compile, which keeps every one of its rows raw.
	colSpec  []Column
	colProgs []columnProgram
	colErr   error
	// lastBuild is the build behind the current run, 0 when it had none.
	lastBuild time.Duration
}

// tuiModel is the whole screen: the target list on the left, the stream on
// the right.
type tuiModel struct {
	group *targetGroup
	// missing is the optional imports whose file is absent, drawn after the list.
	missing []missingImport
	events  <-chan event
	// stop is closed once bubbletea has released the terminal, which retires
	// every pending event reader.
	stop chan struct{}

	targets []*tuiTarget

	// cursorSet marks a cursor a handler placed itself, so keepCursor does not pull it back to its old row.
	cursorSet bool

	cursor  int // highlighted list row, an index into listRows
	help    bool
	helpAll bool // the help overlay shows every key, not the short list
	helpTop int  // the help overlay's first body row, scrolled by j/k and pgup/pgdn
	// confirmQuit is the `q` modal: the frame keeps running behind it, and
	// only enter quits.
	confirmQuit bool
	cfgErr      string

	// paneFocus puts the arrow keys on the log pane: up/down scroll it,
	// left/right pan its columns. False means the left list owns them.
	paneFocus bool
	// listHidden is b's state: the log takes the full width while the list is off.
	listHidden bool
	// showTimes is t's state: the list shows its times column.
	showTimes bool
	paneCol   int // horizontal column offset of the pane
	// panedView is the target view paneCol belongs to.
	panedView string

	// feed is the change batches the fs events modal lists, oldest first.
	feed    []feedBatch
	feedSeq int

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
	// paneCurPlaced keeps the cursor on paneCurRaw's line while the tail follows.
	paneCurPlaced bool
	// revealCur scrolls the pane to the cursor next frame; revealSpan more rows
	// under the cursor must show with it, an expansion's.
	revealCur  bool
	revealSpan int
	// hintedExpand is set once the first expansion's footer hint has shown.
	hintedExpand bool
	// keepNoticeUntil holds the saved-columns notice against a config reload notice.
	keepNoticeUntil time.Time

	// copyHint is the last copy's message. It renders only while m.now is before
	// copyUntil, which is kept apart from hintUntil so the m/s hint cannot resurrect a stale copy.
	copyHint  string
	copyUntil time.Time

	// selAnchor is the content index a frozen drag started on; nil means no
	// selection. selEnd is the drag row's current content index. selMoved marks
	// the press as a drag on its first motion; without it a same-cell release
	// is a click. selCell is the press cell a click's release must land on.
	// selSeq/selSub are the pressed line's identity, its sequence and expansion
	// subrow, so a release after the stream shifted finds the line.
	selAnchor          *int
	selEnd             int
	selMoved           bool
	selCellX, selCellY int
	selSeq, selSub     int
	// selKey marks a selection grown by shift+arrows, which no pointer motion may drag.
	selKey bool
	// paneLastSeq, paneLastSub, and paneLastClickAt are the last same-cell pane click, for double-click.
	paneLastSeq, paneLastSub int
	paneLastX, paneLastY     int
	paneLastClickAt          time.Time

	// orphans are the lines of rows that left the config, oldest first; they keep their prefix but answer to no row.
	orphans []mergedLine
	// visible is the pane's materialized k-way merge of the targets' scrollback, which muting and soloing filter.
	visible     []string
	mergedDirty bool
	// stale counts visible lines whose scrollback copy was evicted from the middle of the merge; enough of them force a rebuild.
	stale int

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
	// visibleAt mirrors visible with each line's arrival second since startAt.
	visibleAt []uint32
	// startAt and clock stamp lines: one clock read per ingested batch, whole seconds since startAt.
	startAt time.Time
	clock   func() time.Time
	batchAt uint32
	// spin is the spinner frame; spinning marks the fast tick as running.
	spin     int
	spinning bool
	inBatch  bool
	// cellBySeq keeps each line's table form across rebuilds, so a scope switch re-decodes nothing.
	cellBySeq map[int]*cellEntry
	// lastHeadH is the pane header height of the last frame.
	lastHeadH int
	// markedRaw is the shown line wearing the (+) cell, -1 for none.
	markedRaw int
	// frameRows is the list drawn by the frame in progress; nil between frames.
	frameRows []listRow
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
	// edits are the unsaved column picks, one per schema or target node.
	edits []colEdit

	// schemaSel is the group whose members the pane merges, or "" for none.
	schemaSel string
	// foldState holds the user's fold toggles by block key (true folded, false expanded); it lives in memory only.
	foldState map[string]bool
	// finder is the Ctrl+K popup over targets and groups.
	finder finderState
	// eventsModal is the `e` fs events modal over the last 10 minutes' change batches.
	eventsModal eventsState
	// original is the `o` modal over the pane's raw lines.
	original originalState
	// listTop is the list's first visible row; it moves only when the cursor leaves the window.
	listTop int
	// listScrolled means a wheel moved the window off the cursor, which it left at listScrollAt.
	listScrolled bool
	listScrollAt int

	// barFocus routes keys to the filter bar; barSel is its picked pill, or -1 for the input.
	barFocus bool
	barSel   int
	// editKindOn and editKind carry a pill's kind through an edit round-trip, so
	// re-committing its text restores filter vs highlight.
	editKindOn bool
	editKind   pillKind
	// editAt is the pill slot an edit took its text from, where enter puts it back.
	editAt int

	// pinSeq is the restart rule the pane holds at its top, 0 for none.
	pinSeq int
	// pinGap hides the pinned row and holds the one after it at the top, which
	// is how a clear leaves the pane blank.
	pinGap bool

	// inputMode is the bar's open input and input its text; an open input owns every key.
	inputMode int
	input     string
	// inputCursor is the text cursor as a rune index into input; inputSel selects the whole input.
	inputCursor int
	inputSel    bool
	// inputHist is the bar input's undo and redo history, fresh each time the input opens.
	inputHist undoStack

	// searchMatches holds the pane rows any pill hits, searchRow the current hit.
	searchMatches []int
	searchRow     int

	// pills are the query bar's committed pills: filter-kind ones narrow the
	// pane, every pill's spans are marked, and hits feed searchMatches.
	pills []pill
	// queries are the config's global saved queries, picked with @ in the bar.
	queries map[string]string
	// draft is the open input's live-compiled pills; queryErr is its parse error.
	draft    pillSet
	draftOn  bool
	queryErr string
	// pickSel is the selected row of the bar's @ picker.
	pickSel      int
	paneSel      []int
	paneSelDirty bool
	// countKept and countTotal cache the bar's kept/total line counts; they are
	// recomputed in rebuildPaneSel, never per frame.
	countKept  int
	countTotal int

	// prefixW is the pane prefix column: the longest name in view, so every
	// stream line starts its message in the same column.
	prefixW int
	// groupW is a name's prefix width in the all view: the longest name in its group.
	groupW map[string]int
	// nameCut is the group or import prefix the pane drops from names, or "" for full names.
	nameCut string

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
	// isDark is the terminal's reported background; dark until it is heard from.
	isDark bool
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
		follow:     true,
		now:        time.Now(),
		startAt:    time.Now(),
		clock:      time.Now,
		lastClick:  -1,
		hover:      -1,
		searchRow:  -1,
		barSel:     -1,
		paneCurSub: -1,
		isDark:     true,
		styles:     newTUIStyles(true),
		// Kept non-nil: copy code walks these lists in parallel with visible.
		visibleRaw:   []string{},
		visibleIdx:   []int{},
		visibleCells: []cellCache{},
		paneSelDirty: true,
		visibleSep:   []sepLine{},
		cellBySeq:    map[int]*cellEntry{},
		blockCache:   map[blockKey][]dispRow{},
		expanded:     map[int]bool{},
	}
	m.vp = viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	for i, target := range targets {
		row := &tuiTarget{
			spec:      target,
			runner:    group.runners[i],
			state:     initialState(target),
			exitCode:  -1,
			lastStart: m.now,
			muted:     target.mutedAtStart(),
		}
		m.targets = append(m.targets, row)
	}
	groupTargets(m.targets)
	m.refreshNames()
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

// streamsTo reports whether the target's output belongs in the merged stream.
// Solo wins over mute, so a muted solo row still shows its own logs.
// rerun's own lines and lines whose row is gone always show.
func (m *tuiModel) streamsTo(i int) bool {
	if i < 0 || i >= len(m.targets) {
		return true
	}
	if m.solo >= 0 {
		return m.solo == i
	}
	if m.targets[i].muted {
		return false
	}
	switch {
	case m.schemaSel != "":
		return m.memberOf(i, m.schemaSel)
	}
	return true
}

func (m *tuiModel) toggleMute(i int) {
	t := m.targets[i]
	t.muted = !t.muted
	m.mergedDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
	if t.muted {
		m.showNotice(t.spec.Name+" muted · m unmutes", false)
	} else {
		m.showNotice(t.spec.Name+" unmuted", false)
	}
}

// toggleDrop is `d`: drops the target's service output and mutes it; again keeps output from then on and unmutes.
func (m *tuiModel) toggleDrop(i int) {
	m.ensurePane()
	curSeq, curKept := m.cursorSeq()
	t := m.targets[i]
	t.dropped = !t.dropped
	t.muted = t.dropped
	if t.dropped {
		m.dropOutput(i)
		m.showNotice(t.spec.Name+" output dropped", false)
	} else {
		m.showNotice(t.spec.Name+" output kept again · unmuted", false)
	}
	m.mergedDirty = true
	m.vpDirty = true
	m.ensurePane()
	if curKept {
		m.restoreCursor(curSeq)
	}
	m.recomputeSearch(false)
}

// cursorSeq is the seq of the line the user's cursor is on, and false while the tail follows unplaced.
func (m *tuiModel) cursorSeq() (int, bool) {
	if m.paneLen() == 0 || (m.follow && !m.paneCurPlaced) {
		return 0, false
	}
	return m.visibleSeq[m.rowInfo(m.paneCursorRow()).raw], true
}

// restoreCursor puts the cursor back on the line with seq, or on the pane's last line when that line is gone.
func (m *tuiModel) restoreCursor(seq int) {
	for raw, s := range m.visibleSeq {
		if s == seq {
			m.setPaneCursor(m.lineRow(raw))
			return
		}
	}
	m.paneCurPlaced = false
	if n := m.paneLen(); n > 0 {
		m.setPaneCursor(n - 1)
	}
}

// dropOutput forgets the target's service output and keeps its rules, which still mark the runs.
func (m *tuiModel) dropOutput(i int) {
	t := m.targets[i]
	kept := make([]mergedLine, 0, len(t.lines))
	var gone []mergedLine
	for _, ml := range t.lines {
		if !ml.sep.isSep() {
			gone = append(gone, ml)
			continue
		}
		kept = append(kept, ml)
		if rec := ml.sep.build; rec != nil {
			rec.out = nil
			delete(m.expanded, ml.seq)
		}
	}
	m.evictLines(gone, m.streamsTo(i))
	t.lines = kept
	m.dispDirty = true
}

// showHint posts a transient message to the pane's bottom line.
func (m *tuiModel) showHint(text string) {
	m.hint = text
	m.hintUntil = m.now.Add(hintDuration)
}

// cancelBuild drops a running build's flags, and its rule reads cancelled. A joined target only leaves its owner's rule,
// and an owner leaving a run that keeps going hands the rule to its next member.
func (m *tuiModel) cancelBuild(i int) {
	t := m.targets[i]
	if rec := t.buildRule; rec != nil && rec.phase == buildRunning {
		switch {
		case rec.owner != t.spec.Name:
			t.buildRule = nil
			rec.members = slices.DeleteFunc(rec.members, func(n string) bool { return n == t.spec.Name })
			m.syncBuildRule(rec)
		case len(rec.members) > 1:
			t.buildRule = nil
			m.handOver(rec, t.spec.Name)
		default:
			m.dropBuild(rec, "cancelled")
		}
	}
	t.buildOwner = ""
	m.setBuildState(i, false, t.buildFailed)
}

// handOver makes the next member of a shared run its owner after from leaves; the rule and its output carry on.
func (m *tuiModel) handOver(rec *buildRec, from string) {
	rec.members = slices.DeleteFunc(rec.members, func(n string) bool { return n == from })
	heir := slices.IndexFunc(rec.members, func(n string) bool { return m.targetIndex(n) >= 0 })
	if heir < 0 {
		m.dropBuild(rec, "cancelled")
		return
	}
	rec.owner = rec.members[heir]
	m.targets[m.targetIndex(rec.owner)].buildRule, m.targets[m.targetIndex(rec.owner)].buildOwner = rec, ""
	for _, n := range rec.members {
		if fi := m.targetIndex(n); fi >= 0 && n != rec.owner {
			m.targets[fi].buildOwner = rec.owner
		}
	}
	m.syncBuildRule(rec)
}

// newConfigTUI is the TUI for a loaded config. A Makefile-only config opens the finder, the only way to pick its targets.
func newConfigTUI(targets []Target, cfg *Config) *tuiModel {
	m := newTUI(targets)
	m.group.configPath = cfg.Path
	m.missing = cfg.Missing
	m.queries = cfg.Queries
	m.group.queries = cfg.Queries
	if cfg.MakeOnly {
		m.finder = finderState{open: true}
	}
	// Once at startup: a reload must keep the scope the user chose.
	if r, ok := m.cursorRow(); ok {
		m.applyRowScope(r)
	}
	// The startup scope is automatic, so a later +name reload must still start new targets.
	m.group.setSoloGuard(false)
	return m
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

// openTarget is ctrl+o: the target's open: value, a URL or a command. Log output never supplies a link.
func (m *tuiModel) openTarget(i int) tea.Cmd {
	t := m.targets[i]
	value := t.spec.Open
	if value == "" {
		m.showNotice(t.spec.Name+" has nothing to open · add open: in rerun.yml", false)
		return nil
	}
	if !isOpenLink(value) {
		return runOpenCommandCmd(value, t.spec.Dir)
	}
	if err := openURL(value); err != nil {
		m.showNotice(fmt.Sprintf("rerun: open %s: %v", value, err), true)
	}
	return nil
}

// targetLines is the target's stored output lines, oldest first.
func (m *tuiModel) targetLines(i int) []string {
	out := make([]string, 0, len(m.targets[i].lines))
	for _, ml := range m.targets[i].lines {
		out = append(out, ml.text)
	}
	return out
}

// addLine appends one line to a target's stream and to the merged view when
// that target streams into it. A dropped target stores nothing, so solo shows nothing either.
func (m *tuiModel) addLine(i int, line string) {
	if m.targets[i].dropped {
		return
	}
	m.addRow(i, line, sepLine{})
}

// refreshRule rewrites a rule's stored raw text, so copy and search match what it now draws.
func (m *tuiModel) refreshRule(rr *restartRule) {
	raw := restartedRule(rr).raw()
	if i := m.targetIndex(rr.name); i >= 0 {
		lines := m.targets[i].lines
		for k := range lines {
			if lines[k].sep.restart == rr {
				lines[k].text = raw
			}
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

// Returns the seq so a build rule can find itself to rewrite later.
func (m *tuiModel) addRow(i int, line string, sep sepLine) int {
	t := m.targets[i]
	m.nextSeq++
	ml := mergedLine{target: i, name: t.spec.Name, text: line, seq: m.nextSeq, obj: isJSONObject(line), sep: sep, at: m.arrivalAt()}
	if ml.obj {
		ml.lvl = jsonLevel(line)
	}
	t.lines = append(t.lines, ml)
	m.trimTarget(i)
	if m.streamsTo(i) {
		m.showLine(ml)
		if !m.follow {
			m.pending++ // only lines the pane actually holds
		}
	} else {
		m.mergedDirty = true
	}
	m.vpDirty = true
	return ml.seq
}

// The rule is in the stream before any output, so output always has a row to fold under.
func (m *tuiModel) newBuildRule(i int, files []string) *buildRec {
	name := m.targets[i].spec.Name
	rec := &buildRec{name: name, owner: name, members: []string{name}, files: files, cmd: oneLine(m.targets[i].spec.Build)}
	s := rec.sep()
	rec.seq = m.addRow(i, s.raw(), s)
	return rec
}

// syncBuildRule rewrites a build's rule where it sits, so one build keeps one line.
// It reports whether the rule is still in the stream.
func (m *tuiModel) syncBuildRule(rec *buildRec) bool {
	s := rec.sep()
	found := false
	if i := m.targetIndex(rec.name); i >= 0 {
		lines := m.targets[i].lines
		for k := len(lines) - 1; k >= 0; k-- {
			if lines[k].seq == rec.seq {
				lines[k].text, lines[k].sep = s.raw(), s
				found = true
				break
			}
		}
	}
	m.mergedDirty = true
	m.vpDirty = true
	return found
}

// sharedRow is the owner's running rule that a target joins, when the event starts the owner's current run.
func (m *tuiModel) sharedRow(t *tuiTarget, ev event) *buildRec {
	if ev.owner == "" || ev.owner == t.spec.Name {
		return nil
	}
	oi := m.targetIndex(ev.owner)
	if oi < 0 {
		return nil
	}
	o := m.targets[oi]
	rec := o.buildRule
	if rec == nil || rec.phase != buildRunning || !o.buildStart.Equal(t.buildStart) {
		return nil
	}
	return rec
}

// A build that never finished reads as dropped, not stuck at building.
func (m *tuiModel) dropBuild(rec *buildRec, reason string) {
	rec.phase, rec.reason = buildDropped, reason
	m.syncBuildRule(rec)
}

func (m *tuiModel) showLine(ml mergedLine) {
	m.visible = append(m.visible, m.prefixed(ml))
	m.visibleRaw = append(m.visibleRaw, ml.text)
	m.visibleIdx = append(m.visibleIdx, ml.target)
	m.visibleCells = append(m.visibleCells, cellCache{})
	m.visibleSeq = append(m.visibleSeq, ml.seq)
	m.visibleObj = append(m.visibleObj, ml.obj)
	m.visibleSep = append(m.visibleSep, ml.sep)
	m.visibleLvl = append(m.visibleLvl, ml.lvl)
	m.visibleAt = append(m.visibleAt, ml.at)
	m.dispDirty = true
	// The bar's kept/total counts track arrivals even without a filter.
	m.paneSelDirty = true
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
	return m.width - leftW - 1 - m.stampGutter()
}

// stampW is the cells a line's clock time and its space take in the pane's left gutter.
const stampW = len("15:04:05 ")

// stampGutter is the width of the pane's time gutter: stampW when there is room to spare, else 0.
func (m *tuiModel) stampGutter() int {
	if m.width <= 0 {
		return 0
	}
	leftW, _ := m.layoutWidths()
	if m.width-leftW-1 < stampW+20 {
		return 0
	}
	return stampW
}

// appendLimit keeps the newest limit lines. The slice is re-sliced rather than
// shifted, so a line costs amortized O(1) and the backing array stays bounded.
func appendLimit[T any](lines []T, line T, limit int) []T {
	if len(lines) >= limit {
		lines = lines[len(lines)-limit+1:]
	}
	return append(lines, line)
}

// arrivalAt stamps a new line: the batch's shared second, or a fresh read outside a batch.
func (m *tuiModel) arrivalAt() uint32 {
	if m.inBatch {
		return m.batchAt
	}
	return m.stampNow()
}

// stampNow is the whole seconds since the TUI started.
func (m *tuiModel) stampNow() uint32 {
	now := time.Now
	if m.clock != nil {
		now = m.clock
	}
	return uint32(max(0, now().Sub(m.startAt)) / time.Second)
}

// stampText is the wall-clock time of an arrival second.
func (m *tuiModel) stampText(at uint32) string {
	return m.startAt.Add(time.Duration(at) * time.Second).Format("15:04:05")
}

func (m *tuiModel) applyEvents(events []event) tea.Cmd {
	m.batchAt, m.inBatch = m.stampNow(), true
	defer func() { m.inBatch = false }()
	var cmds []tea.Cmd
	for _, ev := range events {
		cmds = append(cmds, m.applyEvent(ev))
	}
	if len(m.pills) > 0 {
		m.recomputeSearch(false)
	}
	m.fitEvents()
	cmds = append(cmds, m.startSpin())
	return tea.Batch(cmds...)
}

func (m *tuiModel) applyEvent(ev event) tea.Cmd {
	switch ev.kind {
	case evConfigErr:
		m.showNotice(ev.line, true)
		if ev.err != nil {
			m.cfgErr = printable(ev.err.Error(), true)
		}
		return nil

	case evConfigOK:
		m.cfgErr = ""
		return nil

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
		return nil

	case evNotice:
		m.showNotice(ev.line, ev.err != nil)
		return nil
	}

	i := m.targetIndex(ev.target)
	if i < 0 {
		return nil
	}
	t := m.targets[i]

	switch ev.kind {
	case evOutput:
		m.addLine(i, ev.line)

	case evChanged:
		// The batch writes no pane line; the pane header and the feed show it.
		at := eventTime(ev, m.now)
		t.lastChange = ev.change
		t.restartBatch = 0
		if id := m.recordFeed(ev.change, at, t.spec); ev.restarts {
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
		t.loopFile = ""
		t.buildStart = eventTime(ev, m.now)
		t.buildOwner = ""
		m.setBuildState(i, true, false)
		if old := t.buildRule; old != nil && old.owner == t.spec.Name {
			if old.phase == buildRunning {
				m.dropBuild(old, "replaced")
			}
			// Only a failed build keeps its output; the latest one keeps its own.
			if old.phase != buildBroke {
				old.out = nil
				m.dispDirty = true
			}
		}
		if rec := m.sharedRow(t, ev); rec != nil {
			// The owner's rule already shows this run, so the target joins it instead of adding one.
			t.buildRule, t.buildOwner = rec, ev.owner
			if !slices.Contains(rec.members, t.spec.Name) {
				rec.members = append(rec.members, t.spec.Name)
			}
			m.syncBuildRule(rec)
			break
		}
		t.buildRule = m.newBuildRule(i, ev.files)

	case evBuildOutput:
		// Stale output belongs to no shown build; a joined target's copy would print the owner's rule twice.
		if rec := t.buildRule; !t.dropped && rec != nil && rec.owner == t.spec.Name && rec.phase == buildRunning && ev.buildAt.Equal(t.buildStart) {
			rec.out = appendLimit(rec.out, printable(ev.line, false), buildLogCap)
			m.dispDirty = m.dispDirty || m.expanded[rec.seq]
			m.vpDirty = true
		}

	case evBuildOK:
		if t.exitLabel == "build failed" {
			t.exitLabel = ""
		}
		at := eventTime(ev, m.now)
		t.buildOKAt = at
		t.buildOwner = ""
		m.setBuildState(i, false, false)
		if rec := t.buildRule; rec != nil && rec.phase == buildRunning && rec.owner == t.spec.Name {
			rec.phase, rec.dur = buildPassed, ev.dur
			m.syncBuildRule(rec)
		}
		if rec := t.buildRule; rec != nil && rec.owner != t.spec.Name {
			t.buildRule = nil
		}

	case evBuildCancelled:
		m.cancelBuild(i)

	case evBuildFailed:
		// The failure opens its rule, so the reason and the output show without a click.
		up := t.runStarted && t.state.up()
		m.setBuildState(i, false, true)
		t.buildOwner = ""
		t.restartBatch = 0
		// A joined target's failure is the owner's rule's to show, once.
		if rec := t.buildRule; rec == nil || rec.owner == t.spec.Name {
			if rec == nil || rec.phase != buildRunning {
				rec = m.newBuildRule(i, ev.files)
				t.buildRule = rec
			}
			rec.phase, rec.reason = buildBroke, buildReason(ev)
			rec.note = "nothing running"
			if up {
				rec.note = "still running the old build"
			}
			if m.syncBuildRule(rec) && !t.dropped {
				m.expanded[rec.seq] = true
			}
		}
		if rec := t.buildRule; rec != nil && rec.owner != t.spec.Name {
			t.buildRule = nil
		}
		if !up {
			t.state = stateExited
			t.exitCode = -1
			t.exitLabel = "build failed"
			t.lastRule = nil
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
		// Only a core resume starts a run during a pause, so a start ends the loop.
		t.loopFile = ""
		t.state = stateRunning
		t.lastStart = ev.at
		t.lastBuild = ev.buildDur
		t.runStarted = true
		t.exitCode = -1
		t.exitLabel = ""
		t.backoffUntil = time.Time{}
		t.health = ""
		if t.spec.Health != "" {
			t.health = healthStarting
		}
		if ev.restart {
			rr := &restartRule{name: t.spec.Name, at: eventTime(ev, m.now), files: ev.files, built: ev.buildDur, upFor: ev.prevUp, cmd: oneLine(t.spec.Cmd)}
			t.lastRule = rr
			m.addRule(i, restartedRule(rr))
			if m.follow && m.solo == i && m.schemaSel == "" {
				m.pinSeq, m.pinGap = m.nextSeq, false
			}
		} else {
			t.lastRule = nil
			m.addRule(i, startedRule(t.spec.Name, oneLine(t.spec.Cmd)))
		}
		if t.restartBatch != 0 {
			m.feedRestart(t, eventTime(ev, m.now))
		}

	case evExited:
		// A swap's exit draws nothing; the ↻ rule at the next start stands for it.
		if ev.restart {
			return nil
		}
		t.lastRule = nil
		t.state = stateExited
		t.exitCode = ev.code
		t.exitedAt = eventTime(ev, m.now)
		t.exitLabel = exitLabel(ev.code, ev.err)
		m.addRule(i, exitRule(t, ev, m.now))
		t.runStarted = false

	case evStopped:
		// Someone stopped the service outside rerun, so it reads as stopped, not exited.
		t.loopFile = ""
		t.state = stateStopped
		t.exitLabel = ""
		t.restartBatch = 0
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
		m.showNotice(fmt.Sprintf("rerun: %s: %v", t.spec.Name, ev.err), true)

	case evLoop:
		if len(ev.files) > 0 {
			t.loopFile = ev.files[0]
		}
		m.showNotice(fmt.Sprintf("rerun: loop suspected — %s paused after %d× re-triggered by %s; add it to except, or change another file to resume", t.spec.Name, loopCycles, ev.line), true)

	case evCleared:
		// The TUI owns the screen; the clear bytes belong to the plain path.
	}
	return nil
}

// rebuildRows adopts a reconciled target list. Rows are matched by name, so a
// row keeps its stream and its mute across a reload; only a name that just
// appeared gets a new row.
func (m *tuiModel) rebuildRows(c *configChange) {
	m.missing = c.missing
	m.queries = c.queries
	old := m.targets
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
		row, exists := byName[spec.Name]
		if !exists {
			row = &tuiTarget{runner: c.runners[i], state: initialState(spec), exitCode: -1, lastStart: m.now, muted: spec.mutedAtStart()}
			if i < len(c.stopped) && c.stopped[i] && row.state == stateStarting {
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

	// A line whose target left the config keeps its text and its prefix, and
	// loses only its row: it stops answering to mute and solo.
	for _, t := range old {
		if _, ok := index[t.spec.Name]; ok {
			continue
		}
		for k := range t.lines {
			t.lines[k].target = -1
		}
		m.orphans = append(m.orphans, t.lines...)
	}
	if len(c.targets) < len(old) {
		slices.SortFunc(m.orphans, func(a, b mergedLine) int { return a.seq - b.seq })
		m.trimOrphans()
	}

	m.targets = rows
	for i := range rows {
		m.trimTarget(i)
	}
	m.applyEdits()
	m.solo = -1
	if j, ok := index[soloName]; ok {
		m.solo = j
	}
	m.schemaSel = ""
	if schema != "" && m.hasGroups() && len(m.schemaMembers(schema)) > 0 {
		m.schemaSel = schema
	}
	m.cursor = min(m.cursor, max(0, m.listLen()-1))
	found := false
	for i, r := range m.listRows() {
		if m.rowKey(r) == cursorKey {
			m.cursor, found = i, true
			break
		}
	}
	// A reload may fold the cursor's row away; follow it to its header.
	if i := m.foldingHeader(cursorKey); !found && i >= 0 {
		m.cursor = i
	}
	m.refreshNames()
	m.hover = -1
	m.lastClick = -1
	m.reresolveSaved()
	m.mergedDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
}

// reresolveSaved re-runs resolveSaved on the committed pills after a reload:
// the queries may have changed under them. A pill whose name is gone is
// dropped and named in a notice, never left matching stale pills.
func (m *tuiModel) reresolveSaved() {
	if len(m.pills) == 0 {
		return
	}
	var dropped []string
	kept := m.pills[:0]
	for _, p := range m.pills {
		if p.saved == "" {
			kept = append(kept, p)
			continue
		}
		one := []pill{p}
		if err := m.resolveSaved(one); err != nil {
			dropped = append(dropped, "@"+p.saved)
			continue
		}
		kept = append(kept, one[0])
	}
	m.pills = kept
	if len(dropped) > 0 {
		m.showNotice("reload dropped "+strings.Join(dropped, ", ")+": unknown query", false)
	}
	m.paneSelDirty = true
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

// filterOn reports whether a filter-kind pill or an open draft narrows the pane.
func (m *tuiModel) filterOn() bool {
	return m.draftOn || slices.ContainsFunc(m.pills, func(p pill) bool { return p.kind == pillFilter })
}

// filterActive reports whether any pill narrows the pane.
func (m *tuiModel) filterActive() bool { return m.filterOn() }

// rebuildPaneSel re-derives the visible subset for the current filters and
// refreshes the bar's cached line counts. The filter expressions are compiled
// once per edit, never per line. Run rules are not output: they stay visible
// but never count.
func (m *tuiModel) rebuildPaneSel() {
	m.paneSelDirty = false
	m.dispDirty = true
	m.paneSel = m.paneSel[:0]
	m.countKept, m.countTotal = 0, 0
	if !m.filterActive() {
		for i := range m.visible {
			if !m.visibleSep[i].isSep() {
				m.countTotal++
			}
		}
		m.countKept = m.countTotal
		return
	}
	keep := m.lineKeeper()
	for i := range m.visible {
		// Run rules are not output, so a filter must not swallow the boundaries.
		if m.visibleSep[i].isSep() {
			m.paneSel = append(m.paneSel, i)
			continue
		}
		m.countTotal++
		if keep(m.visibleRaw[i]) {
			m.countKept++
			m.paneSel = append(m.paneSel, i)
		}
	}
}

// lineKeeper is the per-line predicate for the filter-kind pills and the open
// input's draft. The pill set decodes a JSON line once for every pill.
func (m *tuiModel) lineKeeper() func(string) bool {
	var filters []pill
	for _, p := range m.pills {
		if p.kind == pillFilter {
			filters = append(filters, p)
		}
	}
	set := pillSetOf(filters)
	draft, draftOn := m.draft, m.draftOn
	return func(line string) bool {
		if !set.match(line) {
			return false
		}
		if draftOn && !draft.match(line) {
			return false
		}
		return true
	}
}

// recomputeSearch re-sweeps the pane's raw lines for pill hits: a line is a
// hit when a highlight-mode pill matches it or any pill paints a span in it.
// One pass over at most lineCap lines, run on edits and arrivals, never per frame.
// reset parks the hit at the first match in view; otherwise the current hit is
// kept when it still matches.
func (m *tuiModel) recomputeSearch(reset bool) {
	m.ensurePane()
	m.searchMatches = m.searchMatches[:0]
	if len(m.pills) == 0 {
		m.searchRow = -1
		return
	}
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
		if m.lineHit(m.visibleRaw[raw]) {
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
		if r >= m.vp.YOffset() {
			m.searchRow = r
			break
		}
	}
}

// pillHits reports whether the pill hits the line: a painted span, or a raw
// yq match, which has no span to paint.
func pillHits(p pill, root *yqlib.CandidateNode, line string) bool {
	if len(p.spansRoot(root, line)) > 0 {
		return true
	}
	if p.saved != "" {
		return slices.ContainsFunc(p.sub, func(s pill) bool { return pillHits(s, root, line) })
	}
	return !p.neg && p.op == pillOpYq && p.matchRoot(root, line)
}

// lineHit reports whether any pill marks the line, decoding JSON at most once.
func (m *tuiModel) lineHit(line string) bool {
	var root *yqlib.CandidateNode
	decoded := false
	for _, p := range m.pills {
		if !decoded && p.needsJSON() {
			root = decodeJSONLine(line)
			decoded = true
		}
		if p.kind == pillHighlight && p.matchRoot(root, line) {
			return true
		}
		if pillHits(p, root, line) {
			return true
		}
	}
	return false
}

// lineSpans merges every pill's spans in the raw line, decoding JSON at most once.
func (m *tuiModel) lineSpans(line string) [][2]int {
	var root *yqlib.CandidateNode
	decoded := false
	var out [][2]int
	for _, p := range m.pills {
		if !decoded && p.needsJSON() {
			root = decodeJSONLine(line)
			decoded = true
		}
		out = append(out, p.spansRoot(root, line)...)
	}
	return mergeSpans(out)
}

// searchJump moves to the next (dir > 0) or previous match and centers it,
// wrapping at the ends. A search with no matches is a no-op.
func (m *tuiModel) searchJump(dir int) {
	if len(m.searchMatches) == 0 {
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
	maxOff := max(0, len(lines)-m.vp.Height())
	m.vp.SetYOffset(min(max(row-m.vp.Height()/2, 0), maxOff))
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

// pillStatus is the bar's scope, pill count, line counts, and hit position.
func (m *tuiModel) pillStatus() string {
	kept, total := m.matchCounts()
	s := fmt.Sprintf("%s · %s", m.scopeName(), pillCount(len(m.pills)))
	if len(m.searchMatches) > 0 && m.searchRow >= 0 {
		s += " · hit " + m.searchStatus()
	}
	return s + fmt.Sprintf(" · %d/%d lines", kept, total)
}

func pillCount(n int) string {
	if n == 1 {
		return "1 pill"
	}
	return fmt.Sprintf("%d pills", n)
}

// scopeName is the pane scope's label: the solo target, the group, or all.
func (m *tuiModel) scopeName() string {
	if m.solo >= 0 && m.solo < len(m.targets) {
		return m.targets[m.solo].spec.Name
	}
	if m.schemaSel != "" {
		return groupLabel(m.schemaSel)
	}
	return "all"
}

// matchCounts reads the kept/total counts rebuildPaneSel cached; the pane is
// ensured first, so a stale selection is rebuilt here and only here.
func (m *tuiModel) matchCounts() (kept, total int) {
	m.ensurePane()
	return m.countKept, m.countTotal
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
	// at is the second the line arrived, counted from the TUI's start.
	at uint32
}

func (m *tuiModel) prefixed(line mergedLine) string {
	if line.name == "" || line.sep.isSep() {
		return line.text
	}
	return m.namePrefix(line.name) + line.text
}

// singleView reports whether one target fills the pane, so its lines need no name column.
func (m *tuiModel) singleView() bool { return m.solo >= 0 && m.schemaSel == "" }

// namePrefix is a line's name column; "" in single view, blank for name "".
func (m *tuiModel) namePrefix(name string) string {
	if m.singleView() {
		return ""
	}
	w, ok := m.groupW[name]
	if !ok {
		w = m.prefixW
	}
	return padRight(strings.TrimPrefix(name, m.nameCut), w) + "  "
}

// refreshNames sets the name column for the pane's view. A group or import view
// drops the prefix its members share with the key, unless it has no members.
func (m *tuiModel) refreshNames() {
	m.nameCut = ""
	var names []string
	if m.schemaSel != "" {
		for _, i := range m.schemaMembers(m.schemaSel) {
			names = append(names, m.targets[i].spec.Name)
		}
		m.nameCut = sharedNamePrefix(m.schemaSel, names)
	} else {
		for _, t := range m.targets {
			names = append(names, t.spec.Name)
		}
	}
	m.prefixW = 0
	for _, n := range names {
		m.prefixW = max(m.prefixW, displayWidth(strings.TrimPrefix(n, m.nameCut)))
	}
	m.groupW = nil
	if m.schemaSel != "" || !m.hasGroups() {
		return
	}
	byGroup := map[string]int{}
	for _, t := range m.targets {
		byGroup[groupOf(t)] = max(byGroup[groupOf(t)], displayWidth(t.spec.Name))
	}
	m.groupW = make(map[string]int, len(m.targets))
	for _, t := range m.targets {
		m.groupW[t.spec.Name] = byGroup[groupOf(t)]
	}
}

// textCol is the cell where a line's message starts.
func (m *tuiModel) textCol() int { return displayWidth(m.namePrefix("")) }

// lineSource is one scrollback the pane merges.
type lineSource struct {
	lines  []mergedLine
	target int
}

// rebuildMerged is a k-way merge by seq over the scrollback of every target the pane streams.
func (m *tuiModel) rebuildMerged() {
	m.visible = m.visible[:0]
	m.visibleRaw = m.visibleRaw[:0]
	m.visibleIdx = m.visibleIdx[:0]
	m.visibleCells = m.visibleCells[:0]
	m.visibleSeq = m.visibleSeq[:0]
	m.visibleObj = m.visibleObj[:0]
	m.visibleSep = m.visibleSep[:0]
	m.visibleLvl = m.visibleLvl[:0]
	m.visibleAt = m.visibleAt[:0]
	var srcs []lineSource
	total := 0
	for i, t := range m.targets {
		if len(t.lines) == 0 {
			continue
		}
		lines := t.lines
		if !m.streamsTo(i) {
			// A shared build's row also sits in the log of each member the pane shows.
			lines = nil
			for _, ml := range t.lines {
				if ml.sep.build != nil && m.buildStreams(ml.sep.build) {
					lines = append(lines, ml)
				}
			}
		}
		if len(lines) > 0 {
			srcs = append(srcs, lineSource{lines, i})
			total += len(lines)
		}
	}
	if len(m.orphans) > 0 {
		srcs = append(srcs, lineSource{m.orphans, -1})
		total += len(m.orphans)
	}
	m.visible = slices.Grow(m.visible, total)
	for len(srcs) > 0 {
		best := 0
		for k := 1; k < len(srcs); k++ {
			if srcs[k].lines[0].seq < srcs[best].lines[0].seq {
				best = k
			}
		}
		src := &srcs[best]
		ml := src.lines[0]
		ml.target = src.target
		m.showLine(ml)
		if src.lines = src.lines[1:]; len(src.lines) == 0 {
			srcs = slices.Delete(srcs, best, best+1)
		}
	}
	m.mergedDirty = false
	m.stale = 0
	m.paneSelDirty = true // paneSel indexes into the rebuilt visible
	m.dispDirty = true
}

// lineLimit is the most lines the target keeps now.
func (t *tuiTarget) lineLimit() int {
	return t.spec.keep().lineCap()
}

// trimTarget evicts the target's scrollback past its keep: the oldest lines beyond the line cap, then those older than its age.
func (m *tuiModel) trimTarget(i int) {
	t := m.targets[i]
	n := max(0, len(t.lines)-t.lineLimit())
	if age := t.spec.keep().age; age > 0 && !t.dropped {
		now := int64(m.stampNow())
		for n < len(t.lines) && now-int64(t.lines[n].at) > int64(age/time.Second) {
			n++
		}
	}
	m.evictLines(t.lines[:n], m.streamsTo(i))
	t.lines = t.lines[n:]
}

// trimAged evicts lines older than a time-based keep: from targets that went quiet too.
func (m *tuiModel) trimAged() {
	for i, t := range m.targets {
		if t.spec.keep().age > 0 && len(t.lines) > 0 {
			m.trimTarget(i)
		}
	}
}

// trimOrphans bounds the lines of removed rows to the default keep.
func (m *tuiModel) trimOrphans() {
	n := max(0, len(m.orphans)-defaultKeepLines)
	m.evictLines(m.orphans[:n], true)
	m.orphans = m.orphans[n:]
}

// evictLines forgets what hangs off evicted lines. The pane drops one it holds at its front at once;
// from the middle it keeps the copy until enough pile up to make a rebuild cheap.
func (m *tuiModel) evictLines(gone []mergedLine, shown bool) {
	for _, ml := range gone {
		delete(m.expanded, ml.seq)
		delete(m.cellBySeq, ml.seq)
		switch {
		case len(m.visibleSeq) > 0 && m.visibleSeq[0] == ml.seq:
			m.popVisibleFront()
		case shown:
			m.stale++
		}
	}
	if m.stale > max(1000, len(m.visible)/4) {
		m.mergedDirty = true
	}
}

// popVisibleFront drops the pane's oldest line.
func (m *tuiModel) popVisibleFront() {
	m.visible = m.visible[1:]
	m.visibleRaw = m.visibleRaw[1:]
	m.visibleIdx = m.visibleIdx[1:]
	m.visibleCells = m.visibleCells[1:]
	m.visibleSeq = m.visibleSeq[1:]
	m.visibleObj = m.visibleObj[1:]
	m.visibleSep = m.visibleSep[1:]
	m.visibleLvl = m.visibleLvl[1:]
	m.visibleAt = m.visibleAt[1:]
	m.dispDirty = true
	m.paneSelDirty = true
}

// status is the times column: uptime while running, what happened after.
// The build gear has its own cell, so the column never repeats it.
func (m *tuiModel) status(t *tuiTarget) string {
	if t.building {
		return fmtDur(m.now.Sub(t.buildStart))
	}
	if t.buildFailed {
		return "✗ build"
	}
	if t.state.up() && t.health == healthUnhealthy {
		return "unhealthy"
	}
	switch t.state {
	case stateRunning:
		return fmtDur(m.now.Sub(t.lastStart))
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
	case stateOnChange, stateManual:
		return "" // the `·` glyph already says it
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

// copyToClipboard is the one path every copy takes: the terminal's OSC 52 clipboard.
func copyToClipboard(payload string) tea.Cmd {
	return tea.SetClipboard(payload)
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

// copyNotice names a modal copy's outcome for its footer: the lines that reached the clipboard, or why none did.
func copyNotice(payload string, n int, truncated bool) string {
	if payload == "" {
		return "line too long — not copied"
	}
	return copyMessage(n, truncated)
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
