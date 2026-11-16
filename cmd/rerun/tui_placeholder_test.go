package main

import (
	"strings"
	"testing"
)

// The placeholder lists as many leading examples as fit the box, never a cut one.
func TestPlaceholderFitsTheBox(t *testing.T) {
	full := strings.Join(placeholderExamples, "  ")
	for _, width := range []int{40, 60, 90, 130, 200} {
		m := pillModel(t)
		m.width = width
		m.frame()
		got := m.inputPlaceholder()
		ok := false
		for k := 0; k <= len(placeholderExamples); k++ {
			if got == strings.Join(placeholderExamples[:k], "  ") {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("width %d: placeholder %q is not a run of whole examples", width, got)
		}
		for _, line := range strings.Split(m.inputBoxView(), "\n") {
			if displayWidth(line) != m.inputBoxW() {
				t.Fatalf("width %d: box row %q is %d cells, want %d", width, line, displayWidth(line), m.inputBoxW())
			}
		}
		if width == 200 && got != full {
			t.Fatalf("wide placeholder = %q, want every example", got)
		}
	}
}

// At the narrowest box only the first examples fit, and the last shown one stays whole.
func TestPlaceholderNarrowKeepsLeadingExamples(t *testing.T) {
	m := pillModel(t)
	m.width = 60
	m.frame()
	if got := m.inputPlaceholder(); got != "level=error  -healthz" {
		t.Fatalf("narrow placeholder = %q, want the two leading examples", got)
	}
}

// With pills, the placeholder is the pill hint at every width.
func TestPlaceholderWithPillsIsPillHint(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	m.width = 40
	m.frame()
	if got := m.inputPlaceholder(); got != "↑ pills" {
		t.Fatalf("placeholder = %q, want ↑ pills", got)
	}
}

// Even the narrowest box names the first example; the row truncates it, never the hint.
func TestPlaceholderAlwaysNamesFirstExample(t *testing.T) {
	m := pillModel(t)
	m.width = 40
	m.frame()
	if got := m.inputPlaceholder(); got != "level=error" {
		t.Fatalf("placeholder = %q, want the first example even when it is cut", got)
	}
}
