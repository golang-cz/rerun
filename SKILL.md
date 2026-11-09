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

Keys: `j/k` move, `space` start/stop, `r` reboot, `m` mute a target's stream,
`enter` focuses the pane, and its cursor lands on the service's last line (or the newest line when that is off screen). The cursor row narrows the pane to its target or
schema's targets (`esc` backs out). On a schema header, `space`, `r`, and `m` act on its
targets. `o` open the url. `esc` returns to the live output. The right pane is always the merged stream; the cursor row
narrows it to one target, keeping the name prefixes; each line pads the name to
the longest target so messages start in a fixed column. A `muted: true` field in rerun.yml starts a target's stream muted; `hide: true` does too, and a group's `hide: true` also folds its header. A target's own `hide: false` overrides a group's `hide: true`. A target's `hide: false` unmutes it, but the group header still starts folded. A root group named after an import folds that import's section. Folding is a start state; a reload keeps your fold for groups already shown. The pane's top line is
the cursor row's command, with its url linked. Mouse: click a row to show only its
stream, double-click to stop/start like `space`, and a click on a schema header to select its targets. `m` mutes; the footer notice names it.

More keys: `b` cycles the left list: full, names only (the state glyph and name), or hidden for a full-width log with a `b list` cue on the bottom line. `c` copies the selected lines, or the pane cursor line when nothing is selected; `e` opens the fs events list (last minute), `s` stops the row (with the pane focused, every target the pane shows), `space` with the pane focused jumps to the live tail, `→` expands the line under the cursor and `←` collapses it, `ctrl+g` and `ctrl+shift+g` step to the next and previous pill hit, and `ctrl+l` clears the log (older lines stay in scrollback). With the pane focused, `shift`+`↑`/`↓` select lines from the cursor, the same highlight as a drag; any other key clears it.

`/`, `v` or `ctrl+f` opens the query bar: `enter` turns the input into pills that AND together (`level=error -healthz`
adds two). Pill syntax: `timeout` full text, `"two words"` phrase, `-healthz` negated, `level=error` field equals
(`!=`, `>`, `>=`, `<`, `<=`, `~` contains, `~/re/` regex, `=*` exists, `!=*` missing; `level=error|fatal` is any-of),
`.expr` raw yq. A field name is a yq path without the leading dot; a scope column name resolves to its expression.
`←`/`→` walk the pills: `x`/`backspace` removes, `enter` edits the text back into the input, `space` toggles filter
⇄ highlight, `→` on `[@name]` expands it. Filter pills hide lines; every pill's matches are marked, and `n`/`N` jump
between hits. `@` lists the pane scope's saved queries from the `queries:` keys (top level, per group, per target;
most specific wins a name clash). `esc` keeps the pills; `backspace` on an empty input removes the last one.

Build output shows inline: each build is one `⚙` rule in the stream, rewritten in place as it
becomes `built <duration>` or `✗ build failed`. Its output folds under the rule: click anywhere on
the rule line, or press `enter`. A failed build opens itself, and a `▶` rule folds its command when the
pane cuts it short; a click on a cut rule expands it to the full command.

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
an expanded key or scalar row, `a` adds the key and `f` adds the value pill, as the
clicks do. A table header column name is inert; only the `×` after it removes that
column. A schema or target whose columns are unsaved shows a `*` in its marker column, two cells after its name. Picks are
unsaved until `ctrl+s` writes them with the rerun.yml column writer; the footer shows
`unsaved columns · ctrl+s save`, and the quit modal says so too. A target with a schema
and no columns of its own writes its schema. A `columns:` list that a YAML alias uses elsewhere
is refused with a footer error (edit rerun.yml by hand). Writes keep the file's other bytes, indent,
and line endings, and follow a symlink to the file it names. `cmd+s` works only where the terminal
forwards it.

The fs events line above the bottom-left footer ages the newest change batch
(`fs events · 4s ago`, `idle` past a minute), highlights for a second when one
lands, and turns red `⚠ watch failed` after a watcher error; a click (or `e`)
opens the fs events modal. The modal lists the last minute's batches (events
within 150ms of each other), newest first: the time, the first changed file with
`+N` for the rest, and the targets the batch restarted right-aligned behind a `·`.
`→` expands a batch's files, each naming the services whose watch globs it
matched; `y` copies every batch expanded. A created directory shows a trailing `/`
and heads the files reported under it. The pane's top line
names a muted row (`muted`). A rerun.yml reload shows as the `config`
target. Batches older than a minute drop off.

### Imports: other projects' rerun.yml

A `rerun.yml` can list other rerun files under `imports:`, one map per file:

```yaml
imports:
  - path: ../bpn-api/rerun.yml   # relative to this file
    group: bpn-api               # section name and target prefix; default: the dir name
    optional: true               # skip the file while it is missing
    hide: true                   # start its targets muted and its section folded
    start: auto                  # first run for its targets; a target's own wins
    groups: [backend]            # only these groups of that file
```

Imported targets run from their own dir, and their names read `bpn-api/api`.
An import's `hide: true` mutes its targets and folds its section. Its own `muted` or `hide` and a group's still win.
An import's `start:` is the outermost level too: a target's own `start` and a group's `start` win.
A rerun.yml import's targets default to `auto`; a Makefile or compose import's to `manual`.
The import's `path` is required. Unknown keys are errors. An `imports:` key in an
imported file is ignored. A compose file is imported as its services. See Docker Compose below.

### Makefile: a make file's targets

A Makefile is an import source too. Its targets become rows, read as text, never run through `make -p`.

```yaml
imports:
  - path: apps/api/Makefile        # group defaults to make/api
  - path: tools/Makefile
    group: make/tools
    targets: [lint, gen]           # keep only these
```

- A `Makefile` next to `rerun.yml` loads as a folded `make` group by default.
- Subfolder Makefiles load only when imported. rerun never scans subfolders.
- `groups: make: hide: false` unfolds a Makefile group.
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
- Needs the `docker` CLI on PATH. `groups:` is an error on a compose import.

## Building rerun.yml

Flat top-level map, one entry per target (Makefile-like). `run` (older spelling: `cmd`) is required;
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
    url: http://localhost:5173     # o opens it; the row links to it
    open: true                     # open the url once at startup
    delay: 200ms                  # batch file events before restarting
    cooldown: 2s                  # min time between restarts (event storms)
    grace: 5s                     # SIGTERM → wait → SIGKILL

  api:
    on: [cmd, internal]
    cmd: go run ./cmd/api --addr :8080
    selected: false               # listed in the TUI, starts off (space starts it)

  test:
    on: ['**/*.go']
    cmd: go test ./...
    delay: 500ms
    start: onchange               # auto (default), onchange, or manual
```

Defaults: `on: ./`, `delay: 200ms`, `cooldown: 0s`, `grace: 5s`,
`selected: true`, `open: false`, `start: auto`.
Naming a target on the command line overrides `selected` either way: named ones
start, unnamed ones stay off (still listed, still startable with `space`).
`start: onchange` holds the first run back until a file changes, and
`start: manual` watches nothing and runs only when `r` asks for it.
Unknown fields, duplicate fields, and non-string values are errors.

Targets that log the same JSON shape can share columns: define them once under
the reserved top-level `groups:` key (`groups: {backend: {columns: ["level {.level}"]}}`)
and set `group: backend` on each target. A target's own `columns` wins over its
group's, and a `group:` naming no group is an error. A group's `start:` sets the
first run for its members, and a target's own `start:` wins. A group may be empty (`webapps: {}`).
The older `schemas:`/`schema:` spelling still works; use one spelling per file. Quote each column entry,
since a `{` inside a plain `[...]` entry is a YAML error.

A column can also hold literal text and several `{expr}` placeholders. Each
placeholder evaluates on its own, a missing value renders as nothing, and runs of
spaces collapse. With `columns: ["msg {.message} {.error}"]`, a line with both
fields shows `disk full ENOSPC`, and a line with only `.error` shows `ENOSPC`. A
bad expression shows `bad column "msg": <error>` in the title row.

### `targets:` and `-migrate`

Targets sit under a top-level `targets:` key:

```yaml
targets:
  api:
    run: go run ./cmd/api
```

- The top level holds only rerun's own keys: `imports:`, `groups:`, `schemas:` and `targets:`. Any other key is an error.
- A file without `targets:` still loads the old way, with one footer notice: `rerun.yml uses top-level targets; run rerun -migrate`.
- `rerun -migrate` rewrites the nearest rerun.yml in place under `targets:`, keeping comments and key order and dropping blank lines.
- A target under `targets:` may be called `imports` or `groups`.

## Flags

- `-delay`, `-cooldown`, `-grace` — override per run (CLI wins over rerun.yml)
- `-no-gitignore` — turn off .gitignore filtering and watch ignored files too
- `-migrate` — move rerun.yml's top-level targets under `targets:`
- `-h`, `-version`

## Shutdown

Ctrl-C stops all commands gracefully (SIGTERM → grace → SIGKILL, exit 0).
A second Ctrl-C force-kills immediately (exit 130).

Legacy syntax (`-watch a -ignore b -run cmd`) still works this major version,
but prints a deprecation warning.
