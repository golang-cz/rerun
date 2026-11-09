package main

import (
	"reflect"
	"strings"
	"testing"
)

func mustPills(t *testing.T, input string, cols []Column) []pill {
	t.Helper()
	pills, err := parsePills(input, cols)
	if err != nil {
		t.Fatalf("parsePills(%q) error: %v", input, err)
	}
	return pills
}

func TestParsePillsKinds(t *testing.T) {
	cols := []Column{{Name: "lvl", Expr: ".level"}}
	tests := []struct {
		input string
		op    pillOp
		field string
		neg   bool
		saved string
	}{
		{"timeout", pillOpText, "", false, ""},
		{`"connection refused"`, pillOpText, "", false, ""},
		{"-healthz", pillOpText, "", true, ""},
		{"level=error", pillOpEq, "level", false, ""},
		{`level=error|fatal`, pillOpEq, "level", false, ""},
		{"level!=debug", pillOpNe, "level", false, ""},
		{"http.status>=400", pillOpGe, "http.status", false, ""},
		{"http.status<500", pillOpLt, "http.status", false, ""},
		{"msg~refused", pillOpContains, "msg", false, ""},
		{`msg~/time(out|d out)/`, pillOpRegex, "msg", false, ""},
		{"msg=~refus.*", pillOpRegex, "msg", false, ""},
		{"trace_id=*", pillOpExists, "trace_id", false, ""},
		{"trace_id!=*", pillOpMissing, "trace_id", false, ""},
		{`.level=="error"`, pillOpYq, "", false, ""},
		{`."a-b"=1`, pillOpEq, `."a-b"`, false, ""},
		{"lvl=error", pillOpEq, "lvl", false, ""},
		{"@errors", pillOpText, "", false, "errors"},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, cols)
		if len(pills) != 1 {
			t.Fatalf("%q: got %d pills, want 1", tt.input, len(pills))
		}
		p := pills[0]
		if tt.saved != "" {
			if p.saved != tt.saved {
				t.Errorf("%q: saved=%q, want %q", tt.input, p.saved, tt.saved)
			}
			continue
		}
		if p.op != tt.op || p.field != tt.field || p.neg != tt.neg {
			t.Errorf("%q: got op=%v field=%q neg=%v, want op=%v field=%q neg=%v",
				tt.input, p.op, p.field, p.neg, tt.op, tt.field, tt.neg)
		}
	}
}

func TestParsePillsMulti(t *testing.T) {
	pills := mustPills(t, `level=error -healthz "connection refused"`, nil)
	if len(pills) != 3 {
		t.Fatalf("got %d pills, want 3", len(pills))
	}
	if pills[0].op != pillOpEq || pills[1].op != pillOpText || !pills[1].neg || pills[2].op != pillOpText {
		t.Fatalf("unexpected pills: %+v", pills)
	}
	if pills[2].values[0] != "connection refused" {
		t.Errorf("phrase value = %q", pills[2].values[0])
	}
}

func TestParsePillsErrors(t *testing.T) {
	tests := []struct{ input, want string }{
		{"level=", "level=: missing value"},
		{"level!=", "level!=: missing value"},
		{"=error", "=error: missing field"},
		{"-", "-: missing value"},
		{`""`, `""` + ": missing value"},
		{"@", "@: missing saved-query name"},
		{`msg~/[/`, "bad regex"},
		{"http.status>=abc", "not a number"},
		{`"unterminated`, "unterminated quote"},
	}
	for _, tt := range tests {
		_, err := parsePills(tt.input, nil)
		if err == nil {
			t.Errorf("%q: want error containing %q, got nil", tt.input, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q: error %q, want it to contain %q", tt.input, err, tt.want)
		}
	}
}

func TestPillMatch(t *testing.T) {
	cols := []Column{{Name: "lvl", Expr: ".level"}}
	tests := []struct {
		name  string
		input string
		line  string
		want  bool
		cols  []Column
	}{
		{"text hit", "timeout", "connection timeout error", true, nil},
		{"text case-insensitive", "timeout", "a TIMEOUT occurred", true, nil},
		{"text miss", "timeout", "all good", false, nil},
		{"phrase", `"connection refused"`, "dial tcp: connection refused by host", true, nil},
		{"phrase miss", `"connection refused"`, "dial tcp: connection reset", false, nil},
		{"negated text keeps miss", "-healthz", "GET /api/users", true, nil},
		{"negated text drops hit", "-healthz", "GET /healthz 200", false, nil},

		{"eq string", "level=error", `{"level":"error","msg":"x"}`, true, nil},
		{"eq case-insensitive", "level=error", `{"level":"ERROR"}`, true, nil},
		{"eq miss", "level=error", `{"level":"info"}`, false, nil},
		{"eq never matches non-JSON", "level=error", "level=error plain text", false, nil},
		{"eq any-of first", `level=error|fatal`, `{"level":"error"}`, true, nil},
		{"eq any-of second", `level=error|fatal`, `{"level":"FATAL"}`, true, nil},
		{"eq any-of miss", `level=error|fatal`, `{"level":"warn"}`, false, nil},
		{"eq escaped pipe is literal", `level=a\|b`, `{"level":"a|b"}`, true, nil},
		{"eq escaped pipe not split", `level=a\|b`, `{"level":"a"}`, false, nil},
		{"ne drops equal", "level!=debug", `{"level":"debug"}`, false, nil},
		{"ne keeps other", "level!=debug", `{"level":"INFO"}`, true, nil},
		{"ne any-of drops second", `level!=debug|trace`, `{"level":"trace"}`, false, nil},
		{"ne missing field", "level!=debug", `{"msg":"x"}`, false, nil},

		{"eq numeric", "http.status=200", `{"http":{"status":200}}`, true, nil},
		{"eq numeric mismatch", "http.status=200", `{"http":{"status":201}}`, false, nil},
		{"eq numeric vs string", "http.status=200", `{"http":{"status":"200"}}`, true, nil},
		{"eq string vs numeric", `msg=error1`, `{"msg":123}`, false, nil},
		{"ge hit", "http.status>=400", `{"http":{"status":500}}`, true, nil},
		{"ge boundary", "http.status>=400", `{"http":{"status":400}}`, true, nil},
		{"ge miss", "http.status>=400", `{"http":{"status":399}}`, false, nil},
		{"lt hit", "http.status<500", `{"http":{"status":404}}`, true, nil},
		{"gt miss on string field", "http.status>=400", `{"http":{"status":"500"}}`, false, nil},
		{"gt missing field", "http.status>=400", `{"msg":"x"}`, false, nil},
		{"le float", "http.duration<=1.5", `{"http":{"duration":1.5}}`, true, nil},

		{"contains hit", "msg~refused", `{"msg":"connection REFUSED"}`, true, nil},
		{"contains miss", "msg~refused", `{"msg":"connection reset"}`, false, nil},
		{"regex hit alternation", `msg~/time(out|d out)/`, `{"msg":"timed out waiting"}`, true, nil},
		{"regex hit second alt", `msg~/time(out|d out)/`, `{"msg":"TIMEOUT"}`, false, nil},
		{"regex alias", "msg=~refus.*", `{"msg":"refused by peer"}`, true, nil},
		{"regex alias miss", "msg=~^refused", `{"msg":"not refused"}`, false, nil},

		{"exists hit", "trace_id=*", `{"trace_id":"abc"}`, true, nil},
		{"exists miss", "trace_id=*", `{"msg":"x"}`, false, nil},
		{"exists null counts missing", "trace_id=*", `{"trace_id":null}`, false, nil},
		{"missing hit", "trace_id!=*", `{"msg":"x"}`, true, nil},
		{"missing miss", "trace_id!=*", `{"trace_id":"abc"}`, false, nil},

		{"yq truthy keeps", `.level=="error"`, `{"level":"error"}`, true, nil},
		{"yq falsy drops", `.level=="error"`, `{"level":"info"}`, false, nil},
		{"yq non-JSON drops", `.level=="error"`, "plain line", false, nil},

		{"quoted key path", `."a-b"=1`, `{"a-b":1}`, true, nil},
		{"quoted key miss", `."a-b"=1`, `{"a-b":2}`, false, nil},
		{"column alias", "lvl=error", `{"level":"error"}`, true, cols},
		{"column alias miss", "lvl=error", `{"level":"info"}`, false, cols},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, tt.cols)
		if len(pills) != 1 {
			t.Fatalf("%s: %q gave %d pills", tt.name, tt.input, len(pills))
		}
		if got := pills[0].match(tt.line); got != tt.want {
			t.Errorf("%s: %q on %q = %v, want %v", tt.name, tt.input, tt.line, got, tt.want)
		}
	}
}

func TestPillMatchMultiAnd(t *testing.T) {
	pills := mustPills(t, `level=error -healthz`, nil)
	keep := `{"level":"error","msg":"GET /api failed"}`
	drop := `{"level":"error","msg":"GET /healthz failed"}`
	for _, p := range pills {
		if !p.match(keep) {
			t.Errorf("pill %q should match %q", p.raw, keep)
		}
	}
	got := true
	for _, p := range pills {
		got = got && p.match(drop)
	}
	if got {
		t.Errorf("AND of pills should drop %q", drop)
	}
}

func TestPillSpans(t *testing.T) {
	tests := []struct {
		name  string
		input string
		line  string
		want  [][2]int
	}{
		{"text two hits", "timeout", "timeout and TIMEOUT", [][2]int{{0, 7}, {12, 19}}},
		{"phrase", `"refused"`, "connection refused here", [][2]int{{11, 18}}},
		{"eq highlights value", "level=error", `{"level":"error"}`, [][2]int{{10, 15}}},
		{"contains highlights raw", "msg~refused", `{"msg":"connection refused"}`, [][2]int{{19, 26}}},
		{"regex spans", `msg~/time(out|d out)/`, `{"msg":"timed out"}`, [][2]int{{8, 17}}},
		{"negated has no spans", "-healthz", "GET /healthz", nil},
		{"exists highlights value", "trace_id=*", `{"trace_id":"abc"}`, [][2]int{{13, 16}}},
		{"numeric highlights value", "http.status>=400", `{"http":{"status":500}}`, [][2]int{{18, 21}}},
		{"numeric miss has no spans", "http.status>=400", `{"http":{"status":200}}`, nil},
		{"ne highlights value", "level!=debug", `{"level":"error"}`, [][2]int{{10, 15}}},
		{"ne match has no spans", "level!=debug", `{"level":"debug"}`, nil},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		got := pills[0].spans(tt.line)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: spans(%q) = %v, want %v", tt.name, tt.line, got, tt.want)
		}
	}
}

// Quoted field values lose their quotes and escapes before comparing.
func TestParseQuotedFieldValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		op    pillOp
		want  []string
		line  string
		keep  bool
	}{
		{"eq quoted phrase", `msg="a b"`, pillOpEq, []string{"a b"}, `{"msg":"a b"}`, true},
		{"eq quoted phrase miss", `msg="a b"`, pillOpEq, []string{"a b"}, `{"msg":"ab"}`, false},
		{"eq quoted any-of", `level="a"|"b"`, pillOpEq, []string{"a", "b"}, `{"level":"b"}`, true},
		{"eq quoted any-of not literal pipe", `level="a"|"b"`, pillOpEq, []string{"a", "b"}, `{"level":"a|b"}`, false},
		{"regex quoted value", `msg=~"a.b"`, pillOpRegex, nil, `{"msg":"axb"}`, true},
		{"ne quoted phrase hit drops", `msg!="a b"`, pillOpNe, []string{"a b"}, `{"msg":"a b"}`, false},
		{"ne quoted phrase miss keeps", `msg!="a b"`, pillOpNe, []string{"a b"}, `{"msg":"c"}`, true},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if len(pills) != 1 {
			t.Fatalf("%s: %q gave %d pills", tt.name, tt.input, len(pills))
		}
		p := pills[0]
		if p.op != tt.op {
			t.Errorf("%s: op = %v, want %v", tt.name, p.op, tt.op)
		}
		if tt.want != nil && !reflect.DeepEqual(p.values, tt.want) {
			t.Errorf("%s: values = %q, want %q", tt.name, p.values, tt.want)
		}
		if got := p.match(tt.line); got != tt.keep {
			t.Errorf("%s: match(%q) = %v, want %v", tt.name, tt.line, got, tt.keep)
		}
	}
}

// A fully quoted token is always full text, even when it holds pill syntax.
func TestFullyQuotedTokenIsText(t *testing.T) {
	for _, input := range []string{`"a=b"`, `"GET /x=1"`} {
		pills := mustPills(t, input, nil)
		if len(pills) != 1 || pills[0].op != pillOpText {
			t.Fatalf("%q: got %+v, want one text pill", input, pills)
		}
	}
	pills := mustPills(t, `"GET /x=1"`, nil)
	if pills[0].values[0] != "GET /x=1" {
		t.Fatalf("value = %q", pills[0].values[0])
	}
	if !pills[0].match("10.0.0.1 GET /x=1 200") {
		t.Error("quoted text pill should match the raw line")
	}
	if pills[0].match("10.0.0.1 GET /x=2 200") {
		t.Error("quoted text pill matched a line without the phrase")
	}
}

// A dot opens raw yq; only a whole-token quoted-key field stays a field pill.
func TestDotBodyIsYqUnlessQuotedKeyField(t *testing.T) {
	tests := []struct {
		name  string
		input string
		line  string
		keep  bool
		drop  string
	}{
		{"dot field compare", `.a!=.b`, `{"a":1,"b":2}`, true, `{"a":1,"b":1}`},
		{"spaced yq quoted value", `.status != "ok"`, `{"status":"bad"}`, true, `{"status":"ok"}`},
		{"joined yq quoted value", `.status!="ok"`, `{"status":"bad"}`, true, `{"status":"ok"}`},
		{"numeric yq", `.n >= 1`, `{"n":2}`, true, `{"n":0}`},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if len(pills) != 1 || pills[0].op != pillOpYq {
			t.Fatalf("%s: %q gave %+v, want one yq pill", tt.name, tt.input, pills)
		}
		if !pills[0].match(tt.line) {
			t.Errorf("%s: %q should keep %q", tt.name, tt.input, tt.line)
		}
		if pills[0].match(tt.drop) {
			t.Errorf("%s: %q should drop %q", tt.name, tt.input, tt.drop)
		}
	}
	// The whole remaining input parses as yq: it is one pill.
	pills := mustPills(t, `msg~ok .status != "ok"`, nil)
	if len(pills) != 2 || pills[0].op != pillOpContains || pills[1].op != pillOpYq {
		t.Fatalf("got %+v, want a contains pill and one yq pill", pills)
	}
	// The remainder failing yq falls back to one token per pill.
	pills = mustPills(t, `.level=="error" -healthz`, nil)
	if len(pills) != 2 || pills[0].op != pillOpYq || pills[1].op != pillOpText || !pills[1].neg {
		t.Fatalf("got %+v, want a yq pill and a negated text pill", pills)
	}
}

// An unclosed /regex is a parse error; \/ is a literal slash.
func TestRegexSlashRules(t *testing.T) {
	for _, input := range []string{`msg~/unterminated`, `msg=~/abc`} {
		if _, err := parsePills(input, nil); err == nil || !strings.Contains(err.Error(), "unterminated") {
			t.Errorf("%q: err = %v, want an unterminated regex error", input, err)
		}
	}
	pills := mustPills(t, `msg~/a\/b/`, nil)
	if pills[0].op != pillOpRegex {
		t.Fatalf(`msg~/a\/b/: op = %v, want regex`, pills[0].op)
	}
	if !pills[0].match(`{"msg":"a/b"}`) {
		t.Error(`msg~/a\/b/ should match a/b`)
	}
	if pills[0].match(`{"msg":"a"}`) {
		t.Error(`msg~/a\/b/ should not match just a: the escaped slash must not close the regex`)
	}
}

// A negated field or yq pill never keeps a non-JSON line.
func TestNegatedJSONPillsNeverMatchNonJSON(t *testing.T) {
	tests := []struct {
		input string
		line  string
		want  bool
	}{
		{"-level=error", "plain line", false},
		{`-.level=="error"`, "plain line", false},
		{"-level=error", `{"level":"info"}`, true},
		{"-level=error", `{"level":"error"}`, false},
		{`-.level=="error"`, `{"level":"info"}`, true},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if got := pills[0].match(tt.line); got != tt.want {
			t.Errorf("%q on %q = %v, want %v", tt.input, tt.line, got, tt.want)
		}
	}
}

// Case-insensitive spans index the original bytes, never a lowered copy whose
// runes shrink (İ lowercases to i, the Kelvin sign to k).
func TestFoldSpansOnOriginalBytes(t *testing.T) {
	pills := mustPills(t, "timeout", nil)
	if got, want := pills[0].spans("İİ timeout"), [][2]int{{5, 12}}; !reflect.DeepEqual(got, want) {
		t.Errorf("spans(İİ timeout) = %v, want %v", got, want)
	}
	pills = mustPills(t, "k", nil)
	if got, want := pills[0].spans("\u212a x"), [][2]int{{0, 3}}; !reflect.DeepEqual(got, want) {
		t.Errorf("spans(Kelvin x) = %v, want %v", got, want)
	}
}

// Field pills highlight the field's value occurrence in the raw line, never a
// lookalike substring elsewhere.
func TestFieldSpansHighlightOnlyTheFieldValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		line  string
		want  [][2]int
	}{
		{"eq miss highlights nothing", "level=error", `{"level":"info","msg":"error"}`, nil},
		{"anchored regex highlights", "msg=~^refused", `{"msg":"refused by peer"}`, [][2]int{{8, 15}}},
		{"contains stays in its field", "msg~refused", `{"msg":"refused","other":"refused"}`, [][2]int{{8, 15}}},
		{"numeric eq highlights the number", "http.status=200", `{"http":{"status":200}}`, [][2]int{{18, 21}}},
	}
	for _, tt := range tests {
		pills := mustPills(t, tt.input, nil)
		if got := pills[0].spans(tt.line); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: spans(%q) = %v, want %v", tt.name, tt.line, got, tt.want)
		}
	}
}

// A compiled set decodes a JSON line once and ANDs every pill over it.
func TestPillSetMatch(t *testing.T) {
	s, err := compilePills(`level=error http.status>=400 msg~timeout`, nil)
	if err != nil {
		t.Fatalf("compilePills: %v", err)
	}
	keep := `{"level":"error","http":{"status":500},"msg":"a timeout"}`
	if !s.match(keep) {
		t.Errorf("set should keep %q", keep)
	}
	for _, line := range []string{
		`{"level":"info","http":{"status":500},"msg":"a timeout"}`,
		`{"level":"error","http":{"status":200},"msg":"a timeout"}`,
		`{"level":"error","http":{"status":500},"msg":"all good"}`,
		"plain line",
	} {
		if s.match(line) {
			t.Errorf("set should drop %q", line)
		}
	}
}

func BenchmarkPillSetMatch(b *testing.B) {
	s, err := compilePills(`level=error http.status>=400 msg~timeout`, nil)
	if err != nil {
		b.Fatalf("compilePills: %v", err)
	}
	line := `{"level":"error","http":{"status":500},"msg":"a timeout"}`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.match(line)
	}
}

func TestPillSavedParsesOnly(t *testing.T) {
	pills := mustPills(t, "@4xx", nil)
	if pills[0].saved != "4xx" {
		t.Fatalf("saved = %q", pills[0].saved)
	}
	// Unresolved saved pills filter nothing until step 3 resolves them.
	if !pills[0].match("anything") {
		t.Error("unresolved saved pill should not filter")
	}
}
