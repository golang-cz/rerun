package main

import "testing"

// noCtrlQueued fails when the runner was asked to change state.
func noCtrlQueued(t *testing.T, m *tuiModel, i int) {
	t.Helper()
	select {
	case c := <-m.targets[i].runner.ctrlCh:
		t.Fatalf("runner control = %v, want none", c.kind)
	default:
	}
}

// Enter on a stopped target starts it and focuses its pane.
func TestEnterStartsAStoppedTargetAndFocusesThePane(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].state = stateStopped
	m.cursor = 0
	m.liveSelect()

	press(m, "enter")

	if m.targets[0].state != stateRunning {
		t.Fatalf("state = %v, want running", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStart {
		t.Fatalf("control = %v, want ctrlStart", got)
	}
	if !m.paneFocus {
		t.Fatal("enter did not focus the pane")
	}
	if m.solo != 0 {
		t.Fatalf("solo = %d, want the started target at 0", m.solo)
	}
}

// Enter on a running target focuses its pane and never stops it.
func TestEnterOnARunningTargetOnlyFocuses(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].state = stateRunning
	m.cursor = 0
	m.liveSelect()

	press(m, "enter")

	if m.targets[0].state != stateRunning {
		t.Fatalf("state = %v, want running (enter never stops)", m.targets[0].state)
	}
	noCtrlQueued(t, m, 0)
	if !m.paneFocus {
		t.Fatal("enter did not focus the pane")
	}
}

// Enter on an errored target starts it again, the way space does.
func TestEnterOnAnErroredTargetStartsIt(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].state = stateExited
	m.targets[0].exitCode = 1
	m.cursor = 0
	m.liveSelect()

	press(m, "enter")

	if m.targets[0].state != stateRunning {
		t.Fatalf("state = %v, want running", m.targets[0].state)
	}
	if !m.paneFocus {
		t.Fatal("enter did not focus the pane")
	}
}

// Enter on a group header with nothing up starts its auto members, then focuses the group pane.
func TestEnterOnAGroupHeaderStartsAutoMembers(t *testing.T) {
	auto := schemaTarget("api", "backend")
	manual := schemaTarget("job", "backend")
	manual.Start = startManual
	m := newTestModel(t, auto, manual)
	for i := range m.targets {
		m.targets[i].state = stateStopped
	}

	press(m, "enter")

	if m.targets[0].state != stateRunning {
		t.Fatalf("auto member state = %v, want running", m.targets[0].state)
	}
	if m.targets[1].state == stateRunning {
		t.Fatal("a manual member started on enter")
	}
	if !m.paneFocus {
		t.Fatal("enter did not focus the group pane")
	}
	if m.schemaSel != "backend" {
		t.Fatalf("schemaSel = %q, want backend", m.schemaSel)
	}
}

// Enter on a group header with a running member only focuses: it must not stop the member.
func TestEnterOnAGroupWithARunningMemberOnlyFocuses(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	m.targets[1].state = stateRunning
	m.cursor = 0

	press(m, "enter")

	if m.targets[0].state == stateRunning {
		t.Fatal("enter started a member while another one ran")
	}
	if m.targets[1].state != stateRunning {
		t.Fatalf("running member state = %v, want it untouched", m.targets[1].state)
	}
	if !m.paneFocus {
		t.Fatal("enter did not focus the group pane")
	}
}

// A left press in the pane takes the keys, on a line and past the content alike.
func TestClickInThePaneFocusesIt(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "web-1")
	m.frame()
	if m.paneFocus {
		t.Fatal("the pane starts focused")
	}

	x := paneX(m)
	m.handleMouse(mouse(x, m.titleHeight(), false))
	if !m.paneFocus {
		t.Fatal("a pane click on a line did not focus the pane")
	}

	m.paneFocus = false
	m.handleMouse(mouse(x, m.titleHeight()+5, false))
	if !m.paneFocus {
		t.Fatal("a pane click past the content did not focus the pane")
	}
}

// s stops the target under the cursor.
func TestSStopsTheSelectedTarget(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	m.targets[0].state = stateRunning
	m.targets[1].state = stateRunning
	m.cursor = 1

	press(m, "s")

	if m.targets[1].state != stateStopped {
		t.Fatalf("state = %v, want stopped", m.targets[1].state)
	}
	if m.targets[0].state != stateRunning {
		t.Fatal("s stopped a target the cursor was not on")
	}
	if got := queuedCtrl(t, m.targets[1].runner); got != ctrlStop {
		t.Fatalf("control = %v, want ctrlStop", got)
	}
}

// s with the pane focused acts on the pane's scope, not the list cursor's row.
func TestSStopsTheFocusedPanesTargetNotTheCursorRow(t *testing.T) {
	m := newTestModel(t, target("api"), target("web"))
	m.targets[0].state = stateRunning
	m.targets[1].state = stateRunning
	m.cursor = 0
	m.liveSelect()
	press(m, "enter") // focuses api's pane without stopping it
	if m.solo != 0 {
		t.Fatalf("solo = %d, want the pane scoped to api", m.solo)
	}

	m.cursor = 1 // the cursor moves, the pane's scope does not
	press(m, "s")

	if m.targets[0].state != stateStopped {
		t.Fatalf("pane target state = %v, want stopped", m.targets[0].state)
	}
	if got := queuedCtrl(t, m.targets[0].runner); got != ctrlStop {
		t.Fatalf("pane target control = %v, want ctrlStop", got)
	}
	if m.targets[1].state != stateRunning {
		t.Fatalf("cursor row state = %v, want it still running", m.targets[1].state)
	}
	noCtrlQueued(t, m, 1)
}

// s is a stop, never a toggle: a stopped target stays stopped.
func TestSDoesNotStartAStoppedTarget(t *testing.T) {
	m := newTestModel(t, target("api"))
	m.targets[0].state = stateStopped
	m.cursor = 0

	press(m, "s")

	if m.targets[0].state != stateStopped {
		t.Fatalf("state = %v, want stopped (s must not start)", m.targets[0].state)
	}
	noCtrlQueued(t, m, 0)
}

// s on a group header stops its running members and leaves the stopped ones alone.
func TestSOnAGroupHeaderStopsRunningMembers(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	m.targets[0].state = stateRunning
	m.targets[1].state = stateStopped
	m.cursor = 0

	press(m, "s")

	if m.targets[0].state != stateStopped {
		t.Fatalf("running member state = %v, want stopped", m.targets[0].state)
	}
	if m.targets[1].state != stateStopped {
		t.Fatal("s started a stopped member")
	}
	noCtrlQueued(t, m, 1)
}

// pgup and pgdown scroll a full pane once the pane owns the keys.
func TestFocusedPaneScrollsAFullPage(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", streamLines("web", 200)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	press(m, "right")
	if !m.paneFocus {
		t.Fatal("right did not focus the pane")
	}
	page := m.vp.Height()
	if page < 4 {
		t.Fatalf("pane height = %d, too short for a page test", page)
	}

	before := m.vp.YOffset()
	press(m, "pgup")
	if got := before - m.vp.YOffset(); got != page {
		t.Fatalf("pgup moved %d rows, want a full page of %d", got, page)
	}
	press(m, "pgdown")
	if m.vp.YOffset() != before {
		t.Fatalf("YOffset = %d after pgdown, want %d", m.vp.YOffset(), before)
	}
}

// An unfocused pane still scrolls, but only half a page per key.
func TestUnfocusedPaneScrollsHalfAPage(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", streamLines("web", 200)...)
	m.frame()
	m.cursor = 0
	m.liveSelect()
	if m.paneFocus {
		t.Fatal("pane started focused")
	}
	page := m.vp.Height()
	if page < 4 {
		t.Fatalf("pane height = %d, too short for a page test", page)
	}

	press(m, "pgup")
	before := m.vp.YOffset()
	press(m, "pgdown")
	if got := m.vp.YOffset() - before; got != page/2 {
		t.Fatalf("pgdown moved %d rows, want half a page of %d", got, page/2)
	}
}
