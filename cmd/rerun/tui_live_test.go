package main

import (
	"reflect"
	"testing"
)

func TestMovingTheCursorSetsTheScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))

	press(m, "j")
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after j onto api", m.solo)
	}
	press(m, "k")
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 after k onto web", m.solo)
	}
	press(m, "up")
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 kept at the top row", m.solo)
	}
}

func TestMovingOntoASchemaRowSelectsIt(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"))
	m.cursor = 0 // the backend header

	press(m, "j")
	if m.solo != 0 || m.schemaSel != "" {
		t.Fatalf("solo = %d schemaSel = %q, want api soloed", m.solo, m.schemaSel)
	}
	press(m, "k")
	if m.schemaSel != "backend" || m.solo != -1 {
		t.Fatalf("solo = %d schemaSel = %q, want the backend schema", m.solo, m.schemaSel)
	}
}

func TestHomeAndEndSetTheScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"), target("worker"))

	press(m, "end")
	if m.solo != 2 {
		t.Fatalf("solo = %d, want 2 after end", m.solo)
	}
	press(m, "home")
	if m.solo != 0 {
		t.Fatalf("solo = %d, want 0 after home", m.solo)
	}
}

func TestPaneCursorDoesNotMoveTheScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	paneOut(m, "web", "line")
	press(m, "j")
	before := m.solo

	press(m, "right", "j")
	if m.solo != before {
		t.Fatalf("solo = %d after moving in the pane, want %d", m.solo, before)
	}
}

func TestEnterMovesFocusToThePane(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	press(m, "j")
	press(m, "enter")

	if !m.paneFocus {
		t.Fatal("enter did not focus the log pane")
	}
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 kept by enter", m.solo)
	}
}

func TestScopeFollowsTheListLines(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), target("web"))
	paneOut(m, "api", "api-line")
	paneOut(m, "web", "web-line")
	press(m, "j", "j")

	if got, want := m.paneLines(), []string{"api-line"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pane = %q, want %q", got, want)
	}
}
