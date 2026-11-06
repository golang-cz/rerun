package main

import "testing"

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
