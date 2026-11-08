package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// typeString feeds a string into the open bottom-line bar, one key at a time.
func typeString(m *tuiModel, s string) {
	for _, r := range s {
		if r == ' ' {
			m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// bottomPaneLine is the pane's drawn bottom line with styling stripped.
func bottomPaneLine(m *tuiModel) string {
	lines := paneOf(m)
	return strings.TrimRight(ansi.Strip(lines[len(lines)-1]), " ")
}

// rawPane is the filtered pane's raw lines, in display order.
func rawPane(m *tuiModel) []string {
	m.ensurePane()
	out := make([]string, 0, m.paneLen())
	for i := 0; i < m.paneLen(); i++ {
		out = append(out, m.visibleRaw[m.paneRowRaw(i)])
	}
	return out
}

func addLines(m *tuiModel, name string, lines ...string) {
	evs := make([]event, 0, len(lines))
	for _, line := range lines {
		evs = append(evs, event{kind: evOutput, target: name, line: line})
	}
	m.applyEvents(evs)
}

// The counter updates on every keystroke, before the search is committed.
func TestSearchCounterIsIncremental(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 20).events)
	m.frame()

	press(m, "ctrl+f")
	if m.inputMode != inputSearch {
		t.Fatalf("inputMode = %d, want inputSearch", m.inputMode)
	}
	typeString(m, "line-1")
	if got := ansi.Strip(m.inputBoxView()); !strings.Contains(got, "line-1") || !strings.Contains(got, "1/11") {
		t.Fatalf("input box = %q, want line-1 and 1/11", got)
	}
	if got := bottomPaneLine(m); strings.Contains(got, "1/11") {
		t.Fatalf("bottom line = %q, want no counter echo while the box is open", got)
	}

	typeString(m, "9")
	if got := ansi.Strip(m.inputBoxView()); !strings.Contains(got, "line-19") || !strings.Contains(got, "1/1") {
		t.Fatalf("input box = %q, want line-19 and 1/1", got)
	}

	// The counter lands on the first hit in view, so only the total is fixed.
	press(m, "backspace")
	if got := ansi.Strip(m.inputBoxView()); !strings.Contains(got, "line-1") || !strings.Contains(got, "/11") {
		t.Fatalf("after backspace box = %q, want line-1 and n/11", got)
	}
}

// New lines extend the matches while the query is live.
func TestSearchRecomputesOnNewLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 5).events)
	m.frame()
	press(m, "ctrl+f")
	typeString(m, "fresh")
	press(m, "enter")
	if got := bottomPaneLine(m); !strings.Contains(got, "0/0") {
		t.Fatalf("bottom line = %q, want 0/0", got)
	}

	m.applyEvents(streamEvents("web", "fresh", 3).events)
	m.frame()
	if got := bottomPaneLine(m); !strings.Contains(got, "1/3") {
		t.Fatalf("bottom line = %q, want 1/3 after new matching lines", got)
	}
}

// n steps forward, N back, both wrap, and the jump centers the match and
// freezes the tail.
func TestSearchJumpWrapsAndFreezes(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()

	press(m, "ctrl+f")
	typeString(m, "line")
	press(m, "enter")
	if !m.follow {
		t.Fatal("committing a search froze the pane before any jump")
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "41/60") {
		t.Fatalf("bottom line = %q, want 41/60", got)
	}

	press(m, "n")
	if m.follow {
		t.Fatal("the first n did not freeze the tail")
	}
	if m.searchRow != 41 {
		t.Fatalf("searchRow = %d, want 41", m.searchRow)
	}
	if want := 41 - m.vp.Height()/2; m.vp.YOffset() != want {
		t.Fatalf("YOffset = %d, want the match centered at %d", m.vp.YOffset(), want)
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "42/60") {
		t.Fatalf("bottom line after n = %q, want 42/60", got)
	}

	press(m, "N")
	if got := bottomPaneLine(m); !strings.Contains(got, "41/60") {
		t.Fatalf("bottom line after N = %q, want 41/60", got)
	}

	m.searchRow = 59
	press(m, "n")
	if m.searchRow != 0 {
		t.Fatalf("n at the last match did not wrap: row = %d", m.searchRow)
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "1/60") {
		t.Fatalf("bottom line after wrap = %q, want 1/60", got)
	}

	m.searchRow = 0
	press(m, "N")
	if m.searchRow != 59 {
		t.Fatalf("N at the first match did not wrap: row = %d", m.searchRow)
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "60/60") {
		t.Fatalf("bottom line after wrap = %q, want 60/60", got)
	}
}

// n and N are inert without a committed search.
func TestSearchKeysInertWithoutQuery(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()
	yOff := m.vp.YOffset()

	press(m, "n", "N")
	if !m.follow || m.pending != 0 {
		t.Fatalf("n/N without a search changed follow: follow=%v pending=%d", m.follow, m.pending)
	}
	if m.vp.YOffset() != yOff {
		t.Fatalf("n/N without a search moved the pane: %d -> %d", yOff, m.vp.YOffset())
	}
	if m.search != "" || m.searchRow != -1 {
		t.Fatalf("n/N without a search armed something: search=%q row=%d", m.search, m.searchRow)
	}
}

// esc in the open find box clears the query; enter keeps it, and esc in the list never clears it.
func TestSearchEscInBoxClearsTheQuery(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 20).events)
	m.frame()

	press(m, "ctrl+f")
	typeString(m, "line")
	press(m, "esc")
	if m.inputMode != inputNone || m.barFocus || m.search != "" {
		t.Fatalf("esc while typing: mode=%d bar=%v search=%q, want the query cleared and the bar blurred",
			m.inputMode, m.barFocus, m.search)
	}

	press(m, "ctrl+f")
	if m.input != "" {
		t.Fatalf("reopening after esc seeded input = %q, want empty", m.input)
	}
	typeString(m, "line")
	press(m, "enter")
	if m.search != "line" || m.inputMode != inputNone {
		t.Fatalf("enter did not commit: mode=%d search=%q", m.inputMode, m.search)
	}

	press(m, "esc")
	if m.search != "line" {
		t.Fatalf("esc in the list cleared search = %q", m.search)
	}
}

// The marker outranks the search counter, an open bar outranks the marker,
// and an open bar outranks the copy hint.
func TestBottomLinePrecedence(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()

	press(m, "ctrl+f")
	typeString(m, "line")
	press(m, "enter")
	m.scrollUp(5)
	m.applyEvents(streamEvents("web", "line", 3).events)
	if got := bottomPaneLine(m); !strings.Contains(got, "↓ 3 new") {
		t.Fatalf("marker did not outrank the search counter: %q", got)
	}
	if m.layout.marker.w == 0 {
		t.Fatal("the marker was not recorded while a search was active")
	}

	press(m, "ctrl+f")
	if got := ansi.Strip(m.inputBoxView()); !strings.Contains(got, "line") {
		t.Fatalf("the input box lost the seeded query: %q", got)
	}
	if got := paneOf(m)[0]; strings.Contains(got, "find line") {
		t.Fatalf("the bar still shows the prompt: %q", got)
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "↓ 3 new") {
		t.Fatalf("the open bar displaced the marker: %q", got)
	}
	press(m, "enter")

	// Copy feedback wins next, still leaving the marker hit-testable.
	x := paneX(m)
	m.handleMouse(mouse(x, 2, false))
	m.handleMouse(release(x, 2))
	if got := bottomPaneLine(m); !strings.Contains(got, "copied 1 line") {
		t.Fatalf("the copy hint did not outrank the open bar: %q", got)
	}
	if !m.layout.marker.contains(m.layout.marker.x, m.layout.marker.y) {
		t.Fatal("the marker rect was lost while the bar was open")
	}
}

// A plain filter hides every raw line without the needle.
func TestPlainFilterHidesNonMatchingLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "keep-0", "drop-0", "keep-1", "drop-1", "keep-2")
	m.frame()

	press(m, "v")
	if m.inputMode != inputFilter {
		t.Fatalf("inputMode = %d, want inputFilter", m.inputMode)
	}
	typeString(m, "keep")
	press(m, "enter")
	m.frame()

	want := []string{"keep-0", "keep-1", "keep-2"}
	if got := rawPane(m); !equalStrings(got, want) {
		t.Fatalf("filtered pane = %q, want %q", got, want)
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "3 lines") {
		t.Fatalf("bottom line = %q, want 3 lines", got)
	}

	press(m, "esc")
	if !m.filterOn() {
		t.Fatal("esc in the list cleared the filter")
	}
	press(m, "v", "backspace", "esc")
	if m.filterOn() || m.paneSelDirty {
		t.Fatalf("backspace on the empty input left the filter: filters=%d dirty=%v", len(m.filters), m.paneSelDirty)
	}
	if got := len(rawPane(m)); got != 5 {
		t.Fatalf("removing the chip restored %d rows, want 5", got)
	}
}

// A filter that matches nothing names the filter instead of claiming no output.
func TestFilterZeroMatchHint(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "keep-0", "drop-1")
	m.frame()
	press(m, "v")
	typeString(m, "nomatch")
	press(m, "enter")
	m.frame()

	if view := renderedBody(m); !strings.Contains(view, "no lines match the filters") {
		t.Fatalf("pane hint = %q, want the filter hint", view)
	}
	if view := renderedBody(m); strings.Contains(view, "no output yet") {
		t.Fatalf("pane hint lies about output while filtered: %q", view)
	}
}

// A leading "." evaluates yq per line: matching JSON stays, everything else
// (including a non-JSON line) is dropped.
func TestYQFilterKeepsTruthyJSONOnly(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web",
		`{"response":{"status":200}}`,
		`{"response":{"status":500}}`,
		`{"response":{"status":404}}`,
		`not json`,
		`{"other":1}`,
	)
	m.frame()

	press(m, "v")
	typeString(m, ".response.status >= 400")
	press(m, "enter")
	m.frame()

	want := []string{
		`{"response":{"status":500}}`,
		`{"response":{"status":404}}`,
	}
	if got := rawPane(m); !equalStrings(got, want) {
		t.Fatalf("yq pane = %q, want %q", got, want)
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "2 lines") {
		t.Fatalf("bottom line = %q, want 2 lines", got)
	}
}

// Copying a display range under a filter copies the kept raw lines, not the
// hidden ones between them.
func TestCopyUnderFilterMapsToKeptRawLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "keep-0", "drop-0", "keep-1", "drop-1", "keep-2")
	m.frame()
	press(m, "v")
	typeString(m, "keep")
	press(m, "enter")
	m.frame()

	anchor := 0
	m.selAnchor = &anchor
	m.selEnd = 1
	m.copySelection()

	want := "keep-0\nkeep-1\n"
	if got := decodeCopy(t, m.copySeq); got != want {
		t.Fatalf("filtered copy = %q, want %q", got, want)
	}
	if m.copyHint != "copied 2 lines" {
		t.Fatalf("hint = %q, want copied 2 lines", m.copyHint)
	}
}

// Muting a row while filtered re-derives the subset against the new visible.
func TestSoloMuteRebuildsTheFilteredSubset(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	addLines(m, "web", "keep-web", "drop-web")
	addLines(m, "api", "keep-api", "drop-api")
	m.frame()
	press(m, "v")
	typeString(m, "keep")
	press(m, "enter")
	m.frame()

	if got := rawPane(m); !equalStrings(got, []string{"keep-web", "keep-api"}) {
		t.Fatalf("filtered pane = %q", got)
	}

	m.toggleMute(0)
	m.frame()
	if got := rawPane(m); !equalStrings(got, []string{"keep-api"}) {
		t.Fatalf("after muting web the filtered pane = %q, want [keep-api]", got)
	}

	m.toggleMute(0)
	m.applyScope(0, "")
	m.frame()
	if got := rawPane(m); !equalStrings(got, []string{"keep-web"}) {
		t.Fatalf("after soloing web the filtered pane = %q, want [keep-web]", got)
	}
}

// New lines still collect behind the marker while a filter hides the pane's
// content; pending counts arrivals, not visible rows.
func TestNewLinesCollectWhileFiltered(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()
	press(m, "v")
	typeString(m, "line")
	press(m, "enter")
	m.frame()

	m.scrollUp(5)
	if len(m.paneLines()) != 60 {
		t.Fatalf("filtered pane holds %d rows, want 60", len(m.paneLines()))
	}

	addLines(m, "web", "other-0", "other-1", "other-2")
	m.frame()
	if m.pending != 3 {
		t.Fatalf("pending = %d, want 3", m.pending)
	}
	if len(m.paneLines()) != 60 {
		t.Fatalf("filtered pane grew to %d rows, want 60", len(m.paneLines()))
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "↓ 3 new") {
		t.Fatalf("bottom line = %q, want the marker", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// renderedBody is the pane exactly as drawn, bottom line included.
func renderedBody(m *tuiModel) string {
	return strings.Join(paneOf(m), "\n")
}

// Editing a filter must refresh the pane body, not just the model.
func TestFilterEditRefreshesRenderedPane(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "keep-0", "drop-0", "keep-1", "drop-1", "keep-2")
	m.frame()

	press(m, "v")
	typeString(m, "keep")
	m.frame()
	if body := renderedBody(m); strings.Contains(body, "drop-0") {
		t.Fatalf("the live filter left stale rows on screen:\n%s", body)
	} else if !strings.Contains(body, "keep-2") {
		t.Fatalf("the live filter hid the kept rows:\n%s", body)
	}
	press(m, "enter")

	// Reopen, swap the query, and check the rendered body follows.
	press(m, "v")
	for range len("keep") {
		press(m, "backspace")
	}
	typeString(m, "drop")
	m.frame()
	if body := renderedBody(m); strings.Contains(body, "keep-0") {
		t.Fatalf("the edited filter left stale rows on screen:\n%s", body)
	} else if !strings.Contains(body, "drop-1") {
		t.Fatalf("the edited filter hid the new matches:\n%s", body)
	}
}

// esc after typing a second filter keeps it as a chip beside the first.
func TestFilterEscKeepsTypedChip(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "keep-0", "drop-0", "keep-1")
	m.frame()

	press(m, "v")
	typeString(m, "keep")
	press(m, "enter")

	press(m, "v")
	typeString(m, "junk")
	m.frame()
	if body := renderedBody(m); strings.Contains(body, "keep-0") {
		t.Fatalf("the junk edit did not apply live:\n%s", body)
	}
	press(m, "esc")
	if len(m.filters) != 2 || m.filters[0].query != "keep" || m.filters[1].query != "junk" {
		t.Fatalf("esc left filters = %+v, want keep then junk", m.filters)
	}
	m.frame()
	if body := renderedBody(m); strings.Contains(body, "keep-0") {
		t.Fatalf("the ANDed junk chip did not hide the keep lines:\n%s", body)
	}
}

// A filter that shrinks the pane under a frozen YOffset clamps the viewport.
func TestFilterShrinkClampsFrozenYOffset(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i := 0; i < 25; i++ {
		addLines(m, "web", fmt.Sprintf("aa-%d", i))
	}
	for i := 0; i < 35; i++ {
		addLines(m, "web", fmt.Sprintf("bb-%d", i))
	}
	m.frame()
	m.pauseTail()
	m.scrollUp(25)
	if m.vp.YOffset() != 15 {
		t.Fatalf("YOffset = %d, want 15 before the shrink", m.vp.YOffset())
	}

	press(m, "v")
	typeString(m, "aa")
	m.frame()

	if want := len(rawPane(m)) - m.vp.Height(); m.vp.YOffset() != want {
		t.Fatalf("YOffset = %d, want the clamped %d", m.vp.YOffset(), want)
	}
	if body := renderedBody(m); !strings.Contains(body, fmt.Sprintf("web  aa-%d", m.vp.YOffset())) {
		t.Fatalf("the clamped body does not start at the first surviving row:\n%s", body)
	}
}

// Muting a row recomputes the search, so the counter tracks the live pane.
func TestSearchCounterUpdatesOnMute(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	addLines(m, "web", "alpha")
	addLines(m, "api", "alpha")
	m.frame()

	press(m, "ctrl+f")
	typeString(m, "alpha")
	press(m, "enter")
	if got := bottomPaneLine(m); !strings.Contains(got, "1/2") {
		t.Fatalf("bottom line = %q, want 1/2", got)
	}

	m.toggleMute(0)
	fadeNotice(m)
	m.frame()
	if got := bottomPaneLine(m); !strings.Contains(got, "1/1") {
		t.Fatalf("after muting web the counter = %q, want 1/1", got)
	}
}

// A yq expression that does not parse reports inline and suspends the filter,
// so the pane keeps every line instead of blanking.
func TestBadYQExpressionShowsErrorAndKeepsPane(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"a":1}`, `not json`, `{"a":2}`)
	m.frame()

	press(m, "v")
	typeString(m, ".a >")
	m.frame()
	if m.inputMode != inputFilter {
		t.Fatalf("inputMode = %d, want the bar still open on a parse error", m.inputMode)
	}
	if got := paneOf(m)[0]; !strings.Contains(got, "yq:") {
		t.Fatalf("bar line = %q, want the yq error", got)
	}
	if m.filterOn() {
		t.Fatal("a parse error left the filter active")
	}
	if got := m.paneLen(); got != 3 {
		t.Fatalf("pane kept %d rows, want all 3", got)
	}

	press(m, "enter")
	if m.inputMode != inputFilter {
		t.Fatal("enter closed the bar despite the parse error")
	}

	press(m, "esc")
	if len(m.filters) != 0 || m.inputMode != inputNone {
		t.Fatalf("esc kept the invalid draft: filters=%d mode=%d", len(m.filters), m.inputMode)
	}
	if got := m.paneLen(); got != 3 {
		t.Fatalf("after esc the pane kept %d rows, want 3", got)
	}
}
