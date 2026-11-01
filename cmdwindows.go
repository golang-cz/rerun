//go:build windows

package rerun

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func newPlatformCmd(cmdline []string, grace time.Duration, opts ...Option) Command {
	return newCommand(cmdline, grace, opts...)
}

func newExecCmd(cmdline []string) *exec.Cmd {
	cmd := exec.Command("cmd", "/c", ShellScript(cmdline))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = childEnv(os.Environ())
	return cmd
}

// quoteWord double-quotes a word cmd.exe would otherwise split or expand.
func quoteWord(word string) string {
	if word != "" && !strings.ContainsAny(word, " \t\n\r\"&|;<>()^%") {
		return word
	}
	return `"` + strings.ReplaceAll(word, `"`, `\"`) + `"`
}

// signalGroup: Windows has no signal a console child can handle gracefully, so
// both the graceful and the forced pass are the same tree kill.
func signalGroup(cmd *exec.Cmd, st *exitState, force bool) {
	if cmd.Process == nil || reaped(st) {
		return
	}
	// https://stackoverflow.com/a/44551450
	kill := exec.Command("TASKKILL", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.Run()
}
