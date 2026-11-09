package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ungrouped keys the trailing default header; NUL keeps it apart from any config name.
const ungrouped = "\x00default"

// importKeyPrefix keys an import's section, apart from the schema of the same name.
const importKeyPrefix = "\x00import:"

// allKeyPrefix keys an import's ungrouped targets, apart from a group named all.
const allKeyPrefix = "\x00all:"

// indentCells is the list pane indent per nesting level.
const indentCells = 1

// autoHideGrace is how long an auto-hide target that exited 0 stays listed.
const autoHideGrace = 10 * time.Minute

// playGlyph marks a header space would start; a collapsed header keeps its fold marker even so.
const playGlyph = "▶"

// listRow is one list line: a group header, or a target, indented by its depth.
type listRow struct {
	header  bool
	schema  string // the group key a header names; a target's innermost header key
	label   string // a header's short name in the list
	target  int    // a target row's index into m.targets; -1 on a header
	members []int  // a header's member indices, which are contiguous
	depth   int    // indent level: header 0, sub-header and root-group target 1, sub-group target 2
	// collapsed is a header folded shut, which hides every row below it at a deeper depth.
	collapsed bool
	// more is the (+) N hidden row that shows or hides a block's auto-hidden members.
	more     bool
	revealed bool // the block's hidden members are shown, so the row reads (−) hide N
	// dim is an auto-hidden target the list shows because its group is revealed.
	dim bool
}

// indent is the list cells in front of a row's name.
func (r listRow) indent() int { return indentCells * r.depth }

// groupTargets orders targets as the list draws them: sections in first-use order, Makefile imports
// after the rest, sub-groups in first-use order inside an import, and each import's `all` last.
// Members stay contiguous.
func groupTargets(rows []*tuiTarget) {
	section := map[string]int{}
	sub := map[string]int{}
	for _, t := range rows {
		if k := groupOf(t); k != ungrouped {
			if _, ok := section[k]; !ok {
				section[k] = len(section)
			}
		}
		if s := t.spec.Sub; s != "" {
			if _, ok := sub[s]; !ok {
				sub[s] = len(sub)
			}
		}
	}
	sectionOf := func(t *tuiTarget) int {
		if k := groupOf(t); k != ungrouped {
			return section[k]
		}
		return len(section)
	}
	subOf := func(t *tuiTarget) int {
		if s := t.spec.Sub; s != "" {
			return sub[s]
		}
		return len(sub)
	}
	// Makefile imports list last, after the ungrouped rows.
	makeOf := func(t *tuiTarget) int {
		if t.spec.Kind == makeKind {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(rows, func(a, b *tuiTarget) int {
		if c := cmp.Compare(makeOf(a), makeOf(b)); c != 0 {
			return c
		}
		if c := cmp.Compare(sectionOf(a), sectionOf(b)); c != 0 {
			return c
		}
		return cmp.Compare(subOf(a), subOf(b))
	})
}

// groupOf is the top-level section key a target lists under; an import's targets stay in its section.
func groupOf(t *tuiTarget) string {
	switch {
	case t.spec.section() != "":
		return importKeyPrefix + t.spec.section()
	case t.spec.Schema != "":
		return t.spec.Schema
	}
	return ungrouped
}

// inKey reports whether a target belongs to the group a scope key names.
func inKey(t Target, key string) bool {
	switch {
	case key == "":
		return false
	case strings.HasPrefix(key, importKeyPrefix):
		return t.section() == strings.TrimPrefix(key, importKeyPrefix)
	case strings.HasPrefix(key, allKeyPrefix):
		return t.section() != "" && t.section() == strings.TrimPrefix(key, allKeyPrefix) && t.Sub == ""
	case t.section() != "":
		return t.Sub == key
	case key == ungrouped:
		return t.Schema == ""
	}
	return t.Schema == key
}

// sharedNamePrefix is the leading path of a group or import key that every name starts with, or "".
// It stops at the key's own segments, so a name keeps the rest of its path.
func sharedNamePrefix(key string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	segs := strings.Split(groupLabel(key), "/")
	for k := len(segs); k > 0; k-- {
		cut := strings.Join(segs[:k], "/") + "/"
		if !slices.ContainsFunc(names, func(n string) bool { return !strings.HasPrefix(n, cut) || len(n) <= len(cut) }) {
			return cut
		}
	}
	return ""
}

// groupLabel is the full name a group shows in the bottom line and the chips.
func groupLabel(key string) string {
	switch {
	case key == ungrouped:
		return "all"
	case strings.HasPrefix(key, importKeyPrefix):
		return strings.TrimPrefix(key, importKeyPrefix)
	case strings.HasPrefix(key, allKeyPrefix):
		return strings.TrimPrefix(key, allKeyPrefix) + "/all"
	}
	return key
}

// hasGroups reports whether any target has a group. Without one the list stays flat.
func (m *tuiModel) hasGroups() bool {
	for _, t := range m.targets {
		if groupOf(t) != ungrouped {
			return true
		}
	}
	return false
}

// allRows is every row, folded or not: each header precedes its members, and an import nests sub-headers.
func (m *tuiModel) allRows() []listRow {
	if !m.hasGroups() {
		rows := make([]listRow, len(m.targets))
		for i := range m.targets {
			rows[i] = listRow{target: i}
		}
		return rows
	}
	var rows []listRow
	for i := 0; i < len(m.targets); {
		key := groupOf(m.targets[i])
		members := m.schemaMembers(key)
		i += len(members)
		if strings.HasPrefix(key, importKeyPrefix) {
			rows = append(rows, m.importRows(key, members)...)
			continue
		}
		rows = append(rows, listRow{header: true, schema: key, label: groupLabel(key), target: -1, members: members})
		rows = append(rows, targetRows(key, members, 1)...)
	}
	return rows
}

// listRows is the list as drawn: allRows without the rows under a collapsed header, and without
// auto-hidden targets unless their group is revealed. Each block ends in a (+)/(−) row when it hides any.
func (m *tuiModel) listRows() []listRow {
	var out []listRow
	skip := -1
	all := m.allRows()
	for i, r := range all {
		if skip >= 0 && r.depth > skip {
			continue
		}
		skip = -1
		if r.header && m.collapsed[r.schema] {
			r.collapsed = true
			skip = r.depth
		}
		if r.header {
			out = append(out, r)
			continue
		}
		if hidden := m.autoHidden(r.target); !hidden || m.revealed[r.schema] {
			r.dim = hidden
			out = append(out, r)
		}
		if i+1 < len(all) && !all[i+1].header && all[i+1].schema == r.schema {
			continue
		}
		if n := m.hiddenCount(r.schema); n > 0 {
			out = append(out, m.moreRow(r.schema, r.depth, n))
		}
	}
	return out
}

// moreRow is the block's last row: `(+) N hidden`, or `(−) hide N` once its hidden members show.
func (m *tuiModel) moreRow(schema string, depth, n int) listRow {
	r := listRow{schema: schema, target: -1, depth: depth, more: true, revealed: m.revealed[schema]}
	r.label = fmt.Sprintf("(+) %d hidden", n)
	if r.revealed {
		r.label = fmt.Sprintf("(−) hide %d", n)
	}
	return r
}

// autoHidden reports whether an auto-hide target is off the list: not running, starting, backing off,
// or failed, and not the solo target, which never vanishes under the cursor.
func (m *tuiModel) autoHidden(i int) bool {
	t := m.targets[i]
	if !t.spec.AutoHide || i == m.solo {
		return false
	}
	switch t.state {
	case stateRunning, stateStarting, stateBackoff:
		return false
	case stateExited:
		return t.exitCode == 0 && m.now.Sub(t.exitedAt) > autoHideGrace
	}
	return true
}

// hiddenCount is how many members of a group key the list hides.
func (m *tuiModel) hiddenCount(key string) int {
	n := 0
	for _, i := range m.schemaMembers(key) {
		if m.autoHidden(i) {
			n++
		}
	}
	return n
}

// hiddenIn is how many of the given members the list hides.
func (m *tuiModel) hiddenIn(members []int) int {
	n := 0
	for _, i := range members {
		if m.autoHidden(i) {
			n++
		}
	}
	return n
}

// setRevealed shows or hides a group's auto-hidden members.
func (m *tuiModel) setRevealed(key string, on bool) {
	if m.revealed == nil {
		m.revealed = map[string]bool{}
	}
	m.revealed[key] = on
}

// toggleHidden is h: shows or hides the cursor's group's auto-hidden members, from any row of the group.
func (m *tuiModel) toggleHidden() {
	r, ok := m.cursorRow()
	if !ok || r.schema == "" || (!m.revealed[r.schema] && m.hiddenCount(r.schema) == 0) {
		return
	}
	m.setRevealed(r.schema, !m.revealed[r.schema])
}

// keepCursor keeps the cursor on its row when rows above it appear or hide. A key that moved the cursor
// keeps its place, and a row that left the list leaves the cursor on its nearest neighbour.
func (m *tuiModel) keepCursor(idx int, key string) {
	if m.cursor == idx && key != "" {
		if i := m.indexOfKey(key); i >= 0 {
			m.cursor = i
		}
	}
	m.cursor = min(max(m.cursor, 0), max(0, m.listLen()-1))
}

// cursorKey names the row under the cursor, or "" when there is none.
func (m *tuiModel) cursorKey() string {
	if r, ok := m.cursorRow(); ok {
		return m.rowKey(r)
	}
	return ""
}

// importRows is an import's section: its header, then one sub-header per group in file order, then `all`.
func (m *tuiModel) importRows(key string, members []int) []listRow {
	name := strings.TrimPrefix(key, importKeyPrefix)
	rows := []listRow{{header: true, schema: key, label: name, target: -1, members: members}}
	var subs []string
	hasAll := false
	for _, j := range members {
		if s := m.targets[j].spec.Sub; s == "" {
			hasAll = true
		} else if !slices.Contains(subs, s) {
			subs = append(subs, s)
		}
	}
	if len(subs) == 0 {
		return append(rows, targetRows(key, members, 1)...)
	}
	for _, s := range subs {
		sm := m.schemaMembers(s)
		rows = append(rows, listRow{header: true, schema: s, label: strings.TrimPrefix(s, name+"/"), target: -1, members: sm, depth: 1})
		rows = append(rows, targetRows(s, sm, 2)...)
	}
	if hasAll {
		all := allKeyPrefix + name
		am := m.schemaMembers(all)
		rows = append(rows, listRow{header: true, schema: all, label: "all", target: -1, members: am, depth: 1})
		rows = append(rows, targetRows(all, am, 2)...)
	}
	return rows
}

// targetRows lists members under the header key at a depth.
func targetRows(key string, members []int, depth int) []listRow {
	rows := make([]listRow, 0, len(members))
	for _, j := range members {
		rows = append(rows, listRow{schema: key, target: j, depth: depth})
	}
	return rows
}

func (m *tuiModel) setBuildState(i int, building, failed bool) {
	t := m.targets[i]
	t.building, t.buildFailed = building, failed
}

// schemaMembers is the target indices a group key names, in list order.
func (m *tuiModel) schemaMembers(schema string) []int {
	var members []int
	for i, t := range m.targets {
		if inKey(t.spec, schema) {
			members = append(members, i)
		}
	}
	return members
}

// memberOf reports whether target i belongs to the group a scope key names.
func (m *tuiModel) memberOf(i int, key string) bool { return inKey(m.targets[i].spec, key) }

// enclosingHeader is the list index of the header row idx sits under, or -1 when there is none.
// A sub-header's enclosing header is one level up; a target's is the nearest header above.
func (m *tuiModel) enclosingHeader(idx int) int {
	rows := m.listRows()
	if idx < 0 || idx >= len(rows) {
		return -1
	}
	want := -1
	if rows[idx].header {
		want = rows[idx].depth - 1
	}
	for i := idx - 1; i >= 0; i-- {
		if rows[i].header && (want < 0 || rows[i].depth == want) {
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
	switch {
	case r.header:
		return "schema:" + r.schema
	case r.more:
		return "more:" + r.schema
	}
	return "target:" + m.targets[r.target].spec.Name
}

// treeLeft is ← on the list: fold an open header, else step to the enclosing header.
func (m *tuiModel) treeLeft() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	if r.header && !r.collapsed {
		m.toggleCollapse(m.cursor)
		return
	}
	if r.more && r.revealed {
		m.setRevealed(r.schema, false)
		return
	}
	if i := m.enclosingHeader(m.cursor); i >= 0 {
		m.cursor = i
		m.liveSelect()
	}
}

// treeRight is → on the list: unfold a folded header. It reports false on any other row.
func (m *tuiModel) treeRight() bool {
	r, ok := m.cursorRow()
	if ok && r.more && !r.revealed {
		m.setRevealed(r.schema, true)
		return true
	}
	if !ok || !r.header || !r.collapsed {
		return false
	}
	m.toggleCollapse(m.cursor)
	return true
}

// headerKey is the list header a target's group draws, or "" when it draws none.
func headerKey(t Target) string {
	if t.section() == "" {
		return t.Schema
	}
	if t.Sub == "" {
		return importKeyPrefix + t.section()
	}
	return t.Sub
}

// foldKey is the header a target's `hide:` folds: its import section when the import hides.
func foldKey(t Target) string {
	if t.ImportHide {
		return importKeyPrefix + t.section()
	}
	return headerKey(t)
}

// Seed only unknown groups so a reload keeps the user's fold state.
func (m *tuiModel) seedCollapsed(known map[string]bool) {
	for _, t := range m.targets {
		k := foldKey(t.spec)
		if !t.spec.GroupHide || k == "" || known[k] {
			continue
		}
		if m.collapsed == nil {
			m.collapsed = map[string]bool{}
		}
		m.collapsed[k] = true
	}
}

// foldingHeader is the listed header that hides the row key names, or -1; enclosingHeader needs a listed row.
func (m *tuiModel) foldingHeader(key string) int {
	rows := m.allRows()
	at := -1
	for i, r := range rows {
		if m.rowKey(r) == key {
			at = i
			break
		}
	}
	if at < 0 {
		return -1
	}
	fold, depth := "", rows[at].depth
	for i := at - 1; i >= 0 && depth > 0; i-- {
		if r := rows[i]; r.header && r.depth < depth {
			depth = r.depth
			if m.collapsed[r.schema] {
				fold = r.schema
			}
		}
	}
	if fold == "" {
		return -1
	}
	return m.indexOfKey("schema:" + fold)
}

// toggleCollapse folds or unfolds the header at idx. A cursor the fold hides moves to the header.
func (m *tuiModel) toggleCollapse(idx int) {
	r, ok := m.listRowAt(idx)
	if !ok || !r.header {
		return
	}
	cursorKey := ""
	if c, ok := m.cursorRow(); ok {
		cursorKey = m.rowKey(c)
	}
	if m.collapsed[r.schema] {
		delete(m.collapsed, r.schema)
	} else {
		if m.collapsed == nil {
			m.collapsed = map[string]bool{}
		}
		m.collapsed[r.schema] = true
	}
	m.cursor = m.indexOfKey(cursorKey, m.rowKey(r))
}

// indexOfKey is the index of the first listed row named by keys, or -1 when none is listed.
func (m *tuiModel) indexOfKey(keys ...string) int {
	rows := m.listRows()
	for _, k := range keys {
		for i, r := range rows {
			if m.rowKey(r) == k {
				return i
			}
		}
	}
	return -1
}

// toggleRow is space: starts or stops a target, or a header's members.
func (m *tuiModel) toggleRow(r listRow) {
	if r.more {
		return
	}
	if r.header {
		m.toggleTargets(r.members)
		return
	}
	m.toggleTarget(r.target)
}

// enterRow is enter on the list: focuses the pane, starting a stopped row first.
func (m *tuiModel) enterRow() {
	if r, ok := m.cursorRow(); ok && r.more {
		m.setRevealed(r.schema, !r.revealed)
		return
	}
	if r, ok := m.cursorRow(); ok {
		switch {
		case r.header && !m.anyRunning(r.members):
			m.startTargets(r.members)
		case !r.header && !m.targets[r.target].state.up():
			m.toggleTarget(r.target) // the same path as space, so it never stops
		}
		m.applyRowScope(r)
	}
	m.focusPane()
}

// stopRow is `s`: stops the focused pane's scope, else the cursor's row. Never a toggle.
func (m *tuiModel) stopRow() {
	if m.paneFocus {
		if members, ok := m.paneScope(); ok {
			m.stopTargets(members)
			return
		}
	}
	r, ok := m.cursorRow()
	if !ok || r.more {
		return
	}
	if r.header {
		m.stopTargets(r.members)
		return
	}
	if m.targets[r.target].state.up() {
		m.stopTarget(r.target)
	}
}

// stopTargets stops every member that is up, never starting a stopped one.
func (m *tuiModel) stopTargets(members []int) {
	for _, i := range members {
		if m.targets[i].state.up() {
			m.stopTarget(i)
		}
	}
}

// paneScope is the pane selection's members: one solo target or a group's, or
// false when the pane holds no scope.
func (m *tuiModel) paneScope() ([]int, bool) {
	switch {
	case m.solo >= 0:
		return []int{m.solo}, true
	case m.schemaSel != "":
		return m.schemaMembers(m.schemaSel), true
	}
	return nil, false
}

// muteRow is `m`: mutes a target, or a header's members.
func (m *tuiModel) muteRow(r listRow) {
	if r.more {
		return
	}
	if r.header {
		m.muteTargets(r.members)
		return
	}
	m.toggleMute(r.target)
}

// rebootRow is `r`: reboots a target, or a header's selected members.
func (m *tuiModel) rebootRow(r listRow) {
	if r.more {
		return
	}
	if r.header {
		m.rebootTargets(r.members)
		return
	}
	m.rebootTarget(r.target)
}

// clearKey is ctrl+l: a focused pane blanks what it shows without re-scoping; the list scopes to the cursor row first.
func (m *tuiModel) clearKey() {
	if m.paneFocus {
		m.clearTargets()
		return
	}
	if r, ok := m.cursorRow(); ok {
		// Act on the row under the cursor, so a clear always has a pane to blank.
		m.applyRowScope(r)
		m.clearTargets()
	}
}

// A clear must not add or drop lines, so it only moves the pin.
func (m *tuiModel) clearTargets() {
	if !m.follow {
		m.goLive()
	}
	seq, ok := m.lastShownSeq()
	if !ok {
		return
	}
	m.pinSeq, m.pinGap = seq, true
	m.vpDirty = true
}

// lastShownSeq is the seq of the pane's last shown line, the anchor a clear hides behind.
func (m *tuiModel) lastShownSeq() (int, bool) {
	m.ensurePane()
	for row := m.paneLen() - 1; row >= 0; row-- {
		if m.rowInfo(row).sub < 0 {
			return m.visibleSeq[m.paneRowRaw(row)], true
		}
	}
	return 0, false
}

func (m *tuiModel) liveSelect() {
	if m.paneFocus {
		return
	}
	if r, ok := m.cursorRow(); ok {
		m.applyRowScope(r)
	}
}

// applyRowScope scopes the pane to a header's group or to a target alone.
func (m *tuiModel) applyRowScope(r listRow) {
	if r.more {
		return
	}
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
	m.refreshNames()
	m.tableFlip = false
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

// toggleTargets is space on a header: stops the running members, else starts the ones rerun.yml auto-starts.
func (m *tuiModel) toggleTargets(members []int) {
	if m.anyRunning(members) {
		m.stopTargets(members)
		return
	}
	m.startTargets(members)
}

// anyRunning reports whether any member is up.
func (m *tuiModel) anyRunning(members []int) bool {
	for _, i := range members {
		if m.targets[i].state.up() {
			return true
		}
	}
	return false
}

// startTargets starts the group's auto-start members, the ones rerun.yml selects.
func (m *tuiModel) startTargets(members []int) {
	for _, i := range members {
		if m.targets[i].spec.Start == startAuto && m.targets[i].spec.Selected {
			m.startTarget(i)
		}
	}
}

// startsOnSpace reports whether space on the header would start something:
// no member is up, and at least one member auto-starts.
func (m *tuiModel) startsOnSpace(members []int) bool {
	if m.anyRunning(members) {
		return false
	}
	for _, i := range members {
		if t := m.targets[i]; t.spec.Start == startAuto && t.spec.Selected {
			return true
		}
	}
	return false
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
	m.mergedDirty = true
	m.vpDirty = true
	m.recomputeSearch(false)
}

// schemaLine draws `▾ backend` with its running/total count and its unsaved-columns marker.
// A folded header with hidden members trades its count for `· N hidden`, which fits the status cell.
func (m *tuiModel) schemaLine(row int, r listRow, leftW int) string {
	running := 0
	for _, i := range r.members {
		if s := m.targets[i].state; s == stateRunning || s == stateStarting {
			running++
		}
	}
	indent := r.indent()
	nameW := max(0, m.nameWidth(leftW)-indent)
	glyph := "▾"
	switch {
	case r.collapsed:
		glyph = "▸"
	case m.startsOnSpace(r.members):
		glyph = playGlyph
	}
	n := m.hiddenIn(r.members)
	folded := r.collapsed && n > 0
	suffix := fmt.Sprintf("· %d hidden", n)
	name := ellipsizeMiddle(r.label, nameW)
	if folded && m.leftMode != leftFull {
		tail := truncate(" "+suffix, nameW)
		name = ellipsizeMiddle(r.label, max(0, nameW-displayWidth(tail))) + tail
	}
	segs := []seg{
		{text: strings.Repeat(" ", indent)},
		{text: glyph, faint: glyph == playGlyph},
		{text: " "},
		{text: padRight(name, nameW)},
		{text: marker(m.editIndex(colNode{schema: true, name: r.schema}) >= 0)},
	}
	if m.leftMode == leftFull {
		status := fmt.Sprintf("%d/%d", running, len(r.members))
		if folded {
			status = suffix
		}
		segs = append(segs, seg{text: padRight(truncate(status, statusW), statusW)})
	}
	return renderSegs(segs, m.rowStyle(row, r), leftW)
}
