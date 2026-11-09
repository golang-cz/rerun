package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The footer names only the keys that act now: f shows only on JSON-only output.
const (
	originalFooterRaw    = "c copy · esc close"
	originalFooterToggle = "f pretty · c copy · esc close"
	originalFooterPretty = "f raw · c copy · esc close"
)

// originalState is the `o` modal, a snapshot taken on open so later arrivals never move it.
type originalState struct {
	open     bool
	scope    string   // the pane scope's label, the header's first word
	pills    []string // the active pills, bracketed by kind
	lines    []string // raw lines, one per selected line, ANSI and controls stripped
	pretty   []string // indented JSON rows; nil unless every line is a JSON object
	prettyOn bool
	top      int // the first wrapped row drawn
	// rows caches the wrapped body; a resize or a `f` toggle rewraps it.
	rows        []string
	wrapW       int
	copiedMsg   string
	copiedUntil time.Time
}

func (o *originalState) text() []string {
	if o.prettyOn {
		return o.pretty
	}
	return o.lines
}

func (m *tuiModel) openOriginal() {
	lines := m.originalLines()
	if len(lines) == 0 {
		return
	}
	o := originalState{open: true, scope: m.scopeName(), lines: lines}
	for _, p := range m.pills {
		o.pills = append(o.pills, originalPillText(p))
	}
	if pretty, ok := originalPretty(lines); ok {
		o.pretty = pretty
	}
	m.original = o
	m.clearSelection()
	m.fitOriginal()
}

func (m *tuiModel) originalLines() []string {
	if m.paneLen() == 0 {
		return nil
	}
	lo, hi, ok := m.selectionRange()
	if !ok {
		row := m.lineRow(m.rowInfo(m.paneCursorRow()).raw)
		lo, hi = row, row
	}
	var raw []string
	for row := lo; row <= hi; row++ {
		r := m.rowInfo(row)
		// rerun's own rules and expansion rows are not service output.
		if r.sub >= 0 || m.visibleSep[r.raw].isSep() {
			continue
		}
		raw = append(raw, ansi.Strip(m.visibleRaw[r.raw]))
	}
	return raw
}

// Brackets name the pill's mode, so it reads without color.
func originalPillText(p pill) string {
	if p.kind == pillHighlight {
		return "⟨" + p.raw + "⟩"
	}
	return "[" + p.raw + "]"
}

func originalPretty(lines []string) ([]string, bool) {
	var out []string
	for _, line := range lines {
		if !isJSONObject(line) {
			return nil, false
		}
		var b bytes.Buffer
		if err := json.Indent(&b, []byte(strings.TrimSpace(line)), "", "  "); err != nil {
			return nil, false
		}
		out = append(out, strings.Split(b.String(), "\n")...)
	}
	return out, true
}

func (m *tuiModel) originalBodyRows(outerH int) int {
	return max(1, outerH-m.styles.box.GetVerticalFrameSize()-3)
}

func (m *tuiModel) originalRows(innerW int) []string {
	o := &m.original
	if o.rows != nil && o.wrapW == innerW {
		return o.rows
	}
	var rows []string
	for _, line := range o.text() {
		if line == "" {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, hardWrapCells(originalDisplay(line), max(2, innerW))...)
	}
	o.rows, o.wrapW = rows, innerW
	return rows
}

// originalDisplay is a raw line as the modal draws it: tabs run to the next 8-cell stop, controls drop.
func originalDisplay(s string) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += ansi.StringWidth(string(r))
	}
	return printable(b.String(), false)
}

// ansi.Hardwrap drops a space at the break, which would change the raw text.
func hardWrapCells(s string, w int) []string {
	var rows []string
	var cur strings.Builder
	cells := 0
	for _, r := range s {
		rw := ansi.StringWidth(string(r))
		if cells+rw > w && cells > 0 {
			rows = append(rows, cur.String())
			cur.Reset()
			cells = 0
		}
		cur.WriteRune(r)
		cells += rw
	}
	return append(rows, cur.String())
}

func (m *tuiModel) fitOriginal() {
	if !m.original.open {
		return
	}
	outerW, outerH := m.modalSize()
	rows := len(m.originalRows(m.modalInnerW(outerW)))
	body := m.originalBodyRows(outerH)
	m.original.top = min(max(m.original.top, 0), max(0, rows-body))
}

func (m *tuiModel) handleOriginalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	o := &m.original
	_, outerH := m.modalSize()
	page := m.originalBodyRows(outerH)
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+[":
		m.original = originalState{}
		return m, nil
	case "j", "down":
		o.top++
	case "k", "up":
		o.top--
	case "pgdown":
		o.top += page
	case "pgup":
		o.top -= page
	case "f":
		if o.pretty != nil {
			o.prettyOn = !o.prettyOn
			o.rows, o.top = nil, 0
		}
	case "c":
		payload, n, truncated := copyPayload(o.text())
		if !truncated {
			n = len(o.lines) // the footer counts the source lines, not the indented rows
		}
		o.copiedMsg = copyNotice(payload, n, truncated)
		o.copiedUntil = m.now.Add(2 * time.Second)
		m.fitOriginal()
		if payload == "" {
			return m, nil
		}
		return m, copyToClipboard(payload)
	}
	m.fitOriginal()
	return m, nil
}

func (m *tuiModel) originalHeader(innerW int) string {
	o := m.original
	right := "raw"
	if o.prettyOn {
		right = "pretty"
	}
	left := o.scope + " · " + originalLineCount(len(o.lines))
	if len(o.pills) > 0 {
		left += " · " + strings.Join(o.pills, " ")
	}
	left = ellipsize(left, max(1, innerW-displayWidth(right)-1))
	gap := strings.Repeat(" ", max(1, innerW-displayWidth(left)-displayWidth(right)))
	return m.styles.title.Render(left) + gap + m.styles.hint.Render(right)
}

func originalLineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func (m *tuiModel) originalFooter() string {
	o := m.original
	if m.now.Before(o.copiedUntil) {
		return o.copiedMsg
	}
	switch {
	case o.pretty == nil:
		return originalFooterRaw
	case o.prettyOn:
		return originalFooterPretty
	}
	return originalFooterToggle
}

func (m *tuiModel) originalView() string {
	outerW, outerH := m.modalSize()
	innerW := m.modalInnerW(outerW)
	rows := m.originalRows(innerW)
	body := m.originalBodyRows(outerH)
	lines := make([]string, 0, body)
	for i := m.original.top; i < len(rows) && len(lines) < body; i++ {
		lines = append(lines, rows[i])
	}
	for len(lines) < body {
		lines = append(lines, "")
	}
	return m.modalBox(m.originalHeader(innerW), m.originalFooter(), lines, outerW, outerH)
}

// Every shown row, so a copy or `o` takes exactly what the pills keep.
func (m *tuiModel) selectAllPane() {
	n := m.paneLen()
	if n == 0 {
		return
	}
	first := 0
	m.selAnchor, m.selEnd, m.selKey = &first, n-1, true
}
