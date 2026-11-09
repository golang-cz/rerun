package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// withInputMode runs one case against the query bar, the one open input.
func withInputMode(t *testing.T, fn func(t *testing.T, m *tuiModel)) {
	t.Helper()
	t.Run("query", func(t *testing.T) {
		m := newTestModel(t, target("web"))
		addLines(m, "web", "alpha beta", "delta gamma")
		m.frame()
		press(m, "/")
		fn(t, m)
	})
}

// The arrows walk the typed text rune by rune and typing lands at the cursor.
func TestInputCursorMovesByRune(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha")
		press(m, "left", "left")
		if m.inputCursor != 3 {
			t.Fatalf("cursor = %d after two lefts, want 3", m.inputCursor)
		}
		typeString(m, "X")
		if m.input != "alpXha" || m.inputCursor != 4 {
			t.Fatalf("input=%q cursor=%d, want %q at 4", m.input, m.inputCursor, "alpXha")
		}
		press(m, "right")
		if m.inputCursor != 5 {
			t.Fatalf("cursor = %d after right, want 5", m.inputCursor)
		}
	})
}

// alt+left and alt+b step a word back, alt+right and alt+f a word forward.
func TestInputCursorMovesByWord(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha beta")
		press(m, "alt+left")
		if m.inputCursor != 6 {
			t.Fatalf("cursor = %d after alt+left, want 6", m.inputCursor)
		}
		press(m, "alt+b")
		if m.inputCursor != 0 {
			t.Fatalf("cursor = %d after alt+b, want 0", m.inputCursor)
		}
		press(m, "alt+right")
		if m.inputCursor != 5 {
			t.Fatalf("cursor = %d after alt+right, want 5", m.inputCursor)
		}
		press(m, "alt+f")
		if m.inputCursor != 10 {
			t.Fatalf("cursor = %d after alt+f, want 10", m.inputCursor)
		}
		press(m, "alt+left")
		if m.inputCursor != 6 {
			t.Fatalf("cursor = %d after alt+left, want 6", m.inputCursor)
		}
	})
}

// home and ctrl+a go to the start, end and ctrl+e to the end.
func TestInputCursorHomeEnd(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha beta")
		press(m, "home")
		if m.inputCursor != 0 {
			t.Fatalf("cursor = %d after home, want 0", m.inputCursor)
		}
		press(m, "end")
		if m.inputCursor != 10 {
			t.Fatalf("cursor = %d after end, want 10", m.inputCursor)
		}
		press(m, "ctrl+a")
		if m.inputCursor != 0 {
			t.Fatalf("cursor = %d after ctrl+a, want 0", m.inputCursor)
		}
		press(m, "ctrl+e")
		if m.inputCursor != 10 {
			t.Fatalf("cursor = %d after ctrl+e, want 10", m.inputCursor)
		}
	})
}

// backspace eats the rune before the cursor, delete and ctrl+d the one after it.
func TestInputBackspaceAndDeleteAtCursor(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "abcd")
		press(m, "left", "left")
		press(m, "backspace")
		if m.input != "acd" || m.inputCursor != 1 {
			t.Fatalf("input=%q cursor=%d after backspace, want %q at 1", m.input, m.inputCursor, "acd")
		}
		press(m, "delete")
		if m.input != "ad" || m.inputCursor != 1 {
			t.Fatalf("input=%q cursor=%d after delete, want %q at 1", m.input, m.inputCursor, "ad")
		}
		press(m, "ctrl+d")
		if m.input != "a" || m.inputCursor != 1 {
			t.Fatalf("input=%q cursor=%d after ctrl+d, want %q at 1", m.input, m.inputCursor, "a")
		}
	})
}

// ctrl+u drops everything before the cursor.
func TestInputCtrlUClearsToStart(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha beta")
		press(m, "ctrl+u")
		if m.input != "" || m.inputCursor != 0 {
			t.Fatalf("input=%q cursor=%d after ctrl+u, want empty", m.input, m.inputCursor)
		}
		// The box stays open, so the next word starts a fresh query.
		typeString(m, "al")
		if m.input != "al" || m.inputCursor != 2 {
			t.Fatalf("input=%q cursor=%d, want %q at 2", m.input, m.inputCursor, "al")
		}
	})
}

// ctrl+w and alt+backspace drop the word before the cursor.
func TestInputCtrlWDeletesWordBefore(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha beta")
		press(m, "ctrl+w")
		if m.input != "alpha " || m.inputCursor != 6 {
			t.Fatalf("input=%q cursor=%d after ctrl+w, want %q at 6", m.input, m.inputCursor, "alpha ")
		}
		press(m, "alt+backspace")
		if m.input != "" || m.inputCursor != 0 {
			t.Fatalf("input=%q cursor=%d after alt+backspace, want empty", m.input, m.inputCursor)
		}
	})
}

// ←/→ in the input only move the text cursor, never focus the pills.
func TestInputLeftOnEmptyStaysInInput(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	press(m, "/")
	press(m, "left", "right")

	if m.inputMode != inputQuery || m.barSel != -1 {
		t.Fatalf("mode=%d sel=%d, want the bar open and the input focused", m.inputMode, m.barSel)
	}
}

// Typed text at cursor 0: left stays in the input and keeps the text.
func TestInputLeftOnTypedTextStaysInInput(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	press(m, "/")
	typeString(m, "be")
	press(m, "home", "left")

	if m.inputMode != inputQuery || m.input != "be" || m.inputCursor != 0 || m.barSel != -1 {
		t.Fatalf("mode=%d input=%q cursor=%d sel=%d, want the input focused, text kept, cursor at 0",
			m.inputMode, m.input, m.inputCursor, m.barSel)
	}
}

// ↑ from the input focuses the last pill; ↓ or enter edits it.
func TestUpFocusesPillsAndDownEdits(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	press(m, "/")
	press(m, "up")
	if m.barSel != 0 {
		t.Fatalf("up: sel=%d, want the last pill", m.barSel)
	}
	press(m, "down")
	if len(m.pills) != 0 || m.input != "alpha" {
		t.Fatalf("down: pills=%d input=%q, want the pill moved into the input", len(m.pills), m.input)
	}
}

// alt+i selects the whole input: the next rune replaces it.
func TestInputSelectAllTypingReplaces(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha")
		press(m, "alt+i")
		if !m.inputSel {
			t.Fatal("alt+i did not select the input")
		}
		typeString(m, "b")
		if m.input != "b" || m.inputCursor != 1 || m.inputSel {
			t.Fatalf("input=%q cursor=%d sel=%v, want %q at 1 with no selection",
				m.input, m.inputCursor, m.inputSel, "b")
		}
	})
}

// A decoder that keeps the rune text still spells select-all as alt+i.
func TestInputSelectAllWithKeyText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	press(m, "f")
	typeString(m, "alpha")

	m.Update(tea.KeyPressMsg{Code: 'i', Mod: tea.ModAlt, Text: "i"})
	if !m.inputSel || m.input != "alpha" {
		t.Fatalf("input=%q sel=%v, want the whole query selected", m.input, m.inputSel)
	}
}

// A paste replaces the selection too, in one step.
func TestInputSelectAllPasteReplaces(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha")
		press(m, "alt+i")
		m.Update(tea.PasteMsg{Content: "beta"})
		if m.input != "beta" || m.inputCursor != 4 || m.inputSel {
			t.Fatalf("input=%q cursor=%d sel=%v, want %q at 4 with no selection",
				m.input, m.inputCursor, m.inputSel, "beta")
		}
	})
}

// backspace and delete clear a selection instead of eating one rune.
func TestInputSelectAllBackspaceAndDeleteClear(t *testing.T) {
	for _, key := range []string{"backspace", "delete"} {
		t.Run(key, func(t *testing.T) {
			withInputMode(t, func(t *testing.T, m *tuiModel) {
				typeString(m, "alpha")
				press(m, "alt+i", key)
				if m.input != "" || m.inputCursor != 0 || m.inputSel {
					t.Fatalf("input=%q cursor=%d sel=%v after %s, want empty",
						m.input, m.inputCursor, m.inputSel, key)
				}
				if m.inputMode == inputNone {
					t.Fatalf("%s closed the input, want it open", key)
				}
			})
		})
	}
}

// Any cursor move drops the selection and keeps the text.
func TestInputSelectAllDroppedByMove(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha")
		press(m, "alt+i", "left")
		if m.inputSel {
			t.Fatal("left kept the selection")
		}
		if m.input != "alpha" || m.inputCursor != 4 {
			t.Fatalf("input=%q cursor=%d after left, want %q at 4", m.input, m.inputCursor, "alpha")
		}
	})
}

// The whole input draws reversed while it is selected, and plain when it is not.
func TestInputSelectionDrawsReversed(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha")
		if box := m.inputBoxView(); strings.Contains(box, "\x1b[7m") {
			t.Fatalf("box = %q, want no reverse without a selection", box)
		}
		press(m, "alt+i")
		box := m.inputBoxView()
		if !strings.Contains(box, "\x1b[7m") {
			t.Fatalf("box = %q, want the selected text drawn reversed", box)
		}
		if !strings.Contains(ansi.Strip(box), "alpha") {
			t.Fatalf("box = %q, want the text kept", ansi.Strip(box))
		}
	})
}

// The terminal cursor sits at the text cursor, not at the end of the text.
func TestInputCursorSitsAtTheTextCursor(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "alpha")
		press(m, "left", "left")

		view := m.View()
		if view.Cursor == nil {
			t.Fatal("the open input drew no terminal cursor")
		}
		if wantX, wantY := caretCell(t, view.Content, "alp"); view.Cursor.X != wantX || view.Cursor.Y != wantY {
			t.Fatalf("cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
		}

		press(m, "home")
		view = m.View()
		if wantX, wantY := caretCell(t, view.Content, ""); view.Cursor.X != wantX || view.Cursor.Y != wantY {
			t.Fatalf("after home cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
		}
	})
}

// A query wider than the box scrolls, so the cursor and its rune stay visible.
func TestInputLongTextKeepsTheCursorVisible(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		m.width, m.height = 40, 20
		long := strings.Repeat("abcdefghij", 5)
		typeString(m, long)
		press(m, "home")

		view := m.View()
		if view.Cursor == nil {
			t.Fatal("the open input drew no terminal cursor")
		}
		if wantX, wantY := caretCell(t, view.Content, ""); view.Cursor.X != wantX || view.Cursor.Y != wantY {
			t.Fatalf("cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
		}
		if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, "❯ abc") {
			t.Fatalf("box = %q, want the head of the query from the first rune", box)
		}

		press(m, "right", "right", "right")
		view = m.View()
		if wantX, wantY := caretCell(t, view.Content, "abc"); view.Cursor.X != wantX || view.Cursor.Y != wantY {
			t.Fatalf("mid-text cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
		}
	})
}

// Reopening the box starts empty; the committed pill sits on the bar.
func TestInputReopenStartsEmpty(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "alpha beta")
	m.frame()

	press(m, "f")
	typeString(m, "alpha")
	press(m, "home")
	press(m, "enter")

	press(m, "f")
	if m.input != "" || m.inputCursor != 0 {
		t.Fatalf("input=%q cursor=%d, want a fresh empty input", m.input, m.inputCursor)
	}
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" {
		t.Fatalf("pills = %v, want the committed alpha pill", m.pills)
	}
}

// ctrl+w and alt+backspace clear a selection whole, as backspace and delete do.
func TestInputSelectAllCtrlWClears(t *testing.T) {
	for _, key := range []string{"ctrl+w", "alt+backspace"} {
		t.Run(key, func(t *testing.T) {
			withInputMode(t, func(t *testing.T, m *tuiModel) {
				typeString(m, "alpha beta")
				press(m, "alt+i", key)
				if m.input != "" || m.inputCursor != 0 || m.inputSel {
					t.Fatalf("input=%q cursor=%d sel=%v after %s, want empty",
						m.input, m.inputCursor, m.inputSel, key)
				}
			})
		})
	}
}

// A match jump drops the selection and keeps the query it was made from.
func TestInputMatchJumpDropsSelection(t *testing.T) {
	for _, key := range []string{"ctrl+g", "ctrl+shift+g"} {
		t.Run(key, func(t *testing.T) {
			withInputMode(t, func(t *testing.T, m *tuiModel) {
				typeString(m, "alpha")
				press(m, "alt+i", key)
				if m.inputSel {
					t.Fatalf("%s kept the selection", key)
				}
				if m.input != "alpha" {
					t.Fatalf("input = %q after %s, want the query kept", m.input, key)
				}
			})
		})
	}
}

// A multi-line paste stays one line: every break becomes a single space.
func TestInputPasteStaysOneLine(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "beta")
		press(m, "home")
		m.Update(tea.PasteMsg{Content: "alpha\r\nmid\rdelta\n"})
		// The cursor lands after the pasted run, before the text it was typed in front of.
		if want := "alpha mid delta beta"; m.input != want || m.inputCursor != 16 {
			t.Fatalf("input=%q cursor=%d, want %q at 16", m.input, m.inputCursor, want)
		}
		if strings.ContainsAny(m.input, "\r\n") {
			t.Fatalf("input = %q, want no line break kept", m.input)
		}
	})
}

// A wide rune takes two cells in the box, and the caret steps over both of them.
func TestInputWideRuneCaret(t *testing.T) {
	withInputMode(t, func(t *testing.T, m *tuiModel) {
		typeString(m, "ab世")
		if box := ansi.Strip(m.inputBoxView()); !strings.Contains(box, "ab世") {
			t.Fatalf("box = %q, want the wide rune kept", box)
		}
		view := m.View()
		if view.Cursor == nil {
			t.Fatal("the open input drew no terminal cursor")
		}
		if wantX, wantY := caretCell(t, view.Content, "ab世"); view.Cursor.X != wantX || view.Cursor.Y != wantY {
			t.Fatalf("cursor = (%d,%d), want (%d,%d)", view.Cursor.X, view.Cursor.Y, wantX, wantY)
		}
	})
}

// A wide rune at the window edge stays whole, and a budget under it still shows it.
func TestInputWindowKeepsWideRunesWhole(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	press(m, "f")

	m.input, m.inputCursor = "世ab", 1
	if text, caret := m.inputWindow(2); text != "世" || caret != 2 {
		t.Fatalf("edge window text=%q caret=%d, want %q at 2", text, caret, "世")
	}

	m.input, m.inputCursor = "世", 1
	if text, caret := m.inputWindow(1); text != "世" || caret != 0 {
		t.Fatalf("narrow window text=%q caret=%d, want %q at 0", text, caret, "世")
	}
}
