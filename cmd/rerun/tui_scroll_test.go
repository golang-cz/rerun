package main

import (
	"fmt"
	"testing"
)

// manyTargets is a flat list longer than the screen, so the list scrolls.
func manyTargets(t *testing.T, n int) *tuiModel {
	t.Helper()
	var specs []Target
	for i := range n {
		specs = append(specs, target(fmt.Sprintf("t%02d", i)))
	}
	m := newTestModel(t, specs...)
	m.frame()
	return m
}

// offsetAt is the list's stored top row after a frame.
func offsetAt(m *tuiModel) int {
	m.frame()
	return m.layout.offset
}

// Moving down inside the window keeps the top still; the first row past the bottom scrolls one.
func TestListScrollsOnlyAtTheBottomEdge(t *testing.T) {
	m := manyTargets(t, 30)
	visible := m.listHeight()
	for range visible - 1 {
		press(m, "down")
	}
	if got := offsetAt(m); got != 0 {
		t.Fatalf("offset = %d with the cursor on the last visible row, want 0", got)
	}
	press(m, "down")
	if got := offsetAt(m); got != 1 {
		t.Fatalf("offset = %d after the cursor left the window, want 1", got)
	}
	if m.cursor-m.layout.offset != visible-1 {
		t.Fatalf("cursor sits at window row %d, want the last row %d", m.cursor-m.layout.offset, visible-1)
	}
}

// Moving up inside the window keeps the top still; the first row above it scrolls one.
func TestListScrollsOnlyAtTheTopEdge(t *testing.T) {
	m := manyTargets(t, 30)
	for range 25 {
		press(m, "down")
	}
	top := offsetAt(m)
	for range m.cursor - top {
		press(m, "up")
		if got := offsetAt(m); got != top {
			t.Fatalf("offset moved from %d to %d while the cursor stayed in the window", top, got)
		}
	}
	press(m, "up")
	if got := offsetAt(m); got != top-1 {
		t.Fatalf("offset = %d after the cursor left the top, want %d", got, top-1)
	}
}

// ↓ to the end, then ↑ through the visible rows keeps the offset fixed.
func TestUpThroughTheWindowAfterDownToTheEndKeepsOffset(t *testing.T) {
	m := manyTargets(t, 30)
	for range 29 {
		press(m, "down")
	}
	fixed := offsetAt(m)
	if fixed == 0 {
		t.Fatal("setup: the list did not scroll to the end")
	}
	for range m.listHeight() - 1 {
		press(m, "up")
		if got := offsetAt(m); got != fixed {
			t.Fatalf("offset moved from %d to %d on ↑ inside the window", fixed, got)
		}
	}
}

// ↑ to the top, then ↓ through the visible rows keeps the offset fixed.
func TestDownThroughTheWindowAfterUpToTheTopKeepsOffset(t *testing.T) {
	m := manyTargets(t, 30)
	for range 29 {
		press(m, "down")
	}
	for range 29 {
		press(m, "up")
	}
	fixed := offsetAt(m)
	if fixed != 0 {
		t.Fatalf("setup: offset = %d at the top, want 0", fixed)
	}
	for range m.listHeight() - 1 {
		press(m, "down")
		if got := offsetAt(m); got != fixed {
			t.Fatalf("offset moved from %d to %d on ↓ inside the window", fixed, got)
		}
	}
}

// Folding a long group shrinks the rows; the stored offset clamps so no blank rows show below.
func TestOffsetClampsAfterAFold(t *testing.T) {
	var specs []Target
	for i := range 30 {
		s := schemaTarget(fmt.Sprintf("a%02d", i), "a")
		specs = append(specs, s)
	}
	for i := range 5 {
		specs = append(specs, schemaTarget(fmt.Sprintf("b%d", i), "b"))
	}
	m := newTestModel(t, specs...)
	idle(m)
	m.cursor = m.listLen() - 1
	if got := offsetAt(m); got == 0 {
		t.Fatal("setup: the long list did not scroll to the cursor")
	}

	toggleKey(t, m, "a")
	if got := offsetAt(m); got != 0 {
		t.Fatalf("offset = %d after the fold, want 0 with %d rows", got, m.listLen())
	}
	if m.cursor != m.listLen()-1 {
		t.Fatalf("cursor = %d, want the last row %d", m.cursor, m.listLen()-1)
	}
}
