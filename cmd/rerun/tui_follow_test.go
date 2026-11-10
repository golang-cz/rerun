package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// paneOf renders one frame and returns the right pane's lines as drawn.
func paneOf(m *tuiModel) []string {
	_, rightW := m.layoutWidths()
	m.frame()
	return m.rightLines(rightW)
}

var stampPrefix = regexp.MustCompile(`^\d\d:\d\d:\d\d `)

// dropStamp removes the pane's clock gutter from a styling-free row.
func dropStamp(s string) string { return stampPrefix.ReplaceAllString(s, "") }

// paneBody is the pane's rows below the title, styling and clock gutter stripped.
func paneBody(m *tuiModel) []string {
	var out []string
	for _, l := range paneOf(m)[m.titleHeight():] {
		out = append(out, dropStamp(ansi.Strip(l)))
	}
	return out
}

// newestPaneLine is the last pane line on screen, styling stripped.
func newestPaneLine(m *tuiModel) string {
	lines := paneOf(m)
	return strings.TrimRight(dropStamp(ansi.Strip(lines[len(lines)-1])), " ")
}

// streamEvents builds one batch of output for a target.
func streamEvents(name, prefix string, n int) eventsMsg {
	evs := make([]event, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, event{kind: evOutput, target: name, line: fmt.Sprintf("%s-%d", prefix, i)})
	}
	return eventsMsg{events: evs}
}

func TestFollowDefaultsOn(t *testing.T) {
	m := newTestModel(t)
	if !m.follow {
		t.Fatal("follow must default to true in newTUI")
	}
}

// The pane must end every frame on the newest line while following.
func TestFollowPinsPaneToNewestLine(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))

	for batch := 0; batch < 5; batch++ {
		m.applyEvents(streamEvents("web", fmt.Sprintf("web%d", batch), 30).events)
		m.applyEvents(streamEvents("api", fmt.Sprintf("api%d", batch), 30).events)
		m.frame()

		if !m.vp.AtBottom() {
			t.Fatalf("batch %d: viewport is not at the bottom: yOff=%d height=%d lines=%d",
				batch, m.vp.YOffset(), m.vp.Height(), len(m.visible))
		}
		if want := len(m.visible) - m.vp.Height(); m.vp.YOffset() != want {
			t.Fatalf("batch %d: YOffset = %d, want maxYOffset %d", batch, m.vp.YOffset(), want)
		}
		want := fmt.Sprintf("api  api%d-29", batch)
		if got := newestPaneLine(m); got != want {
			t.Fatalf("batch %d: the pane's last line = %q, want the newest line %q", batch, got, want)
		}
	}
}

// The same pinning must hold for a soloed target streaming alone.
func TestFollowPinsSoloedPaneToNewestLine(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvents(streamEvents("web", "web", 40).events)
	m.applyScope(0, "")
	fadeNotice(m)
	m.frame()

	for batch := 0; batch < 3; batch++ {
		m.applyEvents(streamEvents("web", fmt.Sprintf("solo%d", batch), 25).events)
		m.frame()

		if !m.vp.AtBottom() {
			t.Fatalf("batch %d: the soloed pane is not at the bottom", batch)
		}
		want := fmt.Sprintf("solo%d-24", batch)
		if got := newestPaneLine(m); got != want {
			t.Fatalf("batch %d: soloed pane's last line = %q, want %q", batch, got, want)
		}
	}
}

// The frame must advance on the exact runtime path: eventsMsg in, View out.
func TestFollowAdvancesThroughEventsMsgPath(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))

	// Warm up past the pane's height, so the newest line is a scrolled-in row.
	m.Update(streamEvents("web", "warmup", 40))
	m.frame()

	for frame := 0; frame < 10; frame++ {
		m.Update(streamEvents("web", fmt.Sprintf("f%d", frame), 12))
		m.frame()

		want := fmt.Sprintf("web  f%d-11", frame)
		if got := newestPaneLine(m); got != want {
			t.Fatalf("frame %d: the pane's last line = %q, want %q", frame, got, want)
		}
		if !m.vp.AtBottom() {
			t.Fatalf("frame %d: viewport yOff=%d is not at the bottom", frame, m.vp.YOffset())
		}
	}
}

// By design: scrolling up freezes the frame and shows `↓ N new`.
func TestFreezeOnScrollUpDoesNotAdvanceTheFrame(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()

	m.scrollUp(3)
	yOff := m.vp.YOffset()
	first := paneOf(m)[0]

	m.applyEvents(streamEvents("web", "new", 10).events)
	lines := paneOf(m)

	if m.follow {
		t.Fatal("the tail resumed following after scrolling up")
	}
	if m.vp.YOffset() != yOff {
		t.Fatalf("YOffset moved from %d to %d while frozen", yOff, m.vp.YOffset())
	}
	if lines[0] != first {
		t.Fatalf("the frozen frame advanced: %q -> %q", first, lines[0])
	}
	if m.pending != 10 {
		t.Fatalf("pending = %d, want 10", m.pending)
	}
	if got := strings.TrimRight(ansi.Strip(lines[len(lines)-1]), " "); got != "↓ 10 new · scrollback · esc live" {
		t.Fatalf("the paused pane's last line = %q, want the marker and chip", got)
	}
}

// A scroll gesture that cannot move the pane must not freeze the tail: the
// user asked for older lines and there are none above the window.
func TestScrollUpWithNothingAboveKeepsTailing(t *testing.T) {
	gestures := []struct {
		name string
		msg  func(m *tuiModel) tea.Msg
	}{
		{"wheel", func(m *tuiModel) tea.Msg {
			return tea.MouseWheelMsg{X: m.layout.leftW + 1, Button: tea.MouseWheelUp}
		}},
		{"pgup", func(m *tuiModel) tea.Msg { return tea.KeyPressMsg{Code: tea.KeyPgUp} }},
	}
	for _, g := range gestures {
		t.Run(g.name, func(t *testing.T) {
			m := newTestModel(t, target("web"))
			// Fewer lines than the pane is tall: YOffset is already 0.
			m.applyEvents(streamEvents("web", "early", 5).events)
			m.frame()

			m.Update(g.msg(m))
			if !m.follow {
				t.Fatalf("%s: a scroll gesture that could not move the pane froze the tail", g.name)
			}

			m.applyEvents(streamEvents("web", "late", 40).events)
			if !m.vp.AtBottom() {
				t.Fatalf("%s: the pane is not pinned to the newest line", g.name)
			}
			if got, want := newestPaneLine(m), "web  late-39"; got != want {
				t.Fatalf("%s: the pane stopped advancing: last line = %q, want %q", g.name, got, want)
			}
		})
	}
}

// A scroll gesture that really moves the pane still freezes the tail.
func TestScrollUpWithScrollbackStillFreezes(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()

	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.follow {
		t.Fatal("scrolling up over real scrollback did not freeze the tail")
	}
	if m.vp.YOffset() == 0 || m.vp.AtBottom() {
		t.Fatalf("YOffset = %d, want a window above the tail", m.vp.YOffset())
	}
}

// esc resumes the live tail from the frozen state.
func TestEscResumesLiveTailAfterFreeze(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "old", 60).events)
	m.frame()
	m.scrollUp(5)

	press(m, "esc")
	m.applyEvents(streamEvents("web", "new", 5).events)

	if !m.follow || m.pending != 0 {
		t.Fatalf("G did not resume: follow=%v pending=%d", m.follow, m.pending)
	}
	if got, want := newestPaneLine(m), "web  new-4"; got != want {
		t.Fatalf("after G the pane's last line = %q, want %q", got, want)
	}
}
