rerun: re-run command on file system changes
============================================

Credit for this library goes to https://github.com/VojtechVitek/rerun

Lightweight file-watcher that re-runs given command on FS changes. It has simple CLI and optional config file. By default, it uses 200ms delay, which gives enough time for tools like git to update all directories/files within repository before killing the old process (when you switch branches etc).

touch and saves with identical content don't trigger a rebuild.

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
`e` fs events (last 10 min), `m` mute a stream, `d` drop its older output and mute it. `b` hides or shows the left list: hidden gives a full-width log with a `b list` cue on the bottom line, and `esc` there shows the list again. `t` shows times in the left list only: durations, the fs hint age and the build age. Every log line always shows the `HH:MM:SS` it arrived, in a dim column before the line (1 s precision, and only on a pane wide enough to spare it). Focusing the pane puts its cursor on the service's last line, or the newest line when that is off screen. Moving the cursor
scopes the pane to it; `esc` backs the scope out (a target to its group, a group to all).
The target whose logs the pane shows is bold. On a group header, `space`, `r`, and `m` act on its targets; `[`/`]` or `shift`+`↑`/`↓` jump between groups. `o` opens a picker over every `open:` link (a URL, or a command it runs). `ctrl+o` opens the target's `open:`, and says so when it has none; on a group header it says what `ctrl+o` opens; with the pane focused, `i` opens the original or JSON lines (see below). `alt+z` / `alt+y` (⌘Z/⌘Y in Ghostty) undo and redo edits in the query bar, Go-to and the link picker. Scrolling up browses the
scrollback; `esc` returns to the live tail, and `q` or `ctrl+c` ask first; `enter` or a second `ctrl+c` quits.
`/` or `f` opens the query bar: `enter` adds the input as pills (a leading `.` runs a yq expression per JSON line);
`esc` leaves the bar but keeps the pills, `backspace` on an empty input removes the last pill; a selection with `columns`
always shows them as a table. 
More keys: `c` copies the selected lines, or the pane cursor line when nothing is selected; `e` opens the fs events list (last 10 min), `s` stops the target (with the pane focused, every target the pane shows), `space` with the pane focused jumps to the live tail, `→` expands the line under the cursor and `←` collapses it, `ctrl+g` and `ctrl+shift+g` step to the next and previous pill hit, and `ctrl+l` clears the log (older lines stay in scrollback), and `ctrl+u` / `ctrl+d` scroll the pane half a page. The mouse wheel scrolls the list or the log. With the pane focused, `shift`+`↑`/`↓` select lines from the cursor, the same highlight as a drag; any other key clears it. Copy sends the text through the terminal's OSC 52 clipboard sequence, which Ghostty and herdr pass through.

The query bar's pills AND together, and one input can hold several: `level=error -healthz` adds two on `enter`.
Each pill is a filter (`[text]`, solid) or a highlight (`⟨text⟩`, dim): a filter pill hides non-matching lines, a
highlight pill only marks its hits. Every pill's matches are marked in the pane, and `n`/`N`/`ctrl+g`/`ctrl+shift+g`
jump between the hit lines. With the bar open, `←`/`→` move the text cursor; `↑` moves focus to the pills row, where `←`/`→` pick, `x`, `delete` or `backspace` removes the picked one, `enter` or `↓` moves its text back into the input and `enter` puts it back in place, `space` toggles filter ⇄ highlight, `→` on the last `[@name]` pill expands it into its pills, and `esc` returns to the input. A click on a pill with the bar closed opens it picked. A pill that does not parse keeps the input red, with the error
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
cursor on an expanded key, `a` adds it as a column; on a scalar, `=` adds a `path=value`
pill. In an expanded block, click a key to add it as a column of the
selection: the selected schema, else the target the pane shows, else a hint says to select one.
Build output shows inline: each build is one `⚙` rule in the stream, rewritten in place as it
becomes `built <duration>` or `✗ build failed`. Its output collapses under the rule: click the icon or
`+N more`, or press `enter`. A failed build opens itself, and a `▶` rule collapses its command when the
pane cuts it short.
Click a scalar value to add a `path=value` pill to the pane header. Pills AND together;
click a pill's `×`, or press `backspace` with the pane focused, to remove one.
Click the `×` after a column name in the table header to remove that column; a click on
the name does nothing. Column picks are unsaved until `ctrl+s` writes them to rerun.yml;
the footer reads `unsaved columns · ctrl+s save` until then, and a schema or target with
unsaved columns gets a `*` in its marker column, two cells after its name. A `columns:` list that a YAML alias uses elsewhere is
not rewritten: the footer names it, and you edit rerun.yml by hand. Chips and expansions last for the session only. `cmd+s` works only where the
terminal forwards it.

With the pane focused, `i` opens the original lines: the cursor line exactly as printed, with no cut, columns or color, and long lines wrap. A line selection (`shift`+`↑`/`↓` or a drag) shows those lines as NDJSON. `cmd+a` (`alt+i`) selects every line the pills kept, so `cmd+a` then `i` shows the raw filtered log. `f` toggles raw and pretty JSON, pretty only when every line is JSON. `c` copies what the modal shows, and `esc` closes it.

Mouse: click a target to show only its stream, double-click to stop/start like `space`,
the fs row at the top to open the fs events, and a click on a schema header to select
its targets. Click a pane line to copy it; clicking an expandable line expands or
collapses it instead, and drag copies a range and browses the scrollback
(`esc` or `↓ N new` resumes). Shift-drag uses the terminal's own selection.
Targets without a `group:` are listed first, flat, with no header. Targets with one follow under a `▾ name` header that counts
its running members, in the order `rerun.yml` declares its `groups:`; imports that no group names come after, in first-use order, and Makefile imports last. Targets keep their `targets:` order. The right pane is always the
merged stream; clicking a target narrows it to that target, and selecting a schema to its
members, keeping the name prefixes. Each
line pads the name to the longest target, so every message starts in the same
column. The pane's top line shows the cursor target's state, uptime, how many files its
last change batch held, and its command, with its url linked. It names a muted
target in words (`muted`), and uptime reads `7s`, `2m03s`, `1h02m`.
Each target keeps two marker cells after its name: `*` marks unsaved columns. Muting a target posts a footer notice (`api muted · m unmutes`).
A `mute: true` in `rerun.yml` starts the stream muted; `m` still toggles it. A muted target's name is light gray and italic, so it shows on the cursor too. `d` drops the target's older output and mutes it: the name is also struck through, and the footer says `api dropped · earlier output gone`. A second `d` restores its `keep:` for new lines and unmutes. A group's `fold: true` folds its idle members into a `(+) N more` row; see Fold below. Folding never mutes.
The build cell after each name shows an orange braille spinner while building, a green `✓` for 10 seconds after a good build, and a red `✗` until the next build. The name itself tints orange for 1 second after a build starts, green for 1 second after a file-triggered restart, and red while the build is failed or the target loops; red wins. Nothing draws in reverse except the cursor and a selection. A rebuild loop is a file that the target's own build or run keeps writing: six self-written re-triggers in a row pause the target for up to 30 seconds. While paused, the build cell shows a red `∞`, the fs hint reads `loop: <file>` in red, and the fs events modal lists a warning. A change after the pause resumes the target and clears all three.

The first row of the left column is the fs row: `server.go +1` gives the newest
change batch's first file, with `+N` for the other files in the batch. A long name cuts its
stem with `…` and keeps its extension (`ser….gen.go`). `t` adds the batch's age in the times
column, lined up with the target durations. It reads `fs idle` when no batch landed in the last 10 seconds. It updates in place, and
turns red `fs ! too many open files` after a watcher error, for the same 10 seconds. A click
opens the fs events modal, like `e`. The modal keeps the last 10 minutes of batches, newest first: `22:32:00  path +N` with
the targets it restarted, after a 2-space gap and a `↻` mark (`config` for a rerun.yml
reload). A batch is every change within 150ms of its first event, so one save
across several targets is one line. The path is the first changed file relative
to the rerun.yml dir, cut with a middle ellipsis when the row is still too long
after the target list shortens to `a, b +N more`. A created directory keeps a
trailing `/` and heads the files reported under it. A watcher error is one red
line above the batches, never a row. `→` lists a batch's files with
`+`/`~`/`−` glyphs, each naming the services whose watch globs it matched,
wrapping to continuation lines when they don't fit; `←` collapses. `c` copies
every batch expanded to the clipboard. A target a batch restarted
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
| `run` | shell command to run, required |
| `build` | shell command run before each (re)start; the old run keeps going until it passes, and a failed build leaves it untouched |
| `health` | `http(s)://` url (200-399 is healthy), `tcp://host:port`, or a shell command (exit 0 is healthy), probed every second |
| `open` | what `ctrl+o` runs (`o` lists it in the link picker): a URL (a value starting with a scheme, such as `http:`) opens in the browser; anything else runs as a shell command in the target's dir, detached, and a failure shows a footer notice. |
| `columns` | table-mode columns, each `name {.yq.expr}`, e.g. `["method {.request.method}", "status {.response.status}"]` |
| `queries` | saved query pills for the query bar, name → pill string; the target's own win a name clash with its `group`'s |
| `group` | name of a `groups:` entry to take columns from; the target's own `columns` win. |
| `mute` | start the stream muted, `true` or `false` (default false); a group's or import's `mute` fills in when unset. `m` still toggles it |
| `keep` | scrollback this target keeps: a line count (`keep: 5000`) or a time (`keep: 1h`, capped at 200,000 lines). Default 10,000 lines; the group's, then the import's, fills in when unset |
| `start` | when the first run happens: `auto` (default), `onchange` (holds the run until a file changes), or `manual` (watches nothing, runs only on `r`) |
| `delay` | batch window before restarting (default 200ms) |
| `cooldown` | minimum time between restarts (default 0s) |
| `grace` | time between SIGTERM and SIGKILL (default 5s) |

Plain commands without shell syntax run directly; anything else runs via `/bin/sh -c`.

To force a shell for a plain command, write `run: sh -c '...'` or `run: sh ./run.sh`, as in docker compose.

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

- The top level holds only rerun's own keys: `imports:`, `groups:`, `targets:` and `queries:`. Any other top-level key is an error, so a typo surfaces.
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
    run: make -C apps/api run
```

A target's own `columns` wins over its group's. A group's `queries:` pass their saved
queries to the members, and a target's own entry wins a name clash. A group's `start:` sets the
first run for its members, and a target's own `start:` wins. A `group:` that names no
group is a config error. A group's `mute: true` starts its members muted, and a
target's own `mute: false` overrides it. A group's `keep:` is its members' default scrollback, and a target's own `keep:` wins. A group's `fold: true` folds its idle members; folding never mutes. A root group named after an import takes that import's section. A group may be empty (`webapps: {}`). Quote each column entry: a `{` inside a plain entry
of a `[...]` list is a YAML error.

#### Fold

`fold: true` on a group or an import folds its idle targets into one dim `(+) N more` row. An import folds its whole subtree into that one row, and a sub-group with nothing listed leaves the list. `(+) N more` shows only for N of 2 or more; a single folded target is just listed.
A folded group still lists every failing member, and every member that started, restarted or exited in the last 10 minutes.
Failing means a failed build, a non-zero exit, or a restart backoff. Never-run targets, and targets that ran steadily for over 10 minutes, fold.
`fold: false` lists every member. It is the default.

- The header reads `▾` while any member is listed and `▸` once all are folded. Its `running/total` count does not change.
- `→`, or a click on the header's arrow, expands a folded group. `enter` or a click on `(+) N more` does too.
- `←` on an expanded header folds it again. Your toggles stay across config reloads.
- A target the pane shows stays listed, so it never vanishes under the cursor.
- `ctrl+k` still finds folded targets. Jumping to one lists only that target.
- Folding never mutes. Mute a target with `mute: true` or `m`.
- A Makefile import defaults to `fold: true`.

```yaml
imports:
  - path: apps/api/Makefile
    fold: false                    # keep every rule listed
groups:
  tools:
    fold: true                     # default false outside Makefile imports
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
    fold: true                   # fold its idle targets; default false
    mute: true                   # start its targets muted; default false
    keep: 2000                   # scrollback per target: lines, or a time like 1h
    start: auto                  # first run for its targets; default: the file's own
    groups: [backend]            # import only these groups of that file

targets:
  api:
    run: make run
```

- An imported target runs from its own file's dir. It shows under its `group` section as `bpn-api/api`.
- The imported file's groups get the prefix too: `bpn-api/backend`.
- A group in this file with the import's name gives every imported target its columns.
- An import's `mute:` and `keep:` fill in for targets whose own file and group set none; precedence is target, group, import, default. An import's `fold: true` folds its idle targets. It is the outermost level: a root group's `fold` and the file's own groups' `fold` apply only where the import sets none.
- An import's `start:` fills in for its targets where neither they nor their group set one. It is the outermost level: the target's own `start` and a group's `start` still win. A rerun.yml import's targets default to `auto`, a Makefile or compose import's to `manual`.
- A missing optional import shows one dim `bpn-api (not found)` row until its file appears.
- Saving an imported file reloads the config. Column picks save into the file that defines the target or group.
- An `imports:` key inside an imported file is ignored, with a notice. Imports go one level deep.
- The same file imported twice loads once. A file that imports itself is an error.
- An unknown import key is an error. A compose file is imported as its services. See Docker Compose below.
- With a `rerun.yml`, only the files listed under `imports:` load. A Makefile beside it loads only when listed. Startup shows `Makefile not imported · add it under imports:` for an unlisted one.

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
    fold: false                   # list every Makefile target
```

- A `Makefile` next to `rerun.yml` loads by default as a `make` group. It sits last and folds its idle targets. With a `rerun.yml`, it loads only when listed under `imports:`.
- A subfolder's Makefile loads only when you import it. rerun never scans subfolders.
- The default group for a subfolder import is `make/<dir>`, such as `make/api`.
- `groups: make: fold: false` lists every Makefile target.
- Subgroups such as `make/api` need their own entry, like `groups: make/api: fold: false`.
- A target's description is its trailing `## text`, or the comment lines directly above it.
- `targets:` keeps only the named targets. It works on Makefile imports only.
- `targets:` fails the load when a listed target is gone from the Makefile.
- Each target starts manually and does not watch files.
- An import's `start:` overrides that default: `start: auto` brings every rule up at session start, and a target's or group's own `start:` still wins.
- Enter in go to runs a target that is not running, manual and onchange-waiting ones too; its target shows `▶`. Any other enter jumps to the target and focuses its pane. `ctrl+k` or `g` opens the go-to finder, whose input reads `go to target or run…`.
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
`desc` and `open` are cosmetic.
A config that does not parse changes nothing and only prints the error.

# Installation

```bash
go install github.com/golang-cz/rerun/cmd/rerun@latest
```
*You might need to [download Go](https://golang.org/dl/) first.*

Uses [fsnotify](https://github.com/fsnotify/fsnotify) behind the scenes, so technically it should work on most platforms including Linux, Mac OS and Windows.

# License

Licensed under the [MIT License](./LICENSE).
