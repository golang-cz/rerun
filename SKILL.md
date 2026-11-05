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
`enter` focuses the pane. The cursor row narrows the pane to its target or
schema's targets (`esc` backs out). On a schema header, `space`, `r`, and `m` act on its
targets. `o` open the url, `G` live output. The right pane is always the merged stream; the cursor row
narrows it to one target, keeping the name prefixes; each line pads the name to
the longest target so messages start in a fixed column. The pane's top line is
the cursor row's command, with its url linked. Mouse: click a row to show only its
stream, double-click to stop/start like `space`, and a click on a schema header to select its targets. `m` mutes; the footer notice names it.

Expand JSON lines: each JSON object line shows a `(+)` cell at the right edge. A click on
it, or `enter` with the pane focused, expands the pretty-printed object; `(−)` collapses
it. The pane cursor (a reverse-video line) starts on the newest line; `↑/↓` move it when
the pane is focused, and moving it off the live tail pauses the tail (`G` resumes).
`enter` acts on the cursor line, and a non-JSON line gets a hint instead. Opening a block
pauses the tail and keeps the block in view. Expanded keys are underlined and scalar
values tinted. The first expansion of a session shows the click hint.
In an expanded block, a click on a key adds it as a column of the selection (a selected
schema, else the target the pane shows), and a click on a scalar adds a `.path == value` chip
(ANDed with `v`; `×` or `backspace` removes it). With the pane focused and the cursor on
an expanded key or scalar row, `a` adds the key and `f` adds the value chip, as the
clicks do. A table header column name is inert; only the `×` after it removes that
column. A schema or target whose columns are unsaved shows a `*` in its marker column, two cells after its name; a `✱` there marks hidden output. Picks are
unsaved until `ctrl+s` writes them with the rerun.yml column writer; the footer shows
`unsaved columns · ctrl+s save`, and the quit modal says so too. A target with a schema
and no columns of its own writes its schema. A `columns:` list that a YAML alias uses elsewhere
is refused with a footer error (edit rerun.yml by hand). Writes keep the file's other bytes, indent,
and line endings, and follow a symlink to the file it names. `cmd+s` works only where the terminal
forwards it.

The fs drawer under the list is a change feed: one line per batch of changes
(events within 150ms of each other), with the batch age, the first changed file, `+N` for the
rest, and the targets the batch restarted. A created directory shows a trailing `/` and heads
the files reported under it; a narrow column cuts the path from its left. The pane's top line
names a muted row (`muted`). `tab` focuses it, `j/k` move, `enter`
lists a batch's files, `esc` closes. A rerun.yml reload shows as the `config`
target. Batches older than a minute drop off.

## Building rerun.yml

Flat top-level map, one entry per target (Makefile-like). `run` (older spelling: `cmd`) is required;
everything else is optional. Durations are Go-style strings (`200ms`, `5s`).

```yaml
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
group's, and a `group:` naming no group is an error. A group may be empty (`webapps: {}`).
The older `schemas:`/`schema:` spelling still works; use one spelling per file. Quote each column entry,
since a `{` inside a plain `[...]` entry is a YAML error.

## Flags

- `-delay`, `-cooldown`, `-grace` — override per run (CLI wins over rerun.yml)
- `-no-gitignore` — turn off .gitignore filtering and watch ignored files too
- `-h`, `-version`

## Shutdown

Ctrl-C stops all commands gracefully (SIGTERM → grace → SIGKILL, exit 0).
A second Ctrl-C force-kills immediately (exit 130).

Legacy syntax (`-watch a -ignore b -run cmd`) still works this major version,
but prints a deprecation warning.
