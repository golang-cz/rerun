---
name: rerun
description: Re-run a command on file system changes — CLI usage and rerun.yml target authoring
---

# rerun

`rerun` watches files and restarts your command whenever they change.
One invocation keeps running until you press Ctrl-C.

## Running

### Ad-hoc: run one command

```bash
rerun -- go test ./...
```

Watches the current directory (`.gitignore` respected) and restarts
`go test ./...` on every change.

`.gitignore` files are read at startup, so an edit to one takes effect on the
next `rerun`, not live.

With explicit lists:

```bash
rerun --on ./pkg ./cmd --except vendor '*.md' -- go test ./...
```

- Bare words before `--except` are the watch list (`--on` is an optional synonym).
- `--except` starts the ignore list; both are shell word-split, so globs need quoting.
- Watch/ignore patterns use `.gitignore`-style syntax: `vendor/`, `*.md`, `**/*.go`.

Pipe a file list instead (entr-style):

```bash
find . -name '*.go' | rerun -- go test ./...
```

### Targets: rerun.yml

With a `rerun.yml` in the repo (looked up from the current directory up to the
git root):

```bash
rerun                  # run ALL targets in parallel
rerun frontend api     # run a subset
rerun test             # run one target (flags allowed: rerun test -delay 500ms)
```

Each target gets its own watcher and command; non-TTY output prefixes each
line with the target's name.
A target that exits by itself stays down until its next matching file change.

Naming targets sets what starts, not what is listed. In the TUI every rerun.yml
target keeps its row: the named ones start running, the rest show `·` (off) and
`space` starts one on demand. Piped or non-TTY output stays plain prefixed text
and runs the named targets only.

On a terminal, target mode opens the TUI: a row per target on the left, the
merged stream on the right. Piped or non-TTY output stays plain prefixed text.
Press `?` inside it for the short key list; `?` again (or `m`) shows every key.

Keys: `j/k` move, `space` start/stop, `r` reboot, `m` mute a target's stream, `d` drop its older output and mute it,
`enter` focuses the pane, and its cursor lands on the service's last line (or the newest line when that is off screen). The cursor target narrows the pane to its target or
schema's targets (`esc` backs out), and that target is bold. On a schema header, `space`, `r`, and `m` act on its
targets. `o` opens a picker over every `open:` link (a URL, or a command it runs). `ctrl+o` opens the target's `open:`, and says so when it has none; on a group header it says what `ctrl+o` opens; with the pane focused, `i` opens the original or JSON lines (see below). `alt+z` / `alt+y` (⌘Z/⌘Y in Ghostty) undo and redo edits in the query bar, Go-to and the link picker. `esc` returns to the live output. The right pane is always the merged stream; the cursor target
narrows it to one target, keeping the name prefixes; each line pads the name to
the longest target so messages start in a fixed column. A `mute: true` field in rerun.yml starts a target's stream muted (a group or an import may set it too); `d` drops a target's older output and mutes it, and a second `d` restores it. A muted name is light gray and italic. `keep:` sets each target's scrollback, a line count (`keep: 5000`) or a time (`keep: 1h`, at most 200,000 lines), default 10,000 lines; target, then group, then import wins. Ungrouped targets list first with no header, groups follow in `groups:` order, then undeclared imports, Makefile imports last. A group's `fold: true` folds its idle members into a `(+) N more` row, and folding never mutes. The pane's top line is
the cursor target's command, with its url linked. Mouse: click a target to show only its
stream, double-click to stop/start like `space`, and a click on a schema header to select its targets. `m` mutes; the footer notice names it.

More keys: `b` hides or shows the left list: hidden gives a full-width log with a `b list` cue on the bottom line, and `esc` there shows the list again. `t` shows times in the left list only: durations, the fs hint age and the build age beside the version. Every log line always shows its `HH:MM:SS` in a dim column before it (selection, copy and search never include it). `c` copies the selected lines, or the pane cursor line when nothing is selected; `e` opens the fs events list (last 10 min), `s` stops the target (with the pane focused, every target the pane shows), `space` with the pane focused jumps to the live tail, `→` expands the line under the cursor and `←` collapses it, `ctrl+g` and `ctrl+shift+g` step to the next and previous pill hit, and `ctrl+l` clears the log (older lines stay in scrollback), and `ctrl+u` / `ctrl+d` scroll the pane half a page. The mouse wheel scrolls the list or the log. With the pane focused, `shift`+`↑`/`↓` select lines from the cursor, the same highlight as a drag; any other key clears it.

`/` or `f` opens the query bar: `enter` turns the input into pills that AND together (`level=error -healthz`
adds two). Pill syntax: `timeout` full text, `"two words"` phrase, `-healthz` negated, `level=error` field equals
(`!=`, `>`, `>=`, `<`, `<=`, `~` contains, `~/re/` regex, `=*` exists, `!=*` missing; `level=error|fatal` is any-of),
`.expr` raw yq. A field name is a yq path without the leading dot; a scope column name resolves to its expression.
`↑` moves focus to the pills row: `←`/`→` pick, `delete`/`backspace` removes, `enter`/`↓` edits the text back into the input (`enter` puts it back in place), `space` toggles filter
⇄ highlight, `→` on the last `[@name]` expands it, `esc` returns to the input. With the bar closed, clicking a pill opens it picked. Filter pills hide lines; every pill's matches are marked, and `n`/`N` jump
between hits. `@` lists the pane scope's saved queries from the `queries:` keys (top level, per group, per target;
most specific wins a name clash). `esc` keeps the pills; `backspace` on an empty input removes the last one.

Build output shows inline: each build is one `⚙` rule in the stream, rewritten in place as it
becomes `built <duration>` or `✗ build failed`. Its output collapses under the rule: click anywhere on
the rule line, or press `enter`. A failed build opens itself, and a `▶` rule collapses its command when the
pane cuts it short; a click on a cut rule expands it to the full command.

With the pane focused, `i` opens the original lines: the cursor line exactly as printed, with no cut, columns or color, and long lines wrap. A line selection (`shift+↑/↓` or a drag) shows those lines as NDJSON. `cmd+a` (`alt+i`) selects every line the pills kept, so `cmd+a` then `i` shows the raw filtered log. `f` toggles raw and pretty JSON, pretty only when every line is JSON. `c` copies what the modal shows, and `esc` closes it.

Expand JSON lines: each JSON object line shows a `(+)` cell at the right edge. A click on
it, or `enter` with the pane focused, expands the pretty-printed object; `(−)` collapses
it. A click anywhere on an expandable line toggles it too, and a click on a row of an open
block collapses it. A click means press and release on one cell: a press that moves is a
drag, which selects text and never toggles. A click on a line with nothing under it
copies the line. Clicks on a block's key and value cells still pick a column or pill and
never collapse the block. The pane cursor (a reverse-video line) starts on the newest line; `↑/↓` move it when
the pane is focused, and moving it off the live tail pauses the tail (`end` resumes).
`enter` acts on the cursor line, and a non-JSON line gets a hint instead. Opening a block
pauses the tail and scrolls only as far as needed: a block that runs past the bottom scrolls until its last row shows, and one taller than the pane puts its line at the top. Expanded keys are underlined and scalar
values tinted. The first expansion of a session shows the click hint.
In an expanded block, a click on a key adds it as a column of the selection (a selected
schema, else the target the pane shows), and a click on a scalar adds a `path=value` pill
(ANDed with the other pills; `×` or `backspace` removes it). With the pane focused and the cursor on
an expanded key or scalar row, `a` adds the key and `=` adds the value pill, as the
clicks do. A table header column name is inert; only the `×` after it removes that
column. A schema or target whose columns are unsaved shows a `*` in its marker column, two cells after its name. Picks are
unsaved until `ctrl+s` writes them with the rerun.yml column writer; the footer shows
`unsaved columns · ctrl+s save`, and the quit modal says so too. A target with a schema
and no columns of its own writes its schema. A `columns:` list that a YAML alias uses elsewhere
is refused with a footer error (edit rerun.yml by hand). Writes keep the file's other bytes, indent,
and line endings, and follow a symlink to the file it names. `cmd+s` works only where the terminal
forwards it.

The build cell after each name shows an orange braille spinner while building, a green `✓` for 10 seconds after a good build, and a red `✗` until the next build. The name itself tints orange for 1 second after a build starts, green for 1 second after a file-triggered restart, and red while the build is failed or the target loops; red wins. Nothing draws in reverse except the cursor and a selection. A rebuild loop is the core detector's pause: six self-written re-triggers in a row pause the target for up to 30 seconds. While paused, the cell shows a red `∞`, the fs hint reads `loop: <file>` in red, and the fs events modal lists a warning. A change after the pause resumes the target and clears all three. The modal never auto-opens.

The fs row at the top of the left column names the newest change batch's first
file (`server.go +2`, `+N` for the other files in the batch, `fs idle` past 10 seconds); `t`
adds the batch's age in the times column, updates in place, and turns red `fs ! <error>`
after a watcher error; a click (or `e`) opens the fs events modal. The modal lists the last 10 minutes' batches (events
within 150ms of each other), newest first: the time, the first changed file with
`+N` for the rest, and the targets the batch restarted after a 2-space gap and a `↻` mark.
`→` expands a batch's files, each naming the services whose watch globs it
matched; `c` copies every batch expanded. A created directory shows a trailing `/`
and heads the files reported under it. The pane's top line
names a muted target (`muted`). A rerun.yml reload shows as the `config`
target. Batches older than 10 minutes drop off.

### Fold: idle targets

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

### Imports: other projects' rerun.yml

A `rerun.yml` can list other rerun files under `imports:`, one map per file:

```yaml
imports:
  - path: ../bpn-api/rerun.yml   # relative to this file
    group: bpn-api               # section name and target prefix; default: the dir name
    optional: true               # skip the file while it is missing
    fold: true                   # fold its idle targets; default false
    start: auto                  # first run for its targets; a target's own wins
    groups: [backend]            # only these groups of that file
```

Imported targets run from their own dir, and their names read `bpn-api/api`.
An import's `fold: true` folds its idle targets. The import's `fold` outranks a root group's and the file's own groups'.
An import's `start:` is the outermost level too: a target's own `start` and a group's `start` win.
A rerun.yml import's targets default to `auto`; a Makefile or compose import's to `manual`.
The import's `path` is required. Unknown keys are errors. An `imports:` key in an
imported file is ignored. A compose file is imported as its services. See Docker Compose below.

With a `rerun.yml`, only the files listed under `imports:` load. A Makefile beside it loads only when listed. Startup shows `Makefile not imported · add it under imports:` for an unlisted one.

### Makefile: a make file's targets

A Makefile is an import source too. Its targets become rows, read as text, never run through `make -p`.

```yaml
imports:
  - path: apps/api/Makefile        # group defaults to make/api
  - path: tools/Makefile
    group: make/tools
    targets: [lint, gen]           # keep only these
```

- A `Makefile` next to `rerun.yml` loads as a `make` group that folds its idle targets by default. With a `rerun.yml`, it loads only when listed under `imports:`.
- Subfolder Makefiles load only when imported. rerun never scans subfolders.
- `groups: make: fold: false` lists every Makefile target.
- Descriptions come from a trailing `## text` or the comment above the target.
- Targets start manually and do not watch files. Enter in Ctrl+K runs one.
- An import's `start: auto` overrides that default; a target's or group's own `start:` still wins.
- A `.mk` file runs as `make -f <file>`.

### Docker Compose: a compose file's services

An import of a file with a top-level `services:` key makes each service a target:

```yaml
imports:
  - path: ../devops/devbox/docker-compose.yml
    group: devbox           # default: the compose project name
    optional: true
    services: [postgres]    # default: every service
    profiles: [debug]
```

- Services show as `devbox/postgres`, with state, exit, restart and health rules.
- Space starts or stops a service. `r` restarts it. Services never watch files or build.
- Services start manually by default; an import's `start: auto` starts them at session start.
- State comes from `docker events` and `docker compose ps`. Logs come from `docker logs -f`.
- Quitting rerun leaves the containers running.
- Compose files load only when listed under `imports:`, or when a dir has no `rerun.yml`. rerun never auto-imports one beside a `rerun.yml`.
- Needs the `docker` CLI on PATH. `groups:` is an error on a compose import.

## Building rerun.yml

Flat top-level map, one entry per target (Makefile-like). `run` is required;
everything else is optional. Durations are Go-style strings (`200ms`, `5s`).

```yaml
targets:
  frontend:
    desc: Vite dev server          # shown in output listings
    on: web                       # string or list; gitignore-style globs
    except: [node_modules, dist]  # adds to .gitignore, which always applies
    run: pnpm run dev              # run through sh -c; quotes, &&, pipes work
    build: pnpm run typecheck      # optional; runs first, old run stays up until it passes
    health: http://localhost:5173  # optional; http(s) url, tcp://host:port, or a command
    open: http://localhost:5173    # ctrl+o opens it; the target links to it (or a shell command to run)
    delay: 200ms                  # batch file events before restarting
    cooldown: 2s                  # min time between restarts (event storms)
    grace: 5s                     # SIGTERM → wait → SIGKILL

  api:
    on: [cmd, internal]
    run: go run ./cmd/api --addr :8080
    mute: true                    # starts muted; m unmutes
    keep: 2000                    # scrollback lines, or a time like 1h

  test:
    on: ['**/*.go']
    run: go test ./...
    delay: 500ms
    start: onchange               # auto (default), onchange, or manual
```

Plain commands without shell syntax run directly; anything else runs via `/bin/sh -c`.

To force a shell for a plain command, write `run: sh -c '...'` or `run: sh ./run.sh`, as in docker compose.

Defaults: `on: ./`, `delay: 200ms`, `cooldown: 0s`, `grace: 5s`,
`start: auto`, `keep: 10000`.
Naming a target on the command line starts only the named ones; the others stay off (still listed, still startable with `space`).
`start: onchange` holds the first run back until a file changes, and
`start: manual` watches nothing and runs only when `r` asks for it.
Unknown fields, duplicate fields, and non-string values are errors.

Targets that log the same JSON shape can share columns: define them once under
the reserved top-level `groups:` key (`groups: {backend: {columns: ["level {.level}"]}}`)
and set `group: backend` on each target. A target's own `columns` wins over its
group's, and a `group:` naming no group is an error. A group's `start:` sets the
first run for its members, and a target's own `start:` wins. A group may be empty (`webapps: {}`).
Quote each column entry,
since a `{` inside a plain `[...]` entry is a YAML error.

A column can also hold literal text and several `{expr}` placeholders. Each
placeholder evaluates on its own, a missing value renders as nothing, and runs of
spaces collapse. With `columns: ["msg {.message} {.error}"]`, a line with both
fields shows `disk full ENOSPC`, and a line with only `.error` shows `ENOSPC`. A
bad expression shows `bad column "msg": <error>` in the title row.

### `targets:`

Targets sit under a top-level `targets:` key:

```yaml
targets:
  api:
    run: go run ./cmd/api
```

- The top level holds only rerun's own keys: `imports:`, `groups:` and `targets:`. Any other key is an error.
- A target under `targets:` may be called `imports` or `groups`.

## Flags

- `-delay`, `-cooldown`, `-grace` — override per run (CLI wins over rerun.yml)
- `-no-gitignore` — turn off .gitignore filtering and watch ignored files too
- `-h`, `-version`

## Shutdown

Ctrl-C stops all commands gracefully (SIGTERM → grace → SIGKILL, exit 0).
A second Ctrl-C force-kills immediately (exit 130).

Legacy syntax (`-watch a -ignore b -run cmd`) still works this major version,
but prints a deprecation warning.
