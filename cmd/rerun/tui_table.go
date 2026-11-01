package main

import (
	"slices"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// tableHint is shown when `c` has no columns to work with.
const tableHint = "c needs a target with columns"

// tableWidthRows bounds the width scan: only the newest rows decide the
// columns, so a long history cannot slow a frame.
const tableWidthRows = 1000

// cellCache is one visible line's table form. A zero value means "not
// evaluated yet"; raw means the line has no table form and keeps its raw text.
type cellCache struct {
	vals []string
	raw  bool
}

func (c cellCache) evaluated() bool { return c.raw || c.vals != nil }

// toggleTable flips the pane between raw lines and the columns table. A pane
// with nothing to tabulate says so instead of toggling.
func (m *tuiModel) toggleTable() {
	if m.table {
		m.table = false
		m.vpDirty = true
		return
	}
	if !m.columnsAvailable() {
		m.showHint(tableHint)
		return
	}
	m.table = true
	m.vpDirty = true
}

// columnsAvailable reports whether the row under the cursor can drive a table:
// a target row needs its own columns, and the ALL row needs columns anywhere.
func (m *tuiModel) columnsAvailable() bool {
	if m.cursor > 0 {
		return len(m.targets[m.cursor-1].spec.Columns) > 0
	}
	for _, t := range m.targets {
		if len(t.spec.Columns) > 0 {
			return true
		}
	}
	return false
}

// tableLines renders the pane as a table: each row keeps its name prefix and
// then shows its own target's column values, aligned to per-column widths.
// A row without columns, or one whose line is not a JSON object or whose
// expressions do not compile, keeps its raw text. Only rows not yet evaluated
// are parsed, so a chatty stream pays one decode per arriving line.
func (m *tuiModel) tableLines(lines []string) []string {
	if !m.table || len(lines) == 0 {
		return lines
	}
	m.evaluateCells()
	widths := m.tableWidths()
	budget := m.paneWidth()

	out := make([]string, len(lines))
	for row := range lines {
		raw := m.paneRowRaw(row)
		c := m.visibleCells[raw]
		if !c.evaluated() || c.raw {
			out[row] = lines[row]
			continue
		}
		t := m.visibleTarget(raw)
		if t == nil {
			out[row] = lines[row]
			continue
		}
		var b strings.Builder
		b.WriteString(padRight(t.spec.Name, m.prefixW))
		b.WriteString("  ")
		for i, v := range c.vals {
			if i > 0 {
				b.WriteString("  ")
			}
			w := 0
			if i < len(widths) {
				w = widths[i]
			}
			b.WriteString(padRight(v, w))
		}
		out[row] = truncate(strings.TrimRight(b.String(), " "), budget)
	}
	return out
}

// evaluateCells parses every visible line that has no cached table form yet,
// once. Rebuilds clear the cache, so a solo flip or a config reload reswarms
// it, while an arriving line costs exactly one decode.
func (m *tuiModel) evaluateCells() {
	m.ensurePane()
	for i := range m.visibleRaw {
		if m.visibleCells[i].evaluated() {
			continue
		}
		m.cellEvals++
		m.visibleCells[i] = m.evaluateCell(i)
	}
}

// evaluateCell is one line's table form: its column values, or raw when its
// target has none, its expressions do not compile, or the line is not a JSON
// object.
func (m *tuiModel) evaluateCell(raw int) cellCache {
	t := m.visibleTarget(raw)
	if t == nil {
		return cellCache{raw: true}
	}
	exprs, ok := t.columns()
	if !ok {
		return cellCache{raw: true}
	}
	vals, ok := yqObjectValues(exprs, m.visibleRaw[raw])
	if !ok {
		return cellCache{raw: true}
	}
	return cellCache{vals: vals}
}

// tableWidths is the per-column width over the newest rows' cached cells. Each
// contribution is capped at the row budget, so one huge cell cannot blow the
// row or the frame.
func (m *tuiModel) tableWidths() []int {
	budget := m.tableBudget()
	start := max(0, len(m.visibleCells)-tableWidthRows)
	var widths []int
	for i := start; i < len(m.visibleCells); i++ {
		c := m.visibleCells[i]
		if !c.evaluated() || c.raw {
			continue
		}
		for j, v := range c.vals {
			w := min(displayWidth(v), budget)
			if j >= len(widths) {
				widths = append(widths, w)
			} else if w > widths[j] {
				widths[j] = w
			}
		}
	}
	return widths
}

// tableBudget is the cells a table row's values may use: the pane minus the
// name prefix and its two-space separator.
func (m *tuiModel) tableBudget() int {
	w := m.paneWidth()
	if w <= 0 {
		return 0
	}
	return max(0, w-m.prefixW-2)
}

// columns returns the target's compiled column expressions, reparsing only
// when the spec's columns changed. A spec with a bad expression yields no
// table, so its lines stay raw.
func (t *tuiTarget) columns() ([]*yqlib.ExpressionNode, bool) {
	if !slices.Equal(t.colSpec, t.spec.Columns) {
		t.colSpec = t.spec.Columns
		t.colExprs = t.colExprs[:0]
		t.colBad = false
		for _, col := range t.spec.Columns {
			expr, err := yqParse(col.Expr)
			if err != nil {
				t.colBad = true
				break
			}
			t.colExprs = append(t.colExprs, expr)
		}
	}
	if t.colBad || len(t.colExprs) == 0 {
		return nil, false
	}
	return t.colExprs, true
}

// visibleTarget resolves a raw-line index to the target that produced it, or
// nil for one of rerun's own notice lines.
func (m *tuiModel) visibleTarget(i int) *tuiTarget {
	if i < 0 || i >= len(m.visibleIdx) {
		return nil
	}
	row := m.visibleIdx[i]
	if row < 0 || row >= len(m.targets) {
		return nil
	}
	return m.targets[row]
}
