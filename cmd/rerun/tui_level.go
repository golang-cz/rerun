package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// levelKeys are the JSON keys that carry a line's level, in priority order.
var levelKeys = []string{"level", "severity", "lvl"}

var levelKeyRes = func() map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp, len(levelKeys))
	for _, k := range levelKeys {
		out[k] = regexp.MustCompile(fmt.Sprintf(`(?i)"(%s)"\s*:\s*("[^"]*"|[^,}\s]+)`, k))
	}
	return out
}()

// levelHit is a JSON line's level word and the byte span of its value in the raw line.
type levelHit struct {
	word     string
	from, to int
}

// levelPaint is how a level colors its line.
type levelPaint struct {
	fg, bg lipgloss.TerminalColor
	bold   bool
	// whole paints the text after the name prefix, not only the level cell.
	whole bool
}

// levelSpan is the cells a level paints, and where the line's text starts.
type levelSpan struct {
	from, to, text int
	paint          levelPaint
}

// jsonLevel is the first level key present in the line, in key priority order.
func jsonLevel(line string) levelHit {
	for _, k := range levelKeys {
		loc := levelKeyRes[k].FindStringSubmatchIndex(line)
		if loc == nil {
			continue
		}
		return levelHit{word: strings.Trim(line[loc[4]:loc[5]], `"`), from: loc[4], to: loc[5]}
	}
	return levelHit{}
}

// tableLevel is the column index and value of the first level column the target shows.
func tableLevel(t *tuiTarget, vals []string) (int, string) {
	for _, k := range levelKeys {
		for i, col := range t.spec.Columns {
			if strings.EqualFold(col.Name, k) && i < len(vals) {
				return i, vals[i]
			}
		}
	}
	return -1, ""
}

// levelPaintOf maps a level word, case-insensitively, to its paint.
func levelPaintOf(word string) (levelPaint, bool) {
	blue := lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	green := lipgloss.AdaptiveColor{Light: "28", Dark: "46"}
	yellow := lipgloss.AdaptiveColor{Light: "136", Dark: "220"}
	red := lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "DEBUG", "TRACE", "DEFAULT":
		return levelPaint{fg: blue}, true
	case "INFO", "NOTICE":
		return levelPaint{fg: green}, true
	case "WARN", "WARNING":
		return levelPaint{fg: yellow}, true
	case "ERROR", "ERR":
		return levelPaint{fg: red, whole: true}, true
	case "ALERT", "CRITICAL", "CRIT", "FATAL", "EMERGENCY", "PANIC":
		return levelPaint{fg: lipgloss.Color("15"), bg: red, bold: true, whole: true}, true
	}
	return levelPaint{}, false
}

// levelFor is a row's level span, or false when the row paints nothing.
func (m *tuiModel) levelFor(row int, line string, widths []int, budget int) (levelSpan, bool) {
	r := m.rowInfo(row)
	if r.sub >= 0 || m.visibleSep[r.raw].isSep() {
		return levelSpan{}, false
	}
	if m.tableOn() {
		c := m.visibleCells[r.raw]
		t := m.visibleTarget(r.raw)
		if !c.evaluated() || c.raw || t == nil {
			return levelSpan{}, false
		}
		idx, word := tableLevel(t, c.vals)
		paint, ok := levelPaintOf(word)
		if idx < 0 || !ok {
			return levelSpan{}, false
		}
		_, spans := tableRow(padRight(t.spec.Name, m.prefixW)+"  ", c.vals, widths, budget)
		if idx >= len(spans) {
			return levelSpan{}, false
		}
		text := m.prefixW + 2
		return finishSpan(levelSpan{from: spans[idx].x0, to: spans[idx].x1, text: text, paint: paint}, line), true
	}
	hit := m.visibleLvl[r.raw]
	paint, ok := levelPaintOf(hit.word)
	if !ok {
		return levelSpan{}, false
	}
	raw := m.visibleRaw[r.raw]
	lead := displayWidth(line) - displayWidth(raw)
	return finishSpan(levelSpan{
		from:  lead + displayWidth(raw[:hit.from]),
		to:    lead + displayWidth(raw[:hit.to]),
		text:  lead,
		paint: paint,
	}, line), true
}

// finishSpan widens a whole-text or screaming level to the cells it paints.
func finishSpan(s levelSpan, line string) levelSpan {
	w := displayWidth(line)
	switch {
	case s.paint.bg != nil:
		s.from, s.to = 0, w
	case s.paint.whole:
		s.from, s.to = s.text, w
	}
	return s
}

// paintLevels colors each line's level before panning, so the spans are in pane cells.
func (m *tuiModel) paintLevels(lines []string) []string {
	var widths []int
	if m.tableOn() {
		widths = m.tableWidths()
	}
	budget := m.paneWidth()
	var out []string
	for row, line := range lines {
		span, ok := m.levelFor(row, line, widths, budget)
		if !ok {
			continue
		}
		if out == nil {
			out = append([]string(nil), lines...)
		}
		out[row] = paintSpan(line, span)
	}
	if out == nil {
		return lines
	}
	return out
}

// paintSpan styles the cells a level covers, keeping every other cell as it was.
func paintSpan(line string, s levelSpan) string {
	w := displayWidth(line)
	from, to := max(0, s.from), min(s.to, w)
	if to <= from {
		return line
	}
	st := lipgloss.NewStyle()
	if s.paint.fg != nil {
		st = st.Foreground(s.paint.fg)
	}
	if s.paint.bg != nil {
		st = st.Background(s.paint.bg)
	}
	if s.paint.bold {
		st = st.Bold(true)
	}
	return ansi.Cut(line, 0, from) + st.Render(ansi.Cut(line, from, to)) + ansi.Cut(line, to, w)
}
