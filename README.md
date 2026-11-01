rerun: re-run command on file system changes
============================================

Credit for this library goes to https://github.com/VojtechVitek/rerun

Lightweight file-watcher that re-runs given command on FS changes. It has simple CLI and optional config file. By default, it uses 200ms delay, which gives enough time for tools like git to update all directories/files within repository before killing the old process (when you switch branches etc).

# Usage

```
rerun                                run all rerun.yml targets in parallel
rerun <target> [target...]           start these rerun.yml targets; the TUI
                                     still lists every target, space starts one
rerun -- <cmd>                       ad-hoc run: watch ./ and run <cmd>
rerun a b --except c d -- <cmd>      bare words = watchlist, --except = ignorelist
rerun --on a b --except c d -- <cmd> --on is an explicit watchlist keyword
```

Everything after `--` is the command, passed through verbatim.

Naming targets picks what starts, not what is listed: the TUI still shows
every rerun.yml target. The named ones come up running; the rest start off
and `space` starts one by hand.

Target mode on a terminal opens a TUI: one row per target, `?` lists the keys.
Piped or non-TTY output stays plain prefixed lines.

Keys: `j/k` move the cursor, `space` start/stop, `r` reboot, `t` expand or
collapse the row's change tree, `m` mute a stream, `s` or `enter` solo (`esc`
clears), `o` open the url, `G` live output, `f` freeze or resume the pane.
`/` searches the pane (`n`/`N` step matches, `esc` clears it); `v` filters it
(a leading `.` runs a yq expression per JSON line).
Mouse: click a row to solo it, double-click to stop/start like `space`, the
button cell to mute, the `+/−` cell to expand or collapse that row's change
tree, shift-click to solo, and a click on the ALL row to clear
solo. A click in the live pane copies without freezing; a drag freezes the
tail as it selects. In a frozen pane, click or drag a line to
copy it, sent as OSC 52 so it works over ssh and tmux. The right pane is always the merged
stream; soloing narrows it to one target, keeping the name prefixes. Each
line pads the name to the longest target, so every message starts in the same
column. The pane's top line shows the cursor row's uptime, how many files its
last change batch held, and its command, with its url linked. The selected
row's change tree lists that batch below it, one `+`/`~`/`−` glyph (created,
written, removed) and relative path per row, five rows then `+k more`.

#### Examples:
```bash
# Ad-hoc: watch ./ and re-run the command on any change
$ rerun -- go test ./...

# Explicit watch and ignore lists
$ rerun '**/*.go' --except vendor -- go build ./...

# Named targets from rerun.yml
$ rerun
$ rerun frontend api

# Watchlist from stdin, when it is not a TTY
$ find . -name '*.go' | rerun -- go test ./...
```

#### Flags:
```
-delay <dur>      batch window before restarting a command (default 200ms)
-cooldown <dur>   minimum time between restarts (default 0s)
-grace <dur>      time between SIGTERM and SIGKILL (default 5s)
-no-gitignore     do not filter watched files through .gitignore
-h, -help         show this help
-version          show version
```

Flags override the matching fields of rerun.yml targets, e.g.
`rerun api -delay 1s`.

#### Deprecated: `-watch`, `-ignore` and `-run`
They still work in this major version. Prefer `--on`, `--except` and `--`.

# rerun.yml

Place a `rerun.yml` anywhere in the repo. rerun walks up from the current
directory, stopping at the git root, and takes the first one it finds.

```yaml
frontend:
  desc: Vite dev server
  on: web
  except: [node_modules, dist]
  cmd: pnpm run dev
  delay: 250ms
  cooldown: 2s
  grace: 1s

api:
  on: [cmd, internal]
  except: vendor
  cmd: go run ./cmd/api
```

Each top-level key is one target. Fields:

| Field | Meaning |
|---|---|
| `desc` | free-form description |
| `on` | watch paths or globs, a string or a list |
| `except` | paths or globs to ignore, a string or a list |
| `cmd` | shell command to run, required |
| `url` | url to open for the target (`o`, and `open: true` at startup) |
| `open` | open `url` in the browser once at startup (default false) |
| `selected` | start this target on a bare `rerun` (default true); naming a target on the command line overrides it either way |
| `delay` | batch window before restarting (default 200ms) |
| `cooldown` | minimum time between restarts (default 0s) |
| `grace` | time between SIGTERM and SIGKILL (default 5s) |

#### Reloading

rerun watches `rerun.yml` itself, so saving it applies the change without
restarting rerun. Each save prints one line naming what moved:

```
── config reloaded ── +auth-api  ~omsx(cmd)  ↺webhooks(watch) ──
```

| Glyph | Meaning |
|---|---|
| `+name` | target added, it starts |
| `−name` | target removed, its process stops and its watcher goes away |
| `~name(cmd)` | `cmd` changed, that target restarts |
| `↺name(watch)` | `on`, `except`, `delay` or `no-gitignore` changed, only its watcher is rebuilt and the process is left alone |

A target you stopped by hand stays stopped: a save swaps its spec but never
starts it again. A target added while one stream is soloed waits for `space`.
`desc`, `url` and `open` are cosmetic, and `selected` only counts at startup.
A config that does not parse changes nothing and only prints the error.

# Installation

```bash
go install github.com/golang-cz/rerun/cmd/rerun@latest
```
*You might need to [download Go](https://golang.org/dl/) first.*

Uses [fsnotify](https://github.com/fsnotify/fsnotify) behind the scenes, so technically it should work on most platforms including Linux, Mac OS and Windows.

# License

Licensed under the [MIT License](./LICENSE).
