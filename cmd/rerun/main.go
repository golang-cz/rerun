package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/golang-cz/rerun"
)

const noGitignoreWarning = "rerun: warning: -no-gitignore is not implemented yet"

func warnNoGitignore(args Args, stderr io.Writer) {
	if args.NoGitignore {
		fmt.Fprintln(stderr, noGitignoreWarning)
	}
}

var version = "dev"

const usage = `rerun - re-run a command on file system changes

Usage:
  rerun                                run all rerun.yml targets in parallel
  rerun <target> [target...]           start these rerun.yml targets; the TUI
                                       still lists every target, space starts one
  rerun -- <cmd>                       ad-hoc run: watch ./ and run <cmd>
  rerun a b --except c d -- <cmd>      bare words = watchlist, --except = ignorelist
  rerun --on a b --except c d -- <cmd> --on is an explicit watchlist keyword

Lists are also read from stdin when it is not a TTY:
  find . -name '*.go' | rerun -- go test

Everything after -- is the command, passed through verbatim.

Flags:
  -delay <dur>      batch window before restarting a command (default 200ms)
  -cooldown <dur>   minimum time between restarts (default 0s)
  -grace <dur>      time between SIGTERM and SIGKILL (default 5s)
  -no-gitignore     (not yet implemented)
  -h, -help         show this help
  -version          show version

Deprecated: -watch, -ignore and -run (use --on, --except and --).
`

func main() {
	args, err := Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", err)
		os.Exit(1)
	}

	switch {
	case args.Help:
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	case args.Version:
		fmt.Fprintf(os.Stdout, "rerun %s\n", version)
		os.Exit(0)
	}

	warnNoGitignore(args, os.Stderr)

	if len(args.Cmd) == 0 {
		os.Exit(runConfigMode(args))
	}

	os.Exit(runAdHoc(args, os.Stdout, nil))
}

func runConfigMode(args Args) int {
	if len(args.Targets) > 0 {
		cfg, err := LoadConfig(".")
		if err != nil || !allKnown(cfg, args.Targets) {
			fmt.Fprintf(os.Stderr, "rerun: %v\n", noCmdHint)
			return 1
		}
		return startTargets(resolveTargets(cfg, args))
	}

	cfg, err := LoadConfig(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", err)
		if args.Target == "" {
			fmt.Fprint(os.Stdout, usage)
		}
		return 1
	}

	if args.Target != "" {
		if _, ok := cfg.Find(args.Target); !ok {
			fmt.Fprintf(os.Stderr, "rerun: unknown target %q; available: %s\n", args.Target, strings.Join(cfg.Names(), ", "))
			return 1
		}
	}

	return startTargets(resolveTargets(cfg, args))
}

// resolveTargets keeps every rerun.yml target in the list: named targets are
// the start-set, so the TUI still shows the rest and can start them by hand.
func resolveTargets(cfg *Config, args Args) []Target {
	named := args.Targets
	if args.Target != "" {
		named = []string{args.Target}
	}

	targets := append([]Target(nil), cfg.Targets...)
	if len(named) > 0 {
		for i := range targets {
			targets[i].Selected = containsName(named, targets[i].Name)
		}
	}
	applyOverrides(targets, args)
	return targets
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// startTargets runs the targets: behind the TUI on a terminal, as plain
// prefixed output everywhere else (pipes, CI, ad-hoc).
func startTargets(targets []Target) int {
	// The TUI needs a terminal on both ends, so a redirected stdin keeps the
	// plain path instead of failing to switch the terminal to raw mode.
	if isTerminal(os.Stdout) && isTerminal(os.Stdin) {
		return runTUI(targets, nil)
	}
	return runTargets(targets, os.Stdout, nil)
}

func allKnown(cfg *Config, names []string) bool {
	for _, name := range names {
		if _, ok := cfg.Find(name); !ok {
			return false
		}
	}
	return true
}

func applyOverrides(targets []Target, args Args) {
	for i := range targets {
		if args.Delay > 0 {
			targets[i].Delay = args.Delay
		}
		if args.Cooldown > 0 {
			targets[i].Cooldown = args.Cooldown
		}
		if args.Grace > 0 {
			targets[i].Grace = args.Grace
		}
	}
}

// adHocTarget is the synthetic nameless target for `rerun -- <cmd>`. An empty
// Name is what selects the raw passthrough output mode.
func adHocTarget(args Args) Target {
	targets := []Target{{
		Cmd:      rerun.ShellScript(args.Cmd),
		On:       args.Watch,
		Except:   args.Except,
		Delay:    defaultDelay,
		Grace:    rerun.DefaultGrace,
		Selected: true,
	}}
	applyOverrides(targets, args)
	return targets[0]
}

// runAdHoc is the single-command path: watch, restart on change. A nil
// signals channel installs the default handlers.
func runAdHoc(args Args, out io.Writer, signals <-chan os.Signal) int {
	return runTargets([]Target{adHocTarget(args)}, out, signals)
}
