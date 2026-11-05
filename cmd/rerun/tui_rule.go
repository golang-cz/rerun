package main

import (
	"errors"
	"fmt"
	"os/exec"
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
}

func (s sepLine) isSep() bool { return s.label != "" }

func exitRule(t *tuiTarget, ev event, now time.Time) sepLine {
	status := exitStatus(ev.code, ev.err)
	dur := ""
	if t.runStarted && !t.lastStart.IsZero() {
		d := eventTime(ev, now).Sub(t.lastStart)
		dur = " · " + d.Round(100*time.Millisecond).String()
	}
	tone := toneOK
	if ev.code != 0 {
		tone = toneFail
	}
	return sepLine{label: fmt.Sprintf("■ %s %s%s", t.spec.Name, status, dur), tone: tone}
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
	head := ellipsize("── "+s.label+" ", w)
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
