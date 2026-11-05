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
}

func (s sepLine) isSep() bool { return s.label != "" || s.restart != nil }

// text is the rule's label cut to fit a rule of width w.
func (s sepLine) text(w int) string {
	if s.restart != nil {
		return s.restart.label(max(0, w-4))
	}
	return s.label
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
}

// label cuts from the end, so the name and time survive a narrow pane.
func (r *restartRule) label(maxW int) string {
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

// shortDur is whole seconds past ten and tenths below, so rules stay short.
func shortDur(d time.Duration) string {
	if d >= 10*time.Second {
		return d.Round(time.Second).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// buildFailedRule says whether the old run survived the failed build.
func buildFailedRule(t *tuiTarget, ev event) sepLine {
	// The hint sits near the title, so it survives a pane too narrow for the whole rule.
	parts := []string{fmt.Sprintf("✗ %s build failed", t.spec.Name), "select ⚙ build for output"}
	if f := filesSummary(ev.files); f != "" {
		parts = append(parts, f)
	}
	if t.runStarted && t.state.up() {
		parts = append(parts, "still running the old build")
	} else {
		parts = append(parts, "nothing running")
	}
	return sepLine{label: strings.Join(parts, " · "), tone: toneFail}
}

func buildStartRule(name string, at time.Time, files []string) sepLine {
	label := fmt.Sprintf("⚙ %s build started %s", name, at.Format("15:04:05"))
	if f := filesSummary(files); f != "" {
		label += " · " + f
	}
	return sepLine{label: label}
}

func buildEndRule(name string, ok bool, dur time.Duration) sepLine {
	if ok {
		return sepLine{label: fmt.Sprintf("✓ %s build ok · %s", name, shortDur(dur)), tone: toneOK}
	}
	return sepLine{label: fmt.Sprintf("✗ %s build failed · %s", name, shortDur(dur)), tone: toneFail}
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
		out[row] = renderSep(m.visibleSep[r.raw], m.vp.Width)
	}
	if out == nil {
		return lines
	}
	return out
}

func renderSep(s sepLine, w int) string {
	head := ellipsize("── "+s.text(w)+" ", w)
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
