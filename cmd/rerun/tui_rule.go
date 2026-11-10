package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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
	label string
	tone  sepTone
	// restart is set on a ↻ rule, whose text is cut to the pane width at view time.
	restart *restartRule
	build   *buildRec
	// more is the "+N more" suffix of label.
	more string
	// full is the command a started rule expands to when its label cuts it short.
	full string
	// base is a build rule's label before its command, which text cuts first.
	// lead joins base to the command, and tail follows it.
	base string
	lead string
	tail string
}

func (s sepLine) isSep() bool { return s.label != "" || s.restart != nil }

// text is the rule's label cut to fit a rule of width w.
func (s sepLine) text(w int) string {
	if s.restart != nil {
		return s.restart.label(max(0, w-4))
	}
	if s.base == "" {
		return s.label
	}
	// The name and status outrank the command, so the command takes the cut.
	room := w - 4 - displayWidth(s.base+s.lead+s.tail)
	switch {
	case room >= displayWidth(s.build.cmd):
		return s.label
	case room <= 0:
		return ellipsize(s.base+s.tail, max(0, w-4))
	}
	return s.base + s.lead + ellipsize(s.build.cmd, room) + s.tail
}

// expandable reports whether the rule has anything under it at pane width w.
func (s sepLine) expandable(w int) bool {
	if s.build != nil {
		return s.build.cmd != "" || s.build.note != "" || len(s.build.files) > 1 || len(s.build.out) > 0
	}
	return s.full != "" && displayWidth("── "+s.raw()+" ") > w
}

// cmd is the command a rule runs, which a copy of its single line takes instead.
func (s sepLine) cmd() string {
	if s.build != nil {
		return s.build.cmd
	}
	return s.full
}

// raw is the rule's full text, kept as its pane line for copy and search.
func (s sepLine) raw() string {
	if s.restart != nil {
		return s.restart.label(1 << 16)
	}
	return s.label
}

// restartRule is the ↻ rule a restart leaves. healthy is set later, by the probe.
type restartRule struct {
	name    string
	at      time.Time
	files   []string
	built   time.Duration
	upFor   time.Duration
	healthy time.Duration
	cmd     string
}

// label cuts meta from the end so the name survives; renderSep cuts the command.
func (r *restartRule) label(maxW int) string {
	segs := []string{fmt.Sprintf("↻ %s restarted", r.name)}
	if f := filesSummary(r.files); f != "" {
		segs = append(segs, f)
	}
	if r.built > 0 {
		segs = append(segs, "built "+fmtDur(r.built))
	}
	if r.upFor > 0 {
		segs = append(segs, "was up "+fmtDur(r.upFor))
	}
	if r.healthy > 0 {
		segs = append(segs, "healthy in "+fmtDur(r.healthy))
	}
	cut := ellipsize(segs[0], maxW)
	for n := len(segs); n > 1; n-- {
		if s := strings.Join(segs[:n], " · "); displayWidth(s) <= maxW {
			cut = s
			break
		}
	}
	if r.cmd != "" {
		cut += " · " + r.cmd
	}
	return cut
}

func filesSummary(files []string) string {
	switch len(files) {
	case 0:
		return ""
	case 1:
		return filepath.Base(files[0])
	}
	return fmt.Sprintf("%s +%d", filepath.Base(files[0]), len(files)-1)
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
	s := sepLine{build: r, tone: toneNone}
	if r.phase == buildBroke {
		s.tone = toneFail
	}
	shared := len(r.members) > 1
	icon, status := r.status(shared)
	files := ""
	if len(r.files) > 0 {
		files = filepath.Base(r.files[0])
	}
	if n := len(r.files) - 1; n > 0 {
		s.more = fmt.Sprintf("+%d more", n)
		files += " " + s.more
	}
	if shared {
		// The command leads, so it takes the cut; the status, files and names stay whole.
		s.base, s.lead = icon, " "
		if status != "" {
			s.tail += " " + status
		}
		if files != "" {
			s.tail += " · " + files
		}
		s.tail += " · " + strings.Join(r.members, ", ")
	} else {
		s.base = icon + " " + r.name
		if status != "" {
			s.base += " " + status
		}
		if files != "" {
			s.base += " · " + files
		}
		s.lead = " · "
	}
	if r.cmd == "" {
		// Without a command there is no cut: the rule is its label.
		s.label, s.base, s.lead = s.base+s.tail, "", ""
		return s
	}
	s.label = s.base + s.lead + r.cmd + s.tail
	return s
}

// status is the rule's phase in words, and its icon. A shared run's waiting rule names no phase, as its command already says what runs.
func (r *buildRec) status(shared bool) (icon, text string) {
	switch r.phase {
	case buildPassed:
		return "⚙", "built " + fmtDur(r.dur)
	case buildBroke:
		text = "build failed"
		if r.reason != "" {
			text += " · " + r.reason
		}
		return "✗", text
	case buildDropped:
		return "⚙", "build " + r.reason
	}
	if shared {
		return "⚙", ""
	}
	return "⚙", "building"
}

func (r *buildRec) expansion() []string {
	var rows []string
	if r.cmd != "" {
		rows = append(rows, "$ "+r.cmd)
	}
	if r.note != "" {
		rows = append(rows, r.note)
	}
	if len(r.files) > 1 {
		rows = append(rows, r.files...)
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

// startedRule names the run and its command; a command too long for the pane expands to the full one.
func startedRule(name, cmd string) sepLine {
	s := sepLine{label: fmt.Sprintf("▶ %s started", name)}
	if cmd != "" {
		s.label += " · " + cmd
		s.full = cmd
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
	status := exitStatus(ev.code, ev.err)
	if ev.status == "oom" {
		status = "oom"
	}
	dur := ""
	if t.runStarted && !t.lastStart.IsZero() {
		d := eventTime(ev, now).Sub(t.lastStart)
		dur = " · " + fmtDur(d)
	}
	glyph, tone := "✓", toneOK
	if ev.code != 0 {
		glyph, tone = "■", toneFail
	}
	return sepLine{label: fmt.Sprintf("%s %s %s%s", glyph, t.spec.Name, status, dur), tone: tone}
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
		out[row] = renderSep(m.visibleSep[r.raw], m.vp.Width(), m.isDark)
	}
	if out == nil {
		return lines
	}
	return out
}

func renderSep(s sepLine, w int, isDark bool) string {
	head := ellipsize("── "+s.text(w)+" ", w)
	return sepStyle(s.tone, isDark).Render(head + strings.Repeat("─", max(0, w-displayWidth(head))))
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
