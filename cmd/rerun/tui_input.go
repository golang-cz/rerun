package main

import (
	"strings"
	"unicode"
)

// inputPos is the cursor clamped to the text, so no edit can index past it.
func (m *tuiModel) inputPos() int {
	return min(max(0, m.inputCursor), len([]rune(m.input)))
}

// inputSet is the one place the text and its cursor change, keeping the two in step.
func (m *tuiModel) inputSet(text string, pos int) {
	m.input = text
	m.inputCursor = min(max(0, pos), len([]rune(text)))
	m.inputSel = false
	m.inputChanged()
}

// inputInsert types s at the cursor; a selection is what s replaces.
func (m *tuiModel) inputInsert(s string) {
	// The box is one line, so a pasted line break becomes the space it stood for.
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	if s == "" {
		return
	}
	r := []rune(m.input)
	from, to := m.inputPos(), m.inputPos()
	if m.inputSel {
		from, to = 0, len(r)
	}
	out := append(append([]rune{}, r[:from]...), []rune(s)...)
	m.inputSel = false
	m.inputSet(string(append(out, r[to:]...)), from+len([]rune(s)))
}

// inputMoveRune walks one rune; movement is what drops a selection.
func (m *tuiModel) inputMoveRune(dir int) {
	m.inputMoveTo(m.inputPos() + dir)
}

// inputMoveWord walks one word, a run between the spaces.
func (m *tuiModel) inputMoveWord(dir int) {
	r := []rune(m.input)
	pos := m.inputPos()
	if dir < 0 {
		for pos > 0 && unicode.IsSpace(r[pos-1]) {
			pos--
		}
		for pos > 0 && !unicode.IsSpace(r[pos-1]) {
			pos--
		}
	} else {
		for pos < len(r) && unicode.IsSpace(r[pos]) {
			pos++
		}
		for pos < len(r) && !unicode.IsSpace(r[pos]) {
			pos++
		}
	}
	m.inputSel = false
	m.inputCursor = pos
}

// inputMoveTo puts the cursor at a rune index, keeping it inside the text.
func (m *tuiModel) inputMoveTo(pos int) {
	m.inputSel = false
	m.inputCursor = min(max(0, pos), len([]rune(m.input)))
}

// inputDeleteSpan drops runes [from, to) and leaves the cursor at from.
func (m *tuiModel) inputDeleteSpan(from, to int) {
	r := []rune(m.input)
	from, to = min(max(0, from), len(r)), min(max(0, to), len(r))
	if to <= from {
		m.inputSel = false
		m.inputCursor = from
		return
	}
	m.inputSet(string(append(r[:from:from], r[to:]...)), from)
}

// inputDeleteBefore drops the rune before the cursor, or the whole selection.
func (m *tuiModel) inputDeleteBefore() {
	if m.inputSel {
		m.inputClearSel()
		return
	}
	m.inputDeleteSpan(m.inputPos()-1, m.inputPos())
}

// inputDeleteAfter drops the rune under the cursor, or the whole selection.
func (m *tuiModel) inputDeleteAfter() {
	if m.inputSel {
		m.inputClearSel()
		return
	}
	m.inputDeleteSpan(m.inputPos(), m.inputPos()+1)
}

// inputDeleteWordBefore drops the word before the cursor, as ctrl+w does.
func (m *tuiModel) inputDeleteWordBefore() {
	if m.inputSel {
		m.inputClearSel()
		return
	}
	pos := m.inputPos()
	m.inputMoveWord(-1)
	from := m.inputCursor
	m.inputDeleteSpan(from, pos)
}

// inputDeleteToStart drops everything before the cursor.
func (m *tuiModel) inputDeleteToStart() {
	m.inputDeleteSpan(0, m.inputPos())
}

// inputSelectAll selects the whole input, with the cursor at its end.
func (m *tuiModel) inputSelectAll() {
	m.inputCursor = len([]rune(m.input))
	m.inputSel = m.input != ""
}

// inputClearSel drops the whole selection, so backspace and delete empty the box.
func (m *tuiModel) inputClearSel() {
	m.inputSet("", 0)
}

// inputWindow is the visible slice of the input plus the caret's cells inside it,
// scrolled so the cursor never leaves the box.
func (m *tuiModel) inputWindow(w int) (string, int) {
	r := []rune(m.input)
	pos := m.inputPos()
	start := 0
	for start < pos && displayWidth(string(r[start:pos])) > w {
		start++
	}
	caret := displayWidth(string(r[start:pos]))
	end := pos
	for used := 0; end < len(r); end++ {
		cw := displayWidth(string(r[end]))
		if caret+used+cw > w {
			break
		}
		used += cw
	}
	// A budget under one wide rune still shows a rune, never a blank box.
	if end == start && len(r) > 0 {
		start = min(start, len(r)-1)
		end, caret = start+1, 0
	}
	return string(r[start:end]), caret
}
