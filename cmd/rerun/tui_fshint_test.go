package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
)

// hintRow is the fs events line above the version row, styling stripped.
func hintRow(m *tuiModel) string {
	m.frame()
	leftW, _ := m.layoutWidths()
	return strings.TrimRight(ansi.Strip(m.leftLine(m.height-2, leftW)), " ")
}

// With no batch in the last minute the line reads idle.
func TestFsHintReadsIdleWithoutBatches(t *testing.T) {
	m := newTestModel(t, target("web"))
	if got := hintRow(m); got != "fs events · idle" {
		t.Fatalf("hint = %q, want the idle line", got)
	}

	changeAt(m, "web", spawnClock.Add(-2*time.Minute), map[string]fsnotify.Op{"old.go": fsnotify.Write})
	m.now = spawnClock
	if got := hintRow(m); got != "fs events · idle" {
		t.Fatalf("hint after the batch aged out = %q, want idle", got)
	}
}

// The line ages the newest batch in fmtDur style.
func TestFsHintAgesTheNewestBatch(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock.Add(4 * time.Second)
	if got := hintRow(m); got != "fs events · 4s ago" {
		t.Fatalf("hint = %q, want the batch age", got)
	}

	changeAt(m, "web", spawnClock.Add(4*time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(5 * time.Second)
	if got := hintRow(m); got != "fs events · 1s ago" {
		t.Fatalf("hint = %q, want the newer batch's age", got)
	}
}

// A new batch highlights the line for a second.
func TestFsHintFlashesOnANewBatch(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock

	m.frame()
	leftW, _ := m.layoutWidths()
	flash := m.styles.hint.Reverse(true).Render("x")
	if line := m.leftLine(m.height-2, leftW); !strings.Contains(line, flash[:strings.Index(flash, "x")]) {
		t.Fatalf("hint right after the batch = %q, want the reverse highlight", ansi.Strip(line))
	}

	m.now = spawnClock.Add(2 * time.Second)
	m.frame()
	if line := m.leftLine(m.height-2, leftW); strings.Contains(line, flash[:strings.Index(flash, "x")]) {
		t.Fatalf("hint 2s after the batch = %q, want the highlight gone", ansi.Strip(line))
	}
}

// A watcher error turns the line red until the next successful batch.
func TestFsHintTurnsRedOnAWatchError(t *testing.T) {
	// The long name widens the column, so the whole failure text fits.
	m := newTestModel(t, target("backoffice-webapp"))
	m.width, m.height = 100, 30
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "backoffice-webapp", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")),
	})
	m.now = spawnClock.Add(3 * time.Second)

	if got := hintRow(m); got != "fs events · ⚠ watch failed" {
		t.Fatalf("hint = %q, want the watch failure", got)
	}
	m.frame()
	leftW, _ := m.layoutWidths()
	red := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorRed)).Render("x")
	if line := m.leftLine(m.height-2, leftW); !strings.Contains(line, red[:strings.Index(red, "x")]) {
		t.Fatalf("hint after the error = %q, want red paint", ansi.Strip(line))
	}

	changeAt(m, "backoffice-webapp", spawnClock.Add(4*time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(5 * time.Second)
	if got := hintRow(m); got != "fs events · 1s ago" {
		t.Fatalf("hint after the next good batch = %q, want the failure cleared", got)
	}
}

// At 80 columns the failure text shortens to fit the narrow column, staying
// red while the error batch is in the window, and goes idle once it ages out.
func TestFsHintShortensTheFailureText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")),
	})

	leftW, _ := m.layoutWidths()
	if displayWidth("fs events · ⚠ watch failed") <= leftW {
		t.Fatalf("leftW = %d, want narrower than the full failure text", leftW)
	}
	if got := hintRow(m); got != "⚠ fs failed" {
		t.Fatalf("hint at 80 cols = %q, want the short failure form", got)
	}
	m.now = spawnClock.Add(3 * time.Second) // past the 1s flash, still in the window
	m.frame()
	red := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorRed)).Render("x")
	if line := m.leftLine(m.height-2, leftW); !strings.Contains(line, red[:strings.Index(red, "x")]) {
		t.Fatalf("hint = %q, want red paint while the error batch is in the window", ansi.Strip(line))
	}

	m.now = spawnClock.Add(2 * time.Minute)
	if got := hintRow(m); got != "fs events · idle" {
		t.Fatalf("hint after the error batch aged out = %q, want idle", got)
	}
}

// A click on the line opens the fs events modal, like e; the version row below does not.
func TestFsHintClickOpensTheModal(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	m.frame() // the click resolves against the drawn layout

	m.handleMouse(mouse(2, m.height-2, false))
	if !m.eventsModal.open {
		t.Fatal("a click on the fs events line did not open the modal")
	}
	press(m, "esc")
	m.handleMouse(mouse(2, m.height-1, false))
	if m.eventsModal.open {
		t.Fatal("a click on the version row opened the modal")
	}
}

// Neither the fs events line nor the version row resolves to a list row.
func TestFsHintRowIsNotAListRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	if got := m.rowAt(2, m.height-2); got != -1 {
		t.Fatalf("rowAt on the fs events line = %d, want no row", got)
	}
	if got := m.rowAt(2, m.height-1); got != -1 {
		t.Fatalf("rowAt on the version row = %d, want no row", got)
	}
}

// Hidden, a watch failure moves to the bottom line in red, beside the state chip.
func TestHiddenListWatchFailureShowsOnBottomLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 20
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files")),
	})
	m.now = spawnClock.Add(3 * time.Second)
	press(m, "b", "b")

	lines := paneOf(m)
	bottom := lines[len(lines)-1]
	if got := strings.TrimRight(ansi.Strip(bottom), " "); got != "⚠ fs · b list · ? help" {
		t.Fatalf("bottom line = %q, want the watch failure with the chips", got)
	}
	red := m.styles.hint.Foreground(lipgloss.Color(colorRed)).Render("x")
	if !strings.Contains(bottom, red[:strings.Index(red, "x")]) {
		t.Fatalf("bottom line %q is not red", bottom)
	}
}
