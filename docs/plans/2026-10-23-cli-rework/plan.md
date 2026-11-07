# rerun CLI rework — plan

Status: DONE
Date: 2026-10-23

## Summary

Rework `rerun` from the flag-soup CLI (`-watch ... -ignore ... -run CMD`) into a
sentence-grammar CLI with `rerun.yml` targets, parallel multi-target runs,
configurable timeouts, and a Turbo-style TUI. The library (`rerun.Watcher`)
gets rebuilt around directory watches + allow-list filtering; today's
file-inode watches break on atomic editor saves.

## User flow

A developer in a repo runs one command; it stays running and restarts the
right child process whenever relevant files change:

```bash
rerun                     # run ALL rerun.yml targets, TUI (or plain when piped)
rerun frontend api        # run specific targets in parallel
rerun -- go test ./...    # ad-hoc: watch ./, respect .gitignore
```

## Decisions (log; newest last)

1. **Grammar: everything after `--` is the command**, passed through verbatim.
   No `-run`. Precedent: docker/kubectl/entr.
2. **Sentence form for lists**: bare words = watchlist; `--except words...` =
   ignorelist. `rerun a b --except c d -- go test ./...` reads as a sentence.
3. **`--on` optional**: same watchlist keyword, kept as a synonym for
   explicitness (docs show the bare form). Legacy `-watch` maps to it.
4. **`--` is REQUIRED for ad-hoc commands.** Task names stay unambiguous.
5. **Task vs watchlist rule**: one bare arg, no `--`, no keyword, matches a
   rerun.yml target → that target. Otherwise bare words are the watchlist.
6. **Defaults are hardcoded in the tool, not in yml** (no `defaults:` key):
   - watch root: `./`
   - ignores: `.gitignore` entries (always, plus `.git`); `-no-gitignore` opts out
   - `delay`: `200ms`, `cooldown`: `0s` (off), `grace`: `5s`
7. **Gitignore-style glob patterns** everywhere (`**/*.go`, `vendor/`, `*.md`)
   — one matching engine for CLI lists, rerun.yml, and .gitignore. No new glob
   syntax invented.
8. **Stdin watchlist**: if stdin is not a TTY, read paths line-by-line
   (entr-style). `find . -name '*.go' | rerun -- go test`.
9. **Shell word-splitting, not comma-splitting**: lists are argv words, so
   `$(find ...)`, `$(ls ...)` compose natively. Commas are legal in filenames.
10. **Legacy syntax keeps working**: `-watch`/`-ignore`/`-run` are parsed as
    deprecated synonyms of `--on`/`--except`/`--`; any legacy token prints a
    one-line deprecation warning to stderr. Remove in v3.
11. **Bare `rerun` runs ALL targets** (see user flow). No rerun.yml and no
    args → print help, exit 1.
12. **Multiple targets run in parallel**: `rerun frontend api` — each target
    has its own watcher; output prefixed `[name]`; Ctrl-C kills all. Ad-hoc
    form stays single-target; parallel ad-hoc = shell `&` + `wait`.
13. **Timeouts** (Go-style duration strings, e.g. `200ms`, `5s`, `1m`):
    - `delay` — batch window: collect FS events into one change-set before
      restarting (today's hardcoded 200ms, now configurable)
    - `cooldown` — after a restart, ignore new restarts until it elapses
      (survives branch-switch event storms)
    - `grace` — SIGTERM first, wait, then SIGKILL (fixes instant-SIGKILL)
14. **rerun.yml schema**: flat top-level (Makefile-like). No `defaults:` key —
    built-in defaults are hardcoded (decision 6). `on`/`except` accept a
    string or a list. `cmd` is a string run via `sh -c` (quotes, `&&`,
    pipes work). Fields mirror CLI keywords 1:1.

## rerun.yml shape

```yaml
frontend:
  desc: Vite dev server        # shown in the TUI list (optional)
  on: web                      # string or [list], gitignore-style globs
  except: [node_modules, dist] # optional; gitignore always applies anyway
  cmd: pnpm run dev            # required; run via sh -c
  delay: 200ms                 # optional, default 200ms
  cooldown: 2s                 # optional, default 0s
  grace: 5s                    # optional, default 5s

api:
  on: [cmd, internal]
  cmd: go run ./cmd/api --addr :8080

test:
  desc: Unit tests
  on: ['**/*.go']
  cmd: go test ./...
```

## CLI form (final)

```
rerun                                # all targets; TUI if TTY, else prefixed plain output
rerun frontend api                   # subset, parallel
rerun test                           # single target (one bare arg, no '--')
rerun -- go test ./...               # ad-hoc: watch ./, gitignore respected
rerun a b --except c d -- go test    # explicit lists
rerun '**/*.go' --except vendor -- go test ./...
rerun $(find . -name '*.go') -- go test ./...
find . -name '*.go' | rerun -- go test ./...
rerun -watch . -run go test ./...    # legacy: warns to stderr, keeps working
```

Flags (overridable per-run): `-delay`, `-cooldown`, `-grace`, `-no-gitignore`,
`-h`, `-version`. Precedence: CLI > target fields > hardcoded defaults.

## Exit & restart semantics (proposal, pending confirm)

- Target exits by itself (crash or clean finish): NO auto-restart. Waits for
  the next matching change. TUI shows `exited N` in red.
- Ctrl-C once: SIGTERM all targets → wait `grace` → SIGKILL leftovers → exit 0.
- Ctrl-C twice: SIGKILL everything immediately → exit 130.
- Watcher error on a target: shown in TUI/output; target keeps running.

## Interactive TUI (turbo-style; imitate Turborepo's `"ui": "tui"`)

Layout: left column = target list (name, desc, status: running/exited/restarting),
right pane = live output of the selected target.

Keys (v1 minimal set):
- `↑/↓` or `j/k` select target
- `l` toggle log pane / `h` full-screen logs
- `i` hand stdin to the selected target; `Ctrl+z` stop interacting
- `/` filter targets, `ESC` clear
- `?` or `m` help popup
- Ctrl-C: exit semantics above

Library: bubbletea (bubbles list + viewport) — decision pending confirm.

## Implementation steps (each with verify)

1. **Arg parser state machine** (grammar + task rule + legacy synonyms +
   deprecation warning) — verify: table-driven unit tests covering every row
   of the CLI-form block above, incl. legacy forms.
2. **Glob/gitignore matching engine** (doublestar-style `**` patterns;
   .gitignore loading) — verify: unit tests incl. negation patterns,
   `vendor/` dir-only, `!keep.go` re-includes.
3. **Watcher rework**: directory watches + allow-list event filtering (kills
   the file-inode atomic-save bug), stdin watchlist, new-dirs-added
   — verify: tests: atomic rename (write temp + rename) triggers rerun;
   new subdir watched; ignored new file does not trigger.
4. **Command lifecycle**: grace kill (SIGTERM→SIGKILL), exit states,
   restart-on-change, cooldown — verify: tests with short-lived processes.
5. **rerun.yml loader + parallel runner**: schema validation (cmd required;
   good errors), multi-target orchestration, prefixed output — verify: tests
   with a fixture yml; manual `rerun a b` smoke.
6. **CLI flags + `-h` + `-version`** wired to 2-4 — verify: parser tests.
7. **README rewrite** (new grammar, yml, legacy note) — verify: examples in
   README match parser tests.
8. **TUI** (separate PR after 1-7 land): bubbletea left/right layout, keys,
   statuses — verify: manual smoke; screenshot for the PR.

## Out of scope

- Auto-restart of crashed targets
- Windows-specific behaviors beyond today's (TUI untested on Windows v1)
- Recursive `**` in .gitignore passthrough edge cases beyond what the engine
  supports
- Renaming the tool (kept: `rerun`)
- `-w`/`-i` short flags (dropped)

## Open questions

- Confirm timeout names: `delay`, `cooldown`, `grace`?
- Confirm `except` (vs `skip`/`exclude`)?
- Exit codes: clean shutdown 0, forced 130 — ok?
- TUI library: bubbletea ok?
- yaml lib: gopkg.in/yaml.v3?
