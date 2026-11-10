package main

import (
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// openURL opens a url in the platform's browser. Tests replace it.
var openURL = openInBrowser

// runOpenCommand runs an open: command with sh -c in dir and reports its exit
// code, or the error that stopped it from starting. Tests replace it.
var runOpenCommand = runShellCommand

// openSchemeRE matches a value that starts with a URL scheme, so it opens rather than runs.
var openSchemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

// isOpenLink reports whether an open: value is a URL for the system opener.
func isOpenLink(value string) bool {
	return openSchemeRE.MatchString(value)
}

// openExitMsg reports how an open: command ended, so the TUI can say when it failed.
type openExitMsg struct {
	first string
	code  int
	err   error
}

// runOpenCommandCmd runs an open: command off the UI loop and reports its exit.
func runOpenCommandCmd(command, dir string) tea.Cmd {
	return func() tea.Msg {
		code, err := runOpenCommand(command, dir)
		first, _, _ := strings.Cut(strings.TrimSpace(command), " ")
		return openExitMsg{first: first, code: code, err: err}
	}
}

// runShellCommand runs command with sh -c in dir, its stdio on the null device.
func runShellCommand(command, dir string) (int, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// openInBrowser hands a url to the platform's browser. The wait is reaped in
// the background so the TUI never blocks on the browser.
func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// start is a cmd.exe builtin; the empty argument is the window title.
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
