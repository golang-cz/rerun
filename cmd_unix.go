//go:build darwin || linux

package rerun

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func newExecCmd(cmdline []string, opts ...Option) *exec.Cmd {
	script := ShellScript(cmdline)
	// Options such as WithDir decide where a relative program path resolves, so read them first.
	probe := &exec.Cmd{}
	applyOptions(probe, opts)
	var cmd *exec.Cmd
	if argv, ok := plainArgv(script, probe.Dir); ok {
		// exec.Command keeps the typed word in Args; the child resolves a relative path after chdir.
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
	applyOptions(cmd, opts)
	return cmd
}

func applyOptions(cmd *exec.Cmd, opts []Option) {
	for _, opt := range opts {
		opt(cmd)
	}
}

// execMagic lists the file starts the kernel execs directly. Anything else, such as
// a script without #!, needs sh's ENOEXEC fallback.
var execMagic = [][]byte{
	[]byte("#!"),
	[]byte("\x7fELF"),
	{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf}, // Mach-O, big endian
	{0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe}, // Mach-O, little endian
	{0xca, 0xfe, 0xba, 0xbe}, {0xbe, 0xba, 0xfe, 0xca}, // Mach-O universal
}

// plainArgv returns the words of a script that needs no shell, or false to keep /bin/sh.
// dir is the child's working directory; a relative program path resolves against it.
func plainArgv(script, dir string) ([]string, bool) {
	if strings.ContainsAny(script, shellMeta+"\n\r") {
		return nil, false
	}
	words := strings.Fields(script)
	if len(words) == 0 || strings.Contains(words[0], "=") || shellBuiltins[words[0]] {
		return nil, false
	}
	prog := words[0]
	if strings.Contains(prog, "/") && dir != "" && !filepath.IsAbs(prog) {
		prog = filepath.Join(dir, prog)
	}
	// A missing or non-executable program stays on sh so its error text is unchanged.
	found, err := exec.LookPath(prog)
	if err != nil || !hasExecMagic(found) {
		return nil, false
	}
	return words, true
}

// hasExecMagic reports whether the file starts with a format the kernel execs directly.
// A read error reports false, which keeps the sh path.
func hasExecMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [4]byte
	n, _ := io.ReadFull(f, head[:])
	for _, magic := range execMagic {
		if n >= len(magic) && bytes.HasPrefix(head[:n], magic) {
			return true
		}
	}
	return false
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
