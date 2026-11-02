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

Left column: one row per target; right pane: one merged log; `?` lists keys.

Keys: `j/k` move the cursor, `←/→` focus the log pane and then scroll its
columns (`↑/↓` scroll it when focused, `home`/`end` jump to its top-left or
live tail, `esc` returns to the list), `space` start/stop, `r` reboot, `t`
toggle the fs drawer under the list (`tab` focuses it), `m` mute a stream, `s` or `enter`
solo a target or select a schema's targets (`esc` clears). On a schema header,
`space`, `r`, and `m` act on its targets. `o` open the url, `G` resume the live tail, `p` browse the
scrollback, `q` or `ctrl+c` ask first; `enter` or a second `ctrl+c` quits.
`/` searches the pane (`n`/`N` step matches, `esc` clears it); `v` filters it
(a leading `.` runs a yq expression per JSON line); `c` toggles the columns
table for the selection, which starts as a table when it has `columns`. A JSON output line's top-level `url`,
`addr`, `listen`, or `port` updates the row's url live.
Expand a JSON line: every JSON object line ends in a `(+)` cell. Click it, or press
`enter` with the pane focused (it acts on the bottom-most line), to show the pretty-printed
object; `(−)` collapses it. In an expanded block, click a key to add it as a column of the
selection: the selected schema, else the soloed target, else a hint says to select one.
Click a scalar value to add a `.path == value` chip to the pane header. Chips AND together
with `v`; click a chip's `×`, or press `backspace` with the pane focused, to remove one.
Click a column name in the table header to remove that column. Column picks are unsaved
until `ctrl+s` writes them to rerun.yml; the footer reads `unsaved columns · ctrl+s save`
until then. Chips and expansions last for the session only. `cmd+s` works only where the
terminal forwards it.
Mouse: click a row to solo it, double-click to stop/start like `space`, the
button cell to mute, a batch in the fs drawer to list its files,
shift-click to solo, and a click on a schema header to select its targets. Click a pane line to copy it; drag copies a range and browses the scrollback
(`p`, `G`, or `↓ N new` resume). Shift-drag uses the terminal's own selection.
Targets with a `schema:` are listed under a `▾ name` header that counts
its running members; targets without one follow. The right pane is always the
merged stream; soloing narrows it to one target, and selecting a schema to its
members, keeping the name prefixes. Each
line pads the name to the longest target, so every message starts in the same
column. The pane's top line shows the cursor row's uptime, how many files its
last change batch held, and its command, with its url linked. The fs drawer
at the bottom of the left column is a feed of recent change batches, newest
first. A batch is every change within 150ms of its first event, so one save
across several targets is one line: `15:04:05  path +N  → targets`. The path is
the first changed file relative to the rerun.yml dir, `+N` counts the rest, and
the arrow names the targets it restarted (`config` for a rerun.yml reload). The
feed keeps batches for a minute and dims the newest when all have aged out.
`tab` focuses the drawer: `j/k` move through the batches, `enter` (or a click)
lists a batch's files with `+`/`~`/`−` glyphs, `esc` closes the list. A target
whose row a batch restarted lights up for a second. `t` toggles the drawer,
which grows on demand and never takes more than seven lines.

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
| `columns` | table-mode columns, each `name {.yq.expr}`, e.g. `["method {.request.method}", "status {.response.status}"]` |
| `schema` | name of a `schemas:` entry to take columns from; the target's own `columns` win |
| `open` | open `url` in the browser once at startup (default false) |
| `selected` | start this target on a bare `rerun` (default true); naming a target on the command line overrides it either way |
| `start` | when the first run happens: `auto` (default), `onchange` (holds the run until a file changes), or `manual` (watches nothing, runs only on `r`) |
| `delay` | batch window before restarting (default 200ms) |
| `cooldown` | minimum time between restarts (default 0s) |
| `grace` | time between SIGTERM and SIGKILL (default 5s) |

#### Schemas

Targets that log the same JSON shape can share columns. Define them once under
the reserved top-level `schemas:` key, then point each target at one with
`schema:`:

```yaml
schemas:
  backend:
    columns: ["level {.level}", "msg {.msg}", "status {.status}"]

omsx-api:
  schema: backend
  cmd: make -C apps/api run
```

A target's own `columns` wins over its schema's. A `schema:` that names no
schema is a config error. Quote each column entry: a `{` inside a plain entry
of a `[...]` list is a YAML error.

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
