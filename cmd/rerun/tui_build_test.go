package main

import (
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

var buildAt = time.Date(2026, 11, 4, 15, 4, 5, 0, time.Local)

func buildTarget(name string) Target {
	t := target(name)
	t.Build = "make " + name
	return t
}

func buildStart(name string, at time.Time, files ...string) event {
	return event{kind: evBuildStarted, target: name, at: at, files: files}
}

func buildOut(name string, at time.Time, line string) event {
	return event{kind: evBuildOutput, target: name, line: line, buildAt: at}
}

func buildOK(name string, dur time.Duration) event {
	return event{kind: evBuildOK, target: name, dur: dur}
}

func buildFail(name string, dur time.Duration) event {
	return event{kind: evBuildFailed, target: name, dur: dur, err: errTest}
}

// listLabels is the list as drawn: a header as ▾group, every target by name.
func listLabels(m *tuiModel) []string {
	var out []string
	for _, r := range m.listRows() {
		if r.header {
			out = append(out, "▾"+groupLabel(r.schema))
			continue
		}
		out = append(out, m.targets[r.target].spec.Name)
	}
	return out
}

// leftLineRaw is one left-column line, unstripped of its trailing blanks.
func leftLineRaw(m *tuiModel, y int) string {
	m.frame()
	leftW, _ := m.layoutWidths()
	return ansi.Strip(m.leftLine(y+1, leftW))
}

func TestBuildNeverChangesTheRowCount(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	want := []string{"api", "web"}
	steps := []event{buildStart("api", buildAt), buildOK("api", time.Second), buildStart("api", buildAt), buildFail("api", time.Second), buildStart("api", buildAt), {kind: evBuildCancelled, target: "api"}}
	for _, ev := range steps {
		m.applyEvents([]event{ev})
		if got := listLabels(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("after %v, rows = %q, want %q", ev.kind, got, want)
		}
	}
}

func TestBuildStatusTextPerState(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.showTimes = true
	m.targets[0].state = stateRunning
	m.targets[0].lastStart = buildAt
	m.now = buildAt.Add(4 * time.Second)
	row := func() string { return strings.TrimRight(leftLineRaw(m, 0), " ") }

	if got := row(); !strings.HasSuffix(got, "4s") {
		t.Fatalf("idle row = %q, want the uptime", got)
	}
	m.applyEvents([]event{buildStart("api", buildAt)})
	if got := row(); !strings.HasPrefix(got, "● api") || !strings.HasSuffix(got, "4s") || spinCount(got) != 1 {
		t.Fatalf("building row = %q, want the run mark, 4s, and one spinner", got)
	}
	m.now = buildAt.Add(64 * time.Second)
	if got := row(); !strings.HasSuffix(got, "1m04s") {
		t.Fatalf("long build row = %q, want 1m04s", got)
	}
	m.applyEvents([]event{buildFail("api", time.Second)})
	if got := row(); !strings.HasSuffix(got, "✗ build") || spinCount(got) != 0 || strings.Count(got, "✗") != 3 {
		t.Fatalf("failed row = %q, want ✗ glyph, ✗ cell and ✗ build, no spinner", got)
	}
}

func TestBuildStatusKeepsColumnWidth(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.showTimes = true
	m.targets[0].lastStart = buildAt
	m.now = buildAt.Add(4 * time.Second)
	leftW, _ := m.layoutWidths()
	statusCell := func(line string) string { return strings.TrimSpace(sliceCells(line, leftW-statusW, leftW)) }
	m.applyEvents([]event{buildStart("api", buildAt)})
	m.now = buildAt.Add(64 * time.Second)
	long := leftLineRaw(m, 0)
	m.applyEvents([]event{buildFail("api", time.Second)})
	failed := leftLineRaw(m, 0)
	for want, line := range map[string]string{"1m04s": long, "✗ build": failed} {
		if displayWidth(line) != leftW || statusCell(line) != want {
			t.Fatalf("row = %q, want status %q in the %d-cell column", line, want, leftW)
		}
	}
}

// The duration must end at the right edge whether the spinner shows or not.
func TestBuildDurationStaysPutWhenGearShows(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.showTimes = true
	m.targets[0].state = stateRunning
	m.targets[0].lastStart = buildAt
	m.now = buildAt.Add(4 * time.Second)
	leftW, _ := m.layoutWidths()
	durCol := func() int {
		line := leftLineRaw(m, 0)
		idx := strings.Index(line, "4s")
		if idx < 0 {
			t.Fatalf("row = %q, want the 4s duration", line)
		}
		return displayWidth(line[:idx+len("4s")])
	}
	idle := durCol()
	m.applyEvents([]event{buildStart("api", buildAt)})
	if building := durCol(); building != idle || idle != leftW-1 {
		t.Fatalf("duration column: idle = %d, building = %d, want both %d", idle, building, leftW-1)
	}
}

func TestBuildFailedStatusHoldsUntilNextSuccessOrStart(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	tg := m.targets[0]
	m.applyEvents([]event{buildStart("api", buildAt), buildFail("api", time.Second)})
	m.stopTarget(0)
	if got := m.status(tg); got != "✗ build" {
		t.Fatalf("status after a stop = %q, want ✗ build until the next build", got)
	}
	m.applyEvents([]event{buildStart("api", buildAt)})
	if got := m.status(tg); strings.Contains(got, "⚙") || strings.Contains(got, "build") {
		t.Fatalf("status on the next build start = %q, want the duration only", got)
	}
	m.applyEvents([]event{buildFail("api", time.Second), buildStart("api", buildAt), buildOK("api", time.Second)})
	if got := m.status(tg); strings.Contains(got, "build") {
		t.Fatalf("status after a successful build = %q, want the uptime", got)
	}
}

func TestRunColorsPerState(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	tg.state = stateRunning
	cases := []struct {
		name  string
		setup func()
		glyph string
		color string
	}{
		{"running", func() {}, "●", runGreen},
		{"starting", func() { tg.state = stateStarting }, "●", colorGray},
		{"healthy", func() { tg.state = stateRunning; tg.health = healthHealthy }, "●", runGreen},
		{"starting probe", func() { tg.health = healthStarting }, "●", colorGray},
		{"unhealthy", func() { tg.health = healthUnhealthy }, "▲", runRed},
	}
	for _, c := range cases {
		tg.health = ""
		tg.state = stateRunning
		c.setup()
		if tg.glyph() != c.glyph || stateColor(tg) != c.color {
			t.Errorf("%s: glyph %q color %q, want %q %q", c.name, tg.glyph(), stateColor(tg), c.glyph, c.color)
		}
	}
	tg.health, tg.state, tg.building = "", stateRunning, true
	if got := statusColor(tg); got != runYellow {
		t.Fatalf("building status color = %q, want %q", got, runYellow)
	}
	tg.building, tg.buildFailed = false, true
	if got := statusColor(tg); got != runRed {
		t.Fatalf("failed status color = %q, want %q", got, runRed)
	}
}

func TestBuildOutputNeverMixesIntoTheServiceStream(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{
		buildStart("api", buildAt),
		buildOut("api", buildAt, "compiling"),
		buildFail("api", time.Second),
	})
	m.cursor = 0
	m.liveSelect()
	for _, line := range rawPane(m) {
		if strings.Contains(line, "compiling") {
			t.Fatalf("service pane shows build output: %q", rawPane(m))
		}
	}
	m.applyEvents([]event{{kind: evOutput, target: "api", line: "server up"}})
	for _, line := range m.visibleRaw {
		if strings.Contains(line, "compiling") {
			t.Fatalf("service stream holds build output: %q", m.visibleRaw)
		}
	}
}

func TestLongBuildLeavesServiceLinesIntact(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{{kind: evOutput, target: "api", line: "server up"}})
	evs := []event{buildStart("api", buildAt)}
	for i := 0; i < 5000; i++ {
		evs = append(evs, buildOut("api", buildAt, "step"))
	}
	m.applyEvents(evs)

	m.cursor = 0
	m.liveSelect()
	m.applyEvents([]event{{kind: evOutput, target: "api", line: "still serving"}})
	got := rawPane(m)
	if len(got) != 3 || got[0] != "server up" || got[2] != "still serving" || !strings.HasPrefix(got[1], "api building") {
		t.Fatalf("service pane after a 5000-line build = %q, want the rule between the service lines", got)
	}
	if n := len(m.targets[0].buildRule.out); n != buildLogCap {
		t.Fatalf("build output holds %d lines, want the cap %d", n, buildLogCap)
	}
}

func TestGroupJumpsSkipOverBuildingServices(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("web", "backend"), schemaTarget("worker", "ops"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	m.cursor = 0
	press(m, "]")
	if r, _ := m.cursorRow(); !r.header || r.schema != "ops" {
		t.Fatalf("] landed on %+v, want the ops header", r)
	}
	press(m, "[")
	if r, _ := m.cursorRow(); !r.header || r.schema != "backend" {
		t.Fatalf("[ landed on %+v, want the backend header", r)
	}
}

func TestPlainSinkStaysQuietForBuildOutput(t *testing.T) {
	s := newPlainSink(io.Discard)
	if got := s.render(buildOut("api", buildAt, "compiling")); got != "" {
		t.Fatalf("plain output = %q, want none", got)
	}
}

func TestTailBufferEmitsEachLineAsItCompletes(t *testing.T) {
	var got []string
	b := &tailBuffer{n: 10, onLine: func(l string) { got = append(got, l) }}
	_, _ = b.Write([]byte("a\nb"))
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("after a partial line, emitted %q", got)
	}
	_, _ = b.Write([]byte("c\r\n"))
	_, _ = b.Write([]byte("d"))
	b.flush()
	if want := []string{"a", "bc", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted %q, want %q", got, want)
	}
	if want := []string{"a", "bc", "d"}; !reflect.DeepEqual(b.tail(), want) {
		t.Fatalf("tail = %q, want %q", b.tail(), want)
	}
}

func TestBuildStreamsItsOutputLiveWithItsStart(t *testing.T) {
	r, log, ctx := buildRunner(t, "echo one; echo two", "sleep 30")
	r.startBuild(nil)
	r.finishBuild(ctx, nextBuild(t, r))

	started := log.kind(evBuildStarted)
	if len(started) != 1 {
		t.Fatalf("evBuildStarted count = %d", len(started))
	}
	var lines []string
	for _, ev := range log.kind(evBuildOutput) {
		if !ev.buildAt.Equal(started[0].at) {
			t.Fatalf("evBuildOutput buildAt = %v, want %v", ev.buildAt, started[0].at)
		}
		lines = append(lines, ansi.Strip(ev.line))
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("streamed lines = %q, want %q", lines, want)
	}
}

func TestStartingAndStoppedDifferByShape(t *testing.T) {
	m := newTestModel(t, target("api"))
	tg := m.targets[0]
	tg.state = stateStarting
	starting := tg.glyph()
	tg.state = stateStopped
	stopped := tg.glyph()
	if starting != "●" || stopped != "○" {
		t.Fatalf("starting draws %q and stopped %q, want a filled ● and a hollow ○", starting, stopped)
	}
}

// buildRuleTexts is each build rule's raw text in the stream, in order.
func buildRuleTexts(m *tuiModel) []string {
	var out []string
	for _, ml := range m.mergedAll() {
		if ml.sep.build != nil {
			out = append(out, ml.text)
		}
	}
	return out
}

func buildRuleSeq(m *tuiModel) int {
	for _, ml := range m.mergedAll() {
		if ml.sep.build != nil {
			return ml.seq
		}
	}
	return -1
}

func TestBuildRuleUpdatesInPlace(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt, "/x/a.go", "/x/b.go"), buildOut("api", buildAt, "compiling")})
	if got := buildRuleTexts(m); len(got) != 1 || got[0] != "api building" {
		t.Fatalf("building rule = %q, want one rule", got)
	}
	m.applyEvents([]event{buildOK("api", 500*time.Millisecond)})
	if got := buildRuleTexts(m); len(got) != 1 || got[0] != "api built 0s" {
		t.Fatalf("rule after the build = %q, want it rewritten in place", got)
	}
	if len(m.mergedAll()) != 1 {
		t.Fatalf("stream holds %d lines, want the one rule", len(m.mergedAll()))
	}
}

func TestEnterShowsBuildCommand(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	press(m, "right", "enter")
	rows := m.paneLines()
	cmd, out := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "$ make api") {
			cmd = i
		}
		if strings.Contains(r, "compiling") {
			out = i
		}
	}
	if cmd < 0 || out < 0 || cmd > out {
		t.Fatalf("expanded build rule rows = %q, want the command before the output", rows)
	}
}

func TestBuildRuleCollapsedWhileBuilding(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.frame()
	if seq := buildRuleSeq(m); m.expanded[seq] || m.paneLen() != 1 {
		t.Fatalf("building rule expanded=%v with %d rows, want collapsed to one row", m.expanded[seq], m.paneLen())
	}
}

func TestBuildFailureAutoExpandsItsRule(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{
		buildStart("api", buildAt, "/x/a.go"),
		buildOut("api", buildAt, "boom"),
		{kind: evBuildFailed, target: "api", dur: 6 * time.Second, code: 2, files: []string{"/x/a.go"}},
	})
	m.frame()
	if got := buildRuleTexts(m); len(got) != 1 || got[0] != "api build failed · exit 2" {
		t.Fatalf("failed rule = %q", got)
	}
	if !m.expanded[buildRuleSeq(m)] {
		t.Fatal("a failed build's rule is collapsed, want it open")
	}
	rows := strings.Join(m.paneLines(), "\n")
	if !strings.Contains(rows, "boom") || !strings.Contains(rows, "nothing running") {
		t.Fatalf("failed rule shows %q, want its output and the run state", rows)
	}
}

func TestEnterTogglesBuildRule(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling"), buildOK("api", time.Second)})
	press(m, "right", "enter")
	if !m.expanded[buildRuleSeq(m)] || m.paneLen() < 2 {
		t.Fatalf("enter did not open the build rule: expanded=%v rows=%d", m.expanded, m.paneLen())
	}
	press(m, "enter")
	if len(m.expanded) != 0 {
		t.Fatal("second enter did not close the build rule")
	}
}

func TestDoubleClickOnBuildIconTogglesRule(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.frame()
	row := m.paneCursorRow()
	doubleClick(m, m.layout.leftW+1+3, m.titleHeight()+row-m.vp.YOffset())
	if !m.expanded[buildRuleSeq(m)] {
		t.Fatal("a click on the build icon did not open the rule")
	}
}

// A single click on a rule places the cursor on it and never expands it.
func TestClickOnARuleLinePlacesTheCursorOnly(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.frame()
	y := m.titleHeight() + m.paneCursorRow() - m.vp.YOffset()

	if copied := clipboardText(t, click(m, paneX(m), y)); copied != "" {
		t.Fatal("the click copied the rule")
	}
	if m.expanded[buildRuleSeq(m)] {
		t.Fatal("a single click expanded the rule")
	}
	if m.paneCurRaw != slices.Index(m.visibleSeq, buildRuleSeq(m)) {
		t.Fatal("a single click did not place the cursor on the rule")
	}
}

// A double-click on a rule line expands it, and a second double-click collapses it.
func TestDoubleClickOnARuleLineTogglesIt(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.frame()
	y := m.titleHeight() + m.paneCursorRow() - m.vp.YOffset()

	if copied := clipboardText(t, doubleClick(m, paneX(m), y)); copied != "" {
		t.Fatal("the double-click copied the rule it opened")
	}
	if !m.expanded[buildRuleSeq(m)] {
		t.Fatal("a double-click did not open the rule")
	}

	doubleClick(m, paneX(m), y)
	if m.expanded[buildRuleSeq(m)] {
		t.Fatal("the second double-click did not close the rule")
	}
}

// A click on a started rule whose command the pane cut short expands it to the full text.
func TestDoubleClickOnACutCommandExpandsIt(t *testing.T) {
	long := "go run ./cmd/api " + strings.Repeat("--flag ", 30) + "end"
	tg := target("api")
	tg.Cmd = long
	m := newTestModel(t, tg)
	m.applyEvents([]event{{kind: evStarted, target: "api", at: buildAt}})
	m.frame()
	y := m.titleHeight() + m.paneCursorRow() - m.vp.YOffset()

	doubleClick(m, paneX(m), y)
	if !m.expanded[m.visibleSeq[0]] {
		t.Fatal("the double-click did not expand the cut command")
	}
	if rows := strings.Join(m.paneLines(), "\n"); !strings.Contains(rows, "--flag end") {
		t.Fatalf("the expansion lacks the full command: %q", rows)
	}
}

func TestExpandedBuildRowListsTriggerFiles(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt, "/x/a.go", "/x/b.go", "/x/c.go")})
	m.frame()
	if rows := strings.Join(m.paneLines(), "\n"); strings.Contains(rows, "/x/c.go") {
		t.Fatalf("files shown before expanding: %q", rows)
	}
	m.toggleExpand(m.paneCursorRow())
	if rows := strings.Join(m.paneLines(), "\n"); !strings.Contains(rows, "files: /x/a.go, /x/b.go, /x/c.go") {
		t.Fatalf("file list not shown after expanding: %q", rows)
	}
}

func TestStartedCommandIsFoldedUntilExpanded(t *testing.T) {
	tg := target("api")
	tg.Cmd = "go run ./cmd/api --flag end"
	m := newTestModel(t, tg)
	m.applyEvents([]event{{kind: evStarted, target: "api", at: buildAt}})
	m.frame()
	row := m.paneCursorRow()
	if rows := strings.Join(m.paneLines(), "\n"); strings.Contains(rows, "--flag end") {
		t.Fatalf("command shown while folded: %q", rows)
	}
	if !m.expandable(row) {
		t.Fatal("a started row with a command is not expandable")
	}
	m.toggleExpand(row)
	if rows := strings.Join(m.paneLines(), "\n"); !strings.Contains(rows, "$ go run ./cmd/api --flag end") {
		t.Fatalf("expansion lacks the command: %q", rows)
	}
}

func TestStartedWithoutCommandIsNotExpandable(t *testing.T) {
	tg := target("api")
	tg.Cmd = ""
	m := newTestModel(t, tg)
	m.applyEvents([]event{{kind: evStarted, target: "api", at: buildAt}})
	m.frame()
	row := m.paneCursorRow()
	if m.expandable(row) {
		t.Fatal("a row with nothing folded shows an expand affordance")
	}
	press(m, "right", "enter")
	if len(m.expanded) != 0 {
		t.Fatal("enter expanded a line with nothing under it")
	}
}

func TestReplacedBuildRuleSaysReplaced(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildStart("api", buildAt.Add(time.Minute))})
	got := buildRuleTexts(m)
	if len(got) != 2 || got[0] != "api build replaced" || got[1] != "api building" {
		t.Fatalf("rules = %q, want the old one replaced and the new one building", got)
	}
}

func TestCancelledBuildRuleSaysCancelled(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), {kind: evBuildCancelled, target: "api"}})
	if got := buildRuleTexts(m); len(got) != 1 || got[0] != "api build cancelled" {
		t.Fatalf("rule = %q, want it to read cancelled", got)
	}
}

func TestBKeyHidesOnlyTheList(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.frame()
	before := len(m.mergedAll())
	press(m, "b")
	if m.solo != -1 || !m.listHidden || len(m.expanded) != 0 || len(m.mergedAll()) != before {
		t.Fatalf("b changed more than the list: solo=%d listHidden=%v expanded=%v", m.solo, m.listHidden, m.expanded)
	}
}

// Only a failed build and the latest build keep their output across many rebuilds.
func TestRebuildsKeepOnlyLatestAndFailedOutput(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	for n := 0; n < 50; n++ {
		at := buildAt.Add(time.Duration(n) * time.Minute)
		evs := []event{buildStart("api", at)}
		for k := 0; k < 300; k++ {
			evs = append(evs, buildOut("api", at, "line"))
		}
		if n%10 == 0 {
			evs = append(evs, event{kind: evBuildFailed, target: "api", dur: time.Second, code: 1})
		} else {
			evs = append(evs, buildOK("api", time.Second))
		}
		m.applyEvents(evs)
	}
	recs, lines := 0, 0
	for _, ml := range m.mergedAll() {
		if ml.sep.build != nil && len(ml.sep.build.out) > 0 {
			recs++
			lines += len(ml.sep.build.out)
		}
	}
	// Five failed builds plus the latest passed one keep output.
	if recs != 6 || lines != 6*buildLogCap {
		t.Fatalf("retained %d records with %d lines, want 6 records with %d lines", recs, lines, 6*buildLogCap)
	}
}

func TestExpansionDropsWhenRuleScrollsOut(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), {kind: evBuildFailed, target: "api", code: 1}})
	seq := buildRuleSeq(m)
	if !m.expanded[seq] {
		t.Fatal("failed rule is not expanded")
	}
	flood := make([]event, 0, defaultKeepLines+5)
	for k := 0; k < defaultKeepLines+5; k++ {
		flood = append(flood, event{kind: evOutput, target: "api", line: "svc"})
	}
	m.applyEvents(flood)
	if len(m.expanded) != 0 {
		t.Fatalf("expanded keeps %d keys after its rule scrolled out", len(m.expanded))
	}
}

func TestBuildEventsAfterScrollOutDoNotLeakExpansion(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	flood := make([]event, 0, defaultKeepLines+5)
	for k := 0; k < defaultKeepLines+5; k++ {
		flood = append(flood, event{kind: evOutput, target: "api", line: "svc"})
	}
	m.applyEvents(flood)
	m.applyEvents([]event{{kind: evBuildFailed, target: "api", code: 1}, buildOK("api", time.Second)})
	if len(m.expanded) != 0 {
		t.Fatalf("expanded holds %d keys for a rule no longer in the stream", len(m.expanded))
	}
}

func TestLateOutputAfterCancelIsIgnored(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "early"), {kind: evBuildCancelled, target: "api"}, buildOut("api", buildAt, "late")})
	rec := m.targets[0].buildRule
	if len(rec.out) != 1 || rec.out[0] != "early" {
		t.Fatalf("output after cancel = %q, want only the early line", rec.out)
	}
}

func TestDoubleClickRuleWhileFiltered(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{
		buildStart("api", buildAt, "/x/a.go", "/x/b.go"),
		{kind: evOutput, target: "web", line: "web line"},
	})
	pills, err := parsePills("building", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.pills = pills
	m.pillsChanged()
	m.frame()
	if !m.filterActive() {
		t.Fatal("the filter is not active")
	}
	row := -1
	for i := 0; i < m.paneLen(); i++ {
		if r := m.rowInfo(i); r.sub < 0 && m.visibleSep[r.raw].build != nil {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("the build rule is not in the filtered pane: %q", rawPane(m))
	}
	doubleClick(m, m.layout.leftW+1+3, m.titleHeight()+row-m.vp.YOffset())
	if !m.expanded[buildRuleSeq(m)] {
		t.Fatal("a double-click on the rule's icon while filtered did not open it")
	}
}

// With one target in view there is no name column, so the build icon is still the rule's first cells.
func TestDoubleClickOnBuildIconInSingleViewTogglesRule(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.applyScope(0, "")
	m.frame()
	row := -1
	for i := 0; i < m.paneLen(); i++ {
		if r := m.rowInfo(i); r.sub < 0 && m.visibleSep[r.raw].build != nil {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("the build rule is not in the single view: %q", rawPane(m))
	}
	doubleClick(m, m.layout.leftW+1+3, m.titleHeight()+row-m.vp.YOffset())
	if !m.expanded[buildRuleSeq(m)] {
		t.Fatal("a double-click on the build icon in single view did not open the rule")
	}
}

func TestBuildCellShowsGearOnlyWhileBuilding(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.now = buildAt
	tg := m.targets[0]
	if c := buildCell(tg, m.now, 0); c.text != " " {
		t.Fatalf("idle build cell = %+v, want blank", c)
	}
	m.applyEvents([]event{buildStart("api", buildAt)})
	if c := buildCell(tg, m.now, 0); !slices.Contains(spinnerFrames, c.text) || c.color != runYellow {
		t.Fatalf("building cell = %+v, want an orange spinner", c)
	}
	m.applyEvents([]event{buildFail("api", time.Second)})
	if c := buildCell(tg, m.now, 0); c.text != "✗" || c.color != runRed {
		t.Fatalf("failed cell = %+v, want a red ✗", c)
	}
	m.applyEvents([]event{buildStart("api", buildAt), buildOK("api", time.Second)})
	if c := buildCell(tg, m.now, 0); c.text != "✓" || c.color != runGreen {
		t.Fatalf("cell after a good build = %+v, want a green ✓", c)
	}
}

// The build cell is reserved while idle, so a build never moves the name.
func TestBuildCellKeepsNameWidth(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.frame()
	leftW, _ := m.layoutWidths()
	idle := leftLineRaw(m, 0)
	m.applyEvents([]event{buildStart("api", buildAt)})
	m.frame()
	building := leftLineRaw(m, 0)
	if w, _ := m.layoutWidths(); w != leftW {
		t.Fatalf("leftW = %d while building, want %d", w, leftW)
	}
	at := func(line string) int { return displayWidth(line[:strings.Index(line, "api")]) }
	if at(idle) != at(building) || spinCount(idle) != 0 || spinCount(building) == 0 {
		t.Fatalf("idle %q and building %q: want the name in one cell, the spinner only while building", idle, building)
	}
}

// The build cell sits one space after the longest visible name, in one column for every row.
func TestBuildCellAlignsAfterLongestName(t *testing.T) {
	m := newTestModel(t, target("service 1"), target("service 2"), target("svc3"))
	m.showTimes = true
	m.applyEvents([]event{buildStart("service 1", buildAt), buildStart("svc3", buildAt)})
	m.frame()
	cellAt := func(i int) int {
		line := leftLineRaw(m, i)
		return displayWidth(line[:spinIndex(line)])
	}
	want := displayWidth("  service 2") + 1
	if got := cellAt(0); got != want {
		t.Fatalf("build cell at %d, want %d (one space after the longest name)", got, want)
	}
	if got := cellAt(2); got != want {
		t.Fatalf("svc3 cell at %d, want %d", got, want)
	}
	if spinCount(leftLineRaw(m, 1)) != 0 {
		t.Fatal("idle row shows a build cell")
	}
	leftW, _ := m.layoutWidths()
	for i := 0; i < 3; i++ {
		line := leftLineRaw(m, i)
		if displayWidth(line) != leftW {
			t.Fatalf("row %d = %q, want %d cells", i, line, leftW)
		}
		if gap := sliceCells(line, want+1, want+3); gap != "  " {
			t.Fatalf("row %d gap after the build cell = %q, want blank cells before the times", i, gap)
		}
	}
}

// spinCount is how many spinner frames a rendered row holds.
func spinCount(s string) int {
	n := 0
	for _, f := range spinnerFrames {
		n += strings.Count(s, f)
	}
	return n
}

// spinIndex is the byte offset of the row's spinner frame, or -1.
func spinIndex(s string) int {
	for _, f := range spinnerFrames {
		if i := strings.Index(s, f); i >= 0 {
			return i
		}
	}
	return -1
}

func TestBuildRowIsConcise(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt, "/x/a.go", "/x/b.go"), buildOK("api", 2*time.Second)})
	m.frame()
	got := strings.TrimSpace(strings.TrimRight(ansi.Strip(ruleLine(m, "built")), "─ "))
	if !strings.HasSuffix(got, "built 2s") || strings.Contains(got, "a.go") || strings.Contains(got, "⚙") {
		t.Fatalf("row = %q, want only the verb and duration", got)
	}
}

// A dropped target's build output stays out: no old lines, no new ones, no auto-open for a failure.
func TestDroppedBuildStoresNoOutputAndDoesNotOpen(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.toggleDrop(0)
	rec := m.targets[0].buildRule
	if rec == nil {
		t.Fatal("the drop removed the build rule")
	}
	if len(rec.out) != 0 || m.expanded[rec.seq] {
		t.Fatalf("drop kept output %q or the expansion %v", rec.out, m.expanded[rec.seq])
	}

	m.applyEvents([]event{buildOut("api", buildAt, "more"), {kind: evBuildFailed, target: "api", dur: time.Second, code: 2, files: []string{"/x/a.go"}}})
	if len(rec.out) != 0 {
		t.Fatalf("a dropped build stored output: %q", rec.out)
	}
	if m.expanded[rec.seq] {
		t.Fatal("a failed build opened its rule for a dropped target")
	}
}
