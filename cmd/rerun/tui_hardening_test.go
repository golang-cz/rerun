package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"
)

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func keyCmd(m *tuiModel, k string) tea.Cmd {
	_, cmd := m.Update(keyMsg(k))
	return cmd
}

// Controls from the payloads never reach the screen as raw bytes.
func TestControlPayloadsDoNotReachTheTerminal(t *testing.T) {
	payloads := []string{"\x1b]52;c;Zm9v\x07", "\x1b[2J", "\u009b2J", "bell\x07"}
	for _, p := range payloads {
		m := newTestModel(t, target("web"))
		m.applyEvent(configErrEvent(&configErr{path: "rerun.yml", msg: "bad " + p}))
		m.applyEvent(event{kind: evExited, target: "web", code: -1, err: errors.New("boom " + p)})
		view := m.View().Content
		for _, raw := range []string{"\x1b]52", "\x1b[2J", "\u009b2J", "\x07"} {
			if strings.Contains(view, raw) {
				t.Fatalf("payload %q reached the view as raw %q", p, raw)
			}
		}
		if plain := ansi.Strip(view); strings.ContainsAny(strings.ReplaceAll(plain, "\n", ""), "\x07\x9b") {
			t.Fatalf("payload %q left a control rune on screen", p)
		}
	}
}

// An expanded value with controls is quoted, so no block body carries them.
func TestExpandedControlsAreQuoted(t *testing.T) {
	for _, s := range []string{"a\n\x1b[2Jb", "x\n\u009b2J", "tail\x07\n"} {
		line, _ := json.Marshal(map[string]string{"msg": s})
		rows, ok := jsonBlock(string(line), 0)
		if !ok {
			t.Fatalf("jsonBlock refused %q", line)
		}
		for _, r := range rows {
			if strings.ContainsAny(r.text, "\x07\u009b") || strings.Contains(r.text, "\x1b[2J") {
				t.Fatalf("row %q carries a raw control", r.text)
			}
		}
	}
}

func TestControlsFallBackToOneQuotedLine(t *testing.T) {
	rows, _ := jsonBlock(`{"msg":"a\n\u0007b"}`, 0)
	if got := ansi.Strip(rows[0].text); got != `msg: "a\n\u0007b"` {
		t.Fatalf("row = %q, want the controls quoted on one line", got)
	}
}

func TestQuitKeyAfterTheModalOpensTheConfirm(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(configErrEvent(&configErr{path: "rerun.yml", msg: "bad"}))

	keyCmd(m, "q")
	if !m.confirmQuit || m.cfgErr != "" {
		t.Fatalf("confirm = %v modal = %q, want the confirm open and the modal closed", m.confirmQuit, m.cfgErr)
	}
	if !isQuit(keyCmd(m, "enter")) {
		t.Fatal("enter after q did not quit")
	}
}

func TestEnterQuitsWhenTheConfirmOpenedFirst(t *testing.T) {
	m := newTestModel(t, target("web"))
	keyCmd(m, "q")
	m.applyEvent(configErrEvent(&configErr{path: "rerun.yml", msg: "bad"}))

	if !isQuit(keyCmd(m, "enter")) {
		t.Fatal("enter closed the error modal instead of quitting")
	}
}

func TestQWhileABarIsOpenDoesNotTypeOrClose(t *testing.T) {
	m := newTestModel(t, target("web"))
	press(m, "/")
	m.applyEvent(configErrEvent(&configErr{path: "rerun.yml", msg: "bad"}))

	keyCmd(m, "q")
	if m.input != "" || m.cfgErr == "" {
		t.Fatalf("input = %q modal = %q, want nothing typed and the modal still open", m.input, m.cfgErr)
	}
}

func TestBlockCapKeepsTheHintOnASmallScreen(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 12
	m.applyEvent(configErrEvent(&configErr{path: "rerun.yml", msg: strings.Repeat("beta ", 200)}))
	box := ansi.Strip(m.configErrView())
	if !strings.Contains(box, "esc close") || !strings.Contains(box, "…") {
		t.Fatalf("capped modal lost its hint or ellipsis:\n%s", box)
	}
}

func TestCellCacheStaysBoundedWhileStreaming(t *testing.T) {
	m := newTestModel(t, target("web"))
	for i := 0; i < 2*lineCap+10; i++ {
		m.cellBySeq[-i-1] = &cellEntry{}
	}
	addLines(m, "web", "x")
	if got := len(m.cellBySeq); got > 2*lineCap {
		t.Fatalf("cell cache holds %d entries, want at most %d", got, 2*lineCap)
	}
}

func TestEmptyObjectIsOneRow(t *testing.T) {
	rows, ok := jsonBlock("{}", 0)
	if !ok || len(rows) != 1 || ansi.Strip(rows[0].text) != "{}" {
		t.Fatalf("rows = %+v, want one {} row", rows)
	}
}

func TestLongExpansionIsCappedWithACount(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 600; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%d":1`, i)
	}
	b.WriteString("}")
	rows, _ := jsonBlock(b.String(), 0)
	if len(rows) != maxBlockRows+1 {
		t.Fatalf("rows = %d, want %d plus the count", len(rows), maxBlockRows)
	}
	if last := ansi.Strip(rows[len(rows)-1].text); !strings.Contains(last, "100 more lines") {
		t.Fatalf("last row = %q, want the hidden count", last)
	}
}

func TestExpansionRowsAreCachedPerLine(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", jsonReq)
	first := m.expansionRows(0)
	second := m.expansionRows(0)
	if len(first) == 0 || &first[0] != &second[0] {
		t.Fatal("the second expansion re-rendered the line")
	}
}

func TestClockValueAcceptsZonelessT(t *testing.T) {
	want := "12:01:02"
	if got := clockValue("2026-11-04T12:01:02.250"); got != want {
		t.Fatalf("clockValue = %q, want %q", got, want)
	}
}

// The expansion must read back, through a real YAML parser, to the value it came from.
func TestExpandedYAMLRoundTrips(t *testing.T) {
	cases := []string{
		"", " lead", "trail ", "true", "null", "~", "123", "1_000", "0b101", "0x1F", "1e5", "+1",
		"2026-11-04", "2026-11-04T12:00:00Z", "a: b", "a #b", "-x", "- x", "[x]", "{y}", "*star",
		"&anc", "!tag", "%pct", "@at", "`bt", "'q", `"dq`, `back\slash`, "tab\there", "cr\rhere",
		"esc\x1b[2J", "bel\x07", "csi\u009b2J", "nel\u0085x", "ls x", "ps x", "bom\ufeffx",
		"multi\nline", "lead\n  two", "  lead space\nline", "trail\n", "two\n\n", "\n", "\n\n",
		"x\n\n\ny", "émoji 🙂", "nul\x00x", ".inf", ".NaN", "y", "no", "a b",
		"targets:\n  key: value\n  next: 2", "#comment", "? question", "| pipe", "> fold",
	}
	for _, s := range cases {
		line, _ := json.Marshal(map[string]string{"v": s})
		rows, ok := jsonBlock(string(line), 0)
		if !ok {
			t.Fatalf("jsonBlock refused %q", line)
		}
		var doc strings.Builder
		for _, r := range rows {
			doc.WriteString(ansi.Strip(r.text))
			doc.WriteString("\n")
		}
		var got map[string]any
		if err := yaml.Unmarshal([]byte(doc.String()), &got); err != nil {
			t.Errorf("%q: yaml rejects the expansion: %v\n%s", s, err, doc.String())
			continue
		}
		if !reflect.DeepEqual(got["v"], s) {
			t.Errorf("%q reads back as %#v\n%s", s, got["v"], doc.String())
		}
	}
}
