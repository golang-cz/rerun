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

func TestSlashOpensTheQueryBar(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "/")
	if m.inputMode != inputQuery {
		t.Fatalf("inputMode = %d, want the query bar", m.inputMode)
	}
	press(m, "esc")

	press(m, "/")
	if m.inputMode != inputQuery {
		t.Fatalf("v inputMode = %d, want the query bar", m.inputMode)
	}
}

func TestFindAndFilterKeysWorkFromEveryFocus(t *testing.T) {
	m := newTestModel(t, target("web"))
	focuses := map[string]func(){
		"list": func() {},
		"pane": func() { m.paneFocus = true },
	}
	for name, focus := range focuses {
		for _, key := range []string{"/", "f"} {
			focus()
			press(m, key)
			if m.inputMode != inputQuery {
				t.Fatalf("%s: %s inputMode = %d, want the query bar", name, key, m.inputMode)
			}
			press(m, "esc")
			m.paneFocus = false
		}
	}
}

func TestSlashInAnOpenBarTypesIt(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "/", "/")
	if m.inputMode != inputQuery || m.input != "/" {
		t.Fatalf("inputMode = %d input = %q, want / typed into the query bar", m.inputMode, m.input)
	}
}

func TestHelpNamesFindAndFilter(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 40
	press(m, "?")
	if view := m.View().Content; !strings.Contains(view, "f") || !strings.Contains(view, "pill") {
		t.Errorf("short help should name ctrl+f and the pills:\n%s", view)
	}
	press(m, "?")
	if view := m.View().Content; !strings.Contains(view, "/ f") {
		t.Errorf("full help should name / and f:\n%s", view)
	}
}

// ctrl+f is no longer a search key; f is.
func TestCtrlFDoesNotOpenSearch(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if m.inputMode != inputNone {
		t.Fatal("ctrl+f opened the query bar")
	}
}

// f opens search from the pane too, where = is the value filter.
func TestFOpensSearchWithPaneFocus(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "f")
	if m.inputMode != inputQuery {
		t.Fatal("f did not open the query bar")
	}
	press(m, "esc")
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
		press(m, "f")
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

	// esc closes the box and keeps the typed pill; ctrl+[ is the same key enhanced.
	openSearch := func() *tuiModel {
		box := newTestModel(t, target("web"))
		box.applyEvents(streamEvents("web", "line", 10).events)
		box.frame()
		press(box, "f")
		typeString(box, "line")
		return box
	}
	m = openSearch()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.inputMode != inputNone || len(m.pills) != 1 {
		t.Fatalf("enhanced esc: mode=%d pills=%v, want the pill kept and the box closed", m.inputMode, m.pills)
	}
	m = openSearch()
	press(m, "ctrl+[")
	if m.inputMode != inputNone || len(m.pills) != 1 {
		t.Fatalf("enhanced ctrl+[: mode=%d pills=%v, want the pill kept and the box closed", m.inputMode, m.pills)
	}

	// enter keeps the pill and its hits; ctrl+m is the same key enhanced.
	m = openSearch()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inputMode != inputNone || len(m.pills) != 1 || m.pills[0].raw != "line" || len(m.searchMatches) == 0 {
		t.Fatalf("enhanced enter: mode=%d pills=%v, want the box closed with the pill kept", m.inputMode, m.pills)
	}
	m = openSearch()
	press(m, "ctrl+m")
	if m.inputMode != inputNone || len(m.pills) != 1 || m.pills[0].raw != "line" || len(m.searchMatches) == 0 {
		t.Fatalf("enhanced ctrl+m: mode=%d pills=%v, want the box closed with the pill kept", m.inputMode, m.pills)
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

// v is no longer a query-bar key: only / and ctrl+f open it.
func TestVDoesNotOpenTheQueryBar(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "v")
	if m.inputMode != inputNone {
		t.Fatal("v opened the query bar")
	}
}

// g opens go to like ctrl+k, but inside the query bar it types.
func TestGOpensGoToOutsideTextInputs(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "g")
	if !m.finder.open {
		t.Fatal("g did not open go to")
	}
	press(m, "esc")

	press(m, "/")
	press(m, "g")
	if m.finder.open {
		t.Fatal("g opened go to from the query bar")
	}
	if m.input != "g" {
		t.Fatalf("query input = %q, want g typed", m.input)
	}
}
