package main

import (
	"strings"
	"testing"
)

// OR joins neighbor terms into one pill; the chain stays one pill with every alternative.
func TestParseORChainIsOnePill(t *testing.T) {
	pills := mustPills(t, "a OR b OR c", nil)
	if len(pills) != 1 || len(pills[0].alts) != 3 {
		t.Fatalf("pills = %+v, want one pill with three alternatives", pills)
	}
	if pills[0].raw != "a OR b OR c" {
		t.Fatalf("raw = %q, want the readable OR text", pills[0].raw)
	}
}

// OR binds tighter than the AND between pills.
func TestParseORBindsTighterThanAND(t *testing.T) {
	pills := mustPills(t, "level=error OR msg~panic -healthz", nil)
	if len(pills) != 2 {
		t.Fatalf("pills = %d, want [level=error OR msg~panic] and [-healthz]", len(pills))
	}
	if pills[0].raw != "level=error OR msg~panic" || len(pills[0].alts) != 2 {
		t.Fatalf("first pill = %+v, want the two-way OR", pills[0])
	}
	if pills[1].raw != "-healthz" || len(pills[1].alts) != 0 || !pills[1].neg {
		t.Fatalf("second pill = %+v, want a plain negated pill", pills[1])
	}
}

func TestParseORErrors(t *testing.T) {
	for _, in := range []string{"OR", "a OR", "OR a", "a OR OR b", "a OR b OR"} {
		if _, err := parsePills(in, nil); err == nil {
			t.Errorf("parsePills(%q) = nil error, want an OR error", in)
		}
	}
}

// A yq term has no way to join an OR, so the parse names it.
func TestParseORRejectsYq(t *testing.T) {
	for _, in := range []string{"level=error OR .level", ".level OR msg~x"} {
		if _, err := parsePills(in, nil); err == nil {
			t.Errorf("parsePills(%q) = nil error, want a .yq OR error", in)
		}
	}
}

// Lowercase or and quoted "OR" are plain full-text terms, not the operator.
func TestParseORLiteralForms(t *testing.T) {
	for _, in := range []string{"a or b", `a "OR" b`} {
		pills := mustPills(t, in, nil)
		if len(pills) != 3 {
			t.Errorf("parsePills(%q) = %d pills, want three plain terms", in, len(pills))
		}
		for _, p := range pills {
			if len(p.alts) != 0 {
				t.Errorf("parsePills(%q) made an OR pill: %+v", in, p)
			}
		}
	}
}

func TestORMatchesAnyAlternative(t *testing.T) {
	p := mustPills(t, "level=error OR msg~panic", nil)[0]
	cases := []struct {
		line string
		want bool
	}{
		{`{"level":"error","msg":"x"}`, true},
		{`{"level":"info","msg":"panic now"}`, true},
		{`{"level":"info","msg":"x"}`, false},
		{"plain panic text", false}, // field alternatives never match a non-JSON line
	}
	for _, c := range cases {
		if got := p.match(c.line); got != c.want {
			t.Errorf("match(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// Mixed alternatives: negation, field ops, and plain text each keep their meaning.
func TestORMixedAlternatives(t *testing.T) {
	neg := mustPills(t, "-healthz OR level=error", nil)[0]
	if !neg.match("plain line") || neg.match("healthz probe") {
		t.Errorf("-healthz OR ...: negated alternative mismatched")
	}
	num := mustPills(t, "status>=500 OR panic", nil)[0]
	if !num.match(`{"status":503}`) || !num.match(`{"status":200,"msg":"panic"}`) || num.match(`{"status":200}`) {
		t.Errorf("status>=500 OR panic: numeric alternative mismatched")
	}
}

// Every alternative that keeps the line marks its own hits.
func TestORSpansMarkEveryMatchingAlternative(t *testing.T) {
	p := mustPills(t, "level=error OR msg~panic", nil)[0]
	line := `{"level":"error","msg":"panic"}`
	if got := len(p.spans(line)); got != 2 {
		t.Fatalf("spans = %v, want a hit for each alternative", p.spans(line))
	}
	if got := p.spans(`{"level":"info","msg":"panic"}`); len(got) != 1 {
		t.Fatalf("spans = %v, want only the msg alternative", got)
	}
}

// The pill's raw text re-parses to the same OR, so enter can edit it back into the input.
func TestORRoundTripsThroughRaw(t *testing.T) {
	p := mustPills(t, "level=error OR msg~panic -healthz", nil)[0]
	again := mustPills(t, p.raw, nil)
	if len(again) != 1 || len(again[0].alts) != 2 || again[0].raw != p.raw {
		t.Fatalf("re-parse of %q = %+v, want the same OR pill", p.raw, again)
	}
}

// A saved query can be an OR alternative; it parses without resolving here.
func TestORAcceptsSavedAlternative(t *testing.T) {
	p := mustPills(t, "@slow OR panic", nil)[0]
	if len(p.alts) != 2 || p.alts[0].saved != "slow" {
		t.Fatalf("pill = %+v, want a saved alternative", p)
	}
}

// The OR pill is drawn as one pill whose label is its readable text.
func TestORPillLabel(t *testing.T) {
	p := mustPills(t, "level=error OR msg~panic", nil)[0]
	if got := pillLabel(p); !strings.Contains(got, "[level=error OR msg~panic ×]") {
		t.Fatalf("label = %q, want the OR text in a filter pill", got)
	}
}

// Filtering on an OR pill keeps every line that either alternative matches.
func TestORFiltersTheRightLines(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"level":"error"}`, `{"msg":"panic"}`, `{"level":"info"}`)
	m.frame()
	commitPill(m, "level=error OR msg~panic")
	if got := rawPane(m); len(got) != 2 {
		t.Fatalf("pane = %q, want the two lines either alternative keeps", got)
	}
}

// enter on an OR pill moves its text back into the input, unchanged.
func TestOREditRoundTripsIntoInput(t *testing.T) {
	m := pillModel(t)
	commitPill(m, "level=error OR msg~panic")
	press(m, "/", "up", "enter")
	if m.input != "level=error OR msg~panic" || len(m.pills) != 0 {
		t.Fatalf("input = %q pills = %d, want the OR text back in the input", m.input, len(m.pills))
	}
}

// A saved query holding OR round-trips: @name expands to its own OR pill's matches.
func TestORInsideSavedQuery(t *testing.T) {
	m := queriesModel(t)
	m.queries["either"] = "web-hit OR err"
	commitPill(m, "@either")
	if got := rawPane(m); len(got) != 2 {
		t.Fatalf("pane = %q, want the two lines the saved OR keeps", got)
	}
}

// An unknown @name inside an OR is named as an error and adds no pill.
func TestORUnknownSavedAlternativeIsAnError(t *testing.T) {
	m := queriesModel(t)
	commitPill(m, "@nope OR err")
	if !strings.Contains(m.queryErr, "unknown query @nope") || len(m.pills) != 0 {
		t.Fatalf("queryErr = %q pills = %d, want the unknown query named", m.queryErr, len(m.pills))
	}
}

// A yq term before a later OR is its own pill when it does not swallow the rest.
func TestYqBeforeOROnlyRejectedWhenItSwallows(t *testing.T) {
	pills := mustPills(t, ".ready error OR panic", nil)
	if len(pills) != 2 || pills[0].op != pillOpYq || len(pills[1].alts) != 2 {
		t.Fatalf("pills = %+v, want a yq pill then the error OR panic pill", pills)
	}
}

// A saved query with a yq term keeps an OR pill's hit painted, so n/N reach it.
func TestPillHitsWalkORAlternatives(t *testing.T) {
	p := mustPills(t, "@ycheck OR panic", nil)[0]
	p.alts[0].sub = mustPills(t, `.level == "error"`, nil)
	if !pillHits(p, decodeJSONLine(`{"level":"error"}`), `{"level":"error"}`) {
		t.Fatal("pillHits missed a line only the saved yq alternative keeps")
	}
	if pillHits(p, decodeJSONLine(`{"level":"info"}`), `{"level":"info"}`) {
		t.Fatal("pillHits marked a line no alternative keeps")
	}
}

// Live preview sees a field @name inside an OR as JSON, so it keeps the line Enter keeps.
func TestDraftJSONFlagSeesResolvedORSavedRefs(t *testing.T) {
	m := queriesModel(t)
	m.queries["errs"] = "level=error"
	press(m, "/")
	typeString(m, "@errs OR panic")
	if !m.draftOn || !m.draft.match(`{"level":"error"}`) {
		t.Fatalf("draft on=%v, want the live preview to keep the JSON line", m.draftOn)
	}
}

// An unknown @name inside an OR turns the typed input red, even while the picker is around.
func TestUnknownORSavedRefTurnsInputRed(t *testing.T) {
	m := queriesModel(t)
	press(m, "/")
	typeString(m, "@nope OR err")
	if !strings.Contains(m.inputErr(), "unknown query @nope") {
		t.Fatalf("inputErr = %q, want the unknown query named", m.inputErr())
	}
}

// A saved query that changes or disappears under an OR pill is re-resolved on reload.
func TestReloadReresolvesORSavedAlternatives(t *testing.T) {
	m := queriesModel(t)
	m.queries["either"] = "web-hit"
	commitPill(m, "@either OR nothing")
	if len(m.pills) != 1 {
		t.Fatalf("pills = %d, want the OR pill", len(m.pills))
	}
	m.queries["either"] = "err"
	m.reresolveSaved()
	if got := m.pills[0].alts[0].sub[0].values; len(got) != 1 || got[0] != "err" {
		t.Fatalf("resolved sub = %+v after the query changed, want err", m.pills[0].alts[0].sub)
	}
	delete(m.queries, "either")
	m.reresolveSaved()
	if len(m.pills) != 0 {
		t.Fatalf("pills = %d after the query was deleted, want the OR pill dropped", len(m.pills))
	}
	if !strings.Contains(m.notice, "@either") {
		t.Fatalf("notice = %q, want the dropped query named", m.notice)
	}
}
