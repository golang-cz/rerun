package main

import (
	"cmp"
	"fmt"
	"slices"
)

// ungrouped keys the trailing default header; NUL keeps it apart from any config name.
const ungrouped = "\x00default"

// listRow is one list line: a group header, or a target, indented under its group.
type listRow struct {
	header  bool
	schema  string
	target  int   // a target row's index into m.targets; -1 on a header
	members []int // a header's member indices, which are contiguous
	indent  bool
}

// groupTargets makes each schema's members contiguous and puts ungrouped targets last.
func groupTargets(rows []*tuiTarget) {
	rank := map[string]int{}
	for _, t := range rows {
		if s := t.spec.Schema; s != "" {
			if _, ok := rank[s]; !ok {
				rank[s] = len(rank)
			}
		}
	}
	key := func(t *tuiTarget) int {
		if t.spec.Schema == "" {
			return len(rank)
		}
		return rank[t.spec.Schema]
	}
	slices.SortStableFunc(rows, func(a, b *tuiTarget) int { return cmp.Compare(key(a), key(b)) })
}

// groupOf is the group key a target lists under: its schema, or ungrouped.
func groupOf(t *tuiTarget) string {
	if t.spec.Schema == "" {
		return ungrouped
	}
	return t.spec.Schema
}

// groupLabel is the name a group shows as in the list, chips, and bottom line.
func groupLabel(key string) string {
	if key == ungrouped {
		return "(no group)"
	}
	return key
}

// hasGroups reports whether any target has a group. Without one the list stays flat.
func (m *tuiModel) hasGroups() bool {
	for _, t := range m.targets {
		if t.spec.Schema != "" {
			return true
		}
	}
	return false
}

// listRows is the list as drawn: each group header precedes its members.
func (m *tuiModel) listRows() []listRow {
	grouped := m.hasGroups()
	var rows []listRow
	for i := 0; i < len(m.targets); {
		if !grouped {
			rows = append(rows, listRow{target: i})
			i++
			continue
		}
		key := groupOf(m.targets[i])
		members := m.schemaMembers(key)
		rows = append(rows, listRow{header: true, schema: key, target: -1, members: members})
		for _, j := range members {
			rows = append(rows, listRow{schema: key, target: j, indent: true})
		}
		i += len(members)
	}
	return rows
}

// schemaMembers is the target indices a group key names, in list order.
func (m *tuiModel) schemaMembers(schema string) []int {
	var members []int
	for i, t := range m.targets {
		if groupOf(t) == schema {
			members = append(members, i)
		}
	}
	return members
}

// headerIndex is the list index of a group's header row, or -1 when there is none.
func (m *tuiModel) headerIndex(key string) int {
	for i, r := range m.listRows() {
		if r.header && r.schema == key {
			return i
		}
	}
	return -1
}

// listRowAt is the list row at idx, if idx names one.
func (m *tuiModel) listRowAt(idx int) (listRow, bool) {
	rows := m.listRows()
	if idx < 0 || idx >= len(rows) {
		return listRow{}, false
	}
	return rows[idx], true
}

// cursorRow is the list row under the cursor.
func (m *tuiModel) cursorRow() (listRow, bool) { return m.listRowAt(m.cursor) }

// cursorTarget is the target index under the cursor, or -1 on a schema header.
func (m *tuiModel) cursorTarget() int {
	r, ok := m.cursorRow()
	if !ok {
		return -1
	}
	return r.target
}

// rowKey names a list row so the cursor can follow it across a reload.
func (m *tuiModel) rowKey(r listRow) string {
	if r.header {
		return "schema:" + r.schema
	}
	return "target:" + m.targets[r.target].spec.Name
}

// toggleRow is space: starts or stops a target, or a header's members.
func (m *tuiModel) toggleRow(r listRow) {
	if r.header {
		m.toggleTargets(r.members)
		return
	}
	m.toggleTarget(r.target)
}

// muteRow is `m`: mutes a target, or a header's members.
func (m *tuiModel) muteRow(r listRow) {
	if r.header {
		m.muteTargets(r.members)
		return
	}
	m.toggleMute(r.target)
}

// rebootRow is `r`: reboots a target, or a header's selected members.
func (m *tuiModel) rebootRow(r listRow) {
	if r.header {
		m.rebootTargets(r.members)
		return
	}
	m.rebootTarget(r.target)
}

func (m *tuiModel) liveSelect() {
	if m.paneFocus || m.drawerFocus {
		return
	}
	if r, ok := m.cursorRow(); ok {
		m.applyRowScope(r)
	}
}

// applyRowScope scopes the pane to a header's group, or to a target alone.
func (m *tuiModel) applyRowScope(r listRow) {
	if r.header {
		if m.solo >= 0 || m.schemaSel != r.schema {
			m.applyScope(-1, r.schema)
		}
		return
	}
	if m.solo != r.target || m.schemaSel != "" {
		m.applyScope(r.target, "")
	}
}

// applyScope sets the pane's selection: one target, one schema, or neither.
// Moving the cursor must never unmute, so mutes are left alone.
func (m *tuiModel) applyScope(solo int, schema string) {
	m.solo = solo
	m.schemaSel = schema
	m.tableFlip = false
	m.refreshUnseen()
	m.group.setSoloGuard(solo >= 0 || schema != "")
	m.mergedDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
}

// clearScope drops the solo or schema selection and restores the pane's lines.
func (m *tuiModel) clearScope() {
	if m.solo < 0 && m.schemaSel == "" {
		return
	}
	m.applyScope(-1, "")
}

// toggleTargets is space on a header: stops the running members, else starts them all.
func (m *tuiModel) toggleTargets(members []int) {
	anyUp := false
	for _, i := range members {
		if m.targets[i].state.up() {
			anyUp = true
			break
		}
	}
	for _, i := range members {
		switch {
		case anyUp && m.targets[i].state.up():
			m.stopTarget(i)
		case !anyUp && !m.targets[i].state.up():
			m.startTarget(i)
		}
	}
}

// rebootTargets reboots the members that rerun.yml selects.
func (m *tuiModel) rebootTargets(members []int) {
	for _, i := range members {
		if m.targets[i].spec.Selected {
			m.rebootTarget(i)
		}
	}
}

// muteTargets is m on a header: mutes all members, or unmutes them if all are muted.
func (m *tuiModel) muteTargets(members []int) {
	mute := false
	for _, i := range members {
		if !m.targets[i].muted {
			mute = true
			break
		}
	}
	for _, i := range members {
		if m.solo != i {
			m.targets[i].muted = mute
		}
	}
	m.refreshUnseen()
	m.mergedDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
}

// unseenIn reports whether any of the targets hides unseen output.
func (m *tuiModel) unseenIn(members []int) bool {
	for _, i := range members {
		if m.targets[i].unseen {
			return true
		}
	}
	return false
}

// schemaLine draws `▾ backend` with its running/total count and an unseen badge.
func (m *tuiModel) schemaLine(row int, r listRow, leftW int) string {
	running := 0
	for _, i := range r.members {
		if s := m.targets[i].state; s == stateRunning || s == stateStarting {
			running++
		}
	}
	segs := []seg{
		{text: "▾ "},
		{text: padRight(groupLabel(r.schema), nameWidth(leftW))},
		{text: marker(m.editIndex(colNode{schema: true, name: r.schema}) >= 0, m.unseenIn(r.members))},
		{text: padRight(fmt.Sprintf("%d/%d", running, len(r.members)), statusW)},
	}
	return renderSegs(segs, m.rowStyle(row, r), leftW)
}
