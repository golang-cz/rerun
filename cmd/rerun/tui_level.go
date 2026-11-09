package main

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
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
	fg, bg color.Color
	bold   bool
}

// levelSpan is the cells a level paints.
type levelSpan struct {
	from, to int
	paint    levelPaint
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
func levelPaintOf(word string, isDark bool) (levelPaint, bool) {
	lightDark := lipgloss.LightDark(isDark)
	blue := lightDark(lipgloss.Color("25"), lipgloss.Color("39"))
	green := lightDark(lipgloss.Color("28"), lipgloss.Color("46"))
	yellow := lightDark(lipgloss.Color("136"), lipgloss.Color("220"))
	red := lightDark(lipgloss.Color("160"), lipgloss.Color("196"))
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "DEBUG", "TRACE", "DEFAULT":
		return levelPaint{fg: blue}, true
	case "INFO", "NOTICE":
		return levelPaint{fg: green}, true
	case "WARN", "WARNING":
		return levelPaint{fg: yellow}, true
	case "ERROR", "ERR":
		return levelPaint{fg: red}, true
	case "ALERT", "CRITICAL", "CRIT", "FATAL", "EMERGENCY", "PANIC":
		return levelPaint{fg: lipgloss.Color("15"), bg: red, bold: true}, true
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
		paint, ok := levelPaintOf(word, m.isDark)
		if idx < 0 || !ok {
			return levelSpan{}, false
		}
		_, spans := tableRow(m.namePrefix(t.spec.Name), c.vals, widths, budget)
		if idx >= len(spans) {
			return levelSpan{}, false
		}
		return finishSpan(levelSpan{from: spans[idx].x0, to: spans[idx].x1, paint: paint}, line), true
	}
	hit := m.visibleLvl[r.raw]
	paint, ok := levelPaintOf(hit.word, m.isDark)
	if !ok {
		return levelSpan{}, false
	}
	raw := m.visibleRaw[r.raw]
	lead := displayWidth(line) - displayWidth(raw)
	return finishSpan(levelSpan{
		from:  lead + displayWidth(raw[:hit.from]),
		to:    lead + displayWidth(raw[:hit.to]),
		paint: paint,
	}, line), true
}

// finishSpan widens a screaming level to the whole line, background included.
func finishSpan(s levelSpan, line string) levelSpan {
	if s.paint.bg != nil {
		s.from, s.to = 0, displayWidth(line)
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
	return paintCells(line, s.from, s.to, st)
}
