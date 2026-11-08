package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestModalBoxHeaderDividerFooter(t *testing.T) {
	m := newTestModel(t)
	// 24x7 fits header, rule, two body rows and the footer exactly.
	lines := strings.Split(ansi.Strip(m.modalBox("hello", "esc close", []string{"one", "two"}, 24, 7)), "\n")
	if len(lines) != 7 {
		t.Fatalf("box height = %d lines, want 7:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	inner := lines[1:6]
	if !strings.Contains(inner[0], "hello") {
		t.Fatalf("first inner row = %q, want the header label", inner[0])
	}
	if rule := strings.Trim(inner[1], "│─ "); rule != "" || lipgloss.Width(inner[1])-4 != 20 {
		t.Fatalf("second inner row = %q, want a rule of the 20-cell inner width", inner[1])
	}
	if !strings.Contains(inner[2], "one") || !strings.Contains(inner[3], "two") {
		t.Fatalf("body rows = %q and %q, want one and two", inner[2], inner[3])
	}
	if !strings.Contains(inner[4], "esc close") {
		t.Fatalf("last inner row = %q, want the footer", inner[4])
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != 24 {
			t.Fatalf("line %d is %d cells, want 24", i, w)
		}
	}
}

func TestModalBoxDropsFooterWhenTooShort(t *testing.T) {
	m := newTestModel(t)
	box := ansi.Strip(m.modalBox("hello", "esc close", []string{"one", "two", "three"}, 24, 7))
	if strings.Contains(box, "esc close") {
		t.Fatalf("footer kept in a box with no inner row left for it:\n%s", box)
	}
	if !strings.Contains(box, "three") {
		t.Fatalf("body row dropped before the footer:\n%s", box)
	}
}
