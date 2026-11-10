package main

import (
	"reflect"
	"testing"
	"time"
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
