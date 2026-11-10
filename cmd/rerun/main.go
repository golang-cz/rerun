package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-cz/rerun"
)

// version and buildTime are stamped by ldflags. An unbuilt dev binary keeps
// version = "dev" and an empty buildTime, so no age is shown.
var (
	version   = "dev"
	buildTime string
)

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

rerun.yml targets: a top-level targets: block holds them (older files keep them at
  the top level; run rerun -migrate to move them under targets:)

rerun.yml groups: a top-level groups: block shares columns, and a target names one
  with group: (older spelling: schemas and schema; use one spelling per file)

rerun.yml target keys: run (older spelling: cmd), build, health, desc, on, except, url,
  columns, group, open, selected, start, delay, cooldown, grace

Flags:
  -delay <dur>      batch window before restarting a command (default 200ms)
  -cooldown <dur>   minimum time between restarts (default 0s)
  -grace <dur>      time between SIGTERM and SIGKILL (default 5s)
  -no-gitignore     do not filter watched files through .gitignore
  -migrate          move rerun.yml's top-level targets under targets:
  -h, -help         show this help
  -version          show version

Deprecated: -watch, -ignore and -run (use --on, --except and --).
`

// configHint follows a rerun.yml load failure. The usage wall would bury the
// parse error, so the fix stays one line and points at -h for the key list.
const configHint = "fix rerun.yml and re-run; rerun -h lists keys"

// nestGuard refuses to run when RERUN is set, so a target that runs rerun
// cannot fork-bomb the machine. It returns the process exit code.
func nestGuard() int {
	if os.Getenv("RERUN") == "" {
		return 0
	}
	fmt.Fprintln(os.Stderr, "rerun: refusing to nest rerun (RERUN is set; unset it to force)")
	return 1
}

func main() {
	if code := nestGuard(); code != 0 {
		os.Exit(code)
	}

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
	case args.Migrate:
		os.Exit(runMigrate())
	}

	if len(args.Cmd) == 0 {
		os.Exit(runConfigMode(args))
	}

	os.Exit(runAdHoc(args, os.Stdout, nil))
}

// runMigrate rewrites the nearest rerun.yml in place, so its targets sit under
// the reserved `targets:` key and new reserved keys can never collide.
func runMigrate() int {
	path, err := findConfig(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", err)
		return 1
	}
	status, err := migrateConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: migrate: %v\n", err)
		return 1
	}
	switch status {
	case migrated:
		fmt.Fprintf(os.Stdout, "rerun: %s: targets now live under targets:\n", path)
	case migrateAlready:
		fmt.Fprintf(os.Stdout, "rerun: %s already uses targets:\n", path)
	case migrateRenamed:
		fmt.Fprintf(os.Stdout, "rerun: %s: rewrote retired keys (url:, cmd:, hide:, collapsed:, auto-hide:, open-on-start:)\n", path)
	default:
		fmt.Fprintf(os.Stdout, "rerun: %s has no top-level targets to move\n", path)
	}
	added, err := migrateImports(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: migrate: %v\n", err)
		return 1
	}
	for _, name := range added {
		fmt.Fprintf(os.Stdout, "rerun: %s: added %s to imports:\n", path, name)
	}
	return 0
}

func runConfigMode(args Args) int {
	if len(args.Targets) > 0 {
		cfg, err := LoadConfig(".")
		if err != nil || !allKnown(cfg, args.Targets) {
			fmt.Fprintf(os.Stderr, "rerun: %v\n", noCmdHint)
			return 1
		}
		return startTargets(resolveTargets(cfg, args), cfg)
	}

	cfg, err := LoadConfig(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nrerun: hint: %s\n", configHint)
		return 1
	}
	// A Makefile-only config has no TUI to start its manual targets from, so without a terminal it would wait forever.
	if cfg.MakeOnly && !(isTerminal(os.Stdout) && isTerminal(os.Stdin)) {
		fmt.Fprintf(os.Stderr, "rerun: %v\n", &configNotFoundError{dir: filepath.Dir(cfg.Path)})
		fmt.Fprintf(os.Stderr, "\nrerun: hint: %s\n", configHint)
		return 1
	}

	if args.Target != "" {
		if _, ok := cfg.Find(args.Target); !ok {
			fmt.Fprintf(os.Stderr, "rerun: unknown target %q; available: %s\n", args.Target, strings.Join(cfg.Names(), ", "))
			return 1
		}
	}

	return startTargets(resolveTargets(cfg, args), cfg)
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
// prefixed output everywhere else (pipes, CI, ad-hoc). cfg is the rerun.yml the
// run reloads itself from.
func startTargets(targets []Target, cfg *Config) int {
	// The TUI needs a terminal on both ends, so a redirected stdin keeps the
	// plain path instead of failing to switch the terminal to raw mode.
	if isTerminal(os.Stdout) && isTerminal(os.Stdin) {
		return runTUI(targets, cfg, nil)
	}
	for _, n := range dedupNotices(cfg) {
		fmt.Fprintf(os.Stderr, "rerun: %s\n", n)
	}
	for _, m := range cfg.Missing {
		fmt.Fprintf(os.Stderr, "rerun: import %s: not found, skipped\n", m.Path)
	}
	return runTargets(targets, os.Stdout, cfg.Path, nil)
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
		targets[i].NoGitignore = args.NoGitignore
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

// runAdHoc is the single-command path: watch, restart on change. There is no
// config, so there is nothing to reload. A nil signals channel installs the
// default handlers.
func runAdHoc(args Args, out io.Writer, signals <-chan os.Signal) int {
	return runTargets([]Target{adHocTarget(args)}, out, "", signals)
}
