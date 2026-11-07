package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// selectHint is the footer hint when a column pick has no node to write to.
const selectHint = "select a schema or target to add columns"

// listKeyHint is the footer hint when the selection is an import or its `all`, which name no schema.
const listKeyHint = "select a group or target"

// unsavedNotice is the footer while column picks are waiting for ctrl+s.
const unsavedNotice = "unsaved columns · ctrl+s save"

// columnMarkW is the " ×" a column header puts after each name.
const columnMarkW = 2

// savedNoticeHold is how long the saved notice holds against a config reload notice.
const savedNoticeHold = 2 * time.Second

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
	if m.listOnlyScope() {
		return colNode{}, false
	}
	if m.schemaSel != "" && m.schemaSel != ungrouped {
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

// listOnlyScope reports whether the selection is an import or its `all`, which name no schema to write.
func (m *tuiModel) listOnlyScope() bool {
	return strings.HasPrefix(m.schemaSel, importKeyPrefix) || strings.HasPrefix(m.schemaSel, allKeyPrefix)
}

// noNodeHint is the footer hint for a column action with no node to write to.
func (m *tuiModel) noNodeHint() string {
	if m.listOnlyScope() {
		return listKeyHint
	}
	return selectHint
}

// pickKey adds a clicked JSON key as a column of the selection's node.
func (m *tuiModel) pickKey(f *jsonField) {
	node, ok := m.pickNode()
	if !ok {
		m.showHint(m.noNodeHint())
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
		m.showHint(m.noNodeHint())
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

// saveColumns writes every pending node with setColumns. A failing node stays pending; the rest still save.
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
	var failed []colEdit
	var firstErr error
	for _, e := range m.edits {
		file, local := m.columnsHome(e)
		if err := writeColumns(file, local); err != nil {
			failed = append(failed, e)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	m.edits = failed
	if firstErr != nil {
		m.showNotice(fmt.Sprintf("rerun: save columns: %v", firstErr), true)
		return
	}
	m.keepNoticeUntil = m.now.Add(savedNoticeHold)
	m.showNotice("saved columns to rerun.yml", false)
}

// targetDirty reports whether a target's own or inherited columns await ctrl+s.
func (m *tuiModel) targetDirty(t *tuiTarget) bool {
	if m.editIndex(colNode{name: t.spec.Name}) >= 0 {
		return true
	}
	return t.spec.Schema != "" && !t.spec.ownColumns && m.editIndex(colNode{schema: true, name: t.spec.Schema}) >= 0
}

// columnsHome is the file that defines an edit's node, with the node named as that file writes it.
func (m *tuiModel) columnsHome(e colEdit) (string, colEdit) {
	for _, t := range m.targets {
		if e.node.schema && t.spec.Schema == e.node.name && t.spec.SchemaFile != "" {
			return t.spec.SchemaFile, colEdit{node: colNode{schema: true, name: t.spec.SchemaLocal}, cols: e.cols}
		}
		if !e.node.schema && t.spec.Name == e.node.name && t.spec.File != "" {
			return t.spec.File, colEdit{node: colNode{name: t.spec.localName()}, cols: e.cols}
		}
	}
	return m.group.configPath, e
}

// writeColumns writes one node's columns as `name {.expr}` entries.
func writeColumns(path string, e colEdit) error {
	cols := make([]string, 0, len(e.cols))
	for _, c := range e.cols {
		cols = append(cols, c.Name+" "+c.body())
	}
	if e.node.schema {
		return setColumns(path, e.node.name, "", cols)
	}
	return setColumns(path, "", e.node.name, cols)
}

// headerRows is the pane's fixed header: the filter bar and the table's column
// header. The viewport sits below it.
func (m *tuiModel) headerRows() int {
	n := m.barRows()
	if m.tableHeaderOn() {
		n++
	}
	return n
}

// tableHeaderOn reports whether the table's column names get a header row.
func (m *tuiModel) tableHeaderOn() bool {
	return m.tableOn() && len(m.headerColumns()) > 0
}

// columnHeader draws each column name with its × and records the × cells for header clicks.
func (m *tuiModel) columnHeader(w int) string {
	cols := m.headerColumns()
	widths := m.tableWidths()
	names := make([]string, len(cols))
	marked := make([]bool, len(cols))
	for i, c := range cols {
		// The × only shows when the name leaves room for it, so a narrow column still names itself.
		marked[i] = i < len(widths) && displayWidth(c.Name)+columnMarkW <= widths[i]
		names[i] = c.Name
		if marked[i] {
			names[i] += " ×"
		}
	}
	row, spans := tableRow(m.namePrefix(""), names, widths, m.paneWidth())
	m.layout.heads = m.layout.heads[:0]
	for i, s := range spans {
		if !marked[i] {
			continue
		}
		x := s.x0 + displayWidth(cols[i].Name) + 1
		m.layout.heads = append(m.layout.heads, cellSpan{x0: x, x1: x + 1, col: s.col})
	}
	return m.styles.title.Render(padRight(sliceCells(row, m.paneCol, m.paneCol+w), w))
}
