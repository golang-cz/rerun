package main

import "testing"

// Esc is the one way back to the live tail: it resumes before it backs out of the list.

// escToLive presses esc until the tail follows again: a focused pane gives up focus first.
func escToLive(m *tuiModel) {
	if m.paneFocus {
		press(m, "esc")
	}
	press(m, "esc")
}

func TestEscInScrollbackGoesLiveAndKeepsScope(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	m.applyEvents(streamEvents("api", "old", 60).events)
	m.View()
	m.applyScope(-1, "backend")
	m.scrollUp(5)
	if m.follow {
		t.Fatal("scrolling up did not leave live mode")
	}

	press(m, "esc")
	if !m.follow || m.pending != 0 {
		t.Fatalf("esc in scrollback: follow=%v pending=%d, want live", m.follow, m.pending)
	}
	if m.schemaSel != "backend" {
		t.Fatalf("esc in scrollback cleared the scope to %q, want backend kept", m.schemaSel)
	}
}

func TestSecondEscStepsOutThenClearsScope(t *testing.T) {
	m := nestedFixture(t)
	m.applyScope(-1, "omsx/backend")
	m.cursor = 2 // api, under backend
	m.follow = false

	steps := []struct {
		name       string
		wantCursor int
		wantScope  string
	}{
		{"esc goes live and keeps the cursor and scope", 2, "omsx/backend"},
		{"esc from a target steps to its backend header", 1, "omsx/backend"},
		{"esc from backend steps to the omsx header", 0, importKeyPrefix + "omsx"},
		{"esc from the omsx header clears the scope", 0, ""},
	}
	for _, s := range steps {
		press(m, "esc")
		if m.cursor != s.wantCursor || m.schemaSel != s.wantScope {
			t.Fatalf("%s: cursor=%d scope=%q, want cursor %d scope %q",
				s.name, m.cursor, m.schemaSel, s.wantCursor, s.wantScope)
		}
	}
	if !m.follow {
		t.Fatal("the tail left live mode on a later esc")
	}
}

func TestPAndGDoNothing(t *testing.T) {
	for _, live := range []bool{true, false} {
		m := newTestModel(t, target("web"))
		m.applyEvents(streamEvents("web", "old", 60).events)
		m.View()
		if !live {
			m.pauseTail()
		}
		m.pending = 3
		cursor := m.cursor

		press(m, "p")
		press(m, "G")
		if m.follow != live || m.pending != 3 || m.cursor != cursor {
			t.Fatalf("live=%v: p and G changed follow=%v pending=%d cursor=%d, want no change",
				live, m.follow, m.pending, m.cursor)
		}
	}
}
