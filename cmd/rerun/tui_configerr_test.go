package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func brokenConfigErr(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := os.WriteFile(path, []byte("targets:\n  web:\n    run: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseConfig(path)
	if err == nil {
		t.Fatal("ParseConfig accepted a broken file")
	}
	return err
}

func configErrEvent(err error) event {
	return event{kind: evConfigErr, line: configErrorLine(err), err: err}
}

func TestBrokenReloadOpensTheModalWithTheParserText(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	err := brokenConfigErr(t)
	m.applyEvent(configErrEvent(err))
	view := ansi.Strip(m.View().Content)

	if !strings.Contains(view, "rerun.yml not reloaded") || !strings.Contains(view, "old config still running · esc close") {
		t.Fatalf("modal missing its title or footer:\n%s", view)
	}
	if !strings.Contains(view, ConfigFileName) {
		t.Fatalf("modal does not name the file:\n%s", view)
	}
	for _, word := range strings.Fields(err.Error()) {
		// A path wider than the box is broken by the wrap, so only words that fit are checked.
		if len(word) > 60 {
			continue
		}
		if !strings.Contains(view, word) {
			t.Fatalf("modal does not show %q from the parser text:\n%s", word, view)
		}
	}
}

func TestBrokenReloadLeavesTheTargetsRunning(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	before := make([]string, len(m.targets))
	for i, tg := range m.targets {
		before[i] = tg.spec.Name
	}

	m.applyEvent(configErrEvent(brokenConfigErr(t)))

	if len(m.targets) != len(before) {
		t.Fatalf("targets = %d after a broken reload, want %d", len(m.targets), len(before))
	}
	for i, tg := range m.targets {
		if tg.spec.Name != before[i] {
			t.Fatalf("target %d = %q after a broken reload, want %q", i, tg.spec.Name, before[i])
		}
	}
}

func TestModalSwallowsPasteToTheFinderAndTheBar(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.Update(ctrlK)
	if !m.finder.open {
		t.Fatal("ctrl+k did not open the finder, so the test cannot tell")
	}
	m.applyEvent(configErrEvent(brokenConfigErr(t)))
	m.Update(tea.PasteMsg{Content: "api"})
	if m.finder.query != "" {
		t.Fatalf("finder query = %q after a paste under the modal, want empty", m.finder.query)
	}

	bar := newTestModel(t, target("web"), target("api"))
	press(bar, "/")
	if bar.inputMode == inputNone {
		t.Fatal("/ did not open the filter bar, so the test cannot tell")
	}
	bar.applyEvent(configErrEvent(brokenConfigErr(t)))
	bar.Update(tea.PasteMsg{Content: "api"})
	if bar.input != "" {
		t.Fatalf("bar input = %q after a paste under the modal, want empty", bar.input)
	}
}

func TestEscClosesTheModalAndKeepsTheBottomNotice(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(configErrEvent(brokenConfigErr(t)))

	press(m, "esc")
	if m.cfgErr != "" {
		t.Fatal("esc did not close the modal")
	}
	if got := bottomPaneLine(m); !strings.Contains(got, "config reload failed") {
		t.Fatalf("bottom line = %q, want the reload notice after closing", got)
	}
}

func TestEnterClosesTheModal(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(configErrEvent(brokenConfigErr(t)))

	press(m, "enter")
	if m.cfgErr != "" {
		t.Fatal("enter did not close the modal")
	}
}

func TestOtherKeysAreSwallowedWhileTheModalIsOpen(t *testing.T) {
	m := newTestModel(t, target("web"), target("api"))
	m.applyEvent(configErrEvent(brokenConfigErr(t)))

	press(m, "j", "/", "s")
	if m.cfgErr == "" {
		t.Fatal("a key closed the modal")
	}
	if m.cursor != 0 || m.solo != -1 || m.inputMode != inputNone {
		t.Fatalf("keys reached the list under the modal: cursor %d solo %d input %d", m.cursor, m.solo, m.inputMode)
	}
}

func TestFixedReloadClosesTheModal(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(configErrEvent(brokenConfigErr(t)))

	m.applyEvent(event{kind: evReconcile, line: "── config reloaded ── ~web(cmd) ──"})
	if m.cfgErr != "" {
		t.Fatal("a successful reload left the modal open")
	}
	if strings.Contains(ansi.Strip(m.View().Content), "not reloaded") {
		t.Fatal("the modal is still drawn after a successful reload")
	}
}

func TestNewerErrorReplacesTheModalText(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.applyEvent(configErrEvent(brokenConfigErr(t)))
	second := &configErr{path: "rerun.yml", msg: "second problem"}
	m.applyEvent(configErrEvent(second))

	if m.cfgErr != second.Error() {
		t.Fatalf("modal text = %q, want the newer error", m.cfgErr)
	}
}

func TestLongErrorWrapsInsteadOfTruncating(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 40
	long := &configErr{path: "rerun.yml", msg: strings.Repeat("alpha ", 30) + "TAILMARK"}
	m.applyEvent(configErrEvent(long))

	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "TAILMARK") || strings.Contains(view, "…") {
		t.Fatalf("long error was cut instead of wrapped:\n%s", view)
	}
}

func TestModalHeightIsCappedOnASmallScreen(t *testing.T) {
	m := newTestModel(t, target("web"))
	m.width, m.height = 100, 12
	long := &configErr{path: "rerun.yml", msg: strings.Repeat("beta ", 200)}
	m.applyEvent(configErrEvent(long))

	box := m.configErrView()
	if got := strings.Count(box, "\n") + 1; got > m.height-2 {
		t.Fatalf("modal is %d lines on a %d-row screen", got, m.height)
	}
	if !strings.Contains(ansi.Strip(box), "…") {
		t.Fatalf("capped modal does not end in an ellipsis:\n%s", ansi.Strip(box))
	}
}
