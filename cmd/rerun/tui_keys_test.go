package main

import (
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"

	tea "charm.land/bubbletea/v2"
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
		"list": func() {},
		"pane": func() { m.paneFocus = true },
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
		m.paneFocus = false
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
	if view := m.View().Content; !strings.Contains(view, "ctrl+f") || !strings.Contains(view, "filter") {
		t.Errorf("short help should name ctrl+f and the filter:\n%s", view)
	}
	press(m, "?")
	if view := m.View().Content; !strings.Contains(view, "ctrl+f") || !strings.Contains(view, "/ v") {
		t.Errorf("full help should name ctrl+f and / v:\n%s", view)
	}
}

// The kitty decoder reports some keys with a spelling legacy bytes cannot
// produce; each one must still run the action its legacy form ran.
func TestEnhancedKeySpellingsKeepTheirActions(t *testing.T) {
	shifted := func(code, text rune) tea.KeyPressMsg {
		return tea.KeyPressMsg{Code: code, Mod: tea.ModShift, Text: string(text), ShiftedCode: text}
	}
	armedSearch := func(m *tuiModel) {
		m.applyEvents(streamEvents("web", "line", 60).events)
		m.frame()
		press(m, "ctrl+f")
		typeString(m, "line")
		press(m, "enter")
	}

	// shift+n reports as "N": it steps back to the previous match.
	m := newTestModel(t, target("web"))
	armedSearch(m)
	first := m.searchRow
	press(m, "n")
	if m.searchRow <= first {
		t.Fatalf("n did not advance from %d", first)
	}
	m.Update(shifted('n', 'N'))
	if m.searchRow != first {
		t.Fatalf("enhanced shift+n = %d, want back at %d", m.searchRow, first)
	}

	// shift+/ reports as "?": it opens the help.
	m = newTestModel(t)
	m.width, m.height = 100, 40
	m.Update(shifted('/', '?'))
	if !m.help {
		t.Fatal("enhanced shift+/ did not open help")
	}
	// A terminal that drops the shifted text spells it shift+/ instead.
	m.help = false
	m.Update(tea.KeyPressMsg{Code: '/', Mod: tea.ModShift})
	if !m.help {
		t.Fatal("bare shift+/ did not open help")
	}

	// shift+tab is unbound, exactly as its legacy CSI Z was; plain tab is too.
	m = newTestModel(t, target("web"))
	m.now = spawnClock
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.paneFocus || m.eventsModal.open {
		t.Fatal("shift+tab moved focus")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.paneFocus || m.eventsModal.open {
		t.Fatal("tab moved focus")
	}

	// esc clears and closes the find box; ctrl+[ is the same key enhanced.
	openSearch := func() *tuiModel {
		box := newTestModel(t, target("web"))
		box.applyEvents(streamEvents("web", "line", 10).events)
		box.frame()
		press(box, "ctrl+f")
		typeString(box, "line")
		return box
	}
	m = openSearch()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.inputMode != inputNone || m.search != "" {
		t.Fatalf("enhanced esc: mode=%d search=%q, want cleared and closed", m.inputMode, m.search)
	}
	m = openSearch()
	press(m, "ctrl+[")
	if m.inputMode != inputNone || m.search != "" {
		t.Fatalf("enhanced ctrl+[: mode=%d search=%q, want cleared and closed", m.inputMode, m.search)
	}

	// enter keeps the query and its highlights; ctrl+m is the same key enhanced.
	m = openSearch()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inputMode != inputNone || m.search != "line" || len(m.searchMatches) == 0 {
		t.Fatalf("enhanced enter: mode=%d search=%q, want the box closed with the query kept", m.inputMode, m.search)
	}
	m = openSearch()
	press(m, "ctrl+m")
	if m.inputMode != inputNone || m.search != "line" || len(m.searchMatches) == 0 {
		t.Fatalf("enhanced ctrl+m: mode=%d search=%q, want the box closed with the query kept", m.inputMode, m.search)
	}
}

// The full help explains the play glyph on a startable group header.
func TestHelpNamesThePlayGlyph(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 160, 40
	press(m, "?")
	press(m, "?")
	if view := m.View().Content; !strings.Contains(view, "▶ starts its auto targets") {
		t.Errorf("full help should explain the play glyph:\n%s", view)
	}
}
