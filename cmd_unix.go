//go:build darwin || linux

package rerun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// shellMeta holds the characters a shell expands, splits, or quotes.
const shellMeta = "\"'`\\$&|;<>(){}[]*?!#~"

// shellBuiltins are shell keywords and builtins with no binary, so LookPath cannot reject them.
var shellBuiltins = map[string]bool{
	"cd": true, "exec": true, "export": true, "source": true, ".": true, "eval": true,
	"set": true, "unset": true, "alias": true, "unalias": true, "exit": true, "return": true,
	"read": true, "trap": true, "ulimit": true, "umask": true, "wait": true, "shift": true,
	"local": true, "readonly": true, "type": true, "command": true, "builtin": true,
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true,
	"while": true, "until": true, "do": true, "done": true, "case": true, "esac": true,
	"function": true, "time": true, "{": true, "}": true, "!": true, "select": true,
	"coproc": true, "let": true, "declare": true, "typeset": true, "hash": true,
	"jobs": true, "fg": true, "bg": true, "pushd": true, "popd": true, "dirs": true,
	"getopts": true, "times": true, "fc": true, "history": true, "help": true,
	"bind": true, "enable": true, "logout": true, "suspend": true, "disown": true,
	"caller": true, "complete": true, "compgen": true, "shopt": true,
}

func newPlatformCmd(cmdline []string, grace time.Duration, opts ...Option) Command {
	return newCommand(cmdline, grace, opts...)
}

func newExecCmd(cmdline []string) *exec.Cmd {
	script := ShellScript(cmdline)
	var cmd *exec.Cmd
	if argv, ok := plainArgv(script); ok {
		// exec.Command keeps the typed word in Args; a relative path resolves against cmd.Dir at Start.
		cmd = exec.Command(argv[0], argv[1:]...)
	} else {
		cmd = exec.Command("/bin/sh", "-c", script)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = childEnv(os.Environ())
	// Own process group, so one signal reaches the shell and everything it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// plainArgv returns the words of a script that needs no shell, or false to keep /bin/sh.
func plainArgv(script string) ([]string, bool) {
	if strings.ContainsAny(script, shellMeta+"\n\r") {
		return nil, false
	}
	words := strings.Fields(script)
	if len(words) == 0 || strings.Contains(words[0], "=") || shellBuiltins[words[0]] {
		return nil, false
	}
	return words, true
}

// startError names the program when a direct exec hits ENOEXEC, the file has no #! line.
func startError(cmd *exec.Cmd, err error) error {
	if errors.Is(err, syscall.ENOEXEC) {
		return fmt.Errorf("%s: exec format error; add a #! line to run it without sh: %w", cmd.Args[0], err)
	}
	return err
}

// quoteWord single-quotes a word the shell would otherwise split or expand.
func quoteWord(word string) string {
	if word != "" && !strings.ContainsAny(word, " \t\n\r"+shellMeta) {
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
