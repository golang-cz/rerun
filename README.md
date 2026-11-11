## rerun: the one terminal for your localhost dev

**Think docker-compose + a file watcher + a log explorer, in one native terminal app.
Save a file, and only what depends on it rebuilds, with every log live and searchable.**

Run your services, Makefile targets and docker-compose side by side, and follow and
filter every log in one place. Describe your services in a `rerun.yml`, and rerun runs
them right on your machine: no containers, no image rebuilds.

- **Rebuilds on save.** Only the services that watch the changed files restart.
  The old process keeps serving until the new build passes.
- **One log to rule them all.** Every service streams into one pane. Filter it with
  pills like `level=error` or `http.status>=400`, and turn JSON fields into columns.
- **Everything one keypress away.** Makefile targets, compose services and your
  `open:` links (dashboards, docs, localhost URLs) live in the same TUI.

```bash
$ rerun -- go test ./...    # one command, re-run on every change
$ rerun                     # every target in rerun.yml, in one TUI
```

## Why rerun

- **One terminal for the whole monorepo.** APIs, frontends, workers, codegen, DB
  migrations, tests: each is a target. A change restarts only the targets that watch it.
- **A broken build never takes you down.** A target's `build:` runs first. The old
  process keeps serving until the new build passes.
- **Logs you can actually read.** Every target streams into one merged pane. Filter
  JSON logs with pills like `level=error` or `http.status>=400`. Turn fields into
  table columns. Expand any line.
- **Brings your existing tools along.** Import a Makefile, a `docker-compose.yml`, or
  another repo's `rerun.yml`. They show up as targets, one keypress away.
- **Quiet by design.** Changes batch for 200ms, so a `git checkout` restarts once.
  `touch`, identical saves and `.gitignore`d files never trigger. A target that keeps
  re-triggering itself gets paused, not looped.
- **Live config.** Save `rerun.yml` and only what changed restarts.

## A real-world rerun.yml

Codegen, migrations, an API with a health check, a frontend, and Postgres from compose:

```yaml
imports:
  - path: docker-compose.yml
    services: [postgres]
    start: auto                    # compose services start manually otherwise

targets:
  proto:
    on: proto
    run: make generate

  migrate:
    on: db/migrations
    run: make migrate

  api:
    on: [cmd, internal]
    build: go build -o bin/api ./cmd/api
    run: ./bin/api
    health: http://localhost:8080/health
    open: http://localhost:8080

  web:
    on: web
    except: [node_modules, dist]
    run: pnpm run dev
```

Run `rerun`, then press `?` for keys. Edit a `.proto` file and `proto` regenerates.
If the generated code lands in `internal/`, `api` rebuilds right after it.

## Contents

- [Installation](#installation)
- [Quick start](#quick-start)
- [Command line](#command-line)
- [rerun.yml](#rerunyml): [targets](#target-fields), [groups](#groups), [imports](#imports), [Docker Compose](#docker-compose), [Makefile](#makefile)
- [The TUI](#the-tui): [keys](#keys), [filtering the log](#filtering-the-log), [JSON lines](#json-lines-and-columns)

# Installation

```bash
go install github.com/golang-cz/rerun/cmd/rerun@latest
```
*You might need to [download Go](https://golang.org/dl/) first.*

rerun uses [fsnotify](https://github.com/fsnotify/fsnotify), so it should work on most
platforms, including Linux, macOS and Windows.

# Quick start

```bash
# Watch ./ and re-run the command on any change
$ rerun -- go test ./...

# Explicit watch and ignore lists
$ rerun '**/*.go' --except vendor -- go build ./...

# Run every target from rerun.yml, or only some of them
$ rerun
$ rerun frontend api

# Watchlist from stdin, when stdin is not a TTY
$ find . -name '*.go' | rerun -- go test ./...
```

# Command line

```
rerun                                run all rerun.yml targets in parallel
rerun <target> [target...]           start these rerun.yml targets; the TUI
                                     still lists every target, space starts one
rerun -- <cmd>                       ad-hoc run: watch ./ and run <cmd>
rerun a b --except c d -- <cmd>      bare words = watchlist, --except = ignorelist
rerun --on a b --except c d -- <cmd> --on is an explicit watchlist keyword
```

- Everything after `--` is the command, passed through verbatim.
- Naming targets picks what starts, not what is listed. The TUI still shows every
  `rerun.yml` target. The named ones come up running; the rest stay off until you
  press `space` on them.
- On a terminal, target mode opens the TUI. Piped or non-TTY output stays plain
  prefixed lines.

### Flags

```
-delay <dur>      batch window before restarting a command (default 200ms)
-cooldown <dur>   minimum time between restarts (default 0s)
-grace <dur>      time between SIGTERM and SIGKILL (default 5s)
-no-gitignore     do not filter watched files through .gitignore
-h, -help         show this help
-version          show version
```

Flags override the matching fields of `rerun.yml` targets, e.g. `rerun api -delay 1s`.

**Deprecated:** `-watch`, `-ignore` and `-run` still work in this major version.
Prefer `--on`, `--except` and `--`.

# rerun.yml

Place a `rerun.yml` anywhere in the repo. rerun walks up from the current directory,
stops at the git root, and takes the first one it finds.

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

The top level holds only rerun's own keys:

| Key | What it holds |
|---|---|
| `targets:` | the commands to run, one per key |
| `groups:` | shared settings for related targets, see [Groups](#groups) |
| `queries:` | saved log filters, see [Saved queries](#saved-queries) |
| `imports:` | other `rerun.yml`, compose or Makefile files, see [Imports](#imports) |

Any other top-level key is an error, so typos surface. A target may itself be called
`imports` or `groups`, since targets live under `targets:`.

## Target fields

| Field | Meaning |
|---|---|
| `run` | shell command to run, **required** |
| `desc` | free-form description |
| `on` | watch paths or globs, a string or a list |
| `except` | paths or globs to ignore, a string or a list |
| `build` | shell command run before each (re)start; the old run keeps going until it passes, and a failed build leaves it untouched |
| `health` | `http(s)://` url (200-399 is healthy), `tcp://host:port`, or a shell command (exit 0 is healthy), probed every second |
| `start` | when the first run happens: `auto` (default), `onchange` (holds the run until a file changes), or `manual` (watches nothing, runs only on `r`) |
| `open` | what `ctrl+o` runs (`o` lists it in the link picker): a URL (a value starting with a scheme, such as `http:`) opens in the browser; anything else runs as a shell command in the target's dir, detached, and a failure shows a footer notice |
| `columns` | table-mode columns, each `name {.yq.expr}`, e.g. `["method {.request.method}", "status {.response.status}"]` |
| `queries` | saved query pills for the query bar, name → pill string; the target's own win a name clash with its `group`'s |
| `group` | name of a `groups:` entry to take settings from; the target's own `columns` win |
| `mute` | start the stream muted, `true` or `false` (default false); a group's or import's `mute` fills in when unset. `m` still toggles it |
| `keep` | scrollback this target keeps: a line count (`keep: 5000`) or a time (`keep: 1h`, capped at 200,000 lines). Default 10,000 lines; the group's, then the import's, fills in when unset |
| `delay` | batch window before restarting (default 200ms) |
| `cooldown` | minimum time between restarts (default 0s) |
| `grace` | time between SIGTERM and SIGKILL (default 5s) |

**How commands run.** Plain commands without shell syntax run directly. Anything else
runs via `/bin/sh -c`. To force a shell for a plain command, write `run: sh -c '...'`
or `run: sh ./run.sh`, as in docker compose.

**Columns.** A column can hold literal text and several `{expr}` placeholders.

- Each placeholder evaluates on its own.
- A missing value renders as nothing, and runs of spaces collapse.
- Example: with `columns: ["msg {.message} {.error}"]`, a line with both fields shows
  `disk full ENOSPC`. A line with only `.error` shows `ENOSPC`.
- A bad expression shows `bad column "msg": <error>` in the title row.
- Quote each column entry: a `{` inside a plain entry of a `[...]` list is a YAML error.

## Groups

Targets that log the same JSON shape can share settings. Define a group once under
`groups:`, then point each target at it with `group:`:

```yaml
groups:
  backend:
    columns: ["level {.level}", "msg {.msg}", "status {.status}"]

targets:
  omsx-api:
    group: backend
    run: make -C apps/api run
```

A group can set `columns`, `queries`, `start`, `mute`, `keep` and `fold`.
The target's own value always wins:

- `columns:` and `start:` apply to members that set none.
- `queries:` pass to the members; a target's own entry wins a name clash.
- `mute: true` starts members muted; a target's `mute: false` overrides it.
- `keep:` is the members' default scrollback.
- `fold: true` folds idle members, see [Fold](#fold). Folding never mutes.

Other rules:

- A `group:` that names no group is a config error.
- A group may be empty (`webapps: {}`).
- A root group named after an import takes that import's section.

## Fold

`fold: true` on a group or an import hides its idle targets behind one dim
`(+) N more` row. It keeps the list short when you have many targets.

What stays listed in a folded group:

- every failing member: a failed build, a non-zero exit, or a restart backoff;
- every member that started, restarted or exited in the last 10 minutes;
- the target the pane shows, so it never vanishes under the cursor.

Never-run targets, and targets that ran steadily for over 10 minutes, fold.
`(+) N more` shows only for N of 2 or more; a single folded target is just listed.
An import folds its whole subtree into that one row, and a sub-group with nothing
listed leaves the list.

- `fold: false` lists every member. It is the default, except for Makefile imports.
- The header reads `▾` while any member is listed and `▸` once all are folded. Its
  `running/total` count does not change.
- `→`, `enter`, or a click on the header's arrow or on `(+) N more` expands it.
  `←` on an expanded header folds it again. Your toggles survive config reloads.
- `ctrl+k` still finds folded targets. Jumping to one lists only that target.
- Folding never mutes. Mute a target with `mute: true` or `m`.

```yaml
imports:
  - path: apps/api/Makefile
    fold: false                    # keep every rule listed
groups:
  tools:
    fold: true                     # default false outside Makefile imports
```

## Saved queries

`queries:` maps a name to a pill string (see [Filtering](#filtering-the-log)).
It works at the top level, per group, and per target:

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

- In the query bar, `@` lists the saved queries for the pane's scope: its targets,
  their groups, then the global ones. A name clash keeps the most specific.
- Choosing one adds a `[@name]` pill that matches as its parsed pills.
- A bad pill string is a config error naming the file, the key and the pill.

## Imports

A `rerun.yml` can pull in other files with `imports:`. Three kinds are supported:
another `rerun.yml`, a [Docker Compose](#docker-compose) file, and a [Makefile](#makefile).

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

**Naming.**

- An imported target runs from its own file's dir.
- It shows under its `group` section as `bpn-api/api`.
- The imported file's groups get the prefix too: `bpn-api/backend`.
- A group in this file named after the import gives every imported target its columns.

**Precedence.** Settings resolve as target, then group, then import, then default.

- `mute:` and `keep:` on an import fill in where the target and its group set none.
- `start:` works the same way. A `rerun.yml` import's targets default to `auto`.
  A Makefile or compose import's targets default to `manual`.
- `fold: true` is the outermost level. A root group's `fold` and the file's own
  groups' `fold` apply only where the import sets none.

**Rules.**

- A missing optional import shows one dim `bpn-api (not found)` row until its file appears.
- Saving an imported file reloads the config.
- Column picks save into the file that defines the target or group.
- Imports go one level deep. An `imports:` key inside an imported file is ignored, with a notice.
- The same file imported twice loads once. A file that imports itself is an error.
- An unknown import key is an error.
- With a `rerun.yml`, only the files listed under `imports:` load. A Makefile beside it
  loads only when listed. Startup shows `Makefile not imported · add it under imports:`
  for an unlisted one.
- With no `rerun.yml`, the Makefile and compose file beside it load by default.

### Docker Compose

Importing a file with a top-level `services:` key turns each service into a target:

```yaml
imports:
  - path: ../devops/devbox/docker-compose.yml
    group: devbox                    # default: the compose project name
    optional: true
    services: [postgres, redis]      # default: every service
    profiles: [debug]                # compose profiles to activate
```

- A service shows as `devbox/postgres`, with the state, `▶ started`, `✗ exit 137`,
  `↻ restarted` and health marks.
- `space` starts or stops a service. `r` restarts it.
- Services start manually by default. An import's `start: auto` brings them up at session start.
- Services have no file watching and no build. `docker compose watch` covers that.
- Logs come from `docker logs -f`. Quitting rerun leaves the containers running.
- rerun reads container state from `docker events`, and reconciles with
  `docker compose ps` after every reconnect.
- If docker stops answering, the footer shows `docker unreachable, retrying`.
- The `docker` CLI must be on PATH. A required import without it is an error.
  An optional one is skipped with a notice.
- Compose files load only when listed under `imports:`, or when a dir has no `rerun.yml`
  (see [Makefile](#makefile)). rerun never auto-imports one beside a `rerun.yml`.
- `groups:` is an error on a compose import. `services:` and `profiles:` are errors on a rerun import.

### Makefile

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

**Loading.**

- A `Makefile` next to `rerun.yml` loads by default as a `make` group. It sits last and
  folds its idle targets. With a `rerun.yml`, it loads only when listed under `imports:`.
- With no `rerun.yml`, a compose file loads too, as a group named for its compose project.
  The first of `compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml` wins.
- A repo with only a Makefile opens rerun with the finder open.
- A subfolder's Makefile loads only when you import it. rerun never scans subfolders.
- The default group for a subfolder import is `make/<dir>`, such as `make/api`.
- A `.mk` file runs as `make -f <file>`.

**Targets.**

- A target's description is its trailing `## text`, or the comment lines directly above it.
- `targets:` keeps only the named targets. It works on Makefile imports only.
- `targets:` fails the load when a listed target is gone from the Makefile.
- Each target starts manually and does not watch files.
- An import's `start: auto` brings every rule up at session start.
  A target's or group's own `start:` still wins.

**Folding.**

- `groups: make: fold: false` lists every Makefile target.
- Subgroups such as `make/api` need their own entry, like `groups: make/api: fold: false`.

**Running.** `ctrl+k` or `g` opens the go-to finder, whose input reads
`go to target or run…`. `enter` on a target that is not running runs it, manual and
onchange-waiting ones too; it then shows `▶`. Any other `enter` jumps to the target
and focuses its pane.

## Reloading

rerun watches `rerun.yml` itself. Saving it applies the change without restarting rerun.
Each save prints one line naming what moved:

```
── config reloaded ── +auth-api  ~omsx(cmd)  ↺webhooks(watch) ──
```

| Glyph | Meaning |
|---|---|
| `+name` | target added, it starts |
| `−name` | target removed, its process stops and its watcher goes away |
| `~name(cmd)` | `cmd` changed, that target restarts |
| `↺name(watch)` | `on`, `except`, `delay` or `no-gitignore` changed, only its watcher is rebuilt and the process is left alone |

- A target you stopped by hand stays stopped. A save swaps its spec but never starts it again.
- A target added while the pane shows one target or group waits for `space`.
- `desc` and `open` are cosmetic.
- A config that does not parse changes nothing and only prints the error.

# The TUI

The screen has two parts:

- **Left column:** one row per target, with the fs row on top.
- **Right pane:** one merged log of every target.

Press `?` to list the keys, and `?` again to list them all.

## Target list

**Order.** Targets without a `group:` come first, flat, with no header. Grouped targets
follow under a `▾ name` header that counts its running members.

- Groups appear in the order `rerun.yml` declares its `groups:`.
- Imports that no group names come next, in first-use order.
- Makefile imports come last.
- Targets keep their `targets:` order.

**Markers.** Each row shows the target's name, then a build cell, then two marker cells.

| You see | It means |
|---|---|
| orange braille spinner | building |
| green `✓` | good build, shown for 10 seconds |
| red `✗` | failed build, shown until the next build |
| red `∞` | paused rebuild loop, see below |
| `*` in the marker column | unsaved column picks |
| name tinted orange, 1s | a build just started |
| name tinted green, 1s | a file-triggered restart |
| name red | build failed or the target loops; red wins |
| name light gray, italic | muted |
| name also struck through | dropped with `d` |
| bold name | the target whose logs the pane shows |

Nothing draws in reverse except the cursor and a selection.

**Rebuild loops.** A loop is a file that the target's own build or run keeps writing.
Six self-written re-triggers in a row pause the target for up to 30 seconds.

- The build cell shows a red `∞`.
- The fs hint reads `loop: <file>` in red.
- The fs events modal lists a warning.
- A change after the pause resumes the target and clears all three.

**The fs row** is the first row of the left column. It shows the newest change batch.

- `server.go +1` is the batch's first file, with `+N` for the other files in it.
- A long name cuts its stem with `…` and keeps its extension (`ser….gen.go`).
- It reads `fs idle` when no batch landed in the last 10 seconds.
- After a watcher error it turns red, `fs ! too many open files`, for 10 seconds.
- `t` adds the batch's age in the times column, lined up with the target durations.
- A click opens the fs events modal, like `e`.

**Muting.** `m` mutes a target's stream and posts a footer notice (`api muted · m unmutes`).
`mute: true` in `rerun.yml` starts it muted; `m` still toggles it. `d` drops the target's
older output and mutes it; the footer says `api dropped · earlier output gone`. A second
`d` restores its `keep:` for new lines and unmutes.

## Log pane

The pane is always the merged stream. Selecting a target narrows it to that target.
Selecting a group narrows it to its members. Name prefixes stay either way.

- Each line pads the name to the longest target, so every message starts in the same column.
- Each line shows the `HH:MM:SS` it arrived, in a dim column before it. This needs a
  pane wide enough to spare it.
- The top line shows the cursor target's state, uptime, how many files its last
  change batch held, and its command, with its url linked.
- A muted target reads `muted` there. Uptime reads `7s`, `2m03s`, `1h02m`.
- Moving the cursor off the newest line pauses the tail. `esc` returns to the live tail.

**Build output** shows inline. Each build is one `⚙` rule in the stream, rewritten in
place as `built <duration>` or `✗ build failed`.

- Its output collapses under the rule. Click the icon or `+N more`, or press `enter`, to open it.
- A failed build opens itself.
- A `▶` rule collapses its command when the pane cuts it short.

## Keys

### Target list

| Key | Action |
|---|---|
| `j` / `k` | move the cursor; this scopes the pane to it |
| `[` / `]`, `shift+↑` / `shift+↓` | jump between groups |
| `→` | expand a folded group, else focus the log pane |
| `←` | fold an expanded group |
| `space` | start or stop the target |
| `r` | reboot the target |
| `s` | stop the target |
| `m` | mute or unmute its stream |
| `d` | drop its older output and mute it |
| `esc` | back the scope out: a target to its group, a group to all |

On a group header, `space`, `r` and `m` act on all its targets.

### Log pane

`→` from the list focuses the pane. The cursor lands on the service's last line,
or on the newest line when that is off screen.

| Key | Action |
|---|---|
| `↑` / `↓` | move the pane cursor |
| `→` | expand the line under the cursor, else scroll right |
| `←` | collapse the line, else scroll left, else back to the list |
| `home` / `end` | jump to the top-left / the live tail |
| `ctrl+u` / `ctrl+d` | scroll half a page |
| `space` | jump to the live tail |
| `esc` | go to the live tail first, then back out of the list |
| `enter` | expand or collapse a JSON line or build output |
| `shift+↑` / `shift+↓` | select lines from the cursor, like a drag |
| `cmd+a` (`alt+i`) | select every line the pills kept |
| `c` | copy the selected lines, or the cursor line |
| `i` | open the original lines, see [Raw view](#raw-view) |
| `a` | on an expanded key: add it as a column |
| `=` | on an expanded scalar: add a `path=value` pill |
| `backspace` | remove a pill |
| `n` / `N`, `ctrl+g` / `ctrl+shift+g` | next / previous pill hit |
| `ctrl+l` | clear the log; older lines stay in scrollback |
| `s` | stop every target the pane shows |

Any key other than `shift+↑` / `shift+↓` clears a selection.

### Everywhere

| Key | Action |
|---|---|
| `?` | list keys; again for all keys |
| `/` or `f` | open the query bar |
| `ctrl+k` or `g` | open the go-to finder |
| `b` | hide or show the left list; hidden shows a `b list` cue, and `esc` brings it back |
| `t` | show times in the left list: durations, the fs hint age and the build age |
| `e` | open the fs events list (last 10 min) |
| `o` | open a picker over every `open:` link |
| `ctrl+o` | open the target's `open:`; on a group header, say what it opens |
| `ctrl+s` | save column picks to `rerun.yml` |
| `alt+z` / `alt+y` | undo / redo in the query bar, go-to and the link picker (⌘Z/⌘Y in Ghostty) |
| `q` or `ctrl+c` | ask to quit; `enter` or a second `ctrl+c` quits |

`ctrl+o` says so when the target has no `open:`. `cmd+s` works only where the terminal
forwards it. Copy uses the terminal's OSC 52 clipboard sequence, which Ghostty and
herdr pass through.

### Mouse

| Action | Result |
|---|---|
| wheel | scroll the list or the log, also sideways in the log |
| click a target | show only its stream |
| double-click a target | stop or start it, like `space` |
| click a group header | select its targets |
| click the fs row | open the fs events |
| click a pane line | copy it; on an expandable line, expand or collapse it |
| drag in the pane | copy a range and browse the scrollback; `esc` or `↓ N new` resumes |
| shift-drag | the terminal's own selection |

A click is press and release on one cell. A press that moves is a drag, which
selects text and never toggles.

## Filtering the log

`/` or `f` opens the query bar. Type a filter and press `enter` to add it as pills.

- One input can hold several pills: `level=error -healthz` adds two.
- Pills AND together.
- A leading `.` runs a yq expression per JSON line.
- `esc` leaves the bar but keeps the pills.
- `backspace` on an empty input removes the last pill.
- `@` lists the scope's [saved queries](#saved-queries).
- The bar shows the scope, the pill count and the matching/total lines.
- A pill that does not parse keeps the input red, with the error in the footer.

**Filters and highlights.** Each pill is one of two kinds:

- a filter, `[text]`, solid: hides non-matching lines;
- a highlight, `⟨text⟩`, dim: only marks its hits.

Every pill's matches are marked in the pane. `n`/`N`/`ctrl+g`/`ctrl+shift+g` jump
between hit lines.

**Pill syntax.**

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

- A field name is a yq path without the leading dot: `http.status` ≡ `.http.status`.
- A column name from the scope's `columns:` resolves to that column's expression first.
- A field pill never matches a non-JSON line. Full-text pills match any line.
- A selection with `columns` always shows them as a table.

**Editing pills.** With the bar open:

| Key | Action |
|---|---|
| `←` / `→` | move the text cursor |
| `↑` | move focus to the pills row |
| `←` / `→` (pills row) | pick a pill |
| `x`, `delete`, `backspace` | remove the picked pill |
| `enter` or `↓` | move its text back into the input; `enter` puts it back in place |
| `space` | toggle filter ⇄ highlight |
| `→` on the last `[@name]` pill | expand it into its pills |
| `esc` | return to the input |

With the bar closed, a click on a pill opens the bar with that pill picked. Click a
pill's `×`, or press `backspace` with the pane focused, to remove it.

## JSON lines and columns

Every JSON object line ends in a `(+)` cell. Expanding it shows the pretty-printed
object, and `(−)` collapses it.

- Click the line, or press `enter` with the pane focused, to toggle it.
- A click on a row of an open expansion collapses it.
- Opening a block scrolls only as far as needed to show it.
- Expanded keys are underlined and values tinted.

**Building a table.**

- `a` on an expanded key adds it as a column.
- A click on an expanded key adds it as a column too. It goes to the selected group,
  else the target the pane shows, else a hint asks you to select one.
- `=` on a scalar, or a click on a scalar value, adds a `path=value` pill.
- Click the `×` after a column name in the table header to remove that column.
  A click on the name does nothing.

**Saving columns.** Column picks are unsaved until `ctrl+s` writes them to `rerun.yml`.

- Until then the footer reads `unsaved columns · ctrl+s save`.
- An unsaved group or target gets a `*` in its marker column.
- A `columns:` list that a YAML alias uses elsewhere is not rewritten. The footer
  names it, and you edit `rerun.yml` by hand.
- Pills and expansions last for the session only.

## Raw view

With the pane focused, `i` opens the original lines in a modal.

- It shows the cursor line exactly as printed: no cut, columns or color. Long lines wrap.
- A line selection shows those lines as NDJSON.
- `cmd+a` then `i` shows the raw filtered log.
- `f` toggles raw and pretty JSON, pretty only when every line is JSON.
- `c` copies what the modal shows. `esc` closes it.

## fs events

`e`, or a click on the fs row, opens the fs events modal. It keeps the last 10 minutes
of change batches, newest first:

```
22:32:00  path +N  ↻ api, web
```

- A batch is every change within 150ms of its first event. One save across several
  targets is one line.
- After a 2-space gap, `↻` lists the targets it restarted, or `config` for a `rerun.yml` reload.
- The path is the first changed file, relative to the `rerun.yml` dir. When the row
  is still too long after the target list shortens to `a, b +N more`, the path is cut
  with a middle ellipsis.
- A created directory keeps a trailing `/` and heads the files reported under it.
- A watcher error is one red line above the batches, never a row.
- `→` lists a batch's files with `+`/`~`/`−` glyphs. Each names the services whose watch
  globs it matched, wrapping when they don't fit. `←` collapses.
- `c` copies every batch, expanded, to the clipboard.
- A target a batch restarted lights up for a second.

# License

Licensed under the [MIT License](./LICENSE).
