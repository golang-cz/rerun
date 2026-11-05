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

// listLabels is the list as rows: a header as ▾group, a build row as └ build:name, a target by name.
func listLabels(m *tuiModel) []string {
	var out []string
	for _, r := range m.listRows() {
		switch {
		case r.header:
			out = append(out, "▾"+groupLabel(r.schema))
		case r.build:
			out = append(out, "└ build:"+m.targets[r.target].spec.Name)
		default:
			out = append(out, m.targets[r.target].spec.Name)
		}
	}
	return out
}

// listLine is the left column's line that starts with prefix, trimmed.
func listLine(m *tuiModel, prefix string) string {
	for _, line := range leftPane(m) {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func selectBuildRow(m *tuiModel) {
	m.cursor = m.targetRowIndex(0) + 1
	m.liveSelect()
}

func TestBuildRowAppearsWhileBuilding(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	if got, want := listLabels(m), []string{"api", "└ build:api", "web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	m.now = buildAt.Add(4 * time.Second)
	if line := listLine(m, "└ ⚙ build"); !strings.HasSuffix(strings.TrimRight(line, " "), "4s") {
		t.Fatalf("build row = %q, want its 4s elapsed time", line)
	}
}

func TestBuildRowIsIndentedUnderItsGroupedService(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("web", "backend"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	m.now = buildAt.Add(4 * time.Second)
	if got, want := listLabels(m), []string{"▾backend", "api", "└ build:api", "web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	if line := listLine(m, "  └ ⚙ build"); line == "" {
		t.Fatalf("grouped build row is not indented under its service: %q", leftPane(m))
	}
}

func TestBuildRowLeavesAfterSuccessOrCancel(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOK("api", time.Second)})
	if got := listLabels(m); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("after ok, rows = %q", got)
	}

	m.applyEvents([]event{buildStart("api", buildAt), {kind: evBuildCancelled, target: "api"}})
	if got := listLabels(m); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("after cancel, rows = %q", got)
	}
}

func TestFailedBuildKeepsRedRowUntilTheNextBuild(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildFail("api", time.Second)})
	if got, want := listLabels(m), []string{"api", "└ build:api"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after failure, rows = %q, want %q", got, want)
	}
	if line := listLine(m, "└ ✗ build"); line == "" {
		t.Fatalf("failed build row missing its ✗ glyph: %q", leftPane(m))
	}

	m.applyEvents([]event{buildStart("api", buildAt.Add(time.Minute))})
	if line := listLine(m, "└ ⚙ build"); line == "" {
		t.Fatal("a new build did not replace the failed row with ⚙")
	}
	m.applyEvents([]event{buildOK("api", time.Second)})
	if got := listLabels(m); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("after the retry passed, rows = %q", got)
	}
}

func TestStopClearsTheBuildRow(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt), buildFail("api", time.Second)})
	m.stopTarget(0)
	if got := listLabels(m); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("after stop, rows = %q", got)
	}
}

func TestSelectingBuildRowScopesToBuildLinesOnly(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{
		{kind: evStarted, target: "api", at: buildAt},
		{kind: evOutput, target: "api", line: "server up"},
		buildStart("api", buildAt.Add(time.Second)),
		buildOut("api", buildAt.Add(time.Second), "compiling"),
		buildOut("api", buildAt.Add(time.Second), "linking"),
		{kind: evOutput, target: "web", line: "web line"},
	})
	m.View()
	m.cursor = 0
	press(m, "down")

	want := []string{"⚙ api build started " + buildAt.Add(time.Second).Format("15:04:05"), "compiling", "linking"}
	if got := rawPane(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("pane = %q, want the build log %q", got, want)
	}
}

func TestBuildOutputStreamsLiveIntoItsView(t *testing.T) {
	m := newTestModel(t, buildTarget("api"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	selectBuildRow(m)

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
	if got := rawPane(m); len(got) != 1 || !strings.HasPrefix(got[0], "✗ api build failed") {
		t.Fatalf("service pane = %q, want only the failure rule", got)
	}
	selectBuildRow(m)
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
	selectBuildRow(m)
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
	selectBuildRow(m)
	m.applyEvents([]event{buildOut("api", buildAt, "stale")})
	for _, line := range rawPane(m) {
		if line == "stale" {
			t.Fatalf("a line from the replaced build reached the view: %q", rawPane(m))
		}
	}
}

func TestEscFromBuildRowGoesToItsService(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("web", "backend"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	press(m, "down", "down")
	if r, _ := m.cursorRow(); !r.build {
		t.Fatalf("cursor row = %+v, want the build row", r)
	}
	press(m, "esc")
	r, _ := m.cursorRow()
	if r.build || r.header || m.targets[r.target].spec.Name != "api" {
		t.Fatalf("esc left the cursor on %+v, want the api row", r)
	}
	if m.buildView != -1 || m.solo != 0 {
		t.Fatalf("esc scope buildView=%d solo=%d, want -1 and api", m.buildView, m.solo)
	}
}

func TestGroupJumpsSkipBuildRows(t *testing.T) {
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

func TestCursorFollowsItsRowWhenABuildRowAppears(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.cursor = 1
	m.liveSelect()
	m.applyEvents([]event{buildStart("api", buildAt)})
	if r, _ := m.cursorRow(); r.build || m.targets[r.target].spec.Name != "web" {
		t.Fatalf("cursor moved to %+v, want web", r)
	}
}

func TestBuildRowCursorFallsBackToServiceWhenItVanishes(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt)})
	selectBuildRow(m)
	m.applyEvents([]event{buildOK("api", time.Second)})
	if r, _ := m.cursorRow(); r.build || m.targets[r.target].spec.Name != "api" {
		t.Fatalf("cursor on %+v after the row vanished, want api", r)
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

func TestBuildLogStaysUntilTheCursorMoves(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	selectBuildRow(m)
	m.applyEvents([]event{buildOK("api", time.Second)})

	if r, _ := m.cursorRow(); r.build || m.targets[r.target].spec.Name != "api" {
		t.Fatalf("cursor on %+v after success, want the api row", r)
	}
	if title := ansi.Strip(m.titleLine(80)); !strings.Contains(title, "api · build log (done)") {
		t.Fatalf("title = %q, want it to name the finished build log", title)
	}
	started := "⚙ api build started " + buildAt.Format("15:04:05")
	if got := rawPane(m); len(got) != 3 || got[0] != started || got[1] != "compiling" || got[2] != "✓ api build ok · 1s" {
		t.Fatalf("pane after success = %q, want the build log kept", got)
	}

	press(m, "down")
	if m.buildView != -1 {
		t.Fatalf("buildView = %d after the cursor moved, want -1", m.buildView)
	}
	for _, line := range rawPane(m) {
		if line == "compiling" {
			t.Fatalf("build log still shows after the cursor moved: %q", rawPane(m))
		}
	}
}

func TestStoppedBuildLogSaysCancelled(t *testing.T) {
	m := newTestModel(t, buildTarget("api"), target("web"))
	m.applyEvents([]event{buildStart("api", buildAt), buildOut("api", buildAt, "compiling")})
	selectBuildRow(m)
	m.stopTarget(0)

	if r, _ := m.cursorRow(); r.build {
		t.Fatalf("cursor stayed on the build row after stop: %+v", r)
	}
	if title := ansi.Strip(m.titleLine(80)); !strings.Contains(title, "api · build log (cancelled)") {
		t.Fatalf("title = %q, want it to say cancelled", title)
	}
}
