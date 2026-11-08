package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseColumnTemplates(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    Column
		wantErr string
	}{
		{name: "single placeholder", raw: "level {.level}", want: Column{Name: "level", Expr: ".level"}},
		{name: "two placeholders", raw: "msg {.message} {.error}", want: Column{Name: "msg", Template: "{.message} {.error}"}},
		{name: "literal text around placeholders", raw: "req {.a} of {.b}!", want: Column{Name: "req", Template: "{.a} of {.b}!"}},
		{name: "nested braces in one expression", raw: `obj {{"a": .b}}`, want: Column{Name: "obj", Expr: `{"a": .b}`}},
		{name: "nested braces then a placeholder", raw: `x {{"a": .b} | .a} {.c}`, want: Column{Name: "x", Template: `{{"a": .b} | .a} {.c}`}},
		{name: "brace inside a quoted string", raw: `m {.a | select(. == "}")} {.b}`, want: Column{Name: "m", Template: `{.a | select(. == "}")} {.b}`}},
		{name: "unbalanced nested brace", raw: "msg {.message {.error}", wantErr: "missing a closing }"},
		{name: "unclosed placeholder", raw: "msg {.message", wantErr: "missing a closing }"},
		{name: "empty placeholder in a template", raw: "msg {.a} {}", wantErr: "empty column expression"},
		{name: "escaped quote in a string", raw: `m {.a == "a\"}"} {.b}`, want: Column{Name: "m", Template: `{.a == "a\"}"} {.b}`}},
		{name: "single-quoted brace", raw: `m {.a == 'x}'} {.b}`, want: Column{Name: "m", Template: `{.a == 'x}'} {.b}`}},
		{name: "literal text after one placeholder", raw: "x {.a}!", want: Column{Name: "x", Template: "{.a}!"}},
		{name: "stray closing brace in literal text", raw: "msg {.a} } {.b}", wantErr: "unexpected }"},
		{name: "no name", raw: "{.a} {.b}", wantErr: "empty column name"},
		{name: "no placeholder", raw: "msg", wantErr: "expected a name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseColumn(c.raw)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("parseColumn(%q) error = %v, want %q", c.raw, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseColumn(%q): %v", c.raw, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseColumn(%q) = %+v, want %+v", c.raw, got, c.want)
			}
		})
	}
}

func TestTemplateColumnValues(t *testing.T) {
	cases := []struct {
		name string
		col  string
		line string
		want string
	}{
		{"both fields", "msg {.message} {.error}", `{"message":"disk full","error":"ENOSPC"}`, "disk full ENOSPC"},
		{"message only", "msg {.message} {.error}", `{"message":"disk full"}`, "disk full"},
		{"error only", "msg {.message} {.error}", `{"error":"ENOSPC"}`, "ENOSPC"},
		{"null placeholder", "msg {.message} {.error}", `{"message":null,"error":"ENOSPC"}`, "ENOSPC"},
		{"no placeholder value", "msg {.message} {.error}", `{}`, ""},
		{"literal text kept", "req {.a} of {.b}!", `{"a":1,"b":2}`, "1 of 2!"},
		{"brace inside quoted expression", `msg {.message + " {ok}"} {.error}`, `{"message":"hi","error":"x"}`, "hi {ok} x"},
		{"nested object expression", "obj {{\"v\": .a.b} | .v} done", `{"a":{"b":1}}`, "1 done"},
		{"single placeholder unchanged", "level {.level}", `{"level":"info"}`, "info"},
		{"all placeholders empty drops literal", "latency {.ms}ms", `{"x":1}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col, err := parseColumn(c.col)
			if err != nil {
				t.Fatalf("parseColumn(%q): %v", c.col, err)
			}
			prog, err := compileColumn(col)
			if err != nil {
				t.Fatalf("compileColumn(%q): %v", c.col, err)
			}
			vals, ok := yqObjectValues([]columnProgram{prog}, c.line)
			if !ok {
				t.Fatalf("line %s is not a JSON object", c.line)
			}
			if vals[0] != c.want {
				t.Errorf("value = %q, want %q", vals[0], c.want)
			}
		})
	}
}

func TestBadTemplateExpressionShowsRawLine(t *testing.T) {
	tgt := target("web")
	tgt.Columns = []Column{{Name: "msg", Template: "{.message} {.a |||}"}}
	m := newTestModel(t, tgt)
	addLines(m, "web", `{"message":"hi"}`)
	m.frame()

	if rows := tableRows(m); len(rows) == 0 || !strings.Contains(rows[0], `{"message":"hi"}`) {
		t.Errorf("rows = %q, want the raw line when a column does not compile", rows)
	}
}
