//go:build darwin || linux

package rerun

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func newPlatformCmd(cmdline []string, grace time.Duration, opts ...Option) Command {
	return newCommand(cmdline, grace, opts...)
}

func newExecCmd(cmdline []string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", ShellScript(cmdline))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = childEnv(os.Environ())
	// Own process group, so one signal reaches the shell and everything it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// quoteWord single-quotes a word the shell would otherwise split or expand.
func quoteWord(word string) string {
	if word != "" && !strings.ContainsAny(word, " \t\n\r\"'`\\$&|;<>(){}[]*?!#~") {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// signalGroup signals the process group. A pid recycled in the nanoseconds
// between the reaped check and the syscall is accepted as out of scope.
func signalGroup(cmd *exec.Cmd, st *exitState, force bool) {
	if cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}

	pid := cmd.Process.Pid
	if reaped(st) {
		return
	}
	syscall.Kill(-pid, sig)

	// The child may have moved itself into another group; cover that too.
	if pgid, perr := syscall.Getpgid(pid); perr == nil && pgid != pid && !reaped(st) {
		syscall.Kill(-pgid, sig)
	}
}
