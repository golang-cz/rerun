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
	if got, want := buildRuleTexts(m), []string{"⚙ go build ./cmd/app · api, worker"}; !reflect.DeepEqual(got, want) {
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
	if got, want := buildRuleTexts(m), []string{"⚙ go build ./cmd/app built 1s · api, worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the build = %q, want %q", got, want)
	}
}

func TestJoiningTargetAddsItsNameToTheOpenRule(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api")})
	if got, want := buildRuleTexts(m), []string{"⚙ api building · go build ./cmd/app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules before the join = %q, want %q", got, want)
	}
	m.applyEvents([]event{sharedStart("worker", "api")})
	if got, want := buildRuleTexts(m), []string{"⚙ go build ./cmd/app · api, worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the join = %q, want %q", got, want)
	}
}

func TestSingleTargetBuildKeepsItsLabel(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"))
	m.applyEvents([]event{sharedStart("api", "api")})
	if got, want := buildRuleTexts(m), []string{"⚙ api building · go build ./cmd/app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("building rule = %q, want %q", got, want)
	}
	m.applyEvents([]event{buildOK("api", time.Second)})
	if got, want := buildRuleTexts(m), []string{"⚙ api built 1s · go build ./cmd/app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("built rule = %q, want %q", got, want)
	}
}

func TestSharedBuildFailureNamesEveryTarget(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), buildFail("api", time.Second), buildFail("worker", time.Second)})
	if got, want := buildRuleTexts(m), []string{"✗ go build ./cmd/app build failed · boom · api, worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
}

func TestCancelledJoinerLeavesTheOwnerRule(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), {kind: evBuildCancelled, target: "worker"}})
	if got, want := buildRuleTexts(m), []string{"⚙ api building · go build ./cmd/app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
}

func TestJoinedTargetArrowPointsUpAtOwnerAbove(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api")})
	if cell := targetCell(t, m, "worker"); !strings.Contains(cell, "↑") {
		t.Fatalf("worker row = %q, want ↑ toward api above it", cell)
	}
	if cell := targetCell(t, m, "api"); strings.ContainsAny(cell, "↑↓") {
		t.Fatalf("owner row = %q, want no arrow", cell)
	}
	m.applyEvents([]event{buildOK("api", time.Second), buildOK("worker", time.Second)})
	if cell := targetCell(t, m, "worker"); strings.ContainsAny(cell, "↑↓") {
		t.Fatalf("worker row after the build = %q, want the arrow gone", cell)
	}
}

func TestJoinedTargetArrowPointsDownAtOwnerBelow(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("worker"), sharedBuildTarget("api"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api")})
	if cell := targetCell(t, m, "worker"); !strings.Contains(cell, "↓") {
		t.Fatalf("worker row = %q, want ↓ toward api below it", cell)
	}
}

func TestFoldedOwnerArrowPointsAtItsMoreRow(t *testing.T) {
	api, worker := sharedBuildTarget("api"), sharedBuildTarget("worker")
	api.Schema, worker.Schema = "ops", "ops"
	api.AutoFold, worker.AutoFold = true, true
	api.Start, worker.Start = startManual, startManual
	m := newTestModel(t, sharedBuildTarget("web"), api, worker)
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("web", "api")})
	if rows := nestedRows(m); !slicesContain(rows, "(+) 2 more") || slicesContain(rows, "api") {
		t.Fatalf("rows = %v, want api folded into the more row", rows)
	}
	if cell := targetCell(t, m, "web"); !strings.Contains(cell, "↓") {
		t.Fatalf("web row = %q, want ↓ toward the folded owner's more row", cell)
	}
}

func slicesContain(xs []string, want string) bool {
	for _, x := range xs {
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
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
	if got, want := buildRuleTexts(m), []string{"⚙ go build ./cmd/app · worker, web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %q, want %q", got, want)
	}
	if out := m.targets[m.targetIndex("worker")].buildRule.out; !reflect.DeepEqual(out, []string{"line 1", "line 2"}) {
		t.Fatalf("output = %q, want the old and the new line on the same rule", out)
	}
	if cell := targetCell(t, m, "worker"); strings.ContainsAny(cell, "↑↓") {
		t.Fatalf("new owner row = %q, want the spinner, not an arrow", cell)
	}
	if cell := targetCell(t, m, "web"); !strings.Contains(cell, "↑") {
		t.Fatalf("web row = %q, want ↑ toward the new owner above it", cell)
	}
	m.applyEvents([]event{buildOK("worker", time.Second), buildOK("web", time.Second)})
	if got, want := buildRuleTexts(m), []string{"⚙ go build ./cmd/app built 1s · worker, web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules after the build = %q, want %q", got, want)
	}
}

func TestFollowerArrowClearsWhenOwnerRulePasses(t *testing.T) {
	m := newTestModel(t, sharedBuildTarget("api"), sharedBuildTarget("worker"))
	m.applyEvents([]event{sharedStart("api", "api"), sharedStart("worker", "api"), buildOK("api", time.Second)})
	if cell := targetCell(t, m, "worker"); strings.ContainsAny(cell, "↑↓") {
		t.Fatalf("worker row = %q, want no arrow once the owner's rule passed", cell)
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
