package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func dockModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, target("web"))
	addLines(m, "web", streamLines("web", 30)...)
	addLines(m, "web", jsonReq)
	m.frame()
	return m
}

func TestOpenBarShowsPillsAndInput(t *testing.T) {
	m := dockModel(t)
	commitPill(m, "web")
	press(m, "/")
	typeString(m, "level=info")
	m.frame()
	rows := strings.Split(ansi.Strip(m.frame()), "\n")
	if !strings.Contains(rows[0], "[web ×]") {
		t.Fatalf("row 0 = %q, want the active pill", rows[0])
	}
	if joined := strings.Join(rows[1:], "\n"); !strings.Contains(joined, "❯ level=info") {
		t.Fatalf("frame = %q, want the input box under the pills", joined)
	}
}

func TestOpenBarShrinksTheViewportByTheBoxRows(t *testing.T) {
	m := dockModel(t)
	commitPill(m, "web")
	m.frame()
	before := m.vp.Height()
	press(m, "/")
	m.frame()
	rows := lipgloss.Height(m.inputBoxView())
	if rows != m.inputRows() {
		t.Fatalf("inputRows = %d, want the box's %d rows", m.inputRows(), rows)
	}
	if got := m.vp.Height(); got != before-rows {
		t.Fatalf("viewport height = %d, want %d less %d", got, before, rows)
	}
	press(m, "esc")
	m.frame()
	if got := m.vp.Height(); got != before {
		t.Fatalf("viewport height after closing = %d, want %d", got, before)
	}
}

func TestOpenBarKeepsTheExpandedLineVisible(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", streamLines("web", 30)...)
	addLines(m, "web", jsonReq)
	addLines(m, "web", streamLines("tail", 10)...)
	m.frame()
	m.pauseTail()
	m.setPaneCursor(30)
	m.toggleExpand(30)
	m.frame()
	span := m.spanBelow(30)
	if span == 0 {
		t.Fatal("the line did not expand")
	}
	// The expansion's last row is the viewport's last row.
	m.vp.SetYOffset(30 + span - m.vp.Height() + 1)
	m.frame()
	visible := func() string {
		var all []string
		for _, l := range paneOf(m)[m.titleHeight():] {
			all = append(all, ansi.Strip(l))
		}
		return strings.Join(all, "\n")
	}
	if !strings.Contains(visible(), "GET") {
		t.Fatalf("expansion not fully visible before the bar opens:\n%s", visible())
	}
	press(m, "/")
	m.frame()
	if got := visible(); !strings.Contains(got, `"level":"info"`) || !strings.Contains(got, "GET") {
		t.Fatalf("line or expansion hidden after the bar opened:\n%s", got)
	}
}
