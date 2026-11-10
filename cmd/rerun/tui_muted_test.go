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
		t.Error("the new worker row did not take its config's mute: true")
	}
}

// hiddenGroupModel is api and web in group backend, which starts folded, plus an ungrouped worker.
func hiddenGroupModel(t *testing.T) *tuiModel {
	t.Helper()
	api := mutedSpec("api", true)
	api.Schema = "backend"
	api.AutoFold = true
	web := target("web")
	web.Schema = "backend"
	web.AutoFold = true
	m := newTestModel(t, api, web)
	idle(m)
	return m
}

func TestFoldGroupStartsFolded(t *testing.T) {
	m := hiddenGroupModel(t)
	if got, want := nestedRows(m), []string{"▸backend", " (+) 2 more"}; !reflect.DeepEqual(got, want) {
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

func TestReloadFoldsNewFoldGroup(t *testing.T) {
	m := hiddenGroupModel(t)
	toggleKey(t, m, "backend")
	ops := mutedSpec("ops", true)
	ops.Schema = "ops"
	ops.AutoFold = true
	ops.Start = startManual
	ops2 := mutedSpec("ops2", true)
	ops2.Schema = "ops"
	ops2.AutoFold = true
	ops2.Start = startManual
	runner := newTargetRunner(recTarget("ops", "ops"), &recordingSink{})
	runner2 := newTargetRunner(recTarget("ops2", "ops"), &recordingSink{})
	m.applyEvent(event{
		kind: evReconcile,
		line: "── config reloaded ── +ops ──",
		cfg: &configChange{
			targets: []Target{m.targets[0].spec, m.targets[1].spec, ops, ops2},
			runners: []*targetRunner{m.targets[0].runner, m.targets[1].runner, runner, runner2},
			stopped: []bool{false, false, false, false},
		},
	})
	if got, want := nestedRows(m), []string{"▾backend", " api", " web", "▸ops", " (+) 2 more"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after reload rows = %v, want %v", got, want)
	}
}

func TestReloadKeepsExpandedGroupWhenItGainsFold(t *testing.T) {
	api := target("api")
	api.Schema = "backend"
	web := target("web")
	web.Schema = "backend"
	m := newTestModel(t, api, web)
	if got, want := nestedRows(m), []string{"▾backend", " api", " web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	api.AutoFold = true
	web.AutoFold = true
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
