package main

import (
	"testing"
)

// stubOpen records what `o` hands the system opener for the test's duration.
func stubOpen(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	old := openURL
	openURL = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { openURL = old })
	return &opened
}

// A target with no link to open says so, and names the key that fixes it.
func TestOpenWithNothingToOpenSaysWhy(t *testing.T) {
	opened := stubOpen(t)
	m := newTestModel(t, target("api"))
	m.addLine(0, "fetched https://example.com/docs")
	m.cursor = 0
	press(m, "ctrl+o")

	if len(*opened) != 0 {
		t.Fatalf("opened = %q, want nothing", *opened)
	}
	if want := "api has nothing to open · add open: in rerun.yml"; m.notice != want {
		t.Fatalf("notice = %q, want %q", m.notice, want)
	}
}

// o on a group header says what it opens instead of doing nothing.
func TestOpenOnAGroupHeaderSaysWhatItOpens(t *testing.T) {
	stubOpen(t)
	m := newTestModel(t, schemaTarget("api", "backend"), schemaTarget("web", "backend"))
	m.frame()
	m.cursor = 0 // the backend header
	press(m, "o")

	if want := "no links to open · add open: in rerun.yml"; m.notice != want {
		t.Fatalf("notice = %q, want %q", m.notice, want)
	}
}

// The first ? view lists o for links and i for the pane's original lines.
func TestHelpShortListNamesOAndI(t *testing.T) {
	want := [2]string{"o i", "pick a link · pane i: original lines"}
	for _, kv := range helpShortKeys {
		if kv == want {
			return
		}
	}
	t.Fatalf("helpShortKeys has no %q row", want)
}
