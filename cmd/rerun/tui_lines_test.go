package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// mergedAll is every retained line in arrival order, the pane's merge without any filter.
func (m *tuiModel) mergedAll() []mergedLine {
	var all []mergedLine
	for _, t := range m.targets {
		all = append(all, t.lines...)
	}
	all = append(all, m.orphans...)
	slices.SortFunc(all, func(a, b mergedLine) int { return a.seq - b.seq })
	return all
}

func keepTarget(name string, lines int) Target {
	t := target(name)
	t.Keep = &keepSpec{lines: lines}
	return t
}

func TestNoisyTargetNeverEvictsAQuietOne(t *testing.T) {
	m := newTestModel(t, keepTarget("noisy", 50), keepTarget("quiet", 50))
	addLines(m, "quiet", "q1", "q2")
	for i := 0; i < 5000; i++ {
		m.addLine(0, "n")
	}
	if got := len(m.targets[1].lines); got != 2 {
		t.Fatalf("quiet kept %d lines, want 2", got)
	}
	if got := len(m.targets[0].lines); got != 50 {
		t.Fatalf("noisy kept %d lines, want its keep of 50", got)
	}
	raw := m.paneRaw()
	if got := raw[0:2]; !reflect.DeepEqual(got, []string{"q1", "q2"}) {
		t.Fatalf("pane starts %v, want the quiet lines in arrival order", got)
	}
	if len(raw) != 52 {
		t.Fatalf("pane holds %d lines, want 52", len(raw))
	}
}

func TestMergeKeepsArrivalOrderAcrossTargets(t *testing.T) {
	m := newTestModel(t, target("a"), target("b"))
	addLines(m, "a", "a1")
	addLines(m, "b", "b1")
	addLines(m, "a", "a2")
	m.mergedDirty = true
	if got, want := m.paneRaw(), []string{"a1", "b1", "a2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pane = %v, want %v", got, want)
	}
	m.targets[1].muted = true
	m.mergedDirty = true
	if got, want := m.paneRaw(), []string{"a1", "a2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("muted pane = %v, want %v", got, want)
	}
}

func TestDropClearsTheOutputAndRestores(t *testing.T) {
	m := newTestModel(t, target("api"))
	for i := 0; i < 500; i++ {
		m.addLine(0, "x")
	}
	m.toggleDrop(0)
	if got := len(m.targets[0].lines); got != 0 || !m.targets[0].muted {
		t.Fatalf("dropped: %d lines, muted=%v, want 0 and muted", got, m.targets[0].muted)
	}
	if m.notice != "api output dropped" {
		t.Fatalf("notice = %q", m.notice)
	}
	m.toggleDrop(0)
	for i := 0; i < 5; i++ {
		m.addLine(0, "y")
	}
	if got := len(m.targets[0].lines); got != 5 || m.targets[0].muted {
		t.Fatalf("restored: %d lines, muted=%v, want 5 and unmuted", got, m.targets[0].muted)
	}
	if m.notice != "api output kept again · unmuted" {
		t.Fatalf("notice = %q", m.notice)
	}
}

// Output that arrives while dropped is never stored, so solo shows none of it.
func TestDroppedOutputIsNotStoredOrShownInSolo(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.addLine(0, "before")
	m.toggleDrop(0)
	m.solo = 0
	m.mergedDirty = true
	m.addLine(0, "during")

	if got := len(m.targets[0].lines); got != 0 {
		t.Fatalf("dropped target stored %d lines, want 0", got)
	}
	if got := m.paneRaw(); len(got) != 0 {
		t.Fatalf("solo pane = %v, want no output", got)
	}
	if got := m.emptyHint(); got != "api output dropped · d to keep" {
		t.Fatalf("empty hint = %q", got)
	}
}

// A dropped target's rules stay: they mark the runs even with the output gone.
func TestDropKeepsTheRules(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.addLine(0, "output")
	m.addRule(0, startedRule("api", "sleep 1"))
	m.toggleDrop(0)

	lines := m.targets[0].lines
	if len(lines) != 1 || !lines[0].sep.isSep() {
		t.Fatalf("dropped lines = %+v, want only the started rule", lines)
	}
}

func TestAgeKeepEvictsOldLines(t *testing.T) {
	spec := target("api")
	spec.Keep = &keepSpec{age: time.Minute}
	m := newTestModel(t, spec)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.startAt = now
	m.addLine(0, "old")
	now = now.Add(2 * time.Minute)
	m.addLine(0, "new")
	if got := m.targets[0].lines; len(got) != 1 || got[0].text != "new" {
		t.Fatalf("lines = %+v, want only the new one", got)
	}
}

func TestMuteNoticeNamesTheTarget(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.toggleMute(0)
	if m.notice != "api muted · m unmutes" {
		t.Fatalf("notice = %q", m.notice)
	}
	m.toggleMute(0)
	if m.notice != "api unmuted" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func rankedTarget(name, group string, rank int) Target {
	t := schemaTarget(name, group)
	t.groupRank = rank
	return t
}

func TestListFollowsGroupsOrderThenImportsThenMakefile(t *testing.T) {
	mk := target("make/lint")
	mk.Import, mk.Kind = "make", makeKind
	imp := target("oms/api")
	imp.Import = "oms"
	m := newTestModel(t,
		mk, imp,
		rankedTarget("db", "data", 2),
		rankedTarget("api", "backend", 1),
		target("web"),
		rankedTarget("worker", "backend", 1),
	)
	want := []string{"web", "▾backend", "api", "worker", "▾data", "db", "▾oms", "oms/api", "▾make", "make/lint"}
	if got := listNames(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}
	// Go-to starts in the same order.
	var names []string
	for _, c := range m.finderCandidates() {
		names = append(names, c.name)
	}
	if want := []string{"web", "backend", "api", "worker", "data", "db", "oms", "oms/api", "make", "make lint"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("go-to = %v, want %v", names, want)
	}
}

func subTarget(name, sub string, rank int) Target {
	t := target("oms/" + name)
	t.Import, t.Sub, t.groupRank = "oms", "oms/"+sub, rank
	t.AutoFold = true
	return t
}

func TestFoldedParentFoldsItsWholeSubtreeIntoOneRow(t *testing.T) {
	m := newTestModel(t,
		subTarget("api", "backend", 1), subTarget("worker", "backend", 1),
		subTarget("web", "apps", 2), subTarget("admin", "apps", 2),
	)
	idle(m)
	if got, want := nestedRows(m), []string{"▸oms", " (+) 4 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	// A failing member shows under its own sub-header; empty sub-headers vanish.
	m.targets[m.targetIndex("oms/web")].buildFailed = true
	if got, want := nestedRows(m), []string{"▾oms", " ▾apps", "  web", " (+) 3 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestOneFoldedMemberIsListedNotCounted(t *testing.T) {
	m := newTestModel(t,
		subTarget("api", "backend", 1), subTarget("worker", "backend", 1), subTarget("web", "apps", 2),
	)
	idle(m)
	m.targets[m.targetIndex("oms/web")].buildFailed = true
	m.targets[m.targetIndex("oms/worker")].buildFailed = true
	got := nestedRows(m)
	for _, r := range got {
		if r == " (+) 1 more" || r == " (+) 0 more" {
			t.Fatalf("rows = %v, a one-member fold must list the member", got)
		}
	}
	if want := []string{"▾oms", " ▾backend", "  api", "  worker", " ▾apps", "  web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// paneRaw is the pane's raw lines as it draws them now.
func (m *tuiModel) paneRaw() []string {
	m.ensurePane()
	return slices.Clone(m.visibleRaw)
}

func TestMutedNameIsItalicGrayEvenUnderTheCursor(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	m.targets[0].muted = true
	m.cursor = 0
	line := drawnRow(t, m, "target:api")
	if !strings.Contains(line, "3;") && !strings.Contains(line, "[3m") {
		t.Fatalf("cursor row = %q, want italic", line)
	}
	if !strings.Contains(line, colorMutedName) {
		t.Fatalf("cursor row = %q, want the light gray name", line)
	}
	m.targets[0].dropped = true
	if line := drawnRow(t, m, "target:api"); !strings.Contains(line, ";9m") && !strings.Contains(line, "[9m") {
		t.Fatalf("dropped row = %q, want strikethrough", line)
	}
	m.targets[0].buildFailed = true
	if line := drawnRow(t, m, "target:api"); !strings.Contains(line, "196") {
		t.Fatalf("failed row = %q, want red over gray", line)
	}
}

// Dropping lines above a placed cursor on a rule keeps the cursor on that rule.
func TestDropKeepsTheCursorOnItsRule(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.addLine(0, "old1")
	m.addLine(0, "old2")
	m.addRule(0, startedRule("api", "sleep 1"))
	m.addRule(0, startedRule("api", "sleep 2"))
	m.frame()
	m.pauseTail()
	m.solo = 0 // a muted target's rules are hidden from group views
	m.mergedDirty = true
	ruleAt := func(cmd string) int {
		m.ensurePane()
		for row := 0; row < m.paneLen(); row++ {
			if r := m.rowInfo(row); r.sub < 0 && m.visibleSep[r.raw].cmd() == cmd {
				return row
			}
		}
		t.Fatalf("no rule %q", cmd)
		return -1
	}
	m.setPaneCursor(ruleAt("sleep 1"))

	m.toggleDrop(0)
	m.frame()
	if got, want := m.paneCursorRow(), ruleAt("sleep 1"); got != want {
		t.Fatalf("cursor row = %d after the drop, want the rule at %d", got, want)
	}
}

// A drop removes every service line, so a search over them has no match left to land on.
func TestDropClearsTheSearchWithItsLines(t *testing.T) {
	m := newTestModel(t, target("api"))
	for i := 0; i < 5; i++ {
		m.addLine(0, "match")
	}
	m.frame()
	press(m, "f")
	typeString(m, "match")
	press(m, "enter")
	if len(m.searchMatches) == 0 {
		t.Fatal("setup: the search matched nothing")
	}

	m.toggleDrop(0)
	if m.searchRow != -1 || len(m.searchMatches) != 0 {
		t.Fatalf("after the drop: searchRow=%d matches=%v, want none", m.searchRow, m.searchMatches)
	}
}
