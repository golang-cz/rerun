package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// yamlRows is a JSON line's expansion as plain text, one string per row.
func yamlRows(t *testing.T, line string) []string {
	t.Helper()
	rows, ok := jsonBlock(line, 0)
	if !ok {
		t.Fatalf("jsonBlock(%q) refused the line", line)
	}
	var out []string
	for _, r := range rows {
		out = append(out, ansi.Strip(r.text))
	}
	return out
}

func TestExpandedKeysKeepTheirDocumentOrder(t *testing.T) {
	got := yamlRows(t, `{"zeta":1,"alpha":2,"mid":3}`)
	if want := []string{"zeta: 1", "alpha: 2", "mid: 3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestYAMLQuotesOnlyWhatYAMLNeeds(t *testing.T) {
	cases := map[string]string{
		"":                `""`,
		" lead":           `" lead"`,
		"trail ":          `"trail "`,
		"true":            `"true"`,
		"Null":            `"Null"`,
		"123":             `"123"`,
		"1e9":             `"1e9"`,
		"0x1F":            `"0x1F"`,
		"-x":              `"-x"`,
		"a: b":            `"a: b"`,
		"a #b":            `"a #b"`,
		"ends:":           `"ends:"`,
		"tab\there":       `"tab\there"`,
		"plain text":      "plain text",
		"https://x/y?q=1": "https://x/y?q=1",
		"it's fine":       "it's fine",
	}
	for in, want := range cases {
		if got := yamlScalar(in); got != want {
			t.Errorf("yamlScalar(%q) = %s, want %s", in, got, want)
		}
	}
	if got := yamlKeyText("logging.googleapis.com/sourceLocation"); got != "logging.googleapis.com/sourceLocation" {
		t.Errorf("key = %s, want it unquoted", got)
	}
	if got := yamlKeyText("two words"); got != "two words" {
		t.Errorf("key = %s, want it unquoted", got)
	}
}

func TestMultiLineStringsUseABlock(t *testing.T) {
	got := yamlRows(t, `{"msg":"first\nsecond\n","x":1}`)
	if want := []string{"msg: |", "  first", "  second", "x: 1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	got = yamlRows(t, `{"msg":"first\nsecond"}`)
	if want := []string{"msg: |-", "  first", "  second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no trailing newline: rows = %q, want %q", got, want)
	}
}

func TestNestedArrayOfMapsIndentsUnderItsDash(t *testing.T) {
	got := yamlRows(t, `{"items":[{"id":1,"tags":["x","y"]},{"id":2}],"none":[],"empty":{}}`)
	want := []string{
		"items:",
		"  - id: 1",
		"    tags:",
		"      - x",
		`      - "y"`,
		"  - id: 2",
		"none: []",
		"empty: {}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A click on a nested key picks the same column expression the JSON layout did.
func TestNestedKeyClickKeepsItsPath(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.frame()
	m.liveSelect()
	m.toggleExpand(0)

	x, y := findInPane(t, m, "method: ")
	m.handleMouse(mouse(x+1, y, false))

	if len(m.edits) != 1 || m.edits[0].cols[0].Expr != ".request.method" {
		t.Fatalf("edits = %+v, want the column .request.method", m.edits)
	}
}

// Copying a selection that covers an expansion yields the raw JSON line, not the YAML.
func TestCopyKeepsTheRawJSON(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	m.toggleExpand(0)

	got := m.copyLines(0, m.paneLen()-1)
	if want := []string{jsonReq}; !reflect.DeepEqual(got, want) {
		t.Fatalf("copy = %q, want the raw line %q", got, want)
	}
}

// Expanded keys carry no underline, so the spans they click on are not styled.
func TestExpandedRowsHaveNoKeyUnderline(t *testing.T) {
	rows, _ := jsonBlock(jsonReq, 0)
	for _, r := range rows {
		if strings.Contains(r.text, "\x1b[4m") {
			t.Fatalf("row %q is underlined", r.text)
		}
	}
}

// pinTime freezes the clock and zone so a rendered timestamp is stable.
func pinTime(t *testing.T, now time.Time) {
	t.Helper()
	prevNow, prevLoc := yamlNow, yamlLocation
	yamlNow = func() time.Time { return now }
	yamlLocation = time.UTC
	t.Cleanup(func() { yamlNow, yamlLocation = prevNow, prevLoc })
}

func TestYamlTimestampShowsLocalClockForToday(t *testing.T) {
	pinTime(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	got := yamlRows(t, `{"ts":"2026-11-04T15:04:05.123456789Z"}`)
	if want := []string{"ts: 15:04:05.123"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestYamlTimestampShowsDateWhenNotToday(t *testing.T) {
	pinTime(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	got := yamlRows(t, `{"@timestamp":"2026-11-03T15:04:05.123Z","time":"2026-11-05T01:02:03Z"}`)
	want := []string{`"@timestamp": 2026-11-03 15:04:05.123`, "time: 2026-11-05 01:02:03.000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestYamlTimestampEpochUnits(t *testing.T) {
	at := time.Date(2026, 11, 3, 15, 4, 5, 0, time.UTC)
	pinTime(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	cases := []struct {
		line string
		want string
	}{
		{fmt.Sprintf(`{"ts":%d}`, at.Unix()), "ts: 2026-11-03 15:04:05.000"},
		{fmt.Sprintf(`{"ts":%d}`, at.UnixMilli()), "ts: 2026-11-03 15:04:05.000"},
		{fmt.Sprintf(`{"ts":%d}`, at.UnixMicro()), "ts: 2026-11-03 15:04:05.000"},
		{fmt.Sprintf(`{"ts":%d}`, at.UnixNano()), "ts: 2026-11-03 15:04:05.000"},
	}
	for _, c := range cases {
		if got := yamlRows(t, c.line); !reflect.DeepEqual(got, []string{c.want}) {
			t.Errorf("yamlRows(%s) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestYamlTimestampFormatsNestedKeys(t *testing.T) {
	pinTime(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	got := yamlRows(t, `{"request":{"time":"2026-11-04T01:02:03Z"},"items":[{"timestamp":"2026-11-04T01:02:03Z"}]}`)
	want := []string{"request:", "  time: 01:02:03.000", "items:", "  - timestamp: 01:02:03.000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestYamlTimestampLeavesGarbageAlone(t *testing.T) {
	pinTime(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	got := yamlRows(t, `{"ts":"not a time","time":"2026-13-45T99:99:99Z","timestamp":{"x":1},"@timestamp":null}`)
	want := []string{`ts: not a time`, `time: "2026-13-45T99:99:99Z"`, "timestamp:", "  x: 1", `"@timestamp": null`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}
