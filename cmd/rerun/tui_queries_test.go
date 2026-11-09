package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// queriesConfig is a rerun.yml with saved queries at every level.
const queriesConfig = `
queries:
  errors: level=error|fatal
  slow: http.duration_ms>500

groups:
  backend:
    queries:
      4xx: http.status>=400 http.status<500
      slow: group-slow

targets:
  api:
    group: backend
    run: sleep 1
    queries:
      4xx: api-4xx
  web:
    run: sleep 1
`

func writeQueriesConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, body)
	return path
}

// queries: loads at the top level, per group and per target; the target's own
// entry wins a name clash with its group.
func TestConfigQueriesLoad(t *testing.T) {
	cfg, err := ParseConfig(writeQueriesConfig(t, queriesConfig))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Queries["errors"]; got != "level=error|fatal" {
		t.Fatalf("global errors = %q", got)
	}
	api, _ := cfg.Find("api")
	if got := api.Queries["4xx"]; got != "api-4xx" {
		t.Fatalf("target 4xx = %q, want the target's own over the group's", got)
	}
	if got := api.Queries["slow"]; got != "group-slow" {
		t.Fatalf("target slow = %q, want the group's inherited", got)
	}
	web, _ := cfg.Find("web")
	if len(web.Queries) != 0 {
		t.Fatalf("web queries = %v, want none", web.Queries)
	}
}

// A bad pill string is a config error naming the file, the key and the pill.
func TestConfigQueriesValidate(t *testing.T) {
	cases := map[string]string{
		"top level": "queries:\n  bad: level=\ntargets:\n  web:\n    run: sleep 1\n",
		"group":     "groups:\n  backend:\n    queries:\n      bad: level=\ntargets:\n  web:\n    run: sleep 1\n",
		"target":    "targets:\n  web:\n    run: sleep 1\n    queries:\n      bad: level=\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeQueriesConfig(t, body)
			_, err := ParseConfig(path)
			if err == nil {
				t.Fatal("ParseConfig succeeded on a bad query")
			}
			msg := err.Error()
			for _, want := range []string{path, "bad", "level=", "missing value"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not name %q", msg, want)
				}
			}
		})
	}
}

// A group query's field names resolve against the group's columns at load.
func TestConfigQueriesResolveGroupColumns(t *testing.T) {
	body := "groups:\n  backend:\n    columns: [\"lvl {.level}\"]\n    queries:\n      errors: lvl=error\ntargets:\n  web:\n    group: backend\n    run: sleep 1\n"
	if _, err := ParseConfig(writeQueriesConfig(t, body)); err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
}

// queriesModel has one target with its own queries and a global set.
func queriesModel(t *testing.T) *tuiModel {
	t.Helper()
	web := target("web")
	web.Queries = map[string]string{"hits": "web-hit", "slow": "target-slow"}
	m := newTestModel(t, web)
	m.queries = map[string]string{"errors": "err", "slow": "global-slow"}
	addLines(m, "web", "web-hit ok", "err plain", "nothing here")
	m.frame()
	return m
}

// Typing '@' as the first char opens the saved-query list for the pane's scope.
func TestAtPickerListsScopeQueries(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	if m.pickActive() {
		t.Fatal("the picker opened without @")
	}
	typeString(m, "@")
	if !m.pickActive() {
		t.Fatal("the picker did not open on @")
	}
	box := ansi.Strip(m.inputBoxView())
	for _, want := range []string{"@errors", "@hits", "@slow"} {
		if !strings.Contains(box, want) {
			t.Fatalf("picker = %q, want %q", box, want)
		}
	}

	// The text after '@' narrows the list.
	typeString(m, "h")
	if got := m.pickMatches(); len(got) != 1 || got[0].name != "hits" {
		t.Fatalf("matches = %+v, want only hits", got)
	}

	// A name clash resolves to the most specific: the target's slow, not the global.
	seen := map[string]int{}
	for i, q := range m.scopeQueries() {
		seen[q.name] = i
	}
	if got := m.scopeQueries()[seen["slow"]].query; got != "target-slow" {
		t.Fatalf("slow = %q, want the target's over the global", got)
	}
}

// Choosing a query adds one [@name] pill that matches as its parsed pills.
func TestAtPickerChooseAddsSavedPill(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@hi")
	press(m, "enter")

	if len(m.pills) != 1 || m.pills[0].raw != "@hits" || m.pills[0].saved != "hits" {
		t.Fatalf("pills = %+v, want [@hits]", m.pills)
	}
	if m.input != "" || m.inputMode != inputQuery {
		t.Fatalf("input=%q mode=%d after a pick, want the bar ready for more", m.input, m.inputMode)
	}
	m.frame()
	if want := []string{"web-hit ok"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}
	if got := ansi.Strip(paneOf(m)[0]); !strings.Contains(got, "[@hits ×]") {
		t.Fatalf("bar = %q, want the [@hits] pill", got)
	}

	// esc drops the open @ input without adding anything; the bar is still open
	// from the pick, so no fresh / is needed.
	typeString(m, "@er")
	press(m, "esc")
	if len(m.pills) != 1 {
		t.Fatalf("esc added a pill: %+v", m.pills)
	}
}

// The picker arrows move the selection; enter picks the moved-to query.
func TestAtPickerArrowsMoveSelection(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@")
	if m.pickSel != 0 {
		t.Fatalf("pickSel = %d, want 0", m.pickSel)
	}
	press(m, "down")
	if m.pickSel != 1 {
		t.Fatalf("pickSel = %d after down, want 1", m.pickSel)
	}
	press(m, "enter")
	want := "@" + m.scopeQueries()[1].name
	if len(m.pills) != 1 || m.pills[0].raw != want {
		t.Fatalf("pills = %+v, want [%s]", m.pills, want)
	}
}

// right on a [@name] pill expands it into its pills in place.
func TestExpandSavedPill(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@hi")
	press(m, "enter") // adds [@hits]; the bar stays open

	press(m, "left") // select it
	if m.barSel != 0 {
		t.Fatalf("barSel = %d, want the [@hits] pill selected", m.barSel)
	}
	press(m, "right")
	if len(m.pills) != 1 || m.pills[0].raw != "web-hit" || m.pills[0].saved != "" {
		t.Fatalf("expand: pills = %+v, want the query's own pill", m.pills)
	}
	m.frame()
	if want := []string{"web-hit ok"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}
}

// A multi-pill saved query expands into several pills.
func TestExpandSavedMultiPill(t *testing.T) {
	m := queriesModel(t)
	m.queries["multi"] = "err plain"
	press(m, "/")
	typeString(m, "@multi")
	press(m, "enter")
	press(m, "left", "right")
	if len(m.pills) != 2 || m.pills[0].raw != "err" || m.pills[1].raw != "plain" {
		t.Fatalf("expand: pills = %+v, want err and plain", m.pills)
	}
}

// A typed @name mid-input resolves at commit; an unknown name is an error.
func TestTypedSavedQueryResolves(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "plain @errors")
	press(m, "enter")
	if len(m.pills) != 2 || m.pills[1].saved != "errors" {
		t.Fatalf("pills = %+v, want plain then [@errors]", m.pills)
	}
	m.frame()
	if want := []string{"err plain"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}

	press(m, "/")
	typeString(m, "plain @nope")
	press(m, "enter")
	if m.inputMode != inputQuery || !strings.Contains(m.queryErr, "unknown query @nope") {
		t.Fatalf("mode=%d err=%q, want the bar open naming the unknown query", m.inputMode, m.queryErr)
	}
}

// A saved pill toggled to highlight stops narrowing and only marks hits.
func TestSavedPillToggleHighlight(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@hi")
	press(m, "enter") // adds [@hits]; the bar stays open
	press(m, "left", " ", "esc")
	m.frame()
	if got := len(rawPane(m)); got != 3 {
		t.Fatalf("highlight [@hits] hid lines: pane holds %d, want 3", got)
	}
	if len(m.searchMatches) != 1 {
		t.Fatalf("matches = %v, want the one hit", m.searchMatches)
	}
}

// A config reload adopts the new global queries.
func TestReloadAdoptsQueries(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.rebuildRows(&configChange{
		targets: []Target{target("web")},
		queries: map[string]string{"errors": "err"},
	})
	if got := m.queries["errors"]; got != "err" {
		t.Fatalf("queries = %v, want the reloaded set", m.queries)
	}
}

// In a scope with several targets, a target's own query beats another target's
// inherited group query of the same name.
func TestScopeQueriesOwnBeatsGroupAcrossTargets(t *testing.T) {
	api := schemaTarget("api", "backend")
	api.Queries = map[string]string{"slow": "group-slow"}
	api.groupQueries = map[string]string{"slow": "group-slow"}
	web := schemaTarget("web", "backend")
	web.Queries = map[string]string{"slow": "web-slow"}
	web.ownQueries = map[string]string{"slow": "web-slow"}
	web.groupQueries = map[string]string{"slow": "group-slow"}
	m := newTestModel(t, api, web)

	for _, q := range m.scopeQueries() {
		if q.name == "slow" {
			if q.query != "web-slow" {
				t.Fatalf("slow = %q, want web's own over the group api inherits", q.query)
			}
			return
		}
	}
	t.Fatal("scopeQueries lost slow")
}

// A group query's fields resolve against the group's columns, not the target's
// own columns that replaced them.
func TestSavedQueryParsesWithDefiningColumns(t *testing.T) {
	web := schemaTarget("web", "backend")
	web.Columns = []Column{{Name: "severity", Expr: ".level"}}
	web.ownColumns = true
	web.Queries = map[string]string{"errors": "lvl=error"}
	web.groupQueries = map[string]string{"errors": "lvl=error"}
	web.groupQueryCols = []Column{{Name: "lvl", Expr: ".level"}}
	m := newTestModel(t, web)
	addLines(m, "web", `{"level":"error"}`, `{"level":"info"}`)
	m.frame()

	commitPill(m, "@errors")
	m.frame()
	if want := []string{`{"level":"error"}`}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q: @errors must parse with the group's columns", rawPane(m), want)
	}
}

// A reload re-resolves committed [@name] pills: a changed query changes what
// they match, and a vanished one is dropped with a notice.
func TestReloadReresolvesSavedPills(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.queries = map[string]string{"errors": "err"}
	addLines(m, "web", "err line", "other line")
	m.frame()
	commitPill(m, "@errors")
	m.frame()
	if want := []string{"err line"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q", rawPane(m), want)
	}

	m.rebuildRows(&configChange{
		targets: []Target{target("web")},
		queries: map[string]string{"errors": "other"},
	})
	m.frame()
	if want := []string{"other line"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("after the reload pane = %q, want %q: the pill kept its old pills", rawPane(m), want)
	}

	m.rebuildRows(&configChange{targets: []Target{target("web")}})
	if len(m.pills) != 0 {
		t.Fatalf("pills = %+v, want the vanished @errors dropped", m.pills)
	}
	if !strings.Contains(m.notice, "@errors") {
		t.Fatalf("notice = %q, want it to name the dropped @errors", m.notice)
	}
}

// The @ picker windows past its row cap: the selection stays visible and enter
// adds the query the highlight sits on.
func TestPickerWindowsPastEightRows(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.queries = map[string]string{}
	for i := 0; i < 10; i++ {
		m.queries[strings.ToLower(fmt.Sprintf("q%02d", i))] = "hit"
	}
	press(m, "/")
	typeString(m, "@")
	if got := ansi.Strip(m.inputBoxView()); !strings.Contains(got, "10 queries") {
		t.Fatalf("picker header = %q, want 10 queries", got)
	}
	for i := 0; i < 9; i++ {
		press(m, "down")
	}
	if m.pickSel != 9 {
		t.Fatalf("pickSel = %d, want 9", m.pickSel)
	}
	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, "@q09") {
		t.Fatalf("picker = %q, want the selected q09 in the window", box)
	}
	press(m, "enter")
	if len(m.pills) != 1 || m.pills[0].raw != "@q09" {
		t.Fatalf("pills = %+v, want [@q09], the query the highlight sat on", m.pills)
	}
}

// -@name negates the whole saved query.
func TestNegatedSavedQueryFilters(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "-@hits")
	press(m, "enter")
	m.frame()
	if want := []string{"err plain", "nothing here"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want %q: -@hits must drop the query's lines", rawPane(m), want)
	}
}

// esc with a saved name that does not resolve keeps the bar open with the
// error; the next esc clears it.
func TestEscWithUnresolvedSavedNameKeepsBarOpen(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "plain @nope")
	press(m, "esc")
	if !m.barFocus || !strings.Contains(m.queryErr, "unknown query @nope") {
		t.Fatalf("focus=%v err=%q, want the bar open naming @nope", m.barFocus, m.queryErr)
	}
	press(m, "esc")
	if m.barFocus || len(m.pills) != 0 {
		t.Fatalf("second esc: focus=%v pills=%+v, want closed with nothing added", m.barFocus, m.pills)
	}
}

// esc commits a valid typed draft even while a pill is selected.
func TestEscCommitsDraftWithPillSelected(t *testing.T) {
	m := queriesModel(t)
	commitPill(m, "plain")
	press(m, "/")
	typeString(m, "err")
	press(m, "home", "left") // select the [plain] pill
	if m.barSel < 0 {
		t.Fatal("left at cursor 0 did not select the pill")
	}
	press(m, "esc")
	if len(m.pills) != 2 || m.pills[1].raw != "err" {
		t.Fatalf("pills = %+v, want the draft committed beside the selected pill", m.pills)
	}
	if m.barFocus {
		t.Fatal("esc left the bar focused")
	}
}

// Expanding a highlight [@name] keeps every inserted pill a highlight.
func TestExpandSavedKeepsKind(t *testing.T) {
	m := queriesModel(t)
	m.queries["multi"] = "err plain"
	press(m, "/")
	typeString(m, "@multi")
	press(m, "enter")
	press(m, "left", " ", "right") // highlight, then expand
	if len(m.pills) != 2 {
		t.Fatalf("pills = %+v, want the two query pills", m.pills)
	}
	for _, p := range m.pills {
		if p.kind != pillHighlight {
			t.Fatalf("pill %q kind = %d, want highlight after expanding a highlight", p.raw, p.kind)
		}
	}
	m.frame()
	if got := len(rawPane(m)); got != 3 {
		t.Fatalf("expanded highlights hid lines: pane holds %d, want 3", got)
	}
}

// Editing a pill and committing it again restores its filter ⇄ highlight kind.
func TestEditRestoresPillKind(t *testing.T) {
	m := queriesModel(t)
	commitPill(m, "plain")
	press(m, "/", "left", " ") // toggle to highlight
	press(m, "enter")          // edit the text back into the input
	press(m, "enter")          // commit it again
	if len(m.pills) != 1 || m.pills[0].kind != pillHighlight {
		t.Fatalf("pills = %+v, want the highlight kind restored after the edit", m.pills)
	}
}

// A selected pill's keys show in the input box's footer; → expand only on [@name].
func TestSelectedPillContextFooter(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@hi")
	press(m, "enter")
	press(m, "left") // select [@hits]
	box := ansi.Strip(m.inputBoxView())
	want := "enter edit · x remove · space filter⇄highlight · → expand · esc back"
	if !strings.Contains(box, want) {
		t.Fatalf("box footer = %q, want %q", box, want)
	}

	commitPill(m, "plain")
	press(m, "/", "left")
	box = ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, "enter edit · x remove · space filter⇄highlight · esc back") {
		t.Fatalf("box footer = %q, want the keys without → expand on a plain pill", box)
	}
	if strings.Contains(box, "→ expand") {
		t.Fatalf("box footer = %q, want no → expand on a plain pill", box)
	}
}

// The empty query box shows the pill shapes as a faint placeholder.
func TestInputPlaceholder(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	box := ansi.Strip(m.inputBoxView())
	if !strings.Contains(box, `level=error  "two words"  @saved  .yq`) {
		t.Fatalf("box = %q, want the placeholder", box)
	}
}

// A saved-ref cycle, a chain past the depth cap, and an unknown ref are config
// errors at load, never inert pills.
func TestConfigQueryRefsValidate(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"cycle": {
			"queries:\n  a: \"@b\"\n  b: \"@a\"\ntargets:\n  web:\n    run: sleep 1\n",
			"refers to itself",
		},
		"unknown": {
			"queries:\n  a: \"@nope\"\ntargets:\n  web:\n    run: sleep 1\n",
			"unknown query @nope",
		},
		"too deep": {
			"queries:\n" + deepQueryChain(10) + "targets:\n  web:\n    run: sleep 1\n",
			"past 8 levels",
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeQueriesConfig(t, tt.body)
			if _, err := ParseConfig(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseConfig error = %v, want it to name %q", err, tt.want)
			}
		})
	}

	// A target query referring to a global one is fine.
	ok := "queries:\n  errors: err\ntargets:\n  web:\n    run: sleep 1\n    queries:\n      mine: \"@errors err\"\n"
	if _, err := ParseConfig(writeQueriesConfig(t, ok)); err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
}

// deepQueryChain writes n saved queries, each referring to the next.
func deepQueryChain(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  q%d: \"@q%d\"\n", i, i+1)
	}
	fmt.Fprintf(&b, "  q%d: leaf\n", n)
	return b.String()
}

// A legacy queries: block loads even when a saved query is named like a
// target field; the block shape never reads as a target.
func TestLegacyQueryNamedLikeATargetFieldLoads(t *testing.T) {
	path := writeQueriesConfig(t, "queries:\n  run: sleep 1\nweb:\n  run: sleep 1\n")
	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Queries["run"]; got != "sleep 1" {
		t.Fatalf("queries = %v, want the saved query named run kept", cfg.Queries)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "web" {
		t.Fatalf("targets = %+v, want only web", cfg.Targets)
	}

	path = writeQueriesConfig(t, "queries:\n  errors: level=error\nweb:\n  run: sleep 1\n")
	cfg, err = ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.Queries["errors"]; got != "level=error" {
		t.Fatalf("queries = %v, want the legacy queries: block kept", cfg.Queries)
	}
}

// A config reload that shrinks the query list under the picker's selection
// must not panic the window math; the selection clamps into range.
func TestPickerClampsSelectionWhenQueriesShrink(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.queries = map[string]string{}
	for i := 0; i < 10; i++ {
		m.queries[fmt.Sprintf("q%02d", i)] = "hit"
	}
	press(m, "/")
	typeString(m, "@")
	for i := 0; i < 9; i++ {
		press(m, "down")
	}
	m.queries = map[string]string{"q00": "hit", "q01": "hit"}
	box := ansi.Strip(m.inputBoxView())
	if m.pickSel != 1 {
		t.Fatalf("pickSel = %d, want clamped to the shrunk list's last row", m.pickSel)
	}
	if !strings.Contains(box, "@q01") {
		t.Fatalf("picker = %q, want the clamped selection drawn", box)
	}
}

// An exactly typed @name previews as a draft while the picker stays open.
func TestExactSavedNameDrafts(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@hits")
	if !m.pickActive() {
		t.Fatal("the @ picker must stay open on an exact name")
	}
	if !m.draftOn {
		t.Fatal("an exactly typed @name must resolve into the draft")
	}
	m.frame()
	if want := []string{"web-hit ok"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want the draft preview %q", rawPane(m), want)
	}
}

// esc on a picked name with no candidates keeps the bar open with the error;
// an exactly typed @name commits as a saved pill.
func TestEscInPickerCommitsOrNamesTheError(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@nope")
	press(m, "esc")
	if !m.barFocus || !strings.Contains(m.queryErr, "unknown query @nope") {
		t.Fatalf("focus=%v err=%q, want the bar open naming @nope", m.barFocus, m.queryErr)
	}
	press(m, "esc")
	if m.barFocus {
		t.Fatal("second esc must close the bar")
	}

	press(m, "/")
	typeString(m, "@hits")
	press(m, "esc")
	if m.barFocus || len(m.pills) != 1 || m.pills[0].saved != "hits" {
		t.Fatalf("focus=%v pills=%+v, want [@hits] committed and the bar closed", m.barFocus, m.pills)
	}
}

// → on a negated [@name] refuses to expand: the negation would silently drop.
func TestExpandNegatedSavedPillRefused(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "-@hits")
	press(m, "enter")
	press(m, "/", "left", "right")
	if len(m.pills) != 1 || m.pills[0].saved != "hits" || !m.pills[0].neg {
		t.Fatalf("pills = %+v, want the negated [@hits] kept whole", m.pills)
	}
	if !strings.Contains(m.hint, "negated @query cannot expand") {
		t.Fatalf("hint = %q, want the refusal named", m.hint)
	}
}
