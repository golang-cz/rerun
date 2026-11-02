package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// selectHint is the footer hint when a column pick has no node to write to.
const selectHint = "select a schema or target to add columns"

// unsavedNotice is the footer while column picks are waiting for ctrl+s.
const unsavedNotice = "unsaved columns · ctrl+s save"

// pickChip is a value filter clicked into the pane: its yq expression and the
// compiled program.
type pickChip struct {
	expr string
	prog *yqlib.ExpressionNode
}

// chipHit is a chip's × cell in the header row, and the chip it removes.
type chipHit struct{ x, idx int }

// colNode is what column picks write to: a schema, or a target by name.
type colNode struct {
	schema bool
	name   string
}

// colEdit is one node's columns as picked, unsaved until ctrl+s.
type colEdit struct {
	node colNode
	cols []Column
}

// pickNode is the node the selection's columns belong to. A schema row wins,
// then the soloed target; a target with a schema and no columns of its own
// writes its schema.
func (m *tuiModel) pickNode() (colNode, bool) {
	if m.schemaSel != "" {
		return colNode{schema: true, name: m.schemaSel}, true
	}
	if m.solo < 0 {
		return colNode{}, false
	}
	t := m.targets[m.solo]
	if t.spec.Schema != "" && !t.spec.ownColumns {
		return colNode{schema: true, name: t.spec.Schema}, true
	}
	return colNode{name: t.spec.Name}, true
}

// nodeColumns is the columns a node holds now: its pending edit, else its stored
// columns. A schema's columns come from a member that inherits them, so ok is
// false when every member writes its own.
func (m *tuiModel) nodeColumns(node colNode) ([]Column, bool) {
	if i := m.editIndex(node); i >= 0 {
		return m.edits[i].cols, true
	}
	if !node.schema {
		i := m.targetIndex(node.name)
		if i < 0 {
			return nil, false
		}
		return m.targets[i].spec.Columns, true
	}
	for _, i := range m.schemaMembers(node.name) {
		if !m.targets[i].spec.ownColumns {
			return m.targets[i].spec.Columns, true
		}
	}
	return nil, false
}

// headerColumns is what the table header names: the selection's node, or the
// shared columns when nothing is selected.
func (m *tuiModel) headerColumns() []Column {
	if node, ok := m.pickNode(); ok {
		cols, _ := m.nodeColumns(node)
		return cols
	}
	if m.sharedColumns() {
		return m.targets[0].spec.Columns
	}
	return nil
}

func (m *tuiModel) editIndex(node colNode) int {
	for i, e := range m.edits {
		if e.node == node {
			return i
		}
	}
	return -1
}

// setEdit records a node's picked columns and shows them in the table at once.
func (m *tuiModel) setEdit(node colNode, cols []Column) {
	if i := m.editIndex(node); i >= 0 {
		m.edits[i].cols = cols
	} else {
		m.edits = append(m.edits, colEdit{node: node, cols: cols})
	}
	m.applyEdits()
	m.tableFlip = m.columnsAvailable() != m.tableDefault()
	m.mergedDirty = true
	m.vpDirty = true
	m.dispDirty = true
}

// applyEdits writes every pending node's columns into the rows that use them.
// It runs after a config reload too, so picks survive the reload they precede.
func (m *tuiModel) applyEdits() {
	for _, e := range m.edits {
		for _, t := range m.targets {
			in := t.spec.Name == e.node.name
			if e.node.schema {
				in = t.spec.Schema == e.node.name && !t.spec.ownColumns
			}
			if in {
				t.spec.Columns = slices.Clone(e.cols)
			}
		}
	}
}

// pickKey adds a clicked JSON key as a column of the selection's node.
func (m *tuiModel) pickKey(f *jsonField) {
	node, ok := m.pickNode()
	if !ok {
		m.showHint(selectHint)
		return
	}
	cols, ok := m.nodeColumns(node)
	if !ok {
		m.showHint("every target in " + node.name + " has its own columns")
		return
	}
	for _, c := range cols {
		if c.Expr == f.path {
			m.showHint(f.path + " is already a column")
			return
		}
	}
	m.setEdit(node, append(slices.Clone(cols), Column{Name: f.key, Expr: f.path}))
}

// removeColumn drops the header's column j from the selection's node.
func (m *tuiModel) removeColumn(j int) {
	node, ok := m.pickNode()
	if !ok {
		m.showHint("select a schema or target to remove columns")
		return
	}
	cols, ok := m.nodeColumns(node)
	if !ok || j >= len(cols) {
		return
	}
	m.setEdit(node, slices.Delete(slices.Clone(cols), j, j+1))
}

// pickValue adds a clicked scalar as a filter chip: `.path == literal`.
func (m *tuiModel) pickValue(f *jsonField) {
	expr := f.path + " == " + f.lit
	for _, c := range m.chips {
		if c.expr == expr {
			return
		}
	}
	prog, err := yqParse(expr)
	if err != nil {
		m.showHint("cannot filter on " + expr)
		return
	}
	m.chips = append(m.chips, pickChip{expr: expr, prog: prog})
	m.chipsChanged()
}

// removeChip drops the chip at idx.
func (m *tuiModel) removeChip(idx int) {
	if idx < 0 || idx >= len(m.chips) {
		return
	}
	m.chips = slices.Delete(m.chips, idx, idx+1)
	m.chipsChanged()
}

func (m *tuiModel) chipsChanged() {
	m.paneSelDirty = true
	m.dispDirty = true
	m.vpDirty = true
	m.recomputeSearch(true)
}

func (m *tuiModel) dirty() bool { return len(m.edits) > 0 }

// saveColumns writes every pending node with setColumns. A node that fails
// stays pending, and the nodes before it stay written.
func (m *tuiModel) saveColumns() {
	if !m.dirty() {
		m.showHint("no unsaved columns")
		return
	}
	path := m.group.configPath
	if path == "" {
		m.showNotice("rerun: no rerun.yml to save columns to", true)
		return
	}
	for m.dirty() {
		if err := writeColumns(path, m.edits[0]); err != nil {
			m.showNotice(fmt.Sprintf("rerun: save columns: %v", err), true)
			return
		}
		m.edits = m.edits[1:]
	}
	m.showNotice("saved columns to rerun.yml", false)
}

// writeColumns writes one node's columns as `name {.expr}` entries.
func writeColumns(path string, e colEdit) error {
	cols := make([]string, 0, len(e.cols))
	for _, c := range e.cols {
		cols = append(cols, c.Name+" {"+c.Expr+"}")
	}
	if e.node.schema {
		return setColumns(path, e.node.name, "", cols)
	}
	return setColumns(path, "", e.node.name, cols)
}

// titleRows is 1 when the header carries a title or chips, else 0.
func (m *tuiModel) titleRows() int {
	if m.cursorTarget() >= 0 || len(m.chips) > 0 {
		return 1
	}
	return 0
}

// headerRows is the pane's fixed header: the title row and the table's column
// header. The viewport sits below it.
func (m *tuiModel) headerRows() int {
	n := m.titleRows()
	if m.tableHeaderOn() {
		n++
	}
	return n
}

// tableHeaderOn reports whether the table's column names get a header row.
func (m *tuiModel) tableHeaderOn() bool {
	return m.tableOn() && len(m.headerColumns()) > 0
}

// columnHeader is the table's column names, aligned over the cells below, and
// it records each name's span for header clicks.
func (m *tuiModel) columnHeader(w int) string {
	cols := m.headerColumns()
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	row, spans := tableRow(strings.Repeat(" ", m.prefixW)+"  ", names, m.tableWidths(), m.paneWidth())
	m.layout.heads = spans
	return m.styles.title.Render(padRight(sliceCells(row, m.paneCol, m.paneCol+w), w))
}

// chipSegs is the header's chip row, recording each chip's × cell for clicks.
func (m *tuiModel) chipSegs() []seg {
	var segs []seg
	x := 0
	for i, c := range m.chips {
		text := "[" + c.expr + " ×]"
		m.layout.chips = append(m.layout.chips, chipHit{x: x + displayWidth("["+c.expr+" "), idx: i})
		segs = append(segs, seg{text: text}, seg{text: " "})
		x += displayWidth(text) + 1
	}
	return segs
}
