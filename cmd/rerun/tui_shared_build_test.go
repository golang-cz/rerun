package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// sharedBuildTarget runs one shared command, so every target built with it shares one run.
func sharedBuildTarget(name string) Target {
	t := target(name)
	t.Build = "go build ./cmd/app"
	return t
}

// sharedStart is the event a target gets when the run owner launched starts.
func sharedStart(name, owner string) event {
	return event{kind: evBuildStarted, target: name, owner: owner, at: buildAt}
}

// targetCell is a target's row as the left pane draws it, without styling.
func targetCell(t *testing.T, m *tuiModel, name string) string {
	t.Helper()
	m.frame()
	leftW, _ := m.layoutWidths()
	for k, r := range m.listRows() {
		if !r.header && m.targets[r.target].spec.Name == name {
			return ansi.Strip(m.targetLine(k, r, leftW))
		}
	}
	t.Fatalf("no listed row for %q", name)
	return ""
}

func TestSharedBuildIsOneRowNamingEveryTarget(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{
		sharedStart("api", "api"),
		sharedStart("worker", "api"),
		buildOut("api", buildAt, "go: compiling ./cmd/app"),
		buildOut("worker", buildAt, "go: compiling ./cmd/app"),
	})
	if got, want := buildRuleTexts(m), []string{"api +1 building"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
	rec := m.targets[m.targetIndex("api")].buildRule
	if want := []string{"go: compiling ./cmd/app"}; !reflect.DeepEqual(rec.out, want) {
		t.Fatalf("output = %q, want the line once", rec.out)
	}
	if m.targets[m.targetIndex("worker")].buildRule != rec {
		t.Fatal("worker does not point at the owner's rule")
	}

	m.applyEvents([]event{buildOK("api", time.Second), buildOK("worker", time.Second)})
	if got, want := buildRuleTexts(m), []string{"api +1 built 1s"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the build = %q, want %q", got, want)
	}
}

func TestJoiningTargetAddsItsNameToTheOpenRule(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api")})
	if got, want := buildRuleTexts(m), []string{"api building"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules before the join = %q, want %q", got, want)
	}
	m.applyEvents([]event{sharedStart("worker", "api")})
	if got, want := buildRuleTexts(m), []string{"api +1 building"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the join = %q, want %q", got, want)
	}
}

func TestSingleTargetBuildKeepsItsLabel(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"))
	m.applyEvents([]event{sharedStart("api", "api")})
	if got, want := buildRuleTexts(m), []string{"api building"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("building rule = %q, want %q", got, want)
	}
	m.applyEvents([]event{buildOK("api", time.Second)})
	if got, want := buildRuleTexts(m), []string{"api built 1s"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("built rule = %q, want %q", got, want)
	}
}

func TestSharedBuildFailureNamesEveryTarget(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), buildFail("api", time.Second), buildFail("worker", time.Second)})
	if got, want := buildRuleTexts(m), []string{"api +1 build failed · boom"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
}

func TestCancelledJoinerLeavesTheOwnerRule(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), {kind: evBuildCancelled, target: "worker"}})
	if got, want := buildRuleTexts(m), []string{"api building"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
}

func TestFollowerSpinsInStepWithOwnerButDimmer(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("worker"), sharedBuildTarget("api"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api")})
	owner := buildCell(m.targets[m.targetIndex("api")], m.now, 3)
	follower := buildCell(m.targets[m.targetIndex("worker")], m.now, 3)
	if owner.text != follower.text || owner.text != spinnerFrames[3] {
		t.Fatalf("frames = %q, %q, want the same spinner frame", owner.text, follower.text)
	}
	if owner.color != runYellow || follower.color != runYellowDim {
		t.Fatalf("colors = %q, %q, want bright for the owner and dim for the follower", owner.color, follower.color)
	}
	if cell := targetCell(t, m, "worker"); strings.ContainsAny(cell, "↑↓") || spinIndex(cell) < 0 {
		t.Fatalf("worker row = %q, want a spinner and no arrow", cell)
	}
}

func TestFollowerSpinnerGoesBrightWhenItBecomesOwner(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), {kind: evBuildCancelled, target: "api"}})
	if got := buildCell(m.targets[m.targetIndex("worker")], m.now, 0); got.color != runYellow {
		t.Fatalf("new owner color = %q, want bright", got.color)
	}
}

func TestFollowerSpinnerStopsWhenTheBuildEnds(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), buildOK("api", time.Second), buildOK("worker", time.Second)})
	if got := buildCell(m.targets[m.targetIndex("worker")], m.now, 0); got.color == runYellowDim {
		t.Fatalf("worker cell = %+v, want no dim spinner after the build", got)
	}
}

func TestSharedRowIsNamedPerFilteredMember(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"), sharedBuildTarget("web"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), sharedStart("web", "api")})
	for _, c := range []struct {
		solo int
		want string
	}{{-1, "api"}, {0, "api"}, {1, "worker"}, {2, "web"}} {
		m.applyScope(c.solo, "")
		m.frame()
		row := dropStamp(ruleLine(m, "building"))
		if !strings.HasPrefix(row, c.want+" ") || !strings.Contains(row, "+2 ") {
			t.Fatalf("solo %d: row = %q, want it named %s with +2", c.solo, row, c.want)
		}
	}
}

func TestOwnerCancelHandsSharedRowToNextMember(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"), sharedBuildTarget("web"))
	m.applyEvents([]event{
		sharedStart("api", "api"),
		sharedStart("worker", "api"),
		sharedStart("web", "api"),
		buildOut("api", buildAt, "line 1"),
		{kind: evBuildCancelled, target: "api"},
		buildOut("worker", buildAt, "line 2"),
	})
	if got, want := buildRuleTexts(m), []string{"api +1 building"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
	if out := m.targets[m.targetIndex("worker")].buildRule.out; !reflect.DeepEqual(out, []string{"line 1", "line 2"}) {
		t.Fatalf("output = %q, want the old and the new line on the same rule", out)
	}
	m.applyEvents([]event{buildOK("worker", time.Second), buildOK("web", time.Second)})
	if got, want := buildRuleTexts(m), []string{"api +1 built 1s"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the build = %q, want %q", got, want)
	}
}

func TestCancelledJoinerDropsItsRuleLink(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), {kind: evBuildCancelled, target: "worker"}})
	if rule := m.targets[m.targetIndex("worker")].buildRule; rule != nil {
		t.Fatalf("worker still links to the owner's rule after cancelling")
	}
}

func TestJoinedFailureDropsItsRuleLink(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), buildFail("worker", time.Second)})
	if rule := m.targets[m.targetIndex("worker")].buildRule; rule != nil {
		t.Fatalf("worker still links to the owner's rule after failing")
	}
}

func TestJoinedSuccessDropsItsRuleLink(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), buildOK("worker", time.Second)})
	if rule := m.targets[m.targetIndex("worker")].buildRule; rule != nil {
		t.Fatalf("worker still links to the owner's rule after passing")
	}
}

func TestOwnerLeavingHandsRuleToMemberWithARow(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api")})
	rec := m.targets[m.targetIndex("api")].buildRule
	rec.members = []string{"api", "ghost", "worker"}
	m.applyEvents([]event{{kind: evBuildCancelled, target: "api"}})
	if rec.owner != "worker" {
		t.Fatalf("owner = %q, want worker, the first member with a row", rec.owner)
	}
}

func TestOwnerLeavingWithNoListedMemberDropsRule(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"))
	m.applyEvents([]event{sharedStart("api", "api")})
	rec := m.targets[m.targetIndex("api")].buildRule
	rec.members = []string{"api", "ghost"}
	m.applyEvents([]event{{kind: evBuildCancelled, target: "api"}})
	if rec.phase != buildDropped {
		t.Fatalf("rule phase = %v, want dropped when no member has a row", rec.phase)
	}
}
