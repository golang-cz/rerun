package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
)

// Args is the parsed rerun CLI invocation.
//
// Watch words come either from bare words, from --on (or the deprecated
// -watch), or from a non-TTY stdin. Cmd is everything after -- verbatim.
type Args struct {
	Target      string
	Targets     []string
	Watch       []string
	Except      []string
	Cmd         []string
	Delay       time.Duration
	Cooldown    time.Duration
	Grace       time.Duration
	NoGitignore bool
	Legacy      bool
	Help        bool
	Version     bool
}

const (
	legacyDeprecation = "rerun: -watch/-ignore/-run are deprecated; use --on/--except/--"
	noCmdHint         = "add `-- <cmd>` to run a command"
)

// Parse parses os.Args[1:], reading a piped stdin watchlist when needed.
func Parse(argv []string) (Args, error) {
	return parseArgs(argv, os.Stdin, isTerminal(os.Stdin), os.Stderr)
}

// parseArgs is Parse with its environment injected so tests can drive it.
func parseArgs(argv []string, stdin io.Reader, stdinIsTTY bool, stderr io.Writer) (Args, error) {
	var args Args

	// Bare words append to whichever list is active. Bare watch words are
	// counted separately: a single one may still name a target.
	const (
		listWatch = iota
		listExcept
	)
	list := listWatch
	// Only a list keyword blocks the bare-word target rule. The run-tuning
	// flags (-delay, -cooldown, -grace, -no-gitignore) combine with a target.
	listFlagSeen := false

parse:
	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		switch arg {
		case "--":
			cmd, err := takeCommand(argv[i+1:])
			if err != nil {
				return args, err
			}
			args.Cmd = cmd
			break parse

		case "-run":
			warnLegacy(&args, stderr)
			cmd, err := takeCommand(argv[i+1:])
			if err != nil {
				return args, err
			}
			args.Cmd = cmd
			break parse

		case "--on":
			list = listWatch
			listFlagSeen = true

		case "--except":
			list = listExcept
			listFlagSeen = true

		case "-watch":
			warnLegacy(&args, stderr)
			list = listWatch
			listFlagSeen = true

		case "-ignore":
			warnLegacy(&args, stderr)
			list = listExcept
			listFlagSeen = true

		case "-delay":
			d, err := durationFlag(argv, &i, "-delay")
			if err != nil {
				return args, err
			}
			args.Delay = d

		case "-cooldown":
			d, err := durationFlag(argv, &i, "-cooldown")
			if err != nil {
				return args, err
			}
			args.Cooldown = d

		case "-grace":
			d, err := durationFlag(argv, &i, "-grace")
			if err != nil {
				return args, err
			}
			args.Grace = d

		case "-no-gitignore":
			args.NoGitignore = true

		case "-h", "-help":
			args.Help = true
			break parse

		case "-version":
			args.Version = true
			break parse

		default:
			if arg == "-" {
				return args, errors.New("`-` is reserved for stdin; pass watch paths with --on")
			}
			if strings.HasPrefix(arg, "-") {
				return args, fmt.Errorf("unknown flag %q", arg)
			}
			if list == listExcept {
				args.Except = append(args.Except, arg)
			} else {
				args.Watch = append(args.Watch, arg)
			}
		}
	}

	if args.Help || args.Version {
		return args, nil
	}

	if len(args.Cmd) == 0 {
		// Bare words with no list flag may name rerun.yml targets. main decides
		// once it knows whether a config file exists.
		if len(args.Except) == 0 && !listFlagSeen {
			args.Targets = args.Watch
			args.Watch = nil
			if len(args.Targets) == 1 {
				args.Target = args.Targets[0]
				args.Targets = nil
			}
			return args, nil
		}
		return args, errors.New(noCmdHint)
	}

	if len(args.Watch) == 0 && !stdinIsTTY && stdin != nil {
		paths, err := readStdinList(stdin)
		if err != nil {
			return args, fmt.Errorf("read stdin: %w", err)
		}
		args.Watch = append(args.Watch, paths...)
	}

	return args, nil
}

func takeCommand(rest []string) ([]string, error) {
	if len(rest) == 0 || rest[0] == "--" {
		return nil, errors.New("empty command")
	}
	return append([]string(nil), rest...), nil
}

func warnLegacy(args *Args, stderr io.Writer) {
	if args.Legacy {
		return
	}
	args.Legacy = true
	fmt.Fprintln(stderr, legacyDeprecation)
}

func durationFlag(argv []string, i *int, name string) (time.Duration, error) {
	if *i+1 >= len(argv) {
		return 0, fmt.Errorf("%s: missing value", name)
	}
	*i++
	raw := argv[*i]
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", name, raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: duration must not be negative, got %q", name, raw)
	}
	return d, nil
}

// maxStdinPaths bounds the piped watchlist so a runaway producer cannot make
// rerun allocate without limit.
const maxStdinPaths = 100_000

func readStdinList(r io.Reader) ([]string, error) {
	var paths []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(paths) == maxStdinPaths {
			return nil, fmt.Errorf("stdin watchlist exceeds %d entries", maxStdinPaths)
		}
		paths = append(paths, line)
	}
	return paths, scanner.Err()
}

// isTerminal reports whether f is a terminal. On stat failure it assumes a
// terminal, so a piped stdin never blocks startup on a read.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return true
	}
	// The char-device bit is not enough: /dev/null is one, but not a terminal.
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return term.IsTerminal(f.Fd())
}
