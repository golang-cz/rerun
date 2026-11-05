package main

import (
	"io"
	"reflect"
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

// openBuildLog selects a target's row and presses b.
func openBuildLog(m *tuiModel, name string) {
	for i, r := range m.listRows() {
		if !r.header && m.targets[r.target].spec.Name == name {
			m.cursor = i
			break
		}
	}
	m.liveSelect()
	press(m, "b")
}

// leftLineRaw is one left-column line, unstripped of its trailing blanks.
func leftLineRaw(m *tuiModel, y int) string {
	m.View()
	leftW, _ := m.layoutWidths()
	return ansi.Strip(m.leftLine(y, leftW))
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
	m.targets[0].lastStart = buildAt
	m.now = buildAt.Add(4 * time.Second)
	row := func() string { return strings.TrimRight(leftLineRaw(m, 0), " ") }

	if got := row(); !strings.HasSuffix(got, "0:04") {
		t.Fatalf("idle row = %q, want the uptime", got)
	}
	m.applyEvents([]event{buildStart("api", buildAt)})
	if got := row(); !strings.HasPrefix(got, "● api") || !strings.HasSuffix(got, "⚙ 4s") {
		t.Fatalf("building row = %q, want the run mark and ⚙ 4s", got)
	}
	m.now = buildAt.Add(64 * time.Second)
	if got := row(); !strings.HasSuffix(got, "⚙ 1m4s") {
		t.Fatalf("long build row = %q, want ⚙ 1m4s", got)
	}
	m.applyEvents([]event{buildFail("api", time.Second)})
	if got := row(); !strings.HasSuffix(got, "✗ build") {
		t.Fatalf("failed row = %q, want ✗ build", got)
	}
}

func TestBuildStatusKeepsColumnWidth(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.targets[0].lastStart = buildAt
	m.now = buildAt.Add(4 * time.Second)
	leftW, _ := m.layoutWidths()
	statusCell := func(line string) string { return strings.TrimSpace(sliceCells(line, leftW-statusW, leftW)) }
	m.applyEvents([]event{buildStart("api", buildAt)})
	m.now = buildAt.Add(64 * time.Second)
	long := leftLineRaw(m, 0)
	m.applyEvents([]event{buildFail("api", time.Second)})
	failed := leftLineRaw(m, 0)
	for want, line := range map[string]string{"⚙ 1m4s": long, "✗ build": failed} {
		if displayWidth(line) != leftW || statusCell(line) != want {
			t.Fatalf("row = %q, want status %q in the %d-cell column", line, want, leftW)
		}
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
	if got := m.status(tg); !strings.HasPrefix(got, "⚙") {
		t.Fatalf("status on the next build start = %q, want ⚙", got)
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

func TestClickingTheStatusOpensTheBuildLog(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	m.View()
	leftW, _ := m.layoutWidths()
	m.Update(mouse(leftW-2, 0, false))

	if m.buildView != 0 {
		t.Fatalf("buildView = %d after the status click, want api", m.buildView)
	}
	if got := rawPane(m); got[len(got)-1] != "compiling" {
		t.Fatalf("pane = %q, want the build log", got)
	}
	if title := ansi.Strip(m.titleLine(80)); !strings.Contains(title, "api · build log (building)") {
		t.Fatalf("title = %q", title)
	}
}

func TestBKeyOpensAndClosesTheBuildLog(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{{kind: evStarted, target: "api", at: buildAt}, {kind: evOutput, target: "api", line: "server up"}})
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})

	press(m, "b")
	if m.buildView != 0 || m.solo != 0 {
		t.Fatalf("b left buildView=%d solo=%d, want the api build log", m.buildView, m.solo)
	}
	press(m, "b")
	if m.buildView != -1 || m.solo != 0 {
		t.Fatalf("second b left buildView=%d, want the service log", m.buildView)
	}
	press(m, "b")
	press(m, "esc")
	if m.buildView != -1 || m.solo != 0 {
		t.Fatalf("esc left buildView=%d solo=%d, want the api service log", m.buildView, m.solo)
	}
	for _, line := range rawPane(m) {
		if line == "compiling" {
			t.Fatalf("service log holds build output: %q", rawPane(m))
		}
	}
}

func TestMovingTheCursorLeavesTheBuildLog(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	openBuildLog(m, "api")
	press(m, "down")
	if m.buildView != -1 || m.solo != 1 {
		t.Fatalf("moving to web left buildView=%d solo=%d", m.buildView, m.solo)
	}
}

func TestBuildLogTitleNamesTheState(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	title := func() string { return ansi.Strip(m.titleLine(80)) }
	m.applyEvents([]event{buildStart("api", buildAt)})
	openBuildLog(m, "api")
	if got := title(); !strings.Contains(got, "api · build log (building)") {
		t.Fatalf("building title = %q", got)
	}
	m.applyEvents([]event{buildFail("api", time.Second)})
	if got := title(); !strings.Contains(got, "api · build log (failed)") {
		t.Fatalf("failed title = %q", got)
	}
	m.applyEvents([]event{buildStart("api", buildAt), buildOK("api", time.Second)})
	if got := title(); !strings.Contains(got, "api · build log (done)") {
		t.Fatalf("done title = %q", got)
	}
	m.applyEvents([]event{buildStart("api", buildAt)})
	m.stopTarget(0)
	if got := title(); !strings.Contains(got, "api · build log (cancelled)") {
		t.Fatalf("cancelled title = %q", got)
	}
}

func TestBuildLogStaysUntilTheCursorMoves(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	openBuildLog(m, "api")
	m.applyEvents([]event{buildOK("api", time.Second)})

	if r, _ := m.cursorRow(); r.header || m.targets[r.target].spec.Name != "api" {
		t.Fatalf("cursor on %+v after success, want the api row", r)
	}
	if title := ansi.Strip(m.titleLine(80)); !strings.Contains(title, "api · build log (done)") {
		t.Fatalf("title = %q, want the finished build log", title)
	}
	started := "⚙ api build started " + buildAt.Format("15:04:05")
	if got := rawPane(m); len(got) != 3 || got[0] != started || got[1] != "compiling" || got[2] != "✓ api build ok · 1s" {
		t.Fatalf("pane after success = %q, want the build log kept", got)
	}
	press(m, "down")
	if m.buildView != -1 {
		t.Fatalf("buildView = %d after the cursor moved, want -1", m.buildView)
	}
}

func TestStoppedBuildLogSaysCancelled(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	openBuildLog(m, "api")
	m.stopTarget(0)

	if r, _ := m.cursorRow(); r.header || m.targets[r.target].spec.Name != "api" {
		t.Fatalf("cursor on %+v after stop, want the api row", r)
	}
	if title := ansi.Strip(m.titleLine(80)); !strings.Contains(title, "api · build log (cancelled)") {
		t.Fatalf("title = %q, want it to say cancelled", title)
	}
}

func TestFailedRuleHintPointsAtB(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildFail("api", time.Second)})
	if got := rawPane(m); len(got) != 1 || !strings.Contains(got[0], "press b for output") {
		t.Fatalf("service pane = %q, want the failure rule with the b hint", got)
	}
}

func TestBuildOutputStreamsLiveIntoItsView(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	openBuildLog(m, "api")

	m.applyEvents([]event{buildOut("api", buildAt, "step \x1b[31m1")})
	if got := rawPane(m); got[len(got)-1] != "step [31m1" {
		t.Fatalf("first line not shown live: %q", got)
	}
	m.applyEvents([]event{buildOut("api", buildAt, "step 2")})
	if got := rawPane(m); got[len(got)-1] != "step 2" {
		t.Fatalf("second line not shown live: %q", got)
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

func TestFailedBuildKeepsRuleInBothViews(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{
		buildStart("api", buildAt),
		buildOut("api", buildAt, "boom"),
		buildFail("api", 6*time.Second),
	})
	openBuildLog(m, "api")
	if got := rawPane(m); got[len(got)-1] != "✗ api build failed · 6s" || got[len(got)-2] != "boom" {
		t.Fatalf("build view = %q, want the boom line then the failure rule", got)
	}
}

func TestNewBuildStartsWithRuleAndKeepsHistory(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	first := buildAt
	second := buildAt.Add(time.Minute)
	m.applyEvents([]event{
		buildStart("api", first),
		buildOut("api", first, "old line"),
		buildFail("api", time.Second),
	})
	openBuildLog(m, "api")
	m.applyEvents([]event{buildStart("api", second, "/x/a.go")})

	got := rawPane(m)
	oldAt, newAt := -1, -1
	for i, line := range got {
		switch {
		case line == "old line":
			oldAt = i
		case strings.HasPrefix(line, "⚙ api build started "+second.Format("15:04:05")+" · a.go"):
			newAt = i
		}
	}
	if oldAt < 0 || newAt < 0 || oldAt > newAt {
		t.Fatalf("build view = %q, want the old line kept above the new rule", got)
	}
	if m.pinSeq == 0 {
		t.Fatal("the new build's rule is not pinned to the top")
	}
}

func TestLinesFromAReplacedBuildAreDropped(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildStart("api", buildAt.Add(time.Minute))})
	openBuildLog(m, "api")
	m.applyEvents([]event{buildOut("api", buildAt, "stale")})
	for _, line := range rawPane(m) {
		if line == "stale" {
			t.Fatalf("a line from the replaced build reached the view: %q", rawPane(m))
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
	if len(got) != 2 || got[0] != "server up" || got[1] != "still serving" {
		t.Fatalf("service pane after a 5000-line build holds %d lines, first %q", len(got), got[0])
	}
	if n := len(m.buildLog["api"]); n != buildLogCap {
		t.Fatalf("build log holds %d lines, want the cap %d", n, buildLogCap)
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
	r.startBuild(ctx, nil)
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
