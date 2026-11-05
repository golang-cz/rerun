package main

import (
	"strings"
	"testing"
)

func TestMovingOntoAMutedTargetKeepsItMuted(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.toggleMute(1)

	press(m, "j")
	if m.solo != 1 {
		t.Fatalf("solo = %d, want 1 after moving onto api", m.solo)
	}
	if !m.targets[1].muted {
		t.Fatal("moving the cursor unmuted api")
	}
}

func TestExplicitSoloUnmutesTheTarget(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.toggleMute(1)
	m.cursor = 1

	press(m, "s")
	if m.solo != 1 || m.targets[1].muted {
		t.Fatalf("solo = %d muted = %v, want api soloed and unmuted", m.solo, m.targets[1].muted)
	}
}

func TestSlashOpensTheLineFilter(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "/")
	if m.inputMode != inputFilter {
		t.Fatalf("inputMode = %d, want the filter bar", m.inputMode)
	}
	press(m, "esc")

	press(m, "v")
	if m.inputMode != inputFilter {
		t.Fatalf("v inputMode = %d, want the filter bar", m.inputMode)
	}
}

func TestCtrlFOpensSearch(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "ctrl+f")
	if m.inputMode != inputSearch {
		t.Fatalf("inputMode = %d, want the search bar", m.inputMode)
	}
}

func TestFindAndFilterKeysWorkFromEveryFocus(t *testing.T) {
	m := newTestModel(t, target("web"))
	focuses := map[string]func(){
		"list":   func() {},
		"pane":   func() { m.paneFocus = true },
		"drawer": func() { m.drawerFocus = true },
	}
	for name, focus := range focuses {
		focus()
		press(m, "/")
		if m.inputMode != inputFilter {
			t.Fatalf("%s: / inputMode = %d, want the filter bar", name, m.inputMode)
		}
		press(m, "esc")

		focus()
		press(m, "ctrl+f")
		if m.inputMode != inputSearch {
			t.Fatalf("%s: ctrl+f inputMode = %d, want the search bar", name, m.inputMode)
		}
		press(m, "esc")
		m.paneFocus, m.drawerFocus = false, false
	}
}

func TestSlashInAnOpenBarTypesIt(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "/", "/")
	if m.inputMode != inputFilter || m.input != "/" {
		t.Fatalf("inputMode = %d input = %q, want / typed into the filter bar", m.inputMode, m.input)
	}
}

func TestHelpNamesFindAndFilter(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 40
	press(m, "?")
	if view := m.View(); !strings.Contains(view, "ctrl+f") || !strings.Contains(view, "filter") {
		t.Errorf("short help should name ctrl+f and the filter:\n%s", view)
	}
	press(m, "?")
	if view := m.View(); !strings.Contains(view, "ctrl+f") || !strings.Contains(view, "/ v") {
		t.Errorf("full help should name ctrl+f and / v:\n%s", view)
	}
}
