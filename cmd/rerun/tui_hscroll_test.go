package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func wheelSide(m *tuiModel, button tea.MouseButton, mod tea.KeyMod) {
	m.handleMouse(tea.MouseWheelMsg{X: m.layout.leftW + 2, Y: 3, Button: button, Mod: mod})
	m.frame()
}

func wideModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, target("web"), target("api"))
	addLines(m, "web", "web-start "+strings.Repeat("0123456789", 30))
	addLines(m, "api", "short")
	m.frame()
	return m
}

func TestSidewaysWheelPansThePaneAndClamps(t *testing.T) {
	m := wideModel(t)
	wheelSide(m, tea.MouseWheelLeft, 0)
	if m.paneCol != 0 {
		t.Fatalf("paneCol = %d after wheel left at the edge, want 0", m.paneCol)
	}
	wheelSide(m, tea.MouseWheelRight, 0)
	if m.paneCol != wheelCols {
		t.Fatalf("paneCol = %d after wheel right, want %d", m.paneCol, wheelCols)
	}
	wheelSide(m, tea.MouseWheelRight, 0)
	wheelSide(m, tea.MouseWheelLeft, 0)
	if m.paneCol != wheelCols {
		t.Fatalf("paneCol = %d after right, right, left, want %d", m.paneCol, wheelCols)
	}
	for i := 0; i < 500; i++ {
		wheelSide(m, tea.MouseWheelRight, 0)
	}
	longest := displayWidth(m.paneLines()[0]) - m.vp.Width()
	if m.paneCol != longest {
		t.Fatalf("paneCol = %d, want it clamped at the longest line's reach %d", m.paneCol, longest)
	}
}

func TestShiftWheelPansLikeSidewaysWheel(t *testing.T) {
	m := wideModel(t)
	wheelSide(m, tea.MouseWheelDown, tea.ModShift)
	if m.paneCol != wheelCols {
		t.Fatalf("paneCol = %d after shift+wheel down, want %d", m.paneCol, wheelCols)
	}
	wheelSide(m, tea.MouseWheelUp, tea.ModShift)
	if m.paneCol != 0 {
		t.Fatalf("paneCol = %d after shift+wheel up, want 0", m.paneCol)
	}
}

func TestPanKeepsNameColumnAndMarksCutSides(t *testing.T) {
	m := wideModel(t)
	for i := 0; i < 5; i++ {
		wheelSide(m, tea.MouseWheelRight, 0)
	}
	row := dropStamp(ansi.Strip(paneOf(m)[m.titleHeight()]))
	if !strings.HasPrefix(row, "web ") {
		t.Fatalf("row = %q, want the name column pinned", row)
	}
	if !strings.Contains(row, "‹") || !strings.HasSuffix(strings.TrimRight(row, " "), "›") {
		t.Fatalf("row = %q, want ‹ and › marking the cut sides", row)
	}
	if strings.Contains(row, "web-start") {
		t.Fatalf("row = %q, want the message scrolled", row)
	}
	if stamp := ansi.Strip(paneOf(m)[m.titleHeight()]); !stampPrefix.MatchString(stamp) {
		t.Fatalf("row = %q, want the time column kept", stamp)
	}
}

func TestPanResetsOnFilterAndTargetChange(t *testing.T) {
	m := wideModel(t)
	wheelSide(m, tea.MouseWheelRight, 0)
	press(m, "/")
	typeString(m, "web")
	if m.paneCol != 0 {
		t.Fatalf("paneCol = %d after typing a filter, want 0", m.paneCol)
	}
	press(m, "esc")

	m.paneCol = 8
	m.applyScope(1, "")
	m.frame()
	if m.paneCol != 0 {
		t.Fatalf("paneCol = %d after soloing another target, want 0", m.paneCol)
	}
}
