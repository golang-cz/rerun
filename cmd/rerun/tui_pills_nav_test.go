package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// enter on a picked pill puts it back in its own slot, not at the end.
func TestEditPutsPillBackInPlace(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")
	commitPill(m, "gamma")
	press(m, "/", "up", "left", "enter") // pick beta, edit it
	press(m, "enter")                    // commit it unchanged
	if len(m.pills) != 3 || m.pills[0].raw != "alpha" || m.pills[1].raw != "beta" || m.pills[2].raw != "gamma" {
		t.Fatalf("pills = %v, want alpha beta gamma in order", pillRaws(m))
	}
}

// delete picks the neighbor, and the input returns once no pill is left.
func TestDeleteTakesTheNeighborThenTheInput(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")
	press(m, "/", "up", "left", "delete")
	if len(m.pills) != 1 || m.barSel != 0 {
		t.Fatalf("delete: pills=%v sel=%d, want alpha picked", pillRaws(m), m.barSel)
	}
	press(m, "delete")
	if len(m.pills) != 0 || m.barSel != -1 {
		t.Fatalf("delete last: pills=%v sel=%d, want the input", pillRaws(m), m.barSel)
	}
}

// esc on the pills row returns to the input; a second esc closes the bar.
func TestEscOnPillsReturnsToInput(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	press(m, "/", "up", "esc")
	if m.barSel != -1 || m.inputMode != inputQuery {
		t.Fatalf("esc: sel=%d mode=%d, want the input focused with the bar open", m.barSel, m.inputMode)
	}
	press(m, "esc")
	if m.inputMode != inputNone {
		t.Fatal("second esc left the bar open")
	}
}

// Arrows past the last pill return to the input, as before.
func TestRightPastLastPillReturnsToInput(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	press(m, "/", "up", "right")
	if m.barSel != -1 {
		t.Fatalf("right past last: sel=%d, want the input", m.barSel)
	}
}

// → expands a [@name] pill only when it is the last one; otherwise it picks the next.
func TestRightExpandsOnlyTheLastSavedPill(t *testing.T) {
	m := queriesModel(t)
	m.queries["multi"] = "err plain"
	commitPill(m, "@multi")
	commitPill(m, "err")
	press(m, "/", "up", "left", "right")
	if len(m.pills) != 2 || m.barSel != 1 || m.pills[0].saved != "multi" {
		t.Fatalf("right on a non-last @pill: pills=%v sel=%d, want the next pill picked", pillRaws(m), m.barSel)
	}
	press(m, "left", "right")
	if len(m.pills) != 2 || m.pills[1].saved != "" {
		t.Fatalf("right on the last plain pill: pills=%v, want no expansion", pillRaws(m))
	}
}

// Empty input with pills shows a dim ↑ pills hint; with no pills, the syntax hint.
func TestPlaceholderHintsAtPills(t *testing.T) {
	m := pillModel(t)
	press(m, "/")
	if got := ansi.Strip(m.inputBoxView()); strings.Contains(got, "↑ pills") {
		t.Fatalf("no pills: box = %q, want the syntax placeholder", got)
	}
	commitPill(m, "alpha")
	press(m, "/")
	if got := ansi.Strip(m.inputBoxView()); !strings.Contains(got, "↑ pills") {
		t.Fatalf("pills and empty input: box = %q, want ↑ pills", got)
	}
}

// With the bar closed, clicking a pill's body opens the bar with that pill picked.
func TestClosedBarPillClickPicksIt(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")
	m.frame()
	body := m.layout.chips[0]
	m.handleMouse(mouse(m.layout.leftW+1+body.x0, 0, false))
	if m.inputMode == inputNone || m.barSel != 0 {
		t.Fatalf("click on alpha: mode=%d sel=%d, want the bar open with alpha picked", m.inputMode, m.barSel)
	}
	if len(m.pills) != 2 {
		t.Fatalf("a body click removed a pill: %v", pillRaws(m))
	}
}

func pillRaws(m *tuiModel) []string {
	out := make([]string, len(m.pills))
	for i, p := range m.pills {
		out[i] = p.raw
	}
	return out
}
