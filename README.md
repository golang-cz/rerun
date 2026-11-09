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

Target mode on a terminal opens a TUI: one row per target, `?` lists the keys, and a second `?` lists them all.
Piped or non-TTY output stays plain prefixed lines.

Left column: one row per target; right pane: one merged log; `?` lists keys.

Keys: `j/k` move the cursor, `←/→` focus the log pane and then scroll its
columns (`↑/↓` move its cursor when focused, `home`/`end` jump to its top-left or
live tail; `esc` goes back to the live tail first, then backs out of the list), `space` start/stop, `r` reboot,
`e` fs events (last minute), `m` mute a stream. `b` cycles the left list: full, names only (the state glyph and name), or hidden for a full-width log with a `b list` cue on the bottom line; `esc` there shows the list again. Focusing the pane puts its cursor on the service's last line, or the newest line when that is off screen. Moving the cursor
scopes the pane to its row; `esc` backs the scope out (a target to its group, a group to all).
On a group header, `space`, `r`, and `m` act on its targets; `[`/`]` or `shift`+`↑`/`↓` jump between groups. `o` open the url; with the pane focused, `o` opens the original lines (see below). Scrolling up browses the
scrollback; `esc` returns to the live tail, and `q` or `ctrl+c` ask first; `enter` or a second `ctrl+c` quits.
`/` or `ctrl+f` opens the query bar: `enter` adds the input as pills (a leading `.` runs a yq expression per JSON line);
`esc` leaves the bar but keeps the pills, `backspace` on an empty input removes the last pill; a selection with `columns`
always shows them as a table. A JSON output line's top-level `url`,
`addr`, `listen`, or `port` updates the row's url live.

More keys: `c` copies the selected lines, or the pane cursor line when nothing is selected; `e` opens the fs events list (last minute), `s` stops the row (with the pane focused, every target the pane shows), `space` with the pane focused jumps to the live tail, `→` expands the line under the cursor and `←` collapses it, `ctrl+g` and `ctrl+shift+g` step to the next and previous pill hit, and `ctrl+l` clears the log (older lines stay in scrollback). With the pane focused, `shift`+`↑`/`↓` select lines from the cursor, the same highlight as a drag; any other key clears it. Copy sends the text through the terminal's OSC 52 clipboard sequence, which Ghostty and herdr pass through.

The query bar's pills AND together, and one input can hold several: `level=error -healthz` adds two on `enter`.
Each pill is a filter (`[text]`, solid) or a highlight (`⟨text⟩`, dim): a filter pill hides non-matching lines, a
highlight pill only marks its hits. Every pill's matches are marked in the pane, and `n`/`N`/`ctrl+g`/`ctrl+shift+g`
jump between the hit lines. With the bar open and the input empty or the cursor at 0, `←`/`→` walk the pills: `x` or
`backspace` removes the selected one, `enter` moves its text back into the input, `space` toggles filter ⇄ highlight,
and `→` on a `[@name]` pill expands it into its pills. A pill that does not parse keeps the input red, with the error
in the footer. The bar names the scope, the pill count and the matching/total lines. Typing `@` lists the pane scope's
saved queries (see `queries:` below).

| Pill | Meaning |
|---|---|
| `timeout` | full text, case-insensitive substring of the raw line |
| `"connection refused"` | full-text phrase (quotes allow spaces) |
| `-healthz` | negated full text |
| `level=error` | field equals, case-insensitive for strings, numeric compare for numbers |
| `level=error\|fatal` | field equals any of a `\|` list |
| `level!=debug` | field not equal (any of a `\|` list) |
| `http.status>=400` | `>` `>=` `<` `<=` numeric |
| `msg~refused` | field contains, case-insensitive |
| `msg~/time(out\|d out)/` | field matches regex (Go RE2); `msg=~re` accepted as alias |
| `trace_id=*` / `trace_id!=*` | field exists / missing |
| `.expr` | raw yq, a truthy result keeps the line |

A field name is a yq path without the leading dot (`http.status` ≡ `.http.status`); a column name from the scope's
`columns:` resolves to that column's expression first. A field pill never matches a non-JSON line; full-text pills
match any line.

Expand a JSON line: every JSON object line ends in a `(+)` cell. Click it, or press
`enter` with the pane focused (it acts on the cursor line, which starts on the newest
line), to show the pretty-printed object; `(−)` collapses it. A click anywhere on an
expandable line toggles it the same way, and a click on a row of an open expansion
collapses it. Opening a block scrolls only as far as needed to show it. A click means press and release on one cell: a press that moves is a drag,
which selects text and never toggles. Moving the cursor off the
newest line pauses the tail. Expanded keys are underlined and values tinted. With the
cursor on an expanded key, `a` adds it as a column; on a scalar, `f` adds a `path=value`
pill. In an expanded block, click a key to add it as a column of the
selection: the selected schema, else the target the pane shows, else a hint says to select one.
Build output shows inline: each build is one `⚙` rule in the stream, rewritten in place as it
becomes `built <duration>` or `✗ build failed`. Its output folds under the rule: click the icon or
`+N more`, or press `enter`. A failed build opens itself, and a `▶` rule folds its command when the
pane cuts it short.
Click a scalar value to add a `path=value` pill to the pane header. Pills AND together;
click a pill's `×`, or press `backspace` with the pane focused, to remove one.
Click the `×` after a column name in the table header to remove that column; a click on
the name does nothing. Column picks are unsaved until `ctrl+s` writes them to rerun.yml;
the footer reads `unsaved columns · ctrl+s save` until then, and a schema or target with
unsaved columns gets a `*` in its marker column, two cells after its name. A `columns:` list that a YAML alias uses elsewhere is
not rewritten: the footer names it, and you edit rerun.yml by hand. Chips and expansions last for the session only. `cmd+s` works only where the
terminal forwards it.

With the pane focused, `o` opens the original lines: the cursor line exactly as printed, with no cut, columns or color, and long lines wrap. A line selection (`shift`+`↑`/`↓` or a drag) shows those lines as NDJSON. `cmd+a` (`alt+i`) selects every line the pills kept, so `cmd+a` then `o` shows the raw filtered log. `f` toggles raw and pretty JSON, pretty only when every line is JSON. `c` copies what the modal shows, and `esc` closes it.

Mouse: click a row to show only its stream, double-click to stop/start like `space`,
the fs events line at the bottom to open the change batches, and a click on a schema header to select
its targets. Click a pane line to copy it; clicking an expandable line expands or
collapses it instead, and drag copies a range and browses the scrollback
(`esc` or `↓ N new` resumes). Shift-drag uses the terminal's own selection.
Targets with a `group:` are listed under a `▾ name` header that counts
its running members; targets without one follow. The right pane is always the
merged stream; clicking a row narrows it to one target, and selecting a schema to its
members, keeping the name prefixes. Each
line pads the name to the longest target, so every message starts in the same
column. The pane's top line shows the cursor row's state, uptime, how many files its
last change batch held, and its command, with its url linked. It names a muted
row in words (`muted`), and uptime reads `7s`, `2m03s`, `1h02m`.
Each row keeps two marker cells after its name: `*` marks unsaved columns. Muting a target posts a footer notice (`muted api`).
A `muted: true` in `rerun.yml` starts the stream muted; `m` still toggles it. `hide: true` does the same, and a group's `hide: true` starts its header folded; `→` or a click expands it. Folding is a start state; a reload keeps your fold for groups already shown.
The bottom-left footer reads `? help`. The line above it is the fs events line:
`fs events · 4s ago` ages the newest change batch, reading `fs events · idle`
when none landed in the last minute. It highlights for a second when a batch
lands, turns red `fs events · ⚠ watch failed` after a watcher error until the
next successful batch, and a click opens the fs events modal, like `e`. The
modal lists the last minute's batches, newest first: `22:32:00  path +N` with
the targets it restarted, after a 2-space gap and a `↻` mark (`config` for a rerun.yml
reload). A batch is every change within 150ms of its first event, so one save
across several targets is one line. The path is the first changed file relative
to the rerun.yml dir, cut with a middle ellipsis when the row is still too long
after the target list shortens to `a, b +N more`. A created directory keeps a
trailing `/` and heads the files reported under it. A watcher error is one red
line above the batches, never a row. `→` lists a batch's files with
`+`/`~`/`−` glyphs, each naming the services whose watch globs it matched,
wrapping to continuation lines when they don't fit; `←` collapses. `c` copies
every batch expanded to the clipboard. A target whose row a batch restarted
lights up for a second.

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
-migrate          move rerun.yml's top-level targets under targets:
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
targets:
  frontend:
    desc: Vite dev server
    on: web
    except: [node_modules, dist]
    run: pnpm run dev
    delay: 250ms
    cooldown: 2s
    grace: 1s

  api:
    on: [cmd, internal]
    except: vendor
    run: go run ./cmd/api
```

Each top-level key is one target. Fields:

| Field | Meaning |
|---|---|
| `desc` | free-form description |
| `on` | watch paths or globs, a string or a list |
| `except` | paths or globs to ignore, a string or a list |
| `run` | shell command to run, required. Older spelling: `cmd`; use one |
| `build` | shell command run before each (re)start; the old run keeps going until it passes, and a failed build leaves it untouched |
| `health` | `http(s)://` url (200-399 is healthy), `tcp://host:port`, or a shell command (exit 0 is healthy), probed every second |
| `url` | url to open for the target (`o`, and `open: true` at startup) |
| `columns` | table-mode columns, each `name {.yq.expr}`, e.g. `["method {.request.method}", "status {.response.status}"]` |
| `queries` | saved query pills for the query bar, name → pill string; the target's own win a name clash with its `group`'s |
| `group` | name of a `groups:` entry to take columns from; the target's own `columns` win. Older spelling: `schema`, with `schemas:` |
| `muted` | start the stream muted (default false); a group's `muted` fills in when unset. `m` still toggles it |
| `hide` | start the stream muted, like `muted`; a group's `hide` also starts its header folded. Own `muted` wins over `hide`, and `muted: false` overrides both. `hide: false` overrides a group's `hide: true` |
| `open` | open `url` in the browser once at startup (default false) |
| `selected` | start this target on a bare `rerun` (default true); naming a target on the command line overrides it either way |
| `start` | when the first run happens: `auto` (default), `onchange` (holds the run until a file changes), or `manual` (watches nothing, runs only on `r`) |
| `delay` | batch window before restarting (default 200ms) |
| `cooldown` | minimum time between restarts (default 0s) |
| `grace` | time between SIGTERM and SIGKILL (default 5s) |

A column can also hold literal text and several `{expr}` placeholders. Each
placeholder evaluates on its own, a missing value renders as nothing, and runs of
spaces collapse. With `columns: ["msg {.message} {.error}"]`, a line with both
fields shows `disk full ENOSPC`, and a line with only `.error` shows `ENOSPC`. A
bad expression shows `bad column "msg": <error>` in the title row.

#### `targets:`

Targets sit under a top-level `targets:` key:

```yaml
targets:
  api:
    run: go run ./cmd/api
    on: [cmd, internal]
```

- The top level holds only rerun's own keys: `imports:`, `groups:`, `schemas:`, `targets:` and `queries:`. Any other top-level key is an error, so a typo surfaces.
- A file without a `targets:` key is read the old way, with its targets at the top level. rerun shows one footer notice: `rerun.yml uses top-level targets; run rerun -migrate`.
- `rerun -migrate` rewrites the nearest rerun.yml in place, so its targets move under `targets:`. It keeps comments and key order, and drops blank lines.
- A target under `targets:` may be called `imports` or `groups`, because it no longer collides with those reserved top-level keys.

#### Groups

Targets that log the same JSON shape can share columns. Define them once under
the reserved top-level `groups:` key, then point each target at one with
`group:`:

```yaml
groups:
  backend:
    columns: ["level {.level}", "msg {.msg}", "status {.status}"]

targets:
  omsx-api:
    group: backend
    cmd: make -C apps/api run
```

A target's own `columns` wins over its group's. A group's `queries:` pass their saved
queries to the members, and a target's own entry wins a name clash. A group's `start:` sets the
first run for its members, and a target's own `start:` wins. A `group:` that names no
group is a config error. A group's `muted: true` starts its members muted, and a
target's own `muted: false` overrides it. A group's `hide: true` mutes its members the same way and starts the group's header folded. A target's own `hide: false` overrides a group's `hide: true`. A target's `hide: false` unmutes it, but the group header still starts folded. A root group named after an import folds that import's section. A group may be empty (`webapps: {}`). `schemas:` and
`schema:` are the older spelling and still work; a file uses one spelling, not both. Quote each column entry: a `{` inside a plain entry
of a `[...]` list is a YAML error.

#### Auto-hide

`auto-hide: true` on a group or an import lists its targets only while they run, start, restart or fail.
Stopped, never-run, manual and off targets hide. A target that exits 0 hides ten minutes after it exits, so you can read its result.
A target that exits non-zero stays visible.

- A target the pane shows stays listed, so it never vanishes under the cursor.
- A group's last row counts its hidden members: `(+) 12 hidden`. `→`, `enter` or a click shows them dimmed, and the row reads `(−) hide 12`.
- `h` shows or hides the cursor's group's hidden members from any row of that group. A folded header reads `▸ make · 2 running · 12 hidden`.
- `ctrl+k` still finds hidden targets. Running one makes it visible.
- A Makefile import defaults to `auto-hide: true`. Other imports and groups default to off.

```yaml
imports:
  - path: apps/api/Makefile
    auto-hide: false               # keep every rule listed
groups:
  tools:
    auto-hide: true                # default false outside Makefile imports
```

#### Saved queries

The reserved `queries:` key maps a name to a pill string at the top level, per group, and per target:

```yaml
queries:
  errors: level=error|fatal

groups:
  api:
    queries:
      4xx: http.status>=400 http.status<500

targets:
  web:
    run: pnpm run dev
    queries:
      slow: http.duration_ms>500 -healthz
```

In the query bar, `@` lists the pane scope's saved queries: the scope's targets, their groups, then the global
ones; a name clash keeps the most specific. Choosing one adds a `[@name]` pill that matches as its parsed pills.
A bad pill string is a config error naming the file, the key and the pill.

#### Imports

A `rerun.yml` can pull in another project's `rerun.yml` with the reserved
top-level `imports:` key:

```yaml
imports:
  - path: ../bpn-api/rerun.yml   # relative to this file; ~ expands
    group: bpn-api               # the section name; defaults to the file's dir name
    optional: true               # a missing file is skipped until it appears
    hide: true                   # start its targets muted and its section folded
    start: auto                  # first run for its targets; default: the file's own
    groups: [backend]            # import only these groups of that file

targets:
  api:
    cmd: make run
```

- An imported target runs from its own file's dir. It shows under its `group` section as `bpn-api/api`.
- The imported file's groups get the prefix too: `bpn-api/backend`.
- A group in this file with the import's name gives every imported target its columns.
- An import's `hide: true` mutes its targets and folds its section. It is the outermost level: the target's own `muted` or `hide`, and a group's `muted` or `hide`, still win.
- An import's `start:` fills in for its targets where neither they nor their group set one. It is the outermost level: the target's own `start` and a group's `start` still win. A rerun.yml import's targets default to `auto`, a Makefile or compose import's to `manual`.
- A missing optional import shows one dim `bpn-api (not found)` row until its file appears.
- Saving an imported file reloads the config. Column picks save into the file that defines the target or group.
- An `imports:` key inside an imported file is ignored, with a notice. Imports go one level deep.
- The same file imported twice loads once. A file that imports itself is an error.
- An unknown import key is an error. A compose file is imported as its services. See Docker Compose below.
- With a `rerun.yml`, only the files listed under `imports:` load. A Makefile beside it loads only when listed. Startup shows `Makefile not imported · add it under imports:` for an unlisted one. `rerun -migrate` adds the entry for a file that loaded before.

#### Docker Compose

An import of a file with a top-level `services:` key turns each service into a target:

```yaml
imports:
  - path: ../devops/devbox/docker-compose.yml
    group: devbox                    # default: the compose project name
    optional: true
    services: [postgres, redis]      # default: every service
    profiles: [debug]                # compose profiles to activate
```

- A service shows as `devbox/postgres`, with the state, `▶ started`, `✗ exit 137`, `↻ restarted` and health marks.
- `space` starts or stops a service. `r` restarts it.
- Services have no file watching and no build. `docker compose watch` covers that.
- Services start manually by default; an import's `start: auto` brings them up at session start.
- rerun reads container state from `docker events`, and reconciles with `docker compose ps` after every reconnect.
- Logs come from `docker logs -f`. Quitting rerun leaves the containers running.
- If docker stops answering, the footer shows `docker unreachable, retrying`.
- The `docker` CLI must be on PATH. A required import without it is an error. An optional one is skipped with a notice.
- Compose files load only when listed under `imports:`. rerun never auto-imports one.
- `groups:` is an error on a compose import. `services:` and `profiles:` are errors on a rerun import.

#### Makefile

A Makefile is an import source too. rerun reads its target names; it does not run `make -p`.

```yaml
imports:
  - path: apps/api/Makefile        # group defaults to make/api
  - path: tools/Makefile
    group: make/tools
    optional: true
    targets: [lint, gen]           # keep only these targets

groups:
  make:
    hide: false                    # unfold the default group
```

- A `Makefile` next to `rerun.yml` loads by default as a `make` group. It sits last and starts folded. With a `rerun.yml`, it loads only when listed under `imports:`.
- A subfolder's Makefile loads only when you import it. rerun never scans subfolders.
- The default group for a subfolder import is `make/<dir>`, such as `make/api`.
- `groups: make: hide: false` unfolds a Makefile group.
- Subgroups such as `make/api` need their own entry, like `groups: make/api: hide: false`.
- A target's description is its trailing `## text`, or the comment lines directly above it.
- `targets:` keeps only the named targets. It works on Makefile imports only.
- `targets:` fails the load when a listed target is gone from the Makefile.
- Each target is a row that starts manually and does not watch files.
- An import's `start:` overrides that default: `start: auto` brings every rule up at session start, and a target's or group's own `start:` still wins.
- Enter in go to runs a make target that is not running; its row shows `▶`. Any other enter jumps to the row and focuses its pane. `ctrl+k` or `g` opens the go-to finder, whose input reads `go to target or run…`.
- A `.mk` file runs as `make -f <file>`.
- A repo with only a Makefile opens rerun with the finder open.

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
starts it again. A target added while the pane shows one target or group waits for `space`.
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
