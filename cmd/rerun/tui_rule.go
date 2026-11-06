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

	"github.com/charmbracelet/lipgloss"
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
	// icon is the click target that expands the rule.
	icon  string
	build *buildRec
	// more is the "+N more" suffix of label, a click target too.
	more string
	// full is the command a started rule expands to when its label cuts it short.
	full string
}

func (s sepLine) isSep() bool { return s.label != "" || s.restart != nil }

// text is the rule's label cut to fit a rule of width w.
func (s sepLine) text(w int, grouped bool) string {
	if s.restart != nil {
		return s.restart.label(max(0, w-4), grouped)
	}
	return s.label
}

// expandable reports whether the rule has anything under it at pane width w.
func (s sepLine) expandable(w int) bool {
	if s.build != nil {
		return s.build.note != "" || len(s.build.files) > 1 || len(s.build.out) > 0
	}
	return s.full != "" && displayWidth("── "+s.label+" ") > w
}

// hit reports whether a press at pane cell px opens the rule: its icon, or its "+N more".
func (s sepLine) hit(px, w int) bool {
	if !s.expandable(w) {
		return false
	}
	const lead = 3 // the "── " before the label
	if px >= lead && px < lead+displayWidth(s.icon) {
		return true
	}
	end := displayWidth("── " + s.label)
	return s.more != "" && end+1 <= w && px >= end-displayWidth(s.more) && px < end
}

// raw is the rule's full text, kept as its pane line for copy and search.
func (s sepLine) raw() string {
	if s.restart != nil {
		return s.restart.label(1<<16, false)
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

// label cuts from the end, so the name and time survive a narrow pane. The
// command is appended only when grouped.
func (r *restartRule) label(maxW int, grouped bool) string {
	segs := []string{fmt.Sprintf("↻ %s restarted %s", r.name, r.at.Format("15:04:05"))}
	if f := filesSummary(r.files); f != "" {
		segs = append(segs, f)
	}
	if r.built > 0 {
		segs = append(segs, "built "+shortDur(r.built))
	}
	if r.upFor > 0 {
		segs = append(segs, "was up "+shortDur(r.upFor))
	}
	if r.healthy > 0 {
		segs = append(segs, "healthy in "+shortDur(r.healthy))
	}
	if grouped && r.cmd != "" {
		segs = append(segs, "$ "+r.cmd)
	}
	for n := len(segs); n > 1; n-- {
		if s := strings.Join(segs[:n], " · "); displayWidth(s) <= maxW {
			return s
		}
	}
	return ellipsize(segs[0], maxW)
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

// buildDur rounds a build time up to whole seconds, so a build never reads 0s.
func buildDur(d time.Duration) string {
	secs := max(1, int((d+time.Second-1)/time.Second))
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}

// shortDur is whole seconds past ten and tenths below, so rules stay short.
func shortDur(d time.Duration) string {
	if d >= 10*time.Second {
		return d.Round(time.Second).String()
	}
	return d.Round(100 * time.Millisecond).String()
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
	name   string
	seq    int
	files  []string
	phase  buildPhase
	dur    time.Duration
	reason string
	// note is the first row under a failed build: whether the old run survived.
	note string
	out  []string
}

func (r *buildRec) sep() sepLine {
	icon, head := "⚙", r.name
	switch r.phase {
	case buildRunning:
		head += " building"
	case buildPassed:
		head += " built " + buildDur(r.dur)
	case buildBroke:
		icon, head = "✗", r.name+" build failed"
		if r.reason != "" {
			head += " · " + r.reason
		}
	case buildDropped:
		head += " build " + r.reason
	}
	label := icon + " " + head
	s := sepLine{icon: icon, build: r, tone: toneNone}
	if r.phase == buildBroke {
		s.tone = toneFail
	}
	if len(r.files) > 0 {
		label += " · " + filepath.Base(r.files[0])
	}
	if n := len(r.files) - 1; n > 0 {
		s.more = fmt.Sprintf("+%d more", n)
		label += " " + s.more
	}
	s.label = label
	return s
}

func (r *buildRec) expansion() []string {
	var rows []string
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
func startedRule(name string, at time.Time, cmd string) sepLine {
	s := sepLine{icon: "▶", label: fmt.Sprintf("▶ %s started %s", name, at.Format("15:04:05"))}
	if cmd != "" {
		s.label += " · " + cmd
		s.full = cmd
	}
	return s
}

// followTail pins the pane to the tail, or holds a restart rule at the top until output fills below it.
func (m *tuiModel) followTail() {
	if m.pinSeq != 0 {
		if row, ok := m.ruleRow(m.pinSeq); ok && m.paneLen()-row <= m.vp.Height {
			m.vp.SetYOffset(row)
			return
		}
		m.pinSeq = 0
	}
	m.vp.GotoBottom()
}

// Blank rows let the viewport hold the rule at the top; it clamps the offset otherwise.
func (m *tuiModel) padForPin(out []string) []string {
	if !m.follow || m.pinSeq == 0 {
		return out
	}
	if row, ok := m.ruleRow(m.pinSeq); ok {
		cue := len(out)
		for len(out) < row+m.vp.Height {
			if len(out) == cue {
				out = append(out, m.styles.hint.Render("pinned"))
				continue
			}
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
		dur = " · " + d.Round(100*time.Millisecond).String()
	}
	glyph, tone := "✓", toneOK
	if ev.code != 0 {
		glyph, tone = "■", toneFail
	}
	return sepLine{label: fmt.Sprintf("%s %s %s%s", glyph, t.spec.Name, status, dur), tone: tone}
}

func exitStatus(code int, err error) string {
	if code >= 0 {
		return fmt.Sprintf("exited %d", code)
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

// interleaved is true when several targets share the stream, so a rule names its command.
func (m *tuiModel) interleaved() bool { return m.solo < 0 }

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
		out[row] = renderSep(m.visibleSep[r.raw], m.vp.Width, m.interleaved())
	}
	if out == nil {
		return lines
	}
	return out
}

func renderSep(s sepLine, w int, grouped bool) string {
	head := ellipsize("── "+s.text(w, grouped)+" ", w)
	return sepStyle(s.tone).Render(head + strings.Repeat("─", max(0, w-displayWidth(head))))
}

func sepStyle(t sepTone) lipgloss.Style {
	switch t {
	case toneOK:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))
	case toneFail:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorRed))
	}
	return lipgloss.NewStyle().Faint(true)
}
