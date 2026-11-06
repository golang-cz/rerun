package main

import (
	"strings"
	"testing"
)

// groupedModel has two backend targets, one data target, and one ungrouped target.
func groupedModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("worker", "backend"),
		schemaTarget("db", "data"), target("web"))
	for _, n := range []string{"api", "worker", "db", "web"} {
		addLines(m, n, n+" keep", n+" drop")
	}
	m.View()
	return m
}

// alphaModel is one ungrouped target with lines an "alpha" and "beta" filter splits.
func alphaModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, target("web"))
	addLines(m, "web", "alpha beta", "alpha gamma", "delta beta", "delta gamma")
	m.View()
	return m
}

func commitText(m *tuiModel, mode string, text string) {
	press(m, mode)
	typeString(m, text)
	press(m, "enter")
}

func TestBarChipsRenderAndAndTogether(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "/", "beta")
	m.View()

	if want := []string{"alpha beta"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}
	bar := paneOf(m)[0]
	ia, ib := strings.Index(bar, "[alpha ×]"), strings.Index(bar, "[beta ×]")
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("bar = %q, want [alpha ×] then [beta ×]", bar)
	}
}

func TestBarChipClickRemovesIt(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "/", "beta")
	m.View()

	cross := m.layout.chips[1]
	m.handleMouse(mouse(m.layout.leftW+1+cross.x, 0, false))
	if len(m.filters) != 1 || m.filters[0].query != "alpha" {
		t.Fatalf("filters = %+v after clicking beta's ×, want only alpha", m.filters)
	}
}

// The bar holds only filters, so a scoped group adds no chip and no key drops it.
func TestScopeHasNoBarChipAndBackspaceKeepsIt(t *testing.T) {
	m := groupedModel(t)
	m.cursor = 0
	m.liveSelect()
	m.View()

	if m.schemaSel != "backend" {
		t.Fatalf("schemaSel = %q, want backend from the header", m.schemaSel)
	}
	if n := len(m.barChips()); n != 0 {
		t.Fatalf("bar chips = %d with only a scope, want none", n)
	}
	press(m, "backspace")
	if m.schemaSel != "backend" {
		t.Fatalf("schemaSel = %q after backspace, want backend kept", m.schemaSel)
	}
}

func TestBackspaceOnEmptyInputRemovesLastChip(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "/", "beta")

	press(m, "v", "backspace")
	if len(m.filters) != 1 || m.filters[0].query != "alpha" || m.inputMode != inputFilter {
		t.Fatalf("filters = %+v mode = %d, want alpha kept and the input still open", m.filters, m.inputMode)
	}
}

func TestBackspaceRemovesTheFocusedChip(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	commitText(m, "/", "beta")

	// An empty input and left hand the focus to the last chip.
	press(m, "v", "left")
	if !m.barFocus || m.inputMode != inputNone || m.barSel != 1 {
		t.Fatalf("bar focus=%v mode=%d sel=%d, want focus on the last chip", m.barFocus, m.inputMode, m.barSel)
	}
	press(m, "backspace")
	if len(m.filters) != 1 || m.filters[0].query != "alpha" {
		t.Fatalf("filters = %+v, want beta removed", m.filters)
	}
	press(m, "esc")
	if m.barFocus {
		t.Fatal("esc left the bar focused")
	}
}

// Esc backs out one level per focus and never clears filters, find, or chips.
func TestEscLadderNeverClearsFilters(t *testing.T) {
	m := groupedModel(t)
	commitText(m, "/", "keep")
	commitText(m, "ctrl+f", "keep")
	m.cursor = 2 // worker, a backend member
	m.liveSelect()

	keep := func(step string) {
		t.Helper()
		if len(m.filters) != 1 || m.search != "keep" {
			t.Fatalf("%s cleared filters=%d search=%q", step, len(m.filters), m.search)
		}
	}

	press(m, "esc")
	if m.cursor != 0 || m.schemaSel != "backend" || m.solo != -1 {
		t.Fatalf("esc on a member: cursor=%d schemaSel=%q solo=%d, want the backend header", m.cursor, m.schemaSel, m.solo)
	}
	keep("esc on member")

	press(m, "esc")
	if m.cursor != 0 || m.schemaSel != "" {
		t.Fatalf("esc on a header: cursor=%d schemaSel=%q, want cursor kept and scope all", m.cursor, m.schemaSel)
	}
	keep("esc on header")

	m.cursor = 6 // web, ungrouped: its header is the trailing default group
	m.liveSelect()
	press(m, "esc")
	if m.cursor != 5 || m.schemaSel != ungrouped {
		t.Fatalf("esc on the ungrouped member: cursor=%d schemaSel=%q, want the default header", m.cursor, m.schemaSel)
	}
	keep("esc on default member")

	press(m, "right")
	press(m, "esc")
	if m.paneFocus {
		t.Fatal("esc in the pane did not return focus to the list")
	}
	keep("esc in pane")

	press(m, "esc")
	keep("esc on list with scope all")

	press(m, "/", "esc")
	if m.barFocus {
		t.Fatal("esc in an empty bar input left the bar focused")
	}
	keep("esc in bar")

	press(m, "/", "x")
	press(m, "esc")
	if len(m.filters) != 2 {
		t.Fatalf("esc with typed text: filters = %d, want the typed query kept as a chip", len(m.filters))
	}
}

func TestSlashAndCtrlFFocusTheBarFromEveryFocus(t *testing.T) {
	m := groupedModel(t)
	commitText(m, "/", "keep")

	type focusCase struct {
		name  string
		setup func()
	}
	cases := []focusCase{
		{"list", func() {}},
		{"pane", func() { press(m, "right") }},
		{"bar", func() { press(m, "v", "left") }},
	}
	for _, c := range cases {
		for _, key := range []string{"/", "ctrl+f"} {
			m.barFocus, m.inputMode, m.paneFocus = false, inputNone, false
			c.setup()
			wasPane := m.paneFocus
			press(m, key)
			if !m.barFocus || m.inputMode == inputNone {
				t.Fatalf("%s then %s: bar focus=%v mode=%d, want the bar input open", c.name, key, m.barFocus, m.inputMode)
			}
			press(m, "esc")
			if m.paneFocus != wasPane {
				t.Fatalf("%s then %s: leaving the bar changed the previous focus", c.name, key)
			}
		}
	}
}

func TestGroupKeysJumpBetweenHeaders(t *testing.T) {
	m := groupedModel(t)
	m.cursor = 0
	m.liveSelect()

	steps := []struct {
		key    string
		cursor int
		scope  string
	}{
		{"]", 3, "data"},
		{"]", 5, ungrouped},
		{"]", 5, ungrouped},
		{"[", 3, "data"},
		{"shift+up", 0, "backend"},
		{"shift+down", 3, "data"},
	}
	for _, s := range steps {
		press(m, s.key)
		if m.cursor != s.cursor || m.schemaSel != s.scope || m.solo != -1 {
			t.Fatalf("%s: cursor=%d schemaSel=%q solo=%d, want cursor %d scope %q",
				s.key, m.cursor, m.schemaSel, m.solo, s.cursor, s.scope)
		}
	}

	m.cursor = 2 // worker, a backend member: [ goes to its own header
	m.liveSelect()
	press(m, "[")
	if m.cursor != 0 || m.schemaSel != "backend" {
		t.Fatalf("[ from a member: cursor=%d schemaSel=%q, want the backend header", m.cursor, m.schemaSel)
	}
}

func TestDefaultHeaderOnlyWhenSomeTargetHasAGroup(t *testing.T) {
	flat := newTestModel(t, target("web"), target("api"))
	for _, r := range flat.listRows() {
		if r.header {
			t.Fatalf("flat list has a header %q", r.schema)
		}
	}
	if got, want := listNames(flat), []string{"web", "api"}; !equalStrings(got, want) {
		t.Fatalf("flat list = %v, want %v", got, want)
	}

	mixed := groupedModel(t)
	names := listNames(mixed)
	if names[len(names)-2] != "▾all" || names[len(names)-1] != "web" {
		t.Fatalf("mixed list = %v, want web under a trailing default header", names)
	}

	allGrouped := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("db", "data"))
	for _, n := range listNames(allGrouped) {
		if n == "▾all" {
			t.Fatalf("all-grouped list has a default header: %v", listNames(allGrouped))
		}
	}
}

func TestEscOnUngroupedRowInFlatListClearsScope(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.cursor = 1
	m.liveSelect()
	press(m, "esc")
	if m.solo != -1 || m.cursor != 1 {
		t.Fatalf("esc on a flat row: solo=%d cursor=%d, want scope all and the cursor kept", m.solo, m.cursor)
	}
}
