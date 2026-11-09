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

// With no url or open value, o opens the newest localhost link the target printed.
func TestOpenFallsBackToTheNewestLocalhostLink(t *testing.T) {
	opened := stubOpen(t)
	m := newTestModel(t, target("api"))
	m.addLine(0, "server on http://localhost:8080")
	m.addLine(0, "\x1b[32mvite ready at http://127.0.0.1:5173/\x1b[0m")
	m.cursor = 0
	press(m, "o")

	if len(*opened) != 1 || (*opened)[0] != "http://127.0.0.1:5173/" {
		t.Fatalf("opened = %q, want the newest link without its ANSI codes", *opened)
	}
}

// A 0.0.0.0 bind opens as localhost, and trailing punctuation stays out of the link.
func TestOpenMapsZeroAddressToLocalhost(t *testing.T) {
	opened := stubOpen(t)
	m := newTestModel(t, target("api"))
	m.addLine(0, "listening (http://0.0.0.0:4000/api).")
	m.cursor = 0
	press(m, "o")

	if len(*opened) != 1 || (*opened)[0] != "http://localhost:4000/api" {
		t.Fatalf("opened = %q, want http://localhost:4000/api", *opened)
	}
}

// A target with no link to open says so, and names the key that fixes it.
func TestOpenWithNothingToOpenSaysWhy(t *testing.T) {
	opened := stubOpen(t)
	m := newTestModel(t, target("api"))
	m.addLine(0, "fetched https://example.com/docs")
	m.cursor = 0
	press(m, "o")

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

	if want := "o opens a target's open: link"; m.notice != want {
		t.Fatalf("notice = %q, want %q", m.notice, want)
	}
}

// The first ? view lists o with the pane's original-lines meaning.
func TestHelpShortListNamesO(t *testing.T) {
	want := [2]string{"o", "open the target's open: · pane: original lines"}
	for _, kv := range helpShortKeys {
		if kv == want {
			return
		}
	}
	t.Fatalf("helpShortKeys has no %q row", want)
}
