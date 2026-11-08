package main

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Chip kinds, in the order the bar draws them.
const (
	chipValue = iota // a value clicked in the pane
	chipText         // a committed text filter
	chipFind         // the find query: highlights matches, hides nothing
)

// barChip is one chip on the filter bar. idx indexes m.chips or m.filters.
type barChip struct {
	kind  int
	idx   int
	label string
}

// barChips lists the bar's chips in order: values, text filters, find.
func (m *tuiModel) barChips() []barChip {
	var out []barChip
	for i, c := range m.chips {
		out = append(out, barChip{kind: chipValue, idx: i, label: c.expr})
	}
	for i, f := range m.filters {
		out = append(out, barChip{kind: chipText, idx: i, label: f.query})
	}
	if m.search != "" {
		out = append(out, barChip{kind: chipFind, label: m.search})
	}
	return out
}

// barChipText is a chip as drawn. The × always closes it, and find gets its own brackets.
func barChipText(c barChip) string {
	if c.kind == chipFind {
		return "(⌕ " + c.label + " ×)"
	}
	return "[" + c.label + " ×]"
}

func (m *tuiModel) barRows() int {
	if m.barFocus || len(m.barChips()) > 0 {
		return 1
	}
	return 0
}

// barLine draws the filter bar's one row and records each chip's × cell for clicks.
func (m *tuiModel) barLine(w int) string {
	m.layout.chips = m.layout.chips[:0]
	chips := m.barChips()
	texts := make([]string, len(chips))
	for i, c := range chips {
		texts[i] = barChipText(c)
	}
	hidden := m.barHidden(texts, w)
	var parts []string
	x := 0
	if hidden > 0 {
		more := fmt.Sprintf("+%d", hidden)
		parts = append(parts, m.styles.hint.Render(more))
		x += displayWidth(more) + 1
	}
	for i := hidden; i < len(chips); i++ {
		text := texts[i]
		style := m.styles.chip
		if chips[i].kind == chipFind {
			style = m.styles.chipFind
		}
		if m.barFocus && m.barSel == i {
			style = style.Reverse(true)
		}
		cross := strings.LastIndex(text, "×")
		m.layout.chips = append(m.layout.chips, chipHit{x: x + displayWidth(text[:cross]), idx: i})
		parts = append(parts, style.Render(text))
		x += displayWidth(text) + 1
	}
	switch {
	case len(chips) == 0:
		parts = append(parts, m.styles.hint.Render("/ filter · ctrl+f find"))
	default:
		parts = append(parts, m.styles.hint.Render("/ add · ← backspace remove · ctrl+f find"))
	}
	// The typing box owns the prompt; a yq error still needs a home on the bar.
	if m.inputMode == inputFilter && m.filterErr != "" {
		parts = append(parts, m.styles.hint.Render("yq: "+m.filterErr))
	}
	return padRight(strings.Join(parts, " "), w)
}

// barHidden counts the leading chips to fold into +N, so the rest fit width w whole.
func (m *tuiModel) barHidden(texts []string, w int) int {
	hidden := len(texts) - 1
	for h := 0; h < len(texts)-1; h++ {
		width := 0
		if h > 0 {
			width = displayWidth(fmt.Sprintf("+%d", h)) + 1
		}
		for _, t := range texts[h:] {
			width += displayWidth(t) + 1
		}
		if width <= w {
			hidden = h
			break
		}
	}
	// The focused chip must stay on screen, or the arrows would point at nothing.
	if m.barFocus && m.barSel >= 0 && m.barSel < hidden {
		hidden = m.barSel
	}
	return max(0, hidden)
}

// removeBarChip removes the chip a click or key named, then fixes the bar's focus.
func (m *tuiModel) removeBarChip(c barChip) {
	switch c.kind {
	case chipValue:
		m.removeChip(c.idx)
	case chipText:
		m.removeFilter(c.idx)
	case chipFind:
		m.clearSearch()
	}
	m.clampBar()
}

func (m *tuiModel) removeFilter(idx int) {
	if idx < 0 || idx >= len(m.filters) {
		return
	}
	m.filters = slices.Delete(m.filters, idx, idx+1)
	m.chipsChanged()
}

// Focus must not outlive the last chip it could point at.
func (m *tuiModel) clampBar() {
	n := len(m.barChips())
	if m.barSel >= n {
		m.barSel = n - 1
	}
	if n == 0 && m.inputMode == inputNone {
		m.barFocus = false
	}
}

// openBar focuses the bar with its input open; paneFocus stays set underneath.
func (m *tuiModel) openBar(mode int) {
	m.barFocus = true
	m.barSel = -1
	m.openInput(mode)
}

func (m *tuiModel) blurBar() {
	m.barFocus = false
	m.barSel = -1
}

// handleBarNav owns the arrows, backspace and esc; false means the caller blurs and runs the key.
func (m *tuiModel) handleBarNav(msg tea.KeyPressMsg) bool {
	chips := m.barChips()
	switch msg.String() {
	case "left":
		switch {
		case len(chips) == 0:
		case m.barSel < 0:
			m.barSel = len(chips) - 1
		case m.barSel > 0:
			m.barSel--
		}
	case "right":
		if m.barSel >= 0 {
			if m.barSel < len(chips)-1 {
				m.barSel++
			} else {
				m.barSel = -1
			}
		}
	// ctrl+h is how the enhanced decoder spells backspace (kitty disambiguation).
	case "backspace", "ctrl+h", "delete":
		if len(chips) == 0 {
			break
		}
		idx := m.barSel
		if idx < 0 {
			idx = len(chips) - 1
		}
		m.removeBarChip(chips[idx])
	case "esc", "ctrl+[":
		m.blurBar()
	case "/", "v":
		m.openBar(inputFilter)
	case "ctrl+f":
		m.openBar(inputSearch)
	default:
		return false
	}
	return true
}

func (m *tuiModel) jumpGroup(dir int) {
	rows := m.listRows()
	for i := m.cursor + dir; i >= 0 && i < len(rows); i += dir {
		if rows[i].header {
			m.cursor = i
			m.applyRowScope(rows[i])
			return
		}
	}
}

// scopeState is what esc can change in the list; a comparison tells whether it did.
type scopeState struct {
	solo   int
	sel    string
	cursor int
}

func (m *tuiModel) scopeState() scopeState {
	return scopeState{solo: m.solo, sel: m.schemaSel, cursor: m.cursor}
}

// listFocused reports whether the list, not the pane or drawer, owns the keys.
func (m *tuiModel) listFocused() bool {
	return !m.paneFocus && !m.drawerFocus
}

// escList backs the scope out one level; filters and chips are never touched.
func (m *tuiModel) escList() {
	r, ok := m.cursorRow()
	if !ok || (r.header && r.depth == 0) || !m.hasGroups() {
		m.clearScope()
		return
	}
	i := m.enclosingHeader(m.cursor)
	if i < 0 {
		m.clearScope()
		return
	}
	m.cursor = i
	hr, _ := m.listRowAt(i)
	m.applyRowScope(hr)
}
