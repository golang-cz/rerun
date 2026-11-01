package main

import (
	"encoding/base64"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func release(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
}

// decodeCopy unwraps an OSC 52 sequence back to its payload.
func decodeCopy(t *testing.T, seq string) string {
	t.Helper()
	const prefix = "\x1b]52;c;"
	const suffix = "\x1b\\"
	if !strings.HasPrefix(seq, prefix) || !strings.HasSuffix(seq, suffix) {
		t.Fatalf("copy sequence is not OSC 52: %q", seq)
	}
	raw, err := base64.StdEncoding.DecodeString(seq[len(prefix) : len(seq)-len(suffix)])
	if err != nil {
		t.Fatalf("copy sequence payload is not base64: %v", err)
	}
	return string(raw)
}

// frozenPane streams 60 lines, draws a frame, and freezes with f.
func frozenPane(t *testing.T) *tuiModel {
	t.Helper()
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.View()
	press(m, "f")
	if m.follow {
		t.Fatal("f did not freeze the pane")
	}
	return m
}

// paneX is a screen column well inside the right pane.
func paneX(m *tuiModel) int { return m.layout.leftW + 2 }

func TestFreezeKeyTogglesFollowAndHoldsTheFrame(t *testing.T) {
	m := frozenPane(t)
	if m.pending != 0 {
		t.Fatalf("pending = %d, want 0 on a fresh freeze", m.pending)
	}
	yOff := m.vp.YOffset
	first := paneOf(m)[0]

	m.applyEvents(streamEvents("web", "new", 4).events)
	lines := paneOf(m)

	if lines[0] != first {
		t.Fatalf("the frozen frame moved: %q -> %q", first, lines[0])
	}
	if m.vp.YOffset != yOff {
		t.Fatalf("YOffset moved from %d to %d while frozen", yOff, m.vp.YOffset)
	}
	if m.pending != 4 {
		t.Fatalf("pending = %d, want 4", m.pending)
	}
	if got := strings.TrimRight(lines[len(lines)-1], " "); !strings.Contains(got, "↓ 4 new") {
		t.Fatalf("the frozen pane's last line = %q, want the marker", got)
	}

	press(m, "f")
	if !m.follow || m.pending != 0 {
		t.Fatalf("f did not resume: follow=%v pending=%d", m.follow, m.pending)
	}
	if !m.vp.AtBottom() {
		t.Fatal("the resumed pane is not at the bottom")
	}
	if got, want := newestPaneLine(m), "web  new-3"; got != want {
		t.Fatalf("after resume the last line = %q, want %q", got, want)
	}
}

func TestFrozenClickCopiesOneRawLine(t *testing.T) {
	m := frozenPane(t)
	x, y := paneX(m), 2
	idx := m.vp.YOffset + y
	want := m.visibleRaw[idx]

	m.handleMouse(mouse(x, y, false))
	if m.selAnchor == nil {
		t.Fatal("the press did not anchor a selection")
	}
	m.handleMouse(release(x, y))

	if m.selAnchor != nil {
		t.Fatal("the release kept the selection")
	}
	if m.copySeq == "" {
		t.Fatal("the click did not set a copy sequence")
	}
	if got := decodeCopy(t, m.copySeq); got != want+"\n" {
		t.Fatalf("payload = %q, want %q", got, want+"\n")
	}
	if strings.HasPrefix(want, "web  ") {
		t.Fatalf("the raw line kept the target prefix: %q", want)
	}
	if m.copyHint != "copied 1 line" {
		t.Fatalf("hint = %q, want copied 1 line", m.copyHint)
	}
	wantUntil := m.now.Add(hintDuration)
	if m.copyUntil != wantUntil {
		t.Fatalf("copyUntil = %v, want %v", m.copyUntil, wantUntil)
	}
	if m.hintUntil == wantUntil {
		t.Fatal("the copy armed the m/s hint window instead of its own")
	}
}

func TestFrozenDragCopiesTheRange(t *testing.T) {
	m := frozenPane(t)
	x := paneX(m)

	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(motion(x, 5))
	m.handleMouse(release(x, 5))

	lo := m.vp.YOffset + 2
	hi := m.vp.YOffset + 5
	want := strings.Join(m.visibleRaw[lo:hi+1], "\n") + "\n"
	if got := decodeCopy(t, m.copySeq); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if m.copyHint != "copied 4 lines" {
		t.Fatalf("hint = %q, want copied 4 lines", m.copyHint)
	}
	if m.selAnchor != nil {
		t.Fatal("the release kept the selection")
	}
}

// A drag that starts below and ends above copies the same range.
func TestFrozenDragUpwardCopiesTheRange(t *testing.T) {
	m := frozenPane(t)
	x := paneX(m)

	m.handleMouse(mouse(x, 5, false))
	m.handleMouse(motion(x, 2))
	m.handleMouse(release(x, 2))

	lo := m.vp.YOffset + 2
	hi := m.vp.YOffset + 5
	want := strings.Join(m.visibleRaw[lo:hi+1], "\n") + "\n"
	if got := decodeCopy(t, m.copySeq); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

// A press and release in the live pane copies one raw line without freezing
// the tail; the left pane's row clicks are unaffected.
func TestLivePaneClickCopiesWithoutFreezing(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 40).events)
	m.View()
	if !m.follow {
		t.Fatal("setup: the pane is not following")
	}

	x := paneX(m)
	want := m.visibleRaw[m.vp.YOffset+2] + "\n"
	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(release(x, 2))

	if !m.follow {
		t.Fatal("the live-pane click froze the pane")
	}
	if got := decodeCopy(t, m.copySeq); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if m.selAnchor != nil {
		t.Fatal("the release kept the selection")
	}
}

// A frozen click past the content copies nothing and must not crash.
func TestFrozenClickPastContentCopiesNothing(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 3).events)
	m.View()
	press(m, "f")

	x := paneX(m)
	m.handleMouse(mouse(x, 10, false))
	if m.selAnchor != nil {
		t.Fatal("the press anchored a selection past the content")
	}
	m.handleMouse(release(x, 10))
	if m.copySeq != "" {
		t.Fatalf("a click past the content copied: %q", m.copySeq)
	}
}

// Motion with no button down never starts a selection.
func TestBareMotionStartsNoSelection(t *testing.T) {
	m := frozenPane(t)
	m.handleMouse(motion(paneX(m), 2))
	if m.selAnchor != nil {
		t.Fatal("a bare motion anchored a selection")
	}
}

// The marker rect survives the copy hint, so clicking it still resumes.
func TestMarkerClickResumesFollowWithTheCopyHintUp(t *testing.T) {
	m := frozenPane(t)
	m.applyEvents(streamEvents("web", "new", 3).events)
	m.View()

	marker := m.layout.marker
	if marker.w == 0 {
		t.Fatal("the marker was not recorded for hit testing")
	}

	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(release(x, 2))
	m.View()

	if m.copySeq == "" {
		t.Fatal("the click did not copy")
	}
	if !m.layout.marker.contains(marker.x, marker.y) {
		t.Fatal("the marker rect was lost while the copy hint showed")
	}
	if view := m.View(); !strings.Contains(view, "copied 1 line") {
		t.Fatal("the copy hint is not on screen")
	}

	m.handleMouse(mouse(marker.x, marker.y, false))
	if !m.follow || m.pending != 0 {
		t.Fatalf("the marker click did not resume: follow=%v pending=%d", m.follow, m.pending)
	}
}

// The copy hint owns the bottom line for its 3s window, then the marker returns.
func TestCopyHintBeatsTheMarkerUntilItExpires(t *testing.T) {
	m := frozenPane(t)
	m.applyEvents(streamEvents("web", "new", 3).events)
	m.View()

	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(release(x, 2))

	lines := paneOf(m)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "copied 1 line") {
		t.Fatalf("bottom line = %q, want the copy hint", last)
	}
	if strings.Contains(last, "↓ 3 new") {
		t.Fatalf("the marker took the copy hint's line: %q", last)
	}
	if !strings.Contains(last, m.copySeq) {
		t.Fatal("the copy sequence is not on the hint line")
	}
	// The OSC 52 sequence must not widen the frame.
	for i, line := range strings.Split(m.View(), "\n") {
		if got := displayWidth(line); got != m.width {
			t.Fatalf("line %d width = %d, want %d while the copy hint shows", i, got, m.width)
		}
	}

	// One tick past the window: the marker comes back.
	m.now = m.copyUntil
	lines = paneOf(m)
	last = lines[len(lines)-1]
	if strings.Contains(last, "copied 1 line") {
		t.Fatalf("the copy hint outlived its window: %q", last)
	}
	if !strings.Contains(last, "↓ 3 new") {
		t.Fatalf("the marker did not return: %q", last)
	}
}

// The copy window is its own: re-arming the m/s hint must not resurrect a
// stale copy hint or re-fire its OSC 52 sequence.
func TestStaleCopyHintDoesNotResurrectOnTargetHint(t *testing.T) {
	m := frozenPane(t)
	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(release(x, 2))
	if m.copySeq == "" {
		t.Fatal("the click did not copy")
	}
	seq := m.copySeq

	// Let the copy feedback expire, then press m on the ALL row: that arms
	// the m/s hint window, which used to bring the stale copy back.
	m.now = m.copyUntil
	m.cursor = 0
	press(m, "m")

	lines := paneOf(m)
	last := lines[len(lines)-1]
	if strings.Contains(last, "copied 1 line") {
		t.Fatalf("the stale copy hint came back: %q", last)
	}
	if strings.Contains(last, seq) {
		t.Fatalf("the old copy sequence re-fired: %q", last)
	}
	if !strings.Contains(last, allRowHint) {
		t.Fatalf("bottom line = %q, want the m/s hint", last)
	}
}

// A drag ending on the marker row must not select the content it hides.
func TestFrozenDragOntoMarkerRowStopsAtVisibleContent(t *testing.T) {
	m := frozenPane(t)
	m.applyEvents(streamEvents("web", "new", 3).events)
	m.View()

	marker := m.layout.marker
	if marker.w == 0 {
		t.Fatal("the marker was not recorded for hit testing")
	}

	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(motion(x, marker.y))
	m.handleMouse(release(x, marker.y))

	lo := m.vp.YOffset + 2
	hi := m.vp.YOffset + m.vp.Height - 2 // the marker row is not content
	want := strings.Join(m.visibleRaw[lo:hi+1], "\n") + "\n"
	if got := decodeCopy(t, m.copySeq); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

// One line over the cap reports instead of copying silently, and clears any
// previous sequence so it cannot re-fire.
func TestOverCapSingleLineReportsWithoutCopying(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{{kind: evOutput, target: "web", line: strings.Repeat("a", copyCap+1)}})
	m.View()
	press(m, "f")

	m.copySeq = "stale"
	x := paneX(m)
	m.handleMouse(mouse(x, 0, false))
	m.handleMouse(release(x, 0))

	if m.copySeq != "" {
		t.Fatalf("copySeq = %q, want cleared", m.copySeq)
	}
	if m.copyHint != "line too long — not copied" {
		t.Fatalf("hint = %q, want the too-long message", m.copyHint)
	}
	if wantUntil := m.now.Add(hintDuration); m.copyUntil != wantUntil {
		t.Fatalf("copyUntil = %v, want %v", m.copyUntil, wantUntil)
	}
	if view := m.View(); !strings.Contains(view, "line too long") {
		t.Fatal("the too-long hint is not on screen")
	}
}

// A non-left release must not leave the anchor armed for a stray copy.
func TestNonLeftReleaseClearsTheSelection(t *testing.T) {
	m := frozenPane(t)
	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	if m.selAnchor == nil {
		t.Fatal("the press did not anchor a selection")
	}

	m.handleMouse(tea.MouseMsg{X: x, Y: 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonRight})
	if m.selAnchor != nil {
		t.Fatal("the right-button release kept the selection")
	}

	m.handleMouse(release(x, 4))
	if m.copySeq != "" {
		t.Fatalf("a stray left release copied: %q", m.copySeq)
	}
}

// Any key drops the selection without copying.
func TestSelectionClearsOnKeypress(t *testing.T) {
	m := frozenPane(t)
	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	if m.selAnchor == nil {
		t.Fatal("the press did not anchor a selection")
	}

	press(m, "c") // unbound key
	if m.selAnchor != nil {
		t.Fatal("the keypress kept the selection")
	}
	if m.copySeq != "" {
		t.Fatal("the keypress copied without a release")
	}
}

// A selection spanning the whole payload is capped at 64KB of whole lines.
func TestCopyTruncatesAtTheCap(t *testing.T) {
	m := newTestModel(t, target("web"))
	long := strings.Repeat("a", 4000)
	evs := make([]event, 0, 20)
	for i := 0; i < 20; i++ {
		evs = append(evs, event{kind: evOutput, target: "web", line: long})
	}
	m.applyEvents(evs)
	m.View()
	press(m, "f")

	x := paneX(m)
	m.handleMouse(mouse(x, 0, false))
	m.handleMouse(motion(x, 19))
	m.handleMouse(release(x, 19))

	if m.copySeq == "" {
		t.Fatal("the drag did not copy")
	}
	payload := decodeCopy(t, m.copySeq)
	if !strings.HasSuffix(payload, "\n") {
		t.Fatal("the payload is missing its trailing newline")
	}
	if body := strings.TrimSuffix(payload, "\n"); len(body) > copyCap {
		t.Fatalf("payload = %d bytes, want at most %d", len(body), copyCap)
	}
	if !strings.Contains(m.copyHint, "(truncated)") {
		t.Fatalf("hint = %q, want a truncation note", m.copyHint)
	}
}

// Whitespace-only lines still copy: the user picked them.
func TestCopyKeepsEmptyLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{
		{kind: evOutput, target: "web", line: "one"},
		{kind: evOutput, target: "web", line: ""},
		{kind: evOutput, target: "web", line: "three"},
	})
	m.View()
	press(m, "f")

	x := paneX(m)
	m.handleMouse(mouse(x, 0, false))
	m.handleMouse(motion(x, 2))
	m.handleMouse(release(x, 2))

	want := "one\n\nthree\n"
	if got := decodeCopy(t, m.copySeq); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

// Copied text is plain, the way a native terminal selection is: the pane
// keeps raw ANSI lines, so the payload is stripped on the way out.
func TestCopyStripsANSIFromThePayload(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{{kind: evOutput, target: "web", line: "\x1b[36mcyan text\x1b[0m"}})
	m.View()
	press(m, "f")

	x := paneX(m)
	m.handleMouse(mouse(x, 0, false))
	m.handleMouse(release(x, 0))

	got := decodeCopy(t, m.copySeq)
	if got != "cyan text\n" {
		t.Fatalf("payload = %q, want %q", got, "cyan text\n")
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatal("the payload kept ANSI escapes")
	}
}

// A drag in the live pane freezes the tail on its first motion, then copies
// the range on release.
func TestLivePaneDragFreezesOnFirstMotion(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.View()
	if !m.follow {
		t.Fatal("setup: the pane is not following")
	}
	x := paneX(m)
	yOff := m.vp.YOffset

	m.handleMouse(mouse(x, 2, false))
	if !m.follow {
		t.Fatal("the press alone froze the tail")
	}
	if m.selAnchor == nil || *m.selAnchor != yOff+2 {
		t.Fatalf("anchor = %v, want the pressed row %d", m.selAnchor, yOff+2)
	}

	m.handleMouse(motion(x, 5))
	if m.follow {
		t.Fatal("the first drag motion did not freeze the tail")
	}
	if m.pending != 0 {
		t.Fatalf("pending = %d, want 0 after a drag freeze", m.pending)
	}
	if m.vp.YOffset != yOff {
		t.Fatalf("the frame moved on freeze: %d -> %d", yOff, m.vp.YOffset)
	}

	m.handleMouse(release(x, 5))

	lo := yOff + 2
	hi := yOff + 5
	if got, want := decodeCopy(t, m.copySeq), strings.Join(m.visibleRaw[lo:hi+1], "\n")+"\n"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if m.copyHint != "copied 4 lines" {
		t.Fatalf("hint = %q, want copied 4 lines", m.copyHint)
	}
	if m.selAnchor != nil {
		t.Fatal("the release kept the selection")
	}
}
