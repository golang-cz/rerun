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

// hintRow is the fs row at the top of the left column, styling stripped.
func hintRow(m *tuiModel) string {
	m.frame()
	leftW, _ := m.layoutWidths()
	return strings.TrimRight(ansi.Strip(m.leftLine(0, leftW)), " ")
}

// With no batch in the last minute the row reads idle, and with no batch at all too.
func TestFsHintReadsIdleWithoutBatches(t *testing.T) {
	m := newTestModel(t, target("web"))
	if got := hintRow(m); got != "idle" {
		t.Fatalf("hint = %q, want idle with no batches", got)
	}

	changeAt(m, "web", spawnClock.Add(-2*time.Minute), map[string]fsnotify.Op{"old.go": fsnotify.Write})
	m.now = spawnClock
	if got := hintRow(m); got != "idle" {
		t.Fatalf("hint after the batch aged out = %q, want idle", got)
	}
}

// The row names the newest batch's file and ages it in fmtDur style.
func TestFsHintAgesTheNewestBatch(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock.Add(4 * time.Second)
	if got := hintRow(m); got != "4s · a.go" {
		t.Fatalf("hint = %q, want the age and the file", got)
	}

	changeAt(m, "web", spawnClock.Add(4*time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(5 * time.Second)
	if got := hintRow(m); got != "1s · b.go" {
		t.Fatalf("hint = %q, want the newer batch's age and file", got)
	}
}

// A batch of several files names its first file and counts the rest.
func TestFsHintCountsTheRestOfABatch(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{
		"server.go": fsnotify.Write, "routes.go": fsnotify.Write, "zz.go": fsnotify.Write,
	})
	m.now = spawnClock.Add(4 * time.Second)
	if got := hintRow(m); got != "4s · routes.go +2" {
		t.Fatalf("hint = %q, want the first file in sort order with +2", got)
	}
}

// A long file name shrinks before the age does.
func TestFsHintEllipsizesTheFileName(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"internal/very_long_handler_name.go": fsnotify.Write})
	m.now = spawnClock.Add(4 * time.Second)

	got := hintRow(m)
	if !strings.HasPrefix(got, "4s · very_") || !strings.HasSuffix(got, "…") {
		t.Fatalf("hint = %q, want the age kept and the name cut with an ellipsis", got)
	}
}

// A new batch sets the row in place: no reverse video, at landing or after.
func TestFsHintDoesNotFlashOnANewBatch(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock

	reverse := m.styles.hint.Reverse(true).Render("x")
	reversed := reverse[:strings.Index(reverse, "x")]
	if reversed == "" {
		t.Fatal("reverse style renders no escape, so the check below proves nothing")
	}
	m.frame()
	leftW, _ := m.layoutWidths()
	for _, at := range []time.Duration{0, 500 * time.Millisecond, 2 * time.Second} {
		m.now = spawnClock.Add(at)
		m.frame()
		if line := m.leftLine(0, leftW); strings.Contains(line, reversed) {
			t.Fatalf("hint %v after the batch = %q, want no reverse highlight", at, ansi.Strip(line))
		}
	}
}

// A watcher error turns the row red until the next successful batch.
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

	if got := hintRow(m); got != "fs ! too many open files" {
		t.Fatalf("hint = %q, want the watch failure", got)
	}
	m.frame()
	leftW, _ := m.layoutWidths()
	red := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorRed)).Render("x")
	if line := m.leftLine(0, leftW); !strings.Contains(line, red[:strings.Index(red, "x")]) {
		t.Fatalf("hint after the error = %q, want red paint", ansi.Strip(line))
	}

	changeAt(m, "backoffice-webapp", spawnClock.Add(4*time.Second), map[string]fsnotify.Op{"b.go": fsnotify.Write})
	m.now = spawnClock.Add(5 * time.Second)
	if got := hintRow(m); got != "1s · b.go" {
		t.Fatalf("hint after the next good batch = %q, want the failure cleared", got)
	}
}

// At 80 columns the failure text is cut to the narrow column, staying
// red while the error batch is in the window, and goes idle once it ages out.
func TestFsHintShortensTheFailureText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	m.now = spawnClock
	m.applyEvent(event{
		kind: evChanged, target: "web", count: 1, restarts: true, at: spawnClock,
		change: changeWithError(map[string]fsnotify.Op{"a.go": fsnotify.Write}, errors.New("too many open files in the watcher pool")),
	})

	leftW, _ := m.layoutWidths()
	if displayWidth("fs ! too many open files in the watcher pool") <= leftW {
		t.Fatalf("leftW = %d, want narrower than the full failure text", leftW)
	}
	if got := hintRow(m); !strings.HasPrefix(got, "fs ! too many") || !strings.HasSuffix(got, "…") || displayWidth(got) > leftW {
		t.Fatalf("hint at 80 cols = %q, want the failure cut with an ellipsis", got)
	}
	m.now = spawnClock.Add(3 * time.Second) // past the 1s flash, still in the window
	m.frame()
	red := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorRed)).Render("x")
	if line := m.leftLine(0, leftW); !strings.Contains(line, red[:strings.Index(red, "x")]) {
		t.Fatalf("hint = %q, want red paint while the error batch is in the window", ansi.Strip(line))
	}

	m.now = spawnClock.Add(2 * time.Minute)
	if got := hintRow(m); got != "idle" {
		t.Fatalf("hint after the error batch aged out = %q, want idle", got)
	}
}

// A click on the fs row opens the fs events modal, like e; the version row below does not.
func TestFsHintClickOpensTheModal(t *testing.T) {
	m := newTestModel(t, target("web"))
	changeAt(m, "web", spawnClock, map[string]fsnotify.Op{"a.go": fsnotify.Write})
	m.now = spawnClock
	m.frame() // the click resolves against the drawn layout

	m.handleMouse(mouse(2, 0, false))
	if !m.eventsModal.open {
		t.Fatal("a click on the fs row did not open the modal")
	}
	press(m, "esc")
	m.handleMouse(mouse(2, m.height-1, false))
	if m.eventsModal.open {
		t.Fatal("a click on the version row opened the modal")
	}
}

// Neither the fs row nor the version row resolves to a list row.
func TestFsHintRowIsNotAListRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.frame()
	if got := m.rowAt(2, 0); got != -1 {
		t.Fatalf("rowAt on the fs row = %d, want no row", got)
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
	press(m, "b")

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
