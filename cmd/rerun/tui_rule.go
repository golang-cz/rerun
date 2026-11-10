package main

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"
)

// Color is extra: the text already says what happened.
type sepTone uint8

const (
	toneNone sepTone = iota
	toneOK
	toneFail
)

type sepLine struct {
	// name is the service the row belongs to; a build row picks its own name at view time.
	name string
	// verb is the state in one word; fact is the one detail worth showing beside it.
	verb string
	fact string
	tone sepTone
	// restart is set on a restart row, whose details grow when the health probe reports.
	restart *restartRule
	build   *buildRec
	// cmdText is the command a single-line copy of the row takes instead.
	cmdText string
	// details are the folded rows an expand reveals, above a build's output.
	details []string
}

func (s sepLine) isSep() bool { return s.verb != "" }

// text is the row's words, without the name column.
func (s sepLine) text() string {
	if s.fact == "" {
		return s.verb
	}
	if s.tone == toneFail {
		return s.verb + " · " + s.fact
	}
	return s.verb + " " + s.fact
}

// detailRows are the rows an expand reveals.
func (s sepLine) detailRows() []string {
	switch {
	case s.build != nil:
		return s.build.expansion()
	case s.restart != nil:
		return s.restart.details()
	}
	return s.details
}

// expandable reports whether the row has anything folded under it.
func (s sepLine) expandable() bool { return len(s.detailRows()) > 0 }

// cmd is the command a row runs, which a copy of its single line takes instead.
func (s sepLine) cmd() string { return s.cmdText }

// raw is the row's text with its name, kept as its pane line for copy and search.
func (s sepLine) raw() string {
	name := s.name
	if s.build != nil {
		name = s.build.name
		if n := len(s.build.members) - 1; n > 0 {
			name += fmt.Sprintf(" +%d", n)
		}
	}
	return strings.TrimSpace(name + " " + s.text())
}

// restartRule is the row a restart leaves. healthy is set later, by the probe.
type restartRule struct {
	name    string
	at      time.Time
	files   []string
	built   time.Duration
	upFor   time.Duration
	healthy time.Duration
	cmd     string
}

func (r *restartRule) details() []string {
	var rows []string
	if len(r.files) > 0 {
		rows = append(rows, "files: "+strings.Join(r.files, ", "))
	}
	if r.built > 0 {
		rows = append(rows, "built "+fmtDur(r.built))
	}
	if r.upFor > 0 {
		rows = append(rows, "was up "+fmtDur(r.upFor))
	}
	if r.healthy > 0 {
		rows = append(rows, "healthy in "+fmtDur(r.healthy))
	}
	if r.cmd != "" {
		rows = append(rows, "$ "+r.cmd)
	}
	return rows
}

func restartedRule(rr *restartRule) sepLine {
	return sepLine{name: rr.name, verb: "restarted", restart: rr, cmdText: rr.cmd}
}

type buildPhase uint8

const (
	buildRunning buildPhase = iota
	buildPassed
	buildBroke
	// buildDropped is a build that a newer one replaced or a stop cancelled.
	buildDropped
)

// One build keeps one rule, rewritten in place as the build moves on.
type buildRec struct {
	// name is the target whose stream holds the rule; it never moves.
	name string
	// owner is the member whose events drive the rule: the launcher, or the next member after it leaves.
	owner string
	// members are the targets the rule names: the owner first, then any that joined its shared build.
	members []string
	seq     int
	files   []string
	phase   buildPhase
	dur     time.Duration
	reason  string
	// cmd is the build command, the first row under the rule.
	cmd string
	// note is the first row under a failed build: whether the old run survived.
	note string
	out  []string
}

func (r *buildRec) sep() sepLine {
	s := sepLine{build: r, cmdText: r.cmd}
	s.verb, s.fact = r.status()
	if r.phase == buildBroke {
		s.tone = toneFail
	}
	return s
}

// status is the rule's verb and its one fact.
func (r *buildRec) status() (verb, fact string) {
	switch r.phase {
	case buildPassed:
		return "built", fmtDur(r.dur)
	case buildBroke:
		return "build failed", r.reason
	case buildDropped:
		return "build " + r.reason, ""
	}
	return "building", ""
}

func (r *buildRec) expansion() []string {
	var rows []string
	if r.cmd != "" {
		rows = append(rows, "$ "+r.cmd)
	}
	if len(r.files) > 0 {
		rows = append(rows, "files: "+strings.Join(r.files, ", "))
	}
	if len(r.members) > 1 {
		rows = append(rows, "shared by: "+strings.Join(r.members, ", "))
	}
	if r.note != "" {
		rows = append(rows, r.note)
	}
	return append(rows, r.out...)
}

func buildReason(ev event) string {
	switch {
	case ev.code > 0:
		return fmt.Sprintf("exit %d", ev.code)
	case ev.err != nil:
		return ellipsize(oneLine(printable(ev.err.Error(), false)), 40)
	}
	return ""
}

func startedRule(name, cmd string) sepLine {
	s := sepLine{name: name, verb: "started", cmdText: cmd}
	if cmd != "" {
		s.details = []string{"$ " + cmd}
	}
	return s
}

// followTail pins the pane to the tail, or holds a restart rule at the top until output fills below it.
func (m *tuiModel) followTail() {
	if m.pinSeq != 0 {
		if row, ok := m.pinTop(); ok && m.paneLen()-row <= m.vp.Height() {
			m.vp.SetYOffset(row)
			return
		}
		m.unpin()
	}
	m.vp.GotoBottom()
}

// unpin releases the pane's held row.
func (m *tuiModel) unpin() { m.pinSeq, m.pinGap = 0, false }

// pinTop is the row the pin holds at the pane's top; a clear's pin holds the row after its anchor.
func (m *tuiModel) pinTop() (int, bool) {
	row, ok := m.ruleRow(m.pinSeq)
	if ok && m.pinGap {
		row++
		for row < m.paneLen() && m.rowInfo(row).sub >= 0 {
			row++
		}
	}
	return row, ok
}

// Blank rows let the viewport hold the rule at the top; it clamps the offset otherwise.
func (m *tuiModel) padForPin(out []string) []string {
	if !m.follow || m.pinSeq == 0 {
		return out
	}
	if row, ok := m.pinTop(); ok {
		for len(out) < row+m.vp.Height() {
			out = append(out, "")
		}
	}
	return out
}

func (m *tuiModel) ruleRow(seq int) (int, bool) {
	m.ensurePane()
	for row := 0; row < m.paneLen(); row++ {
		if m.rowInfo(row).sub < 0 && m.visibleSeq[m.paneRowRaw(row)] == seq {
			return row, true
		}
	}
	return 0, false
}

func exitRule(t *tuiTarget, ev event, now time.Time) sepLine {
	s := sepLine{name: t.spec.Name, verb: "exited", tone: toneOK}
	if ev.code != 0 || ev.status == "oom" {
		s.verb, s.tone, s.fact = "crashed", toneFail, exitStatus(ev.code, ev.err)
		if ev.status == "oom" {
			s.fact = "oom"
		}
	}
	if t.runStarted && !t.lastStart.IsZero() {
		s.details = []string{"ran " + fmtDur(eventTime(ev, now).Sub(t.lastStart))}
	}
	return s
}

func exitStatus(code int, err error) string {
	if code >= 0 {
		return fmt.Sprintf("exit %d", code)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		if ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "killed by " + signalName(err)
		}
	}
	msg := "unknown"
	if err != nil {
		msg = err.Error()
	}
	return "failed: " + ellipsize(oneLine(printable(msg, false)), 40)
}

// Drawn at view time, so a resize redraws the rule at the new width.
func (m *tuiModel) sepLines(lines []string) []string {
	var out []string
	for row := range lines {
		r := m.rowInfo(row)
		if r.sub >= 0 || !m.visibleSep[r.raw].isSep() {
			continue
		}
		if out == nil {
			out = slices.Clone(lines)
		}
		out[row] = m.renderSep(m.visibleSep[r.raw], m.vp.Width())
	}
	if out == nil {
		return lines
	}
	return out
}

// sepLead is the row's name column and its +N slot, lined up with the log lines' name column.
func (m *tuiModel) sepLead(s sepLine) string {
	name, n := s.name, 0
	if s.build != nil {
		name, n = m.buildViewName(s.build), len(s.build.members)-1
	}
	slot := "   "
	if n > 0 {
		slot = padRight(fmt.Sprintf("+%d", n), 3)
	}
	if m.singleView() {
		if n == 0 {
			return ""
		}
		return name + " " + slot + " "
	}
	return strings.TrimSuffix(m.namePrefix(name), "  ") + " " + slot + " "
}

// buildViewName is the member a shared build's row is named after: the first one whose log the pane shows.
func (m *tuiModel) buildViewName(r *buildRec) string {
	for _, n := range append([]string{r.owner}, r.members...) {
		if i := m.targetIndex(n); i >= 0 && m.streamsTo(i) {
			return n
		}
	}
	return r.name
}

// buildStreams reports whether the pane shows any member of the build, whose row then joins that member's log.
func (m *tuiModel) buildStreams(r *buildRec) bool {
	for _, n := range r.members {
		if i := m.targetIndex(n); i >= 0 && m.streamsTo(i) {
			return true
		}
	}
	return false
}

func (m *tuiModel) renderSep(s sepLine, w int) string {
	head := ellipsize(m.sepLead(s)+s.text()+" ", w)
	return sepStyle(s.tone, m.isDark).Render(head + strings.Repeat("─", max(0, w-displayWidth(head))))
}

func sepStyle(t sepTone, isDark bool) lipgloss.Style {
	switch t {
	case toneOK:
		// Muted: the ✓ already carries the good news.
		return lipgloss.NewStyle().Foreground(lipgloss.LightDark(isDark)(lipgloss.Color("28"), lipgloss.Color("65")))
	case toneFail:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed))
	}
	return lipgloss.NewStyle().Faint(true)
}
