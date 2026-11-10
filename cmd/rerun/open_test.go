package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// stubOpenCommand records what an open: command is asked to run, and its exit.
func stubOpenCommand(t *testing.T, code int, err error) *[]string {
	t.Helper()
	var ran []string
	old := runOpenCommand
	runOpenCommand = func(command, dir string) (int, error) {
		ran = append(ran, command+" in "+dir)
		return code, err
	}
	t.Cleanup(func() { runOpenCommand = old })
	return &ran
}

func TestOpenValueSchemeRunsAsLink(t *testing.T) {
	for _, value := range []string{"http://localhost:5173", "https://x.test/a", "mailto:a@b.c", "a+b.c-d:x"} {
		if !isOpenLink(value) {
			t.Errorf("isOpenLink(%q) = false, want a link", value)
		}
	}
	for _, value := range []string{"make dev", "./run.sh http://x", "echo hi:there", "/usr/bin/open x:y", ""} {
		if isOpenLink(value) {
			t.Errorf("isOpenLink(%q) = true, want a command", value)
		}
	}
}

func TestOpenLinkUsesSystemOpener(t *testing.T) {
	opened := stubOpen(t)
	ran := stubOpenCommand(t, 0, nil)
	web := target("web")
	web.Open = "http://localhost:5173"
	m := newTestModel(t, web)
	m.cursor = 0
	if cmd := press(m, "ctrl+o"); cmd != nil {
		t.Fatalf("o on a link returned a command")
	}
	if len(*opened) != 1 || (*opened)[0] != "http://localhost:5173" || len(*ran) != 0 {
		t.Fatalf("opened = %q, ran = %q, want the link through the opener only", *opened, *ran)
	}
}

func TestOpenCommandRunsInTargetDirOffTheUILoop(t *testing.T) {
	opened := stubOpen(t)
	ran := stubOpenCommand(t, 0, nil)
	web := target("web")
	web.Open = "make dev"
	web.Dir = "/work/app"
	m := newTestModel(t, web)
	m.cursor = 0
	cmd := press(m, "ctrl+o")
	if cmd == nil {
		t.Fatal("o on a command returned no command to run it")
	}
	if len(*ran) != 0 {
		t.Fatalf("the command ran before the tea.Cmd did: %q", *ran)
	}
	msg := cmd()
	if len(*ran) != 1 || (*ran)[0] != "make dev in /work/app" {
		t.Fatalf("ran = %q, want make dev in /work/app", *ran)
	}
	if len(*opened) != 0 {
		t.Fatalf("a command went to the system opener: %q", *opened)
	}
	m.Update(msg)
	if m.notice != "" {
		t.Fatalf("a zero exit showed notice %q", m.notice)
	}
}

func TestOpenCommandNonZeroExitNamesTheFirstWord(t *testing.T) {
	stubOpenCommand(t, 3, nil)
	web := target("web")
	web.Open = "make dev --watch"
	m := newTestModel(t, web)
	m.cursor = 0
	msg := press(m, "ctrl+o")()
	m.Update(msg)
	if m.notice != "open: make exited 3" || !m.noticeErr {
		t.Fatalf("notice = %q err=%v, want the exit as an error", m.notice, m.noticeErr)
	}
}

func TestOpenCommandStartErrorShowsNotice(t *testing.T) {
	stubOpenCommand(t, -1, errors.New("no such file"))
	web := target("web")
	web.Open = "./run.sh"
	m := newTestModel(t, web)
	m.cursor = 0
	m.Update(press(m, "ctrl+o")())
	if !strings.Contains(m.notice, "open: ./run.sh did not start") || !m.noticeErr {
		t.Fatalf("notice = %q, want a start error naming ./run.sh", m.notice)
	}
}

func TestCapturedEndpointDoesNotReplaceCommand(t *testing.T) {
	stubOpenCommand(t, 0, nil)
	web := target("web")
	web.Open = "make dev"
	m := newTestModel(t, web)
	addLines(m, "web", `{"url":"http://localhost:9"}`)
	if got := m.targets[0].spec.Open; got != "make dev" {
		t.Fatalf("open = %q, want the command kept", got)
	}
}

// writeOpenConfig writes a rerun.yml and parses it, returning the targets and notices.
func writeOpenConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	writeFile(t, path, body)
	return ParseConfig(path)
}

func TestConfigOpenStringIsTheLinkOrCommand(t *testing.T) {
	cfg, err := writeOpenConfig(t, "targets:\n  web:\n    run: x\n    open: make dev\n  app:\n    run: y\n    open: http://localhost:1\n")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Targets[0].Open != "make dev" || cfg.Targets[1].Open != "http://localhost:1" {
		t.Errorf("open = %q, %q", cfg.Targets[0].Open, cfg.Targets[1].Open)
	}
	if len(cfg.Notices) != 0 {
		t.Errorf("notices = %q, want none for the new spelling", cfg.Notices)
	}
}
