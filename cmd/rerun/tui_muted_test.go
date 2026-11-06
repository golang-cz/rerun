package main

import (
	"reflect"
	"testing"
)

func mutedSpec(name string, muted bool) Target {
	t := target(name)
	t.Muted = &muted
	return t
}

func TestMutedDefaultStartsTheStreamMuted(t *testing.T) {
	m := newTestModel(t, mutedSpec("web", true), target("api"))
	if !m.targets[0].muted || m.targets[1].muted {
		t.Fatalf("muted = %v %v, want web muted and api not", m.targets[0].muted, m.targets[1].muted)
	}
}

func TestReloadKeepsRuntimeUnmuteAndGivesNewRowsTheConfigDefault(t *testing.T) {
	m := newTestModel(t, mutedSpec("web", true), target("api"))
	m.targets[0].muted = false // unmuted at runtime with m

	worker := newTargetRunner(recTarget("worker", "worker"), &recordingSink{})
	m.applyEvent(event{
		kind: evReconcile,
		line: "── config reloaded ── +worker ──",
		cfg: &configChange{
			targets: []Target{m.targets[0].spec, m.targets[1].spec, mutedSpec("worker", true)},
			runners: []*targetRunner{m.targets[0].runner, m.targets[1].runner, worker},
			stopped: []bool{false, false, false},
		},
	})

	if m.targets[0].muted {
		t.Error("the reload re-muted a row the user unmuted")
	}
	if !m.targets[2].muted {
		t.Error("the new worker row did not take its config's muted: true")
	}
}

// hiddenGroupModel is api and web in group backend, which starts folded, plus an ungrouped worker.
func hiddenGroupModel(t *testing.T) *tuiModel {
	t.Helper()
	api := mutedSpec("api", true)
	api.Schema = "backend"
	api.GroupHide = true
	web := target("web")
	web.Schema = "backend"
	web.GroupHide = true
	return newTestModel(t, api, web)
}

func TestHiddenGroupStartsCollapsed(t *testing.T) {
	m := hiddenGroupModel(t)
	if got, want := nestedRows(m), []string{"▸backend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestHiddenGroupExpandsOnToggle(t *testing.T) {
	m := hiddenGroupModel(t)
	toggleKey(t, m, "backend")
	if got, want := nestedRows(m), []string{"▾backend", " api", " web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestReloadKeepsUserExpandedHiddenGroup(t *testing.T) {
	m := hiddenGroupModel(t)
	toggleKey(t, m, "backend")
	m.applyEvent(event{
		kind: evReconcile,
		line: "── config reloaded ──",
		cfg: &configChange{
			targets: []Target{m.targets[0].spec, m.targets[1].spec},
			runners: []*targetRunner{m.targets[0].runner, m.targets[1].runner},
			stopped: []bool{false, false},
		},
	})
	if got, want := nestedRows(m), []string{"▾backend", " api", " web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after reload rows = %v, want %v", got, want)
	}
}

func TestReloadFoldsNewHiddenGroup(t *testing.T) {
	m := hiddenGroupModel(t)
	toggleKey(t, m, "backend")
	ops := mutedSpec("ops", true)
	ops.Schema = "ops"
	ops.GroupHide = true
	runner := newTargetRunner(recTarget("ops", "ops"), &recordingSink{})
	m.applyEvent(event{
		kind: evReconcile,
		line: "── config reloaded ── +ops ──",
		cfg: &configChange{
			targets: []Target{m.targets[0].spec, m.targets[1].spec, ops},
			runners: []*targetRunner{m.targets[0].runner, m.targets[1].runner, runner},
			stopped: []bool{false, false, false},
		},
	})
	if got, want := nestedRows(m), []string{"▾backend", " api", " web", "▸ops"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after reload rows = %v, want %v", got, want)
	}
}

func TestReloadKeepsExpandedGroupWhenItGainsHide(t *testing.T) {
	api := target("api")
	api.Schema = "backend"
	web := target("web")
	web.Schema = "backend"
	m := newTestModel(t, api, web)
	if got, want := nestedRows(m), []string{"▾backend", " api", " web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	api.GroupHide = true
	web.GroupHide = true
	m.applyEvent(event{
		kind: evReconcile,
		line: "── config reloaded ──",
		cfg: &configChange{
			targets: []Target{api, web},
			runners: []*targetRunner{m.targets[0].runner, m.targets[1].runner},
			stopped: []bool{false, false},
		},
	})
	if got, want := nestedRows(m), []string{"▾backend", " api", " web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after reload rows = %v, want %v", got, want)
	}
}
