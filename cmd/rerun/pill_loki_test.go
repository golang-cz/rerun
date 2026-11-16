package main

import (
	"reflect"
	"strings"
	"testing"
)

// A quoted value is literal: no any-of split, no wildcard, no regex.
func TestQuotedValuesAreLiteral(t *testing.T) {
	tests := []struct {
		input, line string
		want        bool
	}{
		{`level="error|fatal"`, `{"level":"error|fatal"}`, true},
		{`level="error|fatal"`, `{"level":"error"}`, false},
		{`level=error|fatal`, `{"level":"fatal"}`, true},
		{`trace_id="*"`, `{"trace_id":"*"}`, true},
		{`trace_id="*"`, `{"trace_id":"abc"}`, false},
		{`level="error"`, `{"level":"ERROR"}`, true},
		{`level!="*"`, `{"level":"x"}`, true},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if got := pills[0].match(tt.line); got != tt.want {
			t.Errorf("%s on %s = %v, want %v", tt.input, tt.line, got, tt.want)
		}
	}
}

// =~ is unanchored and takes a bare or quoted value; !~ is its negation.
func TestLokiRegexOperators(t *testing.T) {
	tests := []struct {
		input, line string
		want        bool
	}{
		{`msg=~"time(out|d out)"`, `{"msg":"timed out waiting"}`, true},
		{`msg=~"time(out|d out)"`, `{"msg":"TIMEOUT"}`, false},
		{`level=~error|fatal`, `{"level":"fatal"}`, true},
		{`level=~error|fatal`, `{"level":"warn"}`, false},
		{`msg=~refus`, `{"msg":"connection refused by peer"}`, true},
		{`msg!~refus`, `{"msg":"ok"}`, true},
		{`msg!~refus`, `{"msg":"refused"}`, false},
		{`msg!~refus`, `{"level":"x"}`, false},
		{`msg~/time(out|d out)/`, `{"msg":"timed out"}`, true},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if got := pills[0].match(tt.line); got != tt.want {
			t.Errorf("%s on %s = %v, want %v", tt.input, tt.line, got, tt.want)
		}
	}
}

// Loki's empty-string forms, and their bare-star aliases, mean the same thing.
func TestEmptyStringForms(t *testing.T) {
	tests := []struct {
		input, line string
		want        bool
	}{
		{`trace_id!=""`, `{"trace_id":"abc"}`, true},
		{`trace_id!=""`, `{"trace_id":""}`, false},
		{`trace_id!=""`, `{"msg":"x"}`, false},
		{`trace_id!=*`, `{"msg":"x"}`, true},
		{`trace_id=""`, `{"trace_id":""}`, true},
		{`trace_id=""`, `{"msg":"x"}`, true},
		{`trace_id=""`, `{"trace_id":"abc"}`, false},
		{`trace_id=*`, `{"trace_id":"abc"}`, true},
		{`trace_id=*`, `{"trace_id":""}`, false},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if got := pills[0].match(tt.line); got != tt.want {
			t.Errorf("%s on %s = %v, want %v", tt.input, tt.line, got, tt.want)
		}
	}
}

// LogQL line filters match the whole raw line, with or without a space after the operator.
func TestLogQLLineFilters(t *testing.T) {
	tests := []struct {
		input, line string
		want        bool
	}{
		{`|= "timeout"`, "dial timeout", true},
		{`|=timeout`, "dial timeout", true},
		{`|= "timeout"`, "all good", false},
		{`!= "healthz"`, "GET /healthz", false},
		{`!=healthz`, "GET /api", true},
		{`|~ "time(out|d out)"`, "timed out", true},
		{`|~ "^time"`, "a timeout", false},
		{`!~ "healthz"`, "GET /healthz", false},
		{`!~ "healthz"`, "GET /api", true},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if len(pills) != 1 {
			t.Fatalf("%s: %d pills, want 1", tt.input, len(pills))
		}
		if got := pills[0].match(tt.line); got != tt.want {
			t.Errorf("%s on %q = %v, want %v", tt.input, tt.line, got, tt.want)
		}
	}
}

// Pipeline stages are dropped, but a field filter after a pipe and a value with | are kept.
func TestPipelineStagesIgnored(t *testing.T) {
	pills := mustPills(t, `| json | logfmt level=error|fatal`, nil)
	if len(pills) != 1 || pills[0].field != "level" || len(pills[0].values) != 2 {
		t.Fatalf("pills = %+v, want one level any-of pill", pills)
	}
	pills = mustPills(t, `error | json | pattern "<_> level=<level>"`, nil)
	if len(pills) != 1 || pills[0].raw != "error" {
		t.Fatalf("pills = %+v, want only the text pill", pills)
	}
	pills = mustPills(t, `| bogus`, nil)
	if len(pills) != 1 || pills[0].raw != "bogus" {
		t.Fatalf("pills = %+v, want an unknown stage word as a plain filter", pills)
	}
}

// Each pill shows its canonical text; that text re-parses to the same pill.
func TestCanonicalPillText(t *testing.T) {
	tests := []struct{ input, want string }{
		{`|= timeout`, "timeout"},
		{`|= "timeout"`, "timeout"},
		{`|= "connection refused"`, `"connection refused"`},
		{`!= healthz`, "-healthz"},
		{`!= "healthz"`, "-healthz"},
		{`level="error"`, "level=error"},
		{`level!="debug"`, "level!=debug"},
		{`level="error|fatal"`, `level="error|fatal"`},
		{`level="null"`, `level="null"`},
		{`level=null`, "level=null"},
		{`msg~/re/`, "msg=~re"},
		{`msg=~"re"`, "msg=~re"},
		{`msg=~"re with space"`, `msg=~"re with space"`},
		{`msg=~"time(out|d out)"`, `msg=~"time(out|d out)"`},
		{`trace_id=*`, `trace_id!=""`},
		{`trace_id!=*`, `trace_id=""`},
		{`|~ re`, `|~ "re"`},
		{`!~ re`, `!~ "re"`},
		{`msg!~re`, "msg!~re"},
		{`-level=error`, "-level=error"},
		{`"OR"`, `"OR"`},
		{`level=error OR msg~panic`, "level=error OR msg~panic"},
		{`|= "timeout" OR |~ fatal`, `timeout OR |~ "fatal"`},
		{`http.status>=400`, "http.status>=400"},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if got := pills[len(pills)-1].raw; got != tt.want {
			t.Errorf("%s shows %q, want %q", tt.input, got, tt.want)
		}
		again := mustPills(t, tt.want, nil)
		if len(again) != len(pills) || again[len(again)-1].raw != tt.want {
			t.Errorf("%s re-parsed from %q gives %+v", tt.input, tt.want, again)
		}
	}
}

// OR keeps its shape with the new forms on either side.
func TestLogQLWithOR(t *testing.T) {
	pills := mustPills(t, `|= "timeout" OR |~ "fatal"`, nil)
	if len(pills) != 1 || len(pills[0].alts) != 2 {
		t.Fatalf("pills = %+v, want one OR pill", pills)
	}
	for line, want := range map[string]bool{"dial timeout": true, "fatal crash": true, "all good": false} {
		if got := pills[0].match(line); got != want {
			t.Errorf("OR on %q = %v, want %v", line, got, want)
		}
	}
}

// The full Loki paste: selector ignored, line filters and label filters become pills.
func TestLokiPasteBecomesPills(t *testing.T) {
	pills := mustPills(t, `{app="api"} |= "timeout" != "healthz" | json | level=~"error|fatal" status>=500`, nil)
	want := []string{"timeout", "-healthz", "level=~error|fatal", "status>=500"}
	if len(pills) != len(want) {
		t.Fatalf("got %d pills, want %d: %+v", len(pills), len(want), pills)
	}
	for i, w := range want {
		if pills[i].raw != w {
			t.Errorf("pill %d shows %q, want %q", i, pills[i].raw, w)
		}
	}
}

// A saved query keeps its stored text and its new forms match after @name resolves.
func TestSavedQueryWithLokiForms(t *testing.T) {
	m := queriesModel(t)
	stored := `|= "web" !~ "plain"`
	m.queries["loki"] = stored
	commitPill(m, "@loki")
	if got := rawPane(m); len(got) != 1 || got[0] != "web-hit ok" {
		t.Fatalf("pane = %q, want only the web line", got)
	}
	if m.queries["loki"] != stored {
		t.Fatalf("stored query changed to %q", m.queries["loki"])
	}
}

// A spaced field operator is one field pill, not a text pill and a negation.
func TestSpacedFieldOperators(t *testing.T) {
	tests := []struct{ input, want string }{
		{`level != debug`, "level!=debug"},
		{`level != "debug"`, "level!=debug"},
		{`status >= 500`, "status>=500"},
		{`msg =~ "re"`, "msg=~re"},
		{`msg ~ x`, "msg~x"},
		{`level=error OR level != debug`, "level=error OR level!=debug"},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if len(pills) != 1 || pills[0].raw != tt.want {
			t.Errorf("%s = %+v, want one pill %q", tt.input, pills, tt.want)
		}
	}
}

// A line filter's value is read before the next token, so it never becomes a field.
func TestLineFilterValueIsNotAFieldName(t *testing.T) {
	pills := mustPills(t, `|= timeout != healthz`, nil)
	if len(pills) != 2 || pills[0].raw != "timeout" || pills[1].raw != "-healthz" {
		t.Fatalf("pills = %+v, want timeout and -healthz", pills)
	}
}

// The Loki paste with a spaced label filter after the pipe.
func TestLokiPasteWithSpacedFilters(t *testing.T) {
	pills := mustPills(t, `{app="api"} |= "timeout" | json | level != "error" | status >= 500`, nil)
	want := []string{"timeout", "level!=error", "status>=500"}
	var got []string
	for _, p := range pills {
		got = append(got, p.raw)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pills = %q, want %q", got, want)
	}
}

// A stage's flag and quoted argument are dropped; an unquoted word after it is a filter.
func TestStageArguments(t *testing.T) {
	tests := []struct{ input, want string }{
		{`| logfmt --strict level=error`, "level=error"},
		{`| pattern level=error`, "level=error"},
		{`| pattern "<_> level=<level>" level=error`, "level=error"},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if len(pills) != 1 || pills[0].raw != tt.want {
			t.Errorf("%s = %+v, want one pill %q", tt.input, pills, tt.want)
		}
	}
}

// A negated line filter has one spelling; the - form is an error naming it.
func TestNegatedLineFilterErrors(t *testing.T) {
	for input, want := range map[string]string{`-|~ foo`: "!~", `-|= foo`: "!="} {
		_, err := parsePills(input, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s err = %v, want it to name %s", input, err, want)
		}
	}
}

// A selector's quoted brace does not end it early.
func TestSelectorWithQuotedBrace(t *testing.T) {
	pills := mustPills(t, `{app="a}b"} error`, nil)
	if len(pills) != 1 || pills[0].raw != "error" {
		t.Fatalf("pills = %+v, want only the text pill", pills)
	}
}

// Only a scalar "" is empty; maps, lists, zero and false are present.
func TestEmptyMeansScalarEmptyString(t *testing.T) {
	present := []string{`{"trace_id":{}}`, `{"trace_id":[]}`, `{"trace_id":0}`, `{"trace_id":false}`, `{"trace_id":"x"}`}
	absent := []string{`{"trace_id":""}`, `{"msg":"x"}`}
	for _, line := range present {
		if !mustPills(t, `trace_id!=""`, nil)[0].match(line) || !mustPills(t, `trace_id=*`, nil)[0].match(line) {
			t.Errorf("exists on %s = false, want true", line)
		}
		if mustPills(t, `trace_id=""`, nil)[0].match(line) {
			t.Errorf(`trace_id="" on %s = true, want false`, line)
		}
	}
	for _, line := range absent {
		if mustPills(t, `trace_id!=""`, nil)[0].match(line) || mustPills(t, `trace_id=*`, nil)[0].match(line) {
			t.Errorf("exists on %s = true, want false", line)
		}
		if !mustPills(t, `trace_id=""`, nil)[0].match(line) {
			t.Errorf(`trace_id="" on %s = false, want true`, line)
		}
	}
}

// A negated regex paints its value, so n/N can land on the line.
func TestNotRegexPaintsValue(t *testing.T) {
	p := mustPills(t, `msg!~re`, nil)[0]
	line := `{"msg":"abc"}`
	if spans := p.spans(line); len(spans) != 1 || spans[0] != [2]int{8, 11} {
		t.Fatalf("spans = %v, want the value abc", spans)
	}
}

// A clicked empty string is the same pill as typing `field=""`.
func TestPickEmptyStringMatchesRetype(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"trace_id":""}`, `{"trace_id":"x"}`)
	m.frame()
	m.pickValue(&jsonField{path: ".trace_id", lit: `""`})
	if len(m.pills) != 1 || m.pills[0].raw != `trace_id=""` || m.pills[0].op != pillOpBlank {
		t.Fatalf("pills = %+v, want the blank pill trace_id=\"\"", m.pills)
	}
	again := mustPills(t, m.pills[0].raw, nil)[0]
	if again.op != m.pills[0].op {
		t.Fatalf("reparse op = %d, want %d", again.op, m.pills[0].op)
	}
}
