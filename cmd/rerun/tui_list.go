package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// importKeyPrefix keys an import's section, apart from the schema of the same name.
const importKeyPrefix = "\x00import:"

// allKeyPrefix keys an import's ungrouped targets, apart from a group named all.
const allKeyPrefix = "\x00all:"

// indentCells is the list pane indent per nesting level.
const indentCells = 1

// foldWindow is how long a folded group keeps listing a member that started, restarted or exited.
const foldWindow = 10 * time.Minute

// playGlyph marks a header space would start; a fully folded header keeps its fold marker even so.
const playGlyph = "▶"

// listRow is one list line: a group header, or a target, indented by its depth.
type listRow struct {
	header  bool
	schema  string // the group key a header names; a target's innermost header key
	label   string // a header's short name in the list
	target  int    // a target row's index into m.targets; -1 on a header
	members []int  // a header's member indices, which are contiguous
	depth   int    // indent level: header 0, sub-header and root-group target 1, sub-group target 2
	// hidden is how many of a header's members the list folds away.
	hidden int
	// more is the `(+) N more` row that expands a block's folded members.
	more bool
}

// indent is the list cells in front of a row's name.
func (r listRow) indent() int { return indentCells * r.depth }

// groupTargets orders targets as the list draws them: ungrouped ones first, then sections in the
// order rerun.yml declares its groups, then undeclared sections (imports) in first-use order, and
// Makefile imports last. Sub-groups follow their file's group order. Members stay contiguous and
// keep their `targets:` order.
func groupTargets(rows []*tuiTarget) {
	section := map[string]int{}
	sub := map[string]int{}
	for _, t := range rows {
		if k := groupOf(t); k != "" {
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
	// tier puts ungrouped rows first, declared sections next, other sections after, Makefile imports last.
	tier := func(t *tuiTarget) int {
		switch {
		case groupOf(t) == "":
			return 0
		case t.spec.Kind == makeKind:
			return 3
		case sectionRank(t.spec) > 0:
			return 1
		}
		return 2
	}
	subKey := func(t *tuiTarget) (int, int, int) {
		s := t.spec.Sub
		if s == "" {
			return 1, 0, 0
		}
		rank := t.spec.groupRank
		if rank == 0 {
			rank = len(sub) + 1
		}
		return 0, rank, sub[s]
	}
	slices.SortStableFunc(rows, func(a, b *tuiTarget) int {
		if c := cmp.Compare(tier(a), tier(b)); c != 0 {
			return c
		}
		if c := cmp.Compare(sectionRank(a.spec), sectionRank(b.spec)); c != 0 {
			return c
		}
		if c := cmp.Compare(section[groupOf(a)], section[groupOf(b)]); c != 0 {
			return c
		}
		a1, a2, a3 := subKey(a)
		b1, b2, b3 := subKey(b)
		return cmp.Or(cmp.Compare(a1, b1), cmp.Compare(a2, b2), cmp.Compare(a3, b3))
	})
}

// sectionRank is the 1-based position of the target's section in the root `groups:`, 0 when undeclared.
func sectionRank(t Target) int {
	if t.Import != "" {
		return t.sectionRank
	}
	return t.groupRank
}

// groupOf is the top-level section key a target lists under; an import's targets stay in its section.
func groupOf(t *tuiTarget) string {
	switch {
	case t.spec.section() != "":
		return importKeyPrefix + t.spec.section()
	case t.spec.Schema != "":
		return t.spec.Schema
	}
	return ""
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
	case strings.HasPrefix(key, importKeyPrefix):
		return strings.TrimPrefix(key, importKeyPrefix)
	case strings.HasPrefix(key, allKeyPrefix):
		return strings.TrimPrefix(key, allKeyPrefix) + "/all"
	}
	return key
}

// hasGroups reports whether any target has a group.
func (m *tuiModel) hasGroups() bool {
	for _, t := range m.targets {
		if groupOf(t) != "" {
			return true
		}
	}
	return false
}

// allRows is every row, folded or not: the ungrouped targets without a header, then each header before its
// members, with an import nesting sub-headers.
func (m *tuiModel) allRows() []listRow {
	var rows []listRow
	i := 0
	for ; i < len(m.targets) && groupOf(m.targets[i]) == ""; i++ {
		rows = append(rows, listRow{target: i})
	}
	for i < len(m.targets) {
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

// listRows is the list as drawn: allRows without the members a folded group folds away.
// A section that folds two or more members ends in one `(+) N more` row for its whole subtree.
func (m *tuiModel) listRows() []listRow {
	all := m.allRows()
	var out []listRow
	for at := 0; at < len(all); {
		if !all[at].header {
			out = append(out, all[at])
			at++
			continue
		}
		end := at + 1
		for end < len(all) && !(all[end].header && all[end].depth == 0) {
			end++
		}
		out = append(out, m.sectionRows(all[at:end])...)
		at = end
	}
	return out
}

// sectionRows draws one top-level section. A folded member leaves the list, and a sub-header with nothing
// left under it leaves too. One folded member is listed instead, since `(+) 1 more` hides nothing worth a row.
func (m *tuiModel) sectionRows(sec []listRow) []listRow {
	gone := map[int]bool{}
	for _, r := range sec {
		if !r.header && m.foldedAway(r.target, r.schema) {
			gone[r.target] = true
		}
	}
	if len(gone) < 2 {
		gone = nil
	}
	out := make([]listRow, 0, len(sec)+1)
	for k, r := range sec {
		if r.header {
			for _, j := range r.members {
				if gone[j] {
					r.hidden++
				}
			}
			if k == 0 || r.hidden < len(r.members) {
				out = append(out, r)
			}
			continue
		}
		if !gone[r.target] {
			out = append(out, r)
		}
	}
	if n := len(gone); n > 0 {
		out = append(out, listRow{schema: sec[0].schema, target: -1, depth: 1, more: true, label: fmt.Sprintf("(+) %d more", n)})
	}
	return out
}

// foldedAway reports whether the block key folds target i off the list. The solo target never
// vanishes under the cursor, and a failing or recently active member always shows.
func (m *tuiModel) foldedAway(i int, key string) bool {
	if key == "" || i == m.solo {
		return false
	}
	t := m.targets[i]
	folded, ok := m.foldState[key]
	if !ok {
		folded = t.spec.AutoFold
	}
	return folded && !m.foldActive(t)
}

// foldActive is the rule a folded group keeps a member listed by: failing, or started, restarted or exited within foldWindow.
func (m *tuiModel) foldActive(t *tuiTarget) bool {
	if t.buildFailed || t.state == stateBackoff || (t.state == stateExited && t.exitCode != 0) {
		return true
	}
	if !t.runStarted && !t.state.up() && t.state != stateExited && t.state != stateStopped {
		return false
	}
	at := t.lastStart
	for _, c := range []time.Time{t.exitedAt, t.restartedAt} {
		if c.After(at) {
			at = c
		}
	}
	return m.now.Sub(at) <= foldWindow
}

// blockKeys are the keys of the target blocks under the header at rows[at].
func blockKeys(rows []listRow, at int) []string {
	var keys []string
	for _, r := range rows[at+1:] {
		if r.header && r.depth <= rows[at].depth {
			break
		}
		if !r.header && !slices.Contains(keys, r.schema) {
			keys = append(keys, r.schema)
		}
	}
	return keys
}

// setFold folds or expands the given blocks, which a reload keeps.
func (m *tuiModel) setFold(keys []string, folded bool) {
	if m.foldState == nil {
		m.foldState = map[string]bool{}
	}
	for _, k := range keys {
		m.foldState[k] = folded
	}
}

// keepCursor keeps the cursor on its row when rows above it appear or hide. A key that moved the cursor
// keeps its place, and a row that left the list leaves the cursor on its nearest neighbour.
func (m *tuiModel) keepCursor(idx int, key string) {
	rows := m.listRows()
	switch {
	case m.cursorSet:
		m.cursorSet = false
	case m.cursor == idx && key != "":
		for i, r := range rows {
			if m.rowKey(r) == key {
				m.cursor = i
				break
			}
		}
	}
	m.cursor = min(max(m.cursor, 0), max(0, len(rows)-1))
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

// frameListRows is the list for the frame being drawn, built once per frame instead of per row.
func (m *tuiModel) frameListRows() []listRow {
	if m.frameRows != nil {
		return m.frameRows
	}
	return m.listRows()
}

// listRowAt is the list row at idx, if idx names one.
func (m *tuiModel) listRowAt(idx int) (listRow, bool) {
	rows := m.frameListRows()
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

// treeLeft is ← on the list: fold an unfolded header, else step to the enclosing header.
func (m *tuiModel) treeLeft() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	if r.header && r.hidden == 0 {
		m.toggleFold(m.cursor)
		return
	}
	if i := m.enclosingHeader(m.cursor); i >= 0 {
		m.cursor = i
		m.liveSelect()
	}
}

// treeRight is → on the list: expand a header that folds members. It reports false on any other row.
func (m *tuiModel) treeRight() bool {
	r, ok := m.cursorRow()
	switch {
	case ok && r.more:
		m.expandMore(r)
	case ok && r.header && r.hidden > 0:
		m.toggleFold(m.cursor)
	default:
		return false
	}
	return true
}

// foldingHeader is the listed header of the block that folds away the row key names, or -1.
// A sub-header that vanished with its members leaves the section's header.
func (m *tuiModel) foldingHeader(key string) int {
	if schema, ok := strings.CutPrefix(key, "more:"); ok {
		return m.indexOfKey("schema:" + schema)
	}
	parent := ""
	for _, r := range m.allRows() {
		switch {
		case r.header && r.depth == 0:
			parent = r.schema
		case !r.header && m.rowKey(r) == key:
			return m.indexOfKey("schema:"+r.schema, "schema:"+parent)
		}
	}
	return -1
}

// toggleFold expands the header at idx when it folds members, else folds it. A cursor the fold hides moves to the header.
func (m *tuiModel) toggleFold(idx int) {
	r, ok := m.listRowAt(idx)
	if !ok || !r.header {
		return
	}
	cursorKey := ""
	if c, ok := m.cursorRow(); ok {
		cursorKey = m.rowKey(c)
	}
	all := m.allRows()
	if at := slices.IndexFunc(all, func(s listRow) bool { return s.header && s.schema == r.schema }); at >= 0 {
		m.setFold(blockKeys(all, at), r.hidden == 0)
	}
	m.cursor = max(0, m.indexOfKey(cursorKey, m.rowKey(r)))
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
		m.expandMore(r)
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

// expandMore is enter or a click on a `(+) N more` row: it expands the section's subtree and leaves the cursor on its header.
func (m *tuiModel) expandMore(r listRow) {
	all := m.allRows()
	if at := slices.IndexFunc(all, func(s listRow) bool { return s.header && s.schema == r.schema }); at >= 0 {
		m.setFold(blockKeys(all, at), false)
	}
	m.cursor = max(0, m.indexOfKey("schema:"+r.schema))
}

// headerIndex is the list index of a group's header, or -1 when it is not listed.
func (m *tuiModel) headerIndex(schema string) int {
	return slices.IndexFunc(m.listRows(), func(r listRow) bool { return r.header && r.schema == schema })
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
		if m.allMutedIn(r.members) {
			m.showNotice(groupLabel(r.schema)+" muted · m unmutes", false)
		} else {
			m.showNotice(groupLabel(r.schema)+" unmuted", false)
		}
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
	m.paneCurPlaced = false
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
	// A placed cursor names a line of the old pane, which the new scope replaces.
	m.paneCurPlaced = false
	m.refreshNames()
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
	case r.hidden > 0 && r.hidden == len(r.members):
		glyph = "▸"
	case m.startsOnSpace(r.members):
		glyph = playGlyph
	}
	name := ellipsizeMiddle(r.label, nameW)
	segs := []seg{
		{text: strings.Repeat(" ", indent)},
		{text: glyph, faint: glyph == playGlyph},
		{text: " "},
		{text: name},
	}
	// The header has no build cell; the fill pushes its count to the rows' right edge.
	fill := max(0, leftW-1-(indent+2+displayWidth(name)))
	if m.showTimes {
		fill = max(0, fill-statusW)
		segs = append(segs, seg{text: strings.Repeat(" ", fill)}, seg{text: timesCell(fmt.Sprintf("%d/%d", running, len(r.members)))})
	} else {
		segs = append(segs, seg{text: strings.Repeat(" ", fill)})
	}
	segs = append(segs, seg{text: marker(m.editIndex(colNode{schema: true, name: r.schema}) >= 0)})
	return renderSegs(segs, m.rowStyle(row, r), leftW)
}

// allMutedIn reports whether every member is muted.
func (m *tuiModel) allMutedIn(members []int) bool {
	for _, i := range members {
		if !m.targets[i].muted && m.solo != i {
			return false
		}
	}
	return true
}
