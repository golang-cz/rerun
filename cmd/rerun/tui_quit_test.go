package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// ansiView renders m.View with styling on, so button colors survive.
func ansiView(t *testing.T, m *tuiModel) string {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0)
	defer lipgloss.SetColorProfile(prev)
	return m.View()
}

// q opens the confirm modal instead of quitting, over the still-running frame.
func TestQuitKeyAsksFirst(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(keyMsg("q")); cmd != nil {
		t.Fatal("q returned a quit command instead of asking")
	}
	if !m.confirmQuit {
		t.Fatal("q did not arm the confirm modal")
	}

	view := ansiView(t, m)
	for _, want := range []string{
		"Quit rerun?",
		"Every target is terminated and every watcher stops.",
		"enter quits · esc cancels · ctrl+c again quits",
		"web",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("confirm modal missing %q", want)
		}
	}

	if !strings.Contains(view, "\x1b[1;97;41m[ Quit ]\x1b[0m") {
		t.Errorf("quit button lacks red background / white bold text")
	}
	if !strings.Contains(view, "\x1b[90m[ Cancel ]\x1b[0m") {
		t.Errorf("cancel button is not the plain gray button")
	}
	if strings.Contains(view, "41m[ Cancel ]") {
		t.Errorf("cancel button carries a red background")
	}
}

// enter on the modal takes the graceful quit path.
func TestQuitConfirmEnterQuits(t *testing.T) {
	m := newTestModel(t)
	press(m, "q")
	if _, cmd := m.Update(keyMsg("enter")); cmd == nil {
		t.Fatal("enter on the confirm modal did not return a quit command")
	}
}

// ctrl+c opens the modal like q; a second ctrl+c quits.
func TestCtrlCOpensModalThenSecondPressQuits(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd != nil {
		t.Fatal("ctrl+c returned a quit command instead of asking")
	}
	if !m.confirmQuit {
		t.Fatal("ctrl+c did not arm the confirm modal")
	}
	if !strings.Contains(m.View(), "Quit rerun?") {
		t.Fatal("ctrl+c did not draw the confirm modal")
	}
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd == nil {
		t.Fatal("a second ctrl+c did not quit")
	}
}

// q on the modal dismisses it without quitting, and it stays gone.
func TestQuitConfirmQCancelsWithoutReopen(t *testing.T) {
	m := newTestModel(t)
	press(m, "q")
	if _, cmd := m.Update(keyMsg("q")); cmd != nil {
		t.Fatal("q on the confirm modal returned a quit command")
	}
	if m.confirmQuit {
		t.Fatal("q did not dismiss the confirm modal")
	}
	if strings.Contains(m.View(), "Quit rerun?") {
		t.Fatal("the confirm modal is still rendering after q")
	}
}

// esc on the modal dismisses it without quitting.
func TestQuitConfirmEscCancels(t *testing.T) {
	m := newTestModel(t)
	press(m, "q")
	if _, cmd := m.Update(keyMsg("esc")); cmd != nil {
		t.Fatal("esc on the confirm modal returned a quit command")
	}
	if m.confirmQuit {
		t.Fatal("esc did not dismiss the confirm modal")
	}
	if strings.Contains(m.View(), "Quit rerun?") {
		t.Fatal("the confirm modal is still rendering after esc")
	}
}

// Any other key cancels the modal and still runs normally.
func TestQuitConfirmOtherKeyStays(t *testing.T) {
	m := newTestModel(t)
	press(m, "q")
	if _, cmd := m.Update(keyMsg("j")); cmd != nil {
		t.Fatal("j on the confirm modal returned a quit command")
	}
	if m.confirmQuit {
		t.Fatal("j did not cancel the confirm modal")
	}
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (j ran after cancelling)", m.cursor)
	}
	if strings.Contains(m.View(), "Quit rerun?") {
		t.Error("the confirm modal is still rendering")
	}
}

// Inside help, q closes the overlay and never arms the modal.
func TestQuitConfirmQInHelpClosesHelp(t *testing.T) {
	m := newTestModel(t)
	press(m, "?")
	if _, cmd := m.Update(keyMsg("q")); cmd != nil {
		t.Fatal("q in the help overlay returned a quit command")
	}
	if m.confirmQuit {
		t.Fatal("q in the help overlay armed the confirm modal")
	}
	if m.help {
		t.Fatal("q did not close the help overlay")
	}
}

// An open search bar owns q as typed text, not as the quit key.
func TestQuitConfirmQInInputBarTypes(t *testing.T) {
	m := newTestModel(t)
	press(m, "/")
	m.Update(keyMsg("q"))
	if m.confirmQuit {
		t.Fatal("q in the search bar armed the confirm modal")
	}
	if m.input != "q" {
		t.Errorf("search bar text = %q, want %q", m.input, "q")
	}
}

// The modal swallows mouse input so stray clicks cannot change state.
func TestQuitConfirmMouseInert(t *testing.T) {
	m := newTestModel(t)
	m.View()
	press(m, "q")
	m.handleMouse(mouse(2, 2, false))
	if m.solo != -1 {
		t.Errorf("solo = %d, want -1 (click ignored under the modal)", m.solo)
	}
	if !m.confirmQuit {
		t.Fatal("a click cancelled the confirm modal")
	}
}

// An external SIGINT quits without going through the modal.
func TestInterruptMsgQuitsWithoutModal(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(interruptMsg{}); cmd == nil {
		t.Fatal("interruptMsg did not return a quit command")
	}
	if m.confirmQuit {
		t.Fatal("interruptMsg armed the confirm modal")
	}

	m = newTestModel(t)
	press(m, "q")
	if _, cmd := m.Update(interruptMsg{}); cmd == nil {
		t.Fatal("interruptMsg with the modal up did not quit")
	}
}
