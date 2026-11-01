package main

import (
	"strings"
	"testing"
)

// q opens the confirm modal instead of quitting, over the still-running frame.
func TestQuitKeyAsksFirst(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(keyMsg("q")); cmd != nil {
		t.Fatal("q returned a quit command instead of asking")
	}
	if !m.confirmQuit {
		t.Fatal("q did not arm the confirm modal")
	}
	view := m.View()
	if !strings.Contains(view, "quit rerun?") {
		t.Error("the confirm modal is missing its title")
	}
	if !strings.Contains(view, "enter quits · any other key stays") {
		t.Error("the confirm modal is missing its hint line")
	}
	if !strings.Contains(view, "web") {
		t.Error("the frame is not drawn behind the confirm modal")
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
	if strings.Contains(m.View(), "quit rerun?") {
		t.Error("the confirm modal is still rendering")
	}
}

// ctrl+c quits immediately even with the modal up.
func TestQuitConfirmCtrlCQuits(t *testing.T) {
	m := newTestModel(t)
	press(m, "q")
	if _, cmd := m.Update(keyMsg("ctrl+c")); cmd == nil {
		t.Fatal("ctrl+c with the confirm modal up did not quit")
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
