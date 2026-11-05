package main

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// tableHint is shown when `c` has no columns to work with.
const tableHint = "c needs a target with columns"

const columnCapW = 24

const sampleValues = 100

const clockLayout = "15:04:05"

// cellCache is one visible line's table form. A zero value means "not
// evaluated yet"; raw means the line has no table form and keeps its raw text.
type cellCache struct {
	vals []string
	raw  bool
}

func (c cellCache) evaluated() bool { return c.raw || c.vals != nil }

type cellEntry struct {
	cell cellCache
	// A column change must re-decode the line, so the decoding columns are kept.
	cols []Column
	gen  int
}

// toggleTable flips the selection between raw lines and the columns table. A
// selection with nothing to tabulate says so instead of toggling.
func (m *tuiModel) toggleTable() {
	if !m.columnsAvailable() {
		m.showHint(tableHint)
		return
	}
	m.tableFlip = !m.tableFlip
	m.vpDirty = true
}

// tableOn is the selection's default layout, flipped by c.
func (m *tuiModel) tableOn() bool { return m.tableDefault() != m.tableFlip }

// tableDefault is the layout a selection starts in; nothing selected needs shared columns.
func (m *tuiModel) tableDefault() bool {
	if m.solo < 0 && m.schemaSel == "" {
		return m.sharedColumns()
	}
	return m.anyColumns(m.scopeMembers())
}

// columnsAvailable reports whether c has a table to offer the selection.
func (m *tuiModel) columnsAvailable() bool { return m.anyColumns(m.scopeMembers()) }

// scopeMembers is the target indices the selection shows.
func (m *tuiModel) scopeMembers() []int {
	switch {
	case m.solo >= 0:
		return []int{m.solo}
	case m.schemaSel != "":
		return m.schemaMembers(m.schemaSel)
	}
	all := make([]int, len(m.targets))
	for i := range all {
		all[i] = i
	}
	return all
}

func (m *tuiModel) anyColumns(members []int) bool {
	for _, i := range members {
		if len(m.targets[i].spec.Columns) > 0 {
			return true
		}
	}
	return false
}

// sharedColumns reports whether every target has the same non-empty columns.
func (m *tuiModel) sharedColumns() bool {
	if len(m.targets) == 0 || len(m.targets[0].spec.Columns) == 0 {
		return false
	}
	for _, t := range m.targets[1:] {
		if !slices.Equal(t.spec.Columns, m.targets[0].spec.Columns) {
			return false
		}
	}
	return true
}

// tableLines renders the pane as a table: each row keeps its name prefix and
// then shows its own target's column values, aligned to per-column widths.
// A row without columns, or one whose line is not a JSON object or whose
// expressions do not compile, keeps its raw text. Only rows not yet evaluated
// are parsed, so a chatty stream pays one decode per arriving line.
func (m *tuiModel) tableLines(lines []string) []string {
	if !m.tableOn() || len(lines) == 0 {
		return lines
	}
	m.evaluateCells()
	widths := m.tableWidths()
	budget := m.paneWidth()

	out := make([]string, len(lines))
	for row := range lines {
		r := m.rowInfo(row)
		c := m.visibleCells[r.raw]
		if r.sub >= 0 || !c.evaluated() || c.raw {
			out[row] = lines[row]
			continue
		}
		t := m.visibleTarget(r.raw)
		if t == nil {
			out[row] = lines[row]
			continue
		}
		out[row], _ = tableRow(padRight(t.spec.Name, m.prefixW)+"  ", c.vals, widths, budget)
	}
	return out
}

// cellSpan is the cell range one table column occupies in a row, and its column index.
type cellSpan struct{ x0, x1, col int }

// tableRow lays cells out at widths after lead, two spaces apart, and reports
// where each column sits. The row is cut to budget cells.
func tableRow(lead string, cells []string, widths []int, budget int) (string, []cellSpan) {
	var b strings.Builder
	b.WriteString(lead)
	x := displayWidth(lead)
	spans := make([]cellSpan, 0, len(cells))
	for i, v := range cells {
		if i > 0 {
			b.WriteString("  ")
			x += 2
		}
		w := columnCapW
		if i < len(widths) {
			w = widths[i]
		}
		b.WriteString(padRight(ellipsize(v, w), w))
		spans = append(spans, cellSpan{x0: x, x1: x + w, col: i})
		x += w
	}
	return truncate(strings.TrimRight(b.String(), " "), budget), spans
}

func (m *tuiModel) evaluateCells() {
	m.ensurePane()
	m.syncColumnSample()
	for i := range m.visibleRaw {
		if m.visibleCells[i].evaluated() {
			continue
		}
		m.visibleCells[i] = m.cellFor(i).cell
		m.sampleAt(i)
	}
}

func (m *tuiModel) cellFor(raw int) *cellEntry {
	seq := m.visibleSeq[raw]
	var cols []Column
	if t := m.visibleTarget(raw); t != nil {
		cols = t.spec.Columns
	}
	if e, ok := m.cellBySeq[seq]; ok && slices.Equal(e.cols, cols) {
		return e
	}
	m.cellEvals++
	e := &cellEntry{cell: m.evaluateCell(raw), cols: slices.Clone(cols)}
	m.cellBySeq[seq] = e
	return e
}

func (m *tuiModel) sampleAt(raw int) {
	e, ok := m.cellBySeq[m.visibleSeq[raw]]
	if !ok || e.cell.raw || e.gen == m.colSample.gen {
		return
	}
	e.gen = m.colSample.gen
	m.colSample.observe(e.cell.vals)
}

// evaluateCell is one line's table form: its column values, or raw when its
// target has none, its expressions do not compile, or the line is not a JSON
// object.
func (m *tuiModel) evaluateCell(raw int) cellCache {
	if m.visibleSep[raw].isSep() {
		return cellCache{raw: true}
	}
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
	for i := range vals {
		if i < len(t.spec.Columns) && timeColumn(t.spec.Columns[i].Name) {
			vals[i] = clockValue(vals[i])
		}
	}
	return cellCache{vals: vals}
}

// Widths ignore the rows, so a cell keeps its place as rows arrive.
func (m *tuiModel) tableWidths() []int {
	names := m.syncColumnSample()
	widths := make([]int, len(names))
	for i, name := range names {
		if w, ok := fixedColumnW(name); ok {
			widths[i] = w
		} else {
			widths[i] = m.colSample.width(i)
		}
	}
	if n := len(widths); n > 0 {
		used := 2 * (n - 1)
		for _, w := range widths[:n-1] {
			used += w
		}
		widths[n-1] = max(0, m.tableBudget()-used)
	}
	return widths
}

func fixedColumnW(name string) (w int, ok bool) {
	switch {
	case name == "level":
		return 5, true
	case timeColumn(name):
		return 8, true
	}
	return 0, false
}

func timeColumn(name string) bool {
	return name == "time" || name == "ts" || name == "timestamp"
}

func clockValue(v string) string {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Local().Format(clockLayout)
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t.Format(clockLayout)
		}
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || strings.ContainsAny(v, "eE") {
		return v
	}
	whole, _, _ := strings.Cut(v, ".")
	switch len(whole) {
	case 10:
		sec := math.Floor(f)
		return time.Unix(int64(sec), int64((f-sec)*1e9)).Local().Format(clockLayout)
	case 13:
		return time.UnixMilli(int64(f)).Local().Format(clockLayout)
	}
	return v
}

type colSampler struct {
	names  []string
	seen   []int
	widest []int
	frozen []int
	// Counted once per generation, so a rebuilt cache never double-counts a line.
	gen int
}

func (s *colSampler) reset(names []string) {
	s.gen++
	s.names = slices.Clone(names)
	s.seen = make([]int, len(names))
	s.widest = make([]int, len(names))
	s.frozen = make([]int, len(names))
}

// The last column takes the remainder, so it is never sampled.
func (s *colSampler) observe(vals []string) {
	for i, v := range vals {
		if i >= len(s.names)-1 {
			break
		}
		if _, fixed := fixedColumnW(s.names[i]); fixed || s.frozen[i] > 0 || v == "" {
			continue
		}
		s.seen[i]++
		s.widest[i] = max(s.widest[i], min(displayWidth(v), columnCapW))
		if s.seen[i] >= sampleValues {
			s.frozen[i] = s.provisional(i)
		}
	}
}

func (s *colSampler) provisional(i int) int {
	return min(columnCapW, max(displayWidth(s.names[i])+columnMarkW, s.widest[i]))
}

func (s *colSampler) width(i int) int {
	if s.frozen[i] > 0 {
		return s.frozen[i]
	}
	return s.provisional(i)
}

func (m *tuiModel) syncColumnSample() []string {
	names := m.columnNames()
	if slices.Equal(m.colSample.names, names) {
		return names
	}
	m.colSample.reset(names)
	for i := range m.visibleRaw {
		if m.visibleCells[i].evaluated() {
			m.sampleAt(i)
		}
	}
	return names
}

func (m *tuiModel) columnNames() []string {
	cols := m.headerColumns()
	if len(cols) == 0 {
		for _, i := range m.scopeMembers() {
			if len(m.targets[i].spec.Columns) > 0 {
				cols = m.targets[i].spec.Columns
				break
			}
		}
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names
}

// The (+) mark takes the row's last cells, so a row is never cut under it.
func (m *tuiModel) tableBudget() int {
	w := m.paneWidth()
	if w <= 0 {
		return 0
	}
	return max(0, w-m.prefixW-2-markW)
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
// nil when no target row owns it.
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
