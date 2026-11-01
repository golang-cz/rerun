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

Each target gets its own watcher and command; output is prefixed `[name]`.
A target that exits by itself stays down until its next matching file change.

Naming targets sets what starts, not what is listed. In the TUI every rerun.yml
target keeps its row: the named ones start running, the rest show `·` (off) and
`space` starts one on demand. Piped or non-TTY output stays plain prefixed text
and runs the named targets only.

On a terminal, target mode opens the TUI: a row per target on the left, the
merged stream on the right. Piped or non-TTY output stays plain prefixed text.
Press `?` inside it for the full key list.

Keys: `j/k` move, `space` start/stop, `r` reboot, `m` mute a target's stream,
`s` or `enter` solo a target's stream (`esc` or a second press clears), `o` open
the url, `G` live output. The right pane is always the merged stream; soloing
narrows it to one target, keeping the `[name]` prefixes. Mouse: click a row to
solo it, the button cell to mute, shift-click to solo, and a click on the ALL
row to clear solo.

## Building rerun.yml

Flat top-level map, one entry per target (Makefile-like). `cmd` is required;
everything else is optional. Durations are Go-style strings (`200ms`, `5s`).

```yaml
frontend:
  desc: Vite dev server          # shown in output listings
  on: web                       # string or list; gitignore-style globs
  except: [node_modules, dist]  # adds to .gitignore, which always applies
  cmd: pnpm run dev              # run through sh -c; quotes, &&, pipes work
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
```

Defaults: `on: ./`, `delay: 200ms`, `cooldown: 0s`, `grace: 5s`,
`selected: true`, `open: false`.
Naming a target on the command line overrides `selected` either way: named ones
start, unnamed ones stay off (still listed, still startable with `space`).
Unknown fields, duplicate fields, and non-string values are errors.

## Flags

- `-delay`, `-cooldown`, `-grace` — override per run (CLI wins over rerun.yml)
- `-no-gitignore` — turn off .gitignore filtering and watch ignored files too
- `-h`, `-version`

## Shutdown

Ctrl-C stops all commands gracefully (SIGTERM → grace → SIGKILL, exit 0).
A second Ctrl-C force-kills immediately (exit 130).

Legacy syntax (`-watch a -ignore b -run cmd`) still works this major version,
but prints a deprecation warning.
