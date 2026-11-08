package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestScopeSwitchReusesDecodedCells(t *testing.T) {
	m := newTestModel(t, tableTarget("web"), tableTarget("api"))
	addLines(m, "web", jsonGet, jsonGet, jsonGet)
	addLines(m, "api", jsonPost, jsonPost)
	m.frame()
	before := m.cellEvals

	press(m, "j")
	m.frame()
	press(m, "k")
	m.frame()
	if got := m.cellEvals; got != before {
		t.Fatalf("cellEvals = %d after two scope switches, want %d", got, before)
	}

	addLines(m, "web", jsonGet)
	m.frame()
	if got := m.cellEvals; got != before+1 {
		t.Fatalf("cellEvals = %d after one new line, want %d", got, before+1)
	}
}

func TestRunRuleSurvivesFiltersAndSearch(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{{kind: evStarted, target: "web", at: time.Now()}})
	paneOut(m, "web", "listening")
	hasRule := func() bool {
		for _, l := range rawPane(m) {
			if strings.Contains(l, "▶ web started") {
				return true
			}
		}
		return false
	}

	press(m, "/")
	typeString(m, "zzz")
	press(m, "enter")
	if !hasRule() {
		t.Fatal("a text filter hid the run rule")
	}

	press(m, "esc")
	press(m, "/")
	typeString(m, ".level == \"x\"")
	press(m, "enter")
	if !hasRule() {
		t.Fatal("a yq filter hid the run rule")
	}

	press(m, "v", "backspace", "backspace", "esc")
	press(m, "ctrl+f")
	typeString(m, "started")
	press(m, "enter")
	if len(m.searchMatches) != 0 {
		t.Fatalf("search matched the run rule: %v", m.searchMatches)
	}
	press(m, "ctrl+f")
	for range "started" {
		press(m, "backspace")
	}
	typeString(m, "listen")
	press(m, "enter")
	if len(m.searchMatches) != 1 {
		t.Fatalf("search matches = %d for output, want 1", len(m.searchMatches))
	}
}

func TestUnsampledRowsFeedTheSampleAfterAScopeSwitch(t *testing.T) {
	m := newTestModel(t, tableTarget("api"), tableTarget("web"))
	long := fmt.Sprintf(`{"request":{"method":%q},"response":{"status":200}}`, strings.Repeat("x", 15))
	addLines(m, "api", long)
	addLines(m, "web", jsonGet)
	m.cursor = 1
	m.applyScope(1, "")
	m.frame()
	if got, want := m.tableWidths()[0], displayWidth("method")+columnMarkW; got != want {
		t.Fatalf("width = %d on web alone, want %d", got, want)
	}

	press(m, "k")
	m.frame()
	if got := m.tableWidths()[0]; got != 15 {
		t.Fatalf("width = %d after switching to api, want 15 from its unsampled row", got)
	}
}

func TestFrozenWidthHoldsAcrossAScopeSwitch(t *testing.T) {
	m := newTestModel(t, tableTarget("api"), tableTarget("web"))
	short := make([]string, sampleValues)
	for i := range short {
		short[i] = `{"request":{"method":"ab"},"response":{"status":200}}`
	}
	addLines(m, "api", short...)
	addLines(m, "web", fmt.Sprintf(`{"request":{"method":%q},"response":{"status":200}}`, strings.Repeat("y", 40)))
	m.cursor = 1
	m.applyScope(1, "")
	m.frame()
	press(m, "k")
	m.frame()
	frozen := m.tableWidths()[0]

	addLines(m, "api", fmt.Sprintf(`{"request":{"method":%q},"response":{"status":200}}`, strings.Repeat("z", 30)))
	m.frame()
	if got := m.tableWidths()[0]; got != frozen {
		t.Fatalf("width moved from %d to %d after the sample froze", frozen, got)
	}
}

func TestRowLeavesRoomForTheMarkCell(t *testing.T) {
	m := newTestModel(t, namedColumns("web", "msg", "status"))
	m.frame()
	last := m.tableWidths()[1]
	addLines(m, "web", fmt.Sprintf(`{"msg":"a","status":%q}`, strings.Repeat("x", last)))
	m.frame()

	row := strings.TrimRight(ansi.Strip(paneOf(m)[m.titleHeight()]), " ")
	if !strings.Contains(row, strings.Repeat("x", last)) || strings.Contains(row, "…") || !strings.HasSuffix(row, "(+)") {
		t.Fatalf("row = %q, want the %d-cell last value whole and the (+) at the edge", row, last)
	}
}

func TestExitRuleOmitsDurationWithoutAStart(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvents([]event{{kind: evExited, target: "web", code: 1}})
	if got := m.visibleRaw[len(m.visibleRaw)-1]; got != "■ web exit 1" {
		t.Fatalf("exit rule = %q, want no duration", got)
	}
}

func TestExitRuleNamesSignalsAndErrors(t *testing.T) {
	sigErr := exec.Command("sh", "-c", "kill -TERM $$").Run()
	cases := []struct {
		name string
		ev   event
		want string
		tone sepTone
	}{
		{"signal", event{code: -1, err: sigErr}, "■ web killed by term", toneFail},
		{"error", event{code: -1, err: errors.New("boom")}, "■ web failed: boom", toneFail},
		{"zero", event{code: 0}, "✓ web exit 0", toneOK},
		{"code", event{code: 3}, "■ web exit 3", toneFail},
	}
	for _, c := range cases {
		got := exitRule(&tuiTarget{spec: target("web")}, c.ev, time.Now())
		if got.label != c.want || got.tone != c.tone {
			t.Errorf("%s: rule = %q tone %v, want %q tone %v", c.name, got.label, got.tone, c.want, c.tone)
		}
	}
}

func TestRuleTruncatesWithAnEllipsis(t *testing.T) {
	out := ansi.Strip(renderSep(sepLine{label: strings.Repeat("long ", 20)}, 20, true))
	if displayWidth(out) != 20 || !strings.Contains(out, "…") {
		t.Fatalf("rule = %q, want 20 cells ending in an ellipsis", out)
	}
}

func TestClockValueAcceptsMoreTimestampForms(t *testing.T) {
	local := time.Date(2026, 11, 4, 12, 1, 2, 0, time.Local).Format(clockLayout)
	epoch := time.Unix(1730700000, 123000000).Local().Format(clockLayout)
	cases := map[string]string{
		"2026-11-04 12:01:02":       local,
		"2026-11-04 12:01:02.250":   local,
		"1730700000.123":            epoch,
		"not a time":                "not a time",
		"1e9":                       "1e9",
		"2026-11-04T12:01:02+00:00": time.Date(2026, 11, 4, 12, 1, 2, 0, time.UTC).In(time.Local).Format(clockLayout),
	}
	for in, want := range cases {
		if got := clockValue(in); got != want {
			t.Errorf("clockValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExitRuleTruncatesLongErrors(t *testing.T) {
	got := exitRule(&tuiTarget{spec: target("web")}, event{code: -1, err: errors.New(strings.Repeat("x", 100))}, time.Now())
	if !strings.HasPrefix(got.label, "■ web failed: ") || !strings.Contains(got.label, "…") || displayWidth(got.label) > 60 {
		t.Fatalf("rule = %q, want a failed rule with the error cut to an ellipsis", got.label)
	}
}
