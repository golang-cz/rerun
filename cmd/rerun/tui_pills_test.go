package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// pillModel is one target with lines "alpha beta", "alpha gamma", "delta beta", "delta gamma".
func pillModel(t *testing.T) *tuiModel {
	t.Helper()
	return alphaModel(t)
}

// commitPill types text into the query bar and commits it with enter.
func commitPill(m *tuiModel, text string) {
	press(m, "/")
	typeString(m, text)
	press(m, "enter")
}

// Every opener lands on the same bar: one input mode, the bar focused.
func TestQueryBarOpensWithEveryKey(t *testing.T) {
	for _, key := range []string{"/", "ctrl+f"} {
		m := pillModel(t)
		press(m, key)
		if m.inputMode != inputQuery || !m.barFocus {
			t.Fatalf("%s: mode=%d barFocus=%v, want the query bar open", key, m.inputMode, m.barFocus)
		}
		press(m, "esc")
		if m.inputMode != inputNone || m.barFocus {
			t.Fatalf("%s then esc: mode=%d barFocus=%v, want closed", key, m.inputMode, m.barFocus)
		}
	}
}

// enter parses the whole input and appends one pill per token.
func TestEnterAppendsParsedPills(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha -gamma")
	if len(m.pills) != 2 {
		t.Fatalf("pills = %d, want 2", len(m.pills))
	}
	if m.pills[0].raw != "alpha" || m.pills[1].raw != "-gamma" || !m.pills[1].neg {
		t.Fatalf("pills = %+v", m.pills)
	}
	for _, p := range m.pills {
		if p.kind != pillFilter {
			t.Fatalf("pill %q kind = %d, want filter", p.raw, p.kind)
		}
	}
	if m.inputMode != inputNone {
		t.Fatalf("mode = %d after enter, want the bar closed", m.inputMode)
	}
	m.frame()
	if want := []string{"alpha beta"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}
}

// A parse error keeps the input, marks it red, shows the error in the box
// footer, and enter does not commit it.
func TestParseErrorKeepsInputRed(t *testing.T) {
	m := pillModel(t)
	press(m, "/")
	typeString(m, "level=")
	if m.queryErr == "" {
		t.Fatal("queryErr is empty for level=")
	}
	box := m.inputBoxView()
	if !strings.Contains(ansi.Strip(box), "missing value") {
		t.Fatalf("box = %q, want the parse error in the footer", ansi.Strip(box))
	}
	if !strings.Contains(box, "\x1b[31m") {
		t.Fatalf("box = %q, want the input in red", box)
	}
	if !strings.Contains(ansi.Strip(box), "· esc clears") {
		t.Fatalf("box = %q, want the error to end with · esc clears", ansi.Strip(box))
	}
	press(m, "enter")
	if m.inputMode != inputQuery || m.input != "level=" {
		t.Fatalf("enter on an error: mode=%d input=%q, want the bar open with the input kept", m.inputMode, m.input)
	}
	if len(m.pills) != 0 {
		t.Fatalf("enter on an error added %d pills", len(m.pills))
	}
	press(m, "esc")
	if len(m.pills) != 0 || m.inputMode != inputNone {
		t.Fatalf("esc kept the invalid input: pills=%d mode=%d", len(m.pills), m.inputMode)
	}
}

// backspace on an empty input removes the last pill and keeps the bar open.
func TestEmptyBackspaceRemovesLastPill(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")
	press(m, "/", "backspace")
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" {
		t.Fatalf("pills = %+v, want only alpha", m.pills)
	}
	if m.inputMode != inputQuery {
		t.Fatalf("mode = %d, want the bar still open", m.inputMode)
	}
}

// With the bar focused and the input empty or the cursor at 0, the arrows walk pills.
func TestBarArrowsWalkPills(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")

	press(m, "/", "up")
	if m.barSel != 1 || m.inputMode != inputQuery {
		t.Fatalf("left: sel=%d mode=%d, want the last pill selected with the bar open", m.barSel, m.inputMode)
	}
	press(m, "left")
	if m.barSel != 0 {
		t.Fatalf("left: sel=%d, want 0", m.barSel)
	}
	press(m, "left")
	if m.barSel != 0 {
		t.Fatalf("left at the first pill: sel=%d, want 0", m.barSel)
	}
	press(m, "right")
	if m.barSel != 1 {
		t.Fatalf("right: sel=%d, want 1", m.barSel)
	}
	press(m, "right")
	if m.barSel != -1 {
		t.Fatalf("right past the last pill: sel=%d, want the input (-1)", m.barSel)
	}

	// Cursor at 0 with typed text: left stays in the input.
	typeString(m, "ga")
	press(m, "home", "left")
	if m.barSel != -1 || m.input != "ga" {
		t.Fatalf("left at cursor 0: sel=%d input=%q, want the input and the text kept", m.barSel, m.input)
	}
	// A printable key returns to the text.
	typeString(m, "z")
	if m.barSel != -1 || m.input != "zga" {
		t.Fatalf("typing after a walk: sel=%d input=%q, want -1 and zga", m.barSel, m.input)
	}
}

// On a selected pill: x and backspace remove it, enter edits it back into the
// input, space toggles filter ⇄ highlight.
func TestSelectedPillKeys(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")

	// x removes the selected pill.
	press(m, "/", "up", "x")
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" {
		t.Fatalf("x: pills = %+v, want only alpha", m.pills)
	}

	// enter moves the pill's text back into the input for editing.
	press(m, "left", "enter")
	if len(m.pills) != 0 || m.input != "alpha" {
		t.Fatalf("enter: pills=%d input=%q, want the pill back in the input", len(m.pills), m.input)
	}
	press(m, "enter") // commit it again
	if len(m.pills) != 1 || m.pills[0].raw != "alpha" {
		t.Fatalf("recommit: pills = %+v", m.pills)
	}

	// backspace removes the selected pill.
	press(m, "/", "left", "backspace")
	if len(m.pills) != 0 {
		t.Fatalf("backspace: pills = %+v, want none", m.pills)
	}

	// space toggles the mode; the pill stops narrowing the pane. The bar is
	// still open from the removal, so no fresh / is typed.
	typeString(m, "alpha")
	press(m, "enter")
	press(m, "/", "up", " ", "esc", "esc")
	if m.pills[0].kind != pillHighlight {
		t.Fatalf("kind = %d after space, want highlight", m.pills[0].kind)
	}
	m.frame()
	if got := len(rawPane(m)); got != 4 {
		t.Fatalf("highlight pill hid lines: pane holds %d, want 4", got)
	}
	press(m, "/", "up", " ", "esc", "esc")
	if m.pills[0].kind != pillFilter {
		t.Fatalf("kind = %d after the second space, want filter", m.pills[0].kind)
	}
	m.frame()
	if got := len(rawPane(m)); got != 2 {
		t.Fatalf("filter pill kept %d lines, want 2", got)
	}
}

// Filter pills draw solid in [..], highlight pills dim in ⟨..⟩.
func TestPillRenderingModes(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "beta")
	press(m, "/", "up", " ", "esc", "esc")

	bar := paneOf(m)[0]
	if !strings.Contains(bar, "[alpha ×]") {
		t.Fatalf("bar = %q, want the solid filter pill [alpha ×]", bar)
	}
	if !strings.Contains(bar, "⟨beta ×⟩") {
		t.Fatalf("bar = %q, want the dim highlight pill ⟨beta ×⟩", bar)
	}
}

// Filter pills hide non-matching lines, and every pill's spans are marked in
// the lines that show.
func TestPillSpansPaintedInPane(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	commitPill(m, "gamma")
	press(m, "/", "up", "left", " ", "esc") // alpha highlights, gamma filters
	m.frame()

	want := []string{"alpha gamma", "delta gamma"}
	if !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}
	line := paneOf(m)[m.titleHeight()]
	plain := ansi.Strip(line)
	for _, needle := range []string{"alpha", "gamma"} {
		idx := strings.Index(line, needle)
		if idx < 0 || !strings.Contains(line[:idx], "\x1b[") {
			t.Fatalf("line %q: %q is not styled", line, needle)
		}
	}
	if !strings.Contains(plain, "alpha gamma") {
		t.Fatalf("stripped line = %q, want the raw text kept", plain)
	}
}

// n, N, ctrl+g and ctrl+shift+g jump between the lines any pill hits, wrapping.
func TestHitJumpsCoverPills(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()

	commitPill(m, "line-1") // 11 hits: line-1, line-10..19
	if got := len(m.searchMatches); got != 11 {
		t.Fatalf("matches = %d, want 11", got)
	}
	press(m, "n")
	first := m.searchRow
	if first < 0 {
		t.Fatal("n did not land on a hit")
	}
	press(m, "n")
	if m.searchRow <= first {
		t.Fatalf("n did not advance from %d", first)
	}
	press(m, "N")
	if m.searchRow != first {
		t.Fatalf("N = %d, want back at %d", m.searchRow, first)
	}
	press(m, "ctrl+g")
	if m.searchRow <= first {
		t.Fatalf("ctrl+g did not advance from %d", first)
	}
	press(m, "ctrl+shift+g")
	if m.searchRow != first {
		t.Fatalf("ctrl+shift+g = %d, want back at %d", m.searchRow, first)
	}

	// A highlight-mode pill's hits are jump targets too.
	press(m, "/", "up", " ", "esc", "esc")
	if got := len(m.searchMatches); got != 11 {
		t.Fatalf("highlight pill matches = %d, want 11", got)
	}
}

// The bar's right side shows the scope, the pill count and matching/total.
func TestBarShowsScopeCountAndRatio(t *testing.T) {
	m := pillModel(t)
	m.width = 130 // wide enough for the full status
	commitPill(m, "alpha")
	commitPill(m, "beta")
	m.frame()
	bar := ansi.Strip(paneOf(m)[0])
	if !strings.Contains(bar, "all · 2 pills") && strings.HasSuffix(strings.TrimSpace(bar), "1/4 lines") {
		t.Fatalf("bar = %q, want all · 2 pills · 1/4 lines", bar)
	}

	m.applyScope(0, "") // solo web
	m.frame()
	bar = ansi.Strip(paneOf(m)[0])
	if !strings.Contains(bar, "web · 2 pills") && strings.HasSuffix(strings.TrimSpace(bar), "1/4 lines") {
		t.Fatalf("bar = %q, want the web scope", bar)
	}
}

// While typing, the hit list follows the rows the draft leaves, so n and
// ctrl+g never jump on stale indexes.
func TestDraftRecomputesHitsWhileTyping(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", "err a", "err b", "plain")
	m.frame()
	commitPill(m, "err") // hits on both err lines
	if got := len(m.searchMatches); got != 2 {
		t.Fatalf("matches = %d, want 2", got)
	}
	press(m, "/")
	typeString(m, "err a")
	if got := len(m.searchMatches); got != 1 {
		t.Fatalf("matches = %d while typing, want the one hit the draft leaves", got)
	}
}

// A typed @name resolves in the draft too: it narrows the live preview like
// the committed pill would.
func TestDraftResolvesSavedName(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.queries = map[string]string{"errors": "plain"}
	addLines(m, "web", "err a", "err plain", "plain")
	m.frame()
	press(m, "/")
	typeString(m, "err @errors")
	m.ensurePane()
	if got := rawPane(m); !equalStrings(got, []string{"err plain"}) {
		t.Fatalf("draft pane = %q, want only err plain: @errors must resolve in the draft", got)
	}
}

// esc leaves the bar; the pills stay.
func TestEscLeavesBarKeepsPills(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "alpha")
	press(m, "/")
	typeString(m, "beta")
	press(m, "esc")
	if m.inputMode != inputNone || m.barFocus {
		t.Fatalf("esc: mode=%d barFocus=%v, want the bar closed", m.inputMode, m.barFocus)
	}
	// Today's rule: a valid typed input is kept as a pill on esc.
	if len(m.pills) != 2 {
		t.Fatalf("esc: pills = %+v, want alpha and beta kept", m.pills)
	}
	press(m, "esc")
	if len(m.pills) != 2 {
		t.Fatalf("esc in the list dropped pills: %+v", m.pills)
	}
}

// A clicked scalar becomes a field pill `path=value`, hiding the other lines.
func TestPickValueAddsFieldPill(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"response":{"status":200}}`, `{"response":{"status":500}}`)
	m.frame()

	m.pickValue(&jsonField{path: ".response.status", lit: "200"})
	if len(m.pills) != 1 || m.pills[0].raw != "response.status=200" {
		t.Fatalf("pills = %+v, want response.status=200", m.pills)
	}
	if m.pills[0].kind != pillFilter {
		t.Fatalf("kind = %d, want filter", m.pills[0].kind)
	}
	m.frame()
	if want := []string{`{"response":{"status":200}}`}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}
}

// A clicked string's JSON escapes decode before comparing, and a clicked null
// matches a JSON null, never the string "null" or a missing key.
func TestPickValueRoundTripsEscapesAndNull(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"msg":"a\nb"}`, `{"msg":"ab"}`, `{"msg":null}`, `{"x":1}`)
	m.frame()

	m.pickValue(&jsonField{path: ".msg", lit: `"a\nb"`})
	if len(m.pills) != 1 || m.pills[0].values[0] != "a\nb" {
		t.Fatalf("pills = %+v, want the newline decoded", m.pills)
	}
	m.frame()
	if want := []string{`{"msg":"a\nb"}`}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}

	m.removePill(0)
	m.pickValue(&jsonField{path: ".msg", lit: "null"})
	if len(m.pills) != 1 || !m.pills[0].null {
		t.Fatalf("pills = %+v, want the null pill", m.pills)
	}
	m.frame()
	// A missing key reads as null too, exactly like raw yq's `.msg == null`.
	if want := []string{`{"msg":null}`, `{"x":1}`}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want the null line %q", rawPane(m), want)
	}

	m.pickValue(&jsonField{path: ".msg", lit: "null"})
	if len(m.pills) != 1 {
		t.Fatalf("pills = %+v, want the same click deduped", m.pills)
	}

	// Pipes, spaces and backslashes in a string value survive the round trip.
	m2 := newTestModel(t, target("web"))
	addLines(m2, "web", `{"msg":"a|b c\\d"}`, `{"msg":"a"}`)
	m2.frame()
	m2.pickValue(&jsonField{path: ".msg", lit: `"a|b c\\d"`})
	if len(m2.pills) != 1 || m2.pills[0].values[0] != `a|b c\d` {
		t.Fatalf("pills = %+v, want the pipe, space and backslash decoded", m2.pills)
	}
	m2.frame()
	if want := []string{`{"msg":"a|b c\\d"}`}; !equalStrings(rawPane(m2), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m2), want)
	}
}

// Numeric and exists field pills are hits: their matched value gets a span, so
// n/N land on them.
func TestNumericAndExistsPillsAreHits(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"http":{"status":500}}`, `{"trace_id":"abc"}`, "plain")
	m.frame()
	commitPill(m, "http.status>=400")
	if got := len(m.searchMatches); got != 1 {
		t.Fatalf("numeric pill matches = %d, want its one hit line", got)
	}
	press(m, "/", "backspace", "esc")
	commitPill(m, "trace_id=*")
	if got := len(m.searchMatches); got != 1 {
		t.Fatalf("exists pill matches = %d, want its one hit line", got)
	}
}

// The bar's status names the hit position once pills have hits.
func TestBarShowsHitPosition(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width = 130
	m.applyEvents(streamEvents("web", "line", 60).events)
	m.frame()
	commitPill(m, "line-1") // 11 hits: line-1, line-10..19
	m.frame()
	if got := barLine(m); strings.Contains(got, "hit ") {
		t.Fatalf("bar = %q, want no hit position before the first jump", got)
	}
	press(m, "n")
	m.frame()
	if got := barLine(m); !strings.Contains(got, "hit 1/11") {
		t.Fatalf("bar = %q, want hit 1/11 after n", got)
	}
	press(m, "n")
	m.frame()
	if got := barLine(m); !strings.Contains(got, "hit 2/11") {
		t.Fatalf("bar = %q, want hit 2/11 after the second n", got)
	}
}

// The pill status sits in the pane's header row only, never the bottom line.
func TestPillStatusLivesInTheBarOnly(t *testing.T) {
	m := alphaModel(t)
	commitText(m, "/", "alpha")
	m.frame()
	if got := ansi.Strip(paneOf(m)[0]); !strings.Contains(got, "2/4 lines") {
		t.Fatalf("bar = %q, want the status in the header row", got)
	}
	if got := bottomPaneLine(m); strings.Contains(got, "2/4 lines") {
		t.Fatalf("bottom line = %q, want no status echo", got)
	}
}

// At 80 columns the pills elide from the left behind … and the status survives whole.
func TestBarElidesLeftKeepsStatusAt80(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 80, 24
	addLines(m, "web", "a1", "b1", "c1")
	for _, q := range []string{"alpha-long", "beta-long", "gamma-long"} {
		commitText(m, "/", q)
	}
	m.frame()
	bar := strings.TrimRight(ansi.Strip(paneOf(m)[0]), " ")
	if !strings.HasPrefix(bar, "… ") {
		t.Fatalf("bar = %q, want the oldest pills elided behind …", bar)
	}
	if !strings.HasSuffix(bar, "all · 3 pills · 0/3 lines") {
		t.Fatalf("bar = %q, want the status whole at the right edge", bar)
	}
}

// A clicked null or escaped string encodes canonically: editing the pill
// reparses to the same meaning, and its match paints a span.
func TestClickedNullAndEscapedPillsReparseAndSpan(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.pickValue(&jsonField{path: ".msg", lit: "null"})
	if got := m.pills[0].raw; got != "msg=null" {
		t.Fatalf("null pill raw = %q, want msg=null", got)
	}
	reparsed, err := parsePills("msg=null", nil)
	if err != nil || len(reparsed) != 1 || !reparsed[0].null {
		t.Fatalf("reparse = %+v, %v, want the null pill back", reparsed, err)
	}
	if spans := m.pills[0].spans(`{"msg":null}`); len(spans) != 1 || spans[0] != [2]int{7, 11} {
		t.Fatalf("null spans = %v, want the null literal painted", spans)
	}

	m2 := newTestModel(t, target("web"))
	m2.pickValue(&jsonField{path: ".msg", lit: `"a\nb"`})
	raw := m2.pills[0].raw
	reparsed, err = parsePills(raw, nil)
	if err != nil || len(reparsed) != 1 || len(reparsed[0].values) != 1 || reparsed[0].values[0] != "a\nb" {
		t.Fatalf("reparse of %q = %+v, %v, want the decoded newline back", raw, reparsed, err)
	}
	line := `{"msg":"a\nb"}`
	spans := m2.pills[0].spans(line)
	if len(spans) != 1 || line[spans[0][0]:spans[0][1]] != `a\nb` {
		t.Fatalf("escaped spans = %v, want the raw escaped bytes painted", spans)
	}
}

// A raw yq pill's matches are hits for n/N, even without a span to paint.
func TestYqPillIsAHit(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"http":{"status":500}}`, `{"http":{"status":200}}`)
	m.frame()
	commitPill(m, `.http.status >= 400`)
	if len(m.pills) != 1 || m.pills[0].op != pillOpYq {
		t.Fatalf("pills = %+v, want the raw yq pill", m.pills)
	}
	if got := len(m.searchMatches); got != 1 {
		t.Fatalf("yq pill matches = %d, want its one hit line", got)
	}
}
