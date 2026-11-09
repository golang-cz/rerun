package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var ctrlK = tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}

// targetIndex is the index of the named target in m.targets, or -1.
func targetIndex(m *tuiModel, name string) int {
	for i, t := range m.targets {
		if t.spec.Name == name {
			return i
		}
	}
	return -1
}

func describe(t Target, desc string) Target {
	t.Desc = desc
	return t
}

func TestFinderCtrlKOpens(t *testing.T) {
	m := newTestModel(t)
	m.Update(ctrlK)
	if !m.finder.open {
		t.Fatal("ctrl+k did not open the finder")
	}
	m.Update(ctrlK)
	if m.finder.open {
		t.Fatal("second ctrl+k did not close the finder")
	}
}

func TestFinderFuzzyQuerySelectsTarget(t *testing.T) {
	m := newTestModel(t)
	m.Update(ctrlK)
	press(m, "w", "r", "k")
	if res := m.finderResults(m.finder.query); len(res) == 0 || res[0].name != "worker" {
		t.Fatalf("results for %q = %v, want worker first", m.finder.query, res)
	}
	press(m, "enter")
	if want := targetIndex(m, "worker"); m.solo != want {
		t.Fatalf("solo = %d, want worker at %d", m.solo, want)
	}
	if m.finder.open {
		t.Fatal("enter did not close the finder")
	}
}

func TestFinderDescMatchRanksBelowNameMatch(t *testing.T) {
	m := newTestModel(t,
		describe(target("docs"), "frontend docs"),
		target("frontend"),
	)
	m.Update(ctrlK)
	press(m, "f", "r", "o", "n", "t")
	res := m.finderResults(m.finder.query)
	if len(res) != 2 || res[0].name != "frontend" || res[1].name != "docs" {
		t.Fatalf("results = %v, want frontend (name) then docs (desc)", res)
	}
	if len(res[1].matched) != 0 {
		t.Fatalf("desc-only match highlighted name chars %v", res[1].matched)
	}
}

func TestFinderEnterOnGroupSelectsIt(t *testing.T) {
	m := newTestModel(t,
		target("web"),
		schemaTarget("api", "backend"),
		schemaTarget("worker", "backend"),
	)
	m.Update(ctrlK)
	press(m, "b", "a", "c", "k")
	press(m, "enter")
	if m.schemaSel != "backend" {
		t.Fatalf("schemaSel = %q, want backend", m.schemaSel)
	}
	if r, _ := m.cursorRow(); !r.header || r.schema != "backend" {
		t.Fatalf("cursor row = %+v, want the backend header", r)
	}
}

func TestFinderUnfoldsCollapsedGroup(t *testing.T) {
	m := newTestModel(t,
		target("web"),
		schemaTarget("api", "backend"),
		schemaTarget("worker", "backend"),
	)
	m.collapsed = map[string]bool{"backend": true}
	m.Update(ctrlK)
	press(m, "w", "o", "r", "k", "e", "r")
	press(m, "enter")
	if m.collapsed["backend"] {
		t.Fatal("group stayed collapsed after jumping to a member")
	}
	if want := targetIndex(m, "worker"); m.solo != want {
		t.Fatalf("solo = %d, want worker at %d", m.solo, want)
	}
	if r, _ := m.cursorRow(); r.header || m.targets[r.target].spec.Name != "worker" {
		t.Fatalf("cursor row = %+v, want the worker row", r)
	}
}

func TestFinderEscLeavesScopeUnchanged(t *testing.T) {
	m := newTestModel(t,
		target("web"),
		schemaTarget("api", "backend"),
	)
	m.cursor = m.indexOfKey("target:web")
	r, _ := m.cursorRow()
	m.applyRowScope(r)
	before := m.scopeState()
	m.Update(ctrlK)
	press(m, "a", "p", "i")
	press(m, "esc")
	if m.finder.open {
		t.Fatal("esc did not close the finder")
	}
	if got := m.scopeState(); got != before {
		t.Fatalf("scope = %+v, want unchanged %+v", got, before)
	}
}

func TestFinderEnterOnCollapsedGroupUnfoldsIt(t *testing.T) {
	m := newTestModel(t,
		target("web"),
		schemaTarget("api", "backend"),
	)
	m.collapsed = map[string]bool{"backend": true}
	m.Update(ctrlK)
	press(m, "b", "a", "c", "k", "e", "n", "d")
	press(m, "enter")
	if m.collapsed["backend"] {
		t.Fatal("jumping to a collapsed group left it folded")
	}
	if r, _ := m.cursorRow(); !r.header || r.schema != "backend" {
		t.Fatalf("cursor row = %+v, want the backend header", r)
	}
}

func TestFinderNestedSubHeaderRevealsItsChain(t *testing.T) {
	omsxAPI := importTarget("omsx", "omsx-api", "omsx/backend")
	omsxAPI.Schema = "omsx/backend"
	server := schemaTarget("server", "backend")
	m := newTestModel(t, omsxAPI, server)
	m.collapsed = map[string]bool{importKeyPrefix + "omsx": true, "omsx/backend": true}
	m.Update(ctrlK)
	press(m, "omsx/backend")
	if res := m.finderResults(m.finder.query); len(res) == 0 || res[0].name != "omsx/backend" {
		t.Fatalf("results = %v, want the nested omsx/backend sub-header first", res)
	}
	press(m, "enter")
	if m.collapsed[importKeyPrefix+"omsx"] || m.collapsed["omsx/backend"] {
		t.Fatalf("collapsed = %v, want the import and its sub-header unfolded", m.collapsed)
	}
	if r, _ := m.cursorRow(); !r.header || r.schema != "omsx/backend" {
		t.Fatalf("cursor row = %+v, want the omsx/backend sub-header", r)
	}
}

func TestFinderDuplicateGroupLabelsStayDistinct(t *testing.T) {
	omsxAPI := importTarget("omsx", "omsx-api", "omsx/backend")
	omsxAPI.Schema = "omsx/backend"
	server := schemaTarget("server", "backend")
	m := newTestModel(t, omsxAPI, server)
	names := map[string]bool{}
	for _, c := range m.finderCandidates() {
		if c.kind == "group" {
			names[c.name] = true
		}
	}
	if !names["backend"] || !names["omsx/backend"] {
		t.Fatalf("group names = %v, want both backend and omsx/backend", names)
	}
}

func TestFinderEnterWithNoMatchIsNoOp(t *testing.T) {
	m := newTestModel(t)
	m.Update(ctrlK)
	press(m, "z", "z", "z")
	before := m.cursor
	press(m, "enter")
	if !m.finder.open || m.finder.query != "zzz" {
		t.Fatalf("finder open=%v query=%q, want it kept open with zzz", m.finder.open, m.finder.query)
	}
	if m.cursor != before || m.solo != -1 {
		t.Fatalf("cursor = %d solo = %d, want both unchanged", m.cursor, m.solo)
	}
}

func TestFinderSelectionClampsWhenQueryNarrows(t *testing.T) {
	m := newTestModel(t)
	m.Update(ctrlK)
	press(m, "w", "o", "r", "k")
	m.finder.sel = 2
	press(m, "enter")
	if want := targetIndex(m, "worker"); m.solo != want {
		t.Fatalf("solo = %d, want worker at %d after the stale selection clamped", m.solo, want)
	}
}

func TestFinderJumpScrollsToRowWithListScrolled(t *testing.T) {
	var targets []Target
	for i := range 30 {
		targets = append(targets, target("t"+strconv.Itoa(i)))
	}
	m := newTestModel(t, targets...)
	dest := m.indexOfKey("target:t25")
	m.listScrolled = true
	m.listScrollAt = dest
	m.Update(ctrlK)
	press(m, "t", "2", "5")
	press(m, "enter")
	top := m.listOffset()
	if m.cursor != dest || m.cursor < top || m.cursor >= top+m.listHeight() {
		t.Fatalf("cursor = %d, window = [%d,%d), want the jumped row on screen", m.cursor, top, top+m.listHeight())
	}
}

func TestFinderIgnoresCtrlKInInputHelpAndQuitConfirm(t *testing.T) {
	m := newTestModel(t)
	press(m, "/")
	m.Update(ctrlK)
	if m.finder.open {
		t.Fatal("ctrl+k opened the finder over the input bar")
	}
	press(m, "esc")

	m = newTestModel(t)
	press(m, "?")
	m.Update(ctrlK)
	if m.finder.open {
		t.Fatal("ctrl+k opened the finder over help")
	}

	m = newTestModel(t)
	press(m, "q")
	m.Update(ctrlK)
	if m.finder.open {
		t.Fatal("ctrl+k opened the finder over the quit confirm")
	}
}

func TestFinderIgnoresMouseWhileOpen(t *testing.T) {
	plain := newTestModel(t)
	plain.frame()
	plain.Update(mouse(2, 1, false))
	if plain.solo == -1 {
		t.Fatal("click on row 1 did not change the solo without the finder, so the test cannot tell")
	}
	m := newTestModel(t)
	m.frame()
	m.Update(ctrlK)
	m.Update(mouse(2, 1, false))
	if m.solo != -1 {
		t.Fatalf("solo = %d after a click under the finder, want none", m.solo)
	}
}

func TestFinderSanitizesQueryAndDesc(t *testing.T) {
	m := newTestModel(t, describe(target("docs"), "frontend   \n  docs"))
	m.Update(ctrlK)
	m.Update(tea.PasteMsg{Content: "do\ncs\r\x1b"})
	if m.finder.query != "docs" {
		t.Fatalf("query = %q, want control characters dropped", m.finder.query)
	}
	for _, c := range m.finderCandidates() {
		if c.name == "docs" && c.desc != "frontend docs" {
			t.Fatalf("desc = %q, want whitespace collapsed", c.desc)
		}
	}
}

// Kitty's disambiguation spells backspace as ctrl+h; the finder must delete on both.
func TestFinderCtrlHDeletesLikeBackspace(t *testing.T) {
	m := newTestModel(t)
	m.Update(ctrlK)
	press(m, "w", "o", "r")
	press(m, "ctrl+h")
	if m.finder.query != "wo" {
		t.Fatalf("query = %q after ctrl+h, want %q", m.finder.query, "wo")
	}
}

// The bar types an alt+printable combo; the finder must do the same.
func TestFinderTypesAltPrintableLikeTheBar(t *testing.T) {
	m := newTestModel(t)
	m.Update(ctrlK)
	m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModAlt})
	if m.finder.query != "q" {
		t.Fatalf("query = %q after alt+q, want %q typed like the bar", m.finder.query, "q")
	}
}

func TestFinderLongQueryShowsTail(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 30, 20
	m.Update(ctrlK)
	press(m, strings.Repeat("a", 50)+"Z")
	lines := strings.Split(ansi.Strip(m.finderView()), "\n")
	if !strings.Contains(lines[1], "Z ") {
		t.Fatalf("popup hides the end of the query:\n%s", strings.Join(lines, "\n"))
	}
}

func TestFinderInputRowIsFirstAndShowsCount(t *testing.T) {
	m := newTestModel(t, target("web"), target("worker"), schemaTarget("api", "backend"))
	m.width, m.height = 80, 24
	m.Update(ctrlK)
	press(m, "w", "o", "r", "k")
	lines := strings.Split(ansi.Strip(m.finderView()), "\n")
	if !strings.Contains(lines[1], "❯ work") || !strings.Contains(lines[1], "1/1") {
		t.Fatalf("first inner line = %q, want the query and the 1/1 count", lines[1])
	}
}

func TestFinderDescColumnAlignsAcrossRows(t *testing.T) {
	m := newTestModel(t,
		describe(target("web"), "frontend app"),
		describe(target("worker"), "background jobs"),
	)
	m.width, m.height = 80, 24
	m.Update(ctrlK)
	lines := strings.Split(ansi.Strip(m.finderView()), "\n")
	col := map[string]int{}
	for _, line := range lines {
		for _, desc := range []string{"frontend app", "background jobs"} {
			if i := strings.Index(line, desc); i >= 0 {
				col[desc] = lipgloss.Width(line[:i])
			}
		}
	}
	if len(col) != 2 || col["frontend app"] != col["background jobs"] {
		t.Fatalf("desc columns = %v, want both rows to start at the same column:\n%s", col, strings.Join(lines, "\n"))
	}
}

func TestFinderLongNameTruncatesFromLeft(t *testing.T) {
	m := newTestModel(t, target("services/payments/ledger-reconciler-worker"))
	m.width, m.height = 80, 24
	m.Update(ctrlK)
	var row string
	for _, line := range strings.Split(ansi.Strip(m.finderView()), "\n") {
		if strings.Contains(line, "worker") {
			row = line
		}
	}
	if !strings.Contains(row, "…") || !strings.HasSuffix(strings.TrimRight(row, " │"), "worker") {
		t.Fatalf("row = %q, want the name cut from the left and ending in its own segment", row)
	}
	if strings.Contains(row, "services") {
		t.Fatalf("row = %q, want the long prefix cut away", row)
	}
}

func TestFinderTinyTerminalLinesFitWidth(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 20, 8
	m.Update(ctrlK)
	press(m, strings.Repeat("w", 40))
	for i, line := range strings.Split(m.View().Content, "\n") {
		if w := lipgloss.Width(line); w > 20 {
			t.Fatalf("line %d is %d cells wide on a 20-wide terminal: %q", i, w, line)
		}
	}
}

func TestFinderBoxSizeIgnoresQuery(t *testing.T) {
	sizes := []struct{ w, h, wantW, wantH int }{{200, 50, 120, 40}, {80, 24, 80, 19}}
	for _, s := range sizes {
		for _, q := range []string{"", "w", "zzzz-no-match", "web"} {
			m := newTestModel(t, target("web"), target("worker"), schemaTarget("api", "backend"))
			m.width, m.height = s.w, s.h
			m.Update(ctrlK)
			press(m, strings.Split(q, "")...)
			if q == "web" && len(m.finderResults(q)) != 1 {
				t.Fatalf("query %q matches %d items, want exactly 1", q, len(m.finderResults(q)))
			}
			popup := m.finderView()
			if w, h := lipgloss.Width(popup), lipgloss.Height(popup); w != s.wantW || h != s.wantH {
				t.Fatalf("%dx%d query %q: popup %dx%d, want %dx%d", s.w, s.h, q, w, h, s.wantW, s.wantH)
			}
			for i, line := range strings.Split(popup, "\n") {
				if w := lipgloss.Width(line); w != s.wantW {
					t.Fatalf("%dx%d query %q: line %d is %d cells, want %d", s.w, s.h, q, i, w, s.wantW)
				}
			}
		}
	}
}

func TestFinderMultiWordMatchesAnyOrder(t *testing.T) {
	m := newTestModel(t, target("bpn-api/test"), target("worker"))
	for _, q := range []string{"bpn test", "test bpn"} {
		res := m.finderResults(q)
		if len(res) != 1 || res[0].name != "bpn-api/test" {
			t.Fatalf("results for %q = %v, want bpn-api/test only", q, res)
		}
	}
}

func TestFinderMultiWordNeedsEveryWord(t *testing.T) {
	m := newTestModel(t, target("bpn-api/test"))
	if res := m.finderResults("bpn zzz"); len(res) != 0 {
		t.Fatalf("results for %q = %v, want none", "bpn zzz", res)
	}
}

func TestFinderMultiWordMatchedCoversBothWords(t *testing.T) {
	m := newTestModel(t, target("bpn-api/test"))
	res := m.finderResults("bpn test")
	if len(res) != 1 {
		t.Fatalf("results = %v, want one match", res)
	}
	want := []int{0, 1, 2, 8, 9, 10, 11}
	if fmt.Sprint(res[0].matched) != fmt.Sprint(want) {
		t.Fatalf("matched = %v, want %v", res[0].matched, want)
	}
}

func TestFinderBlankQueryReturnsAll(t *testing.T) {
	m := newTestModel(t, target("web"), target("worker"))
	if res := m.finderResults("   "); len(res) != 2 {
		t.Fatalf("blank query returned %d results, want 2", len(res))
	}
}

func BenchmarkFinderMatch(b *testing.B) {
	names := make([]string, 2000)
	for i := range names {
		names[i] = "group-" + strconv.Itoa(i/20) + "/target-" + strconv.Itoa(i)
	}
	descs := make([]string, len(names))
	for i := range descs {
		descs[i] = "synthetic description for target " + strconv.Itoa(i)
	}
	words := strings.Fields("grp tgt 1")
	b.ResetTimer()
	for b.Loop() {
		nameHits := fuzzyAll(words, names)
		descHits := fuzzyAll(words, descs)
		_, _ = nameHits, descHits
	}
}

// The ctrl+k finder is named "go to" in its input and in the help.
func TestFinderIsNamedGoTo(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 30
	m.Update(ctrlK)
	if view := m.View().Content; !strings.Contains(view, "go to target or run…") {
		t.Fatalf("finder input lacks the go-to placeholder:\n%s", view)
	}
	found := false
	for _, kv := range helpKeys {
		if kv[0] == "ctrl+k g" && kv[1] == "go to a target, or run a make target" {
			found = true
		}
	}
	if !found {
		t.Fatal("help does not name ctrl+k as go to")
	}
}
