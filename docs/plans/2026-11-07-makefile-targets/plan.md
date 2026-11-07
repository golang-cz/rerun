# Makefile targets in the Ctrl+K finder

## Summary

rerun reads the Makefile next to `rerun.yml` (or in the cwd when there is no `rerun.yml`).
Its targets show up in the Ctrl+K finder, not in the service list.
Enter on a make target runs it once, like a terminal would.
A run gets a row in a `make` group so its output stays visible.
With only a Makefile in the cwd, rerun starts with the finder open, instead of exiting with "config not found".

## User-flow impact

- **Repo with `rerun.yml` and a Makefile**: list unchanged. Ctrl+K shows services, groups, and `make/<target>` items.
- **Repo with only a Makefile**: `rerun` opens the TUI with an empty list and the finder already open. Today it exits 1.
- **Repo with neither**: unchanged, exits 1 with the current hint.
- **Running a make target**: Enter → a `make/<target>` row appears in a `make` group at the bottom, becomes the solo target, output streams in the right pane. It exits with `✓ exit 0` or `✗ exit N` and stays visible until rerun quits.
- **Enter on a make target that is already running**: focus it, do not start a second run.
- **Re-running**: Enter on a finished make target in the finder, or the normal restart key on its row.

## Finder look

Make items use the group-prefix style from the finder redesign: `make/` dim, target name prominent, description in the shared desc column.

```
│ ❯ te▊                                     3/41 │
│ ────────────────────────────────────────────── │
│ ▌ make/TEst              run unit tests        │
│   make/TEst-e2e          run e2e against dev   │
│   all/api/              ...                    │
│ ↑↓ move  ⏎ run  esc close                      │   ⏎ label is "run" when a make item is selected
```

## Makefile parsing (no `make -p`)

Text parse of one file. Fast, no subprocess, no side effects from `$(shell ...)`.

- File: first of `GNUmakefile`, `makefile`, `Makefile` in the dir (GNU make's order).
- A target line: starts at column 0, not with a tab or `.`, `name[ name...]:` where the colon is not part of `:=`, `::=`, `?=`.
- Skip names with `%` or `$`. Skip special targets (`.PHONY`, `.DEFAULT`, ...).
- Multiple names on one line become separate items. Dedupe, keep file order.
- Description, first match wins:
  1. trailing `## text` on the target line (the common `make help` convention)
  2. contiguous `#` comment lines directly above the target, joined with a space
- `include` is not followed (out of scope).
- Parse on every finder open. No watcher, never stale.

## API shape (Go, cmd/rerun)

```go
// makefile.go
type makeTarget struct {
	Name string
	Desc string
}

// findMakefile returns the Makefile path in dir, or "" if none.
func findMakefile(dir string) string

// parseMakefile lists the file's own targets in file order.
func parseMakefile(r io.Reader) ([]makeTarget, error)
```

- `config.go`: `const makeKind = "make"`. A run is a `Target{Kind: makeKind, Name: "make/" + name, Cmd: "make " + name, Dir: makefileDir, Start: startManual}`, with no watch globs.
- `Config` gains `MakefileDir string`: the dir searched, set by `LoadConfig` even when no `rerun.yml` exists.
- `backend.go`: make targets use `processBackend`. Exit is final: no auto-restart, no watch.
- `tui_finder.go`: `finderCandidates` appends one `finderItem{kind: "make"}` per target. `enter` starts or focuses, see user flow.
- Make-run rows live only in the TUI model, under a `make` group. `reconcile` on `rerun.yml` reload must keep them.

## Steps

1. **Parser.** `makefile.go` + `makefile_test.go`.
   Verify: table tests cover `##` desc, comment-above desc, multi-name lines, `:=`/`?=` skipped, `%`/`$` skipped, `.PHONY` skipped, tab-indented recipe lines ignored, GNUmakefile priority.
2. **Discovery.** `LoadConfig` records `MakefileDir`. No `rerun.yml` + Makefile in cwd → return an empty config instead of `configNotFoundError`.
   Verify: tests for (yml+Makefile), (Makefile only), (neither → existing error).
3. **Startup.** `runConfigMode` with an empty config and a Makefile opens the TUI with the finder open. Esc shows an empty list with a hint line: `ctrl+k  make targets`.
   Verify: TUI test asserts `m.finder.open` on first frame and the empty-state hint after Esc.
4. **Finder items.** Make items in `finderCandidates`, `make/` dim prefix, footer `⏎ run` when one is selected.
   Verify: finder tests: query matches a make target by name and by desc; footer label switches.
5. **Run row.** Enter creates the `make/<name>` row in a `make` group, starts it, makes it solo. Enter again while running only focuses. Finished run + Enter re-runs.
   Verify: TUI tests for first run, focus-while-running, re-run after exit, exit label rendered.
6. **Reload safety.** `rerun.yml` reload keeps make rows and their output.
   Verify: reconcile test with a make row present across a reload.
7. **Docs.** README section, SKILL.md note, help overlay line.
   Verify: `go test ./...`, `go vet ./...`, `make install`, manual run in this repo (its own Makefile).

## Decisions (suggested default, flip any)

1. Text-parse the Makefile, not `make -pRrq`. **Yes.** `-p` evaluates `$(shell)`, is slow on big Makefiles, and lists built-ins.
2. Makefile found only next to `rerun.yml` or in the cwd, no walking. **Yes.**
3. A run gets a list row in a `make` group, so output and exit status stay visible. **Yes.** Alternative: output only in the right pane, no row.
4. Enter on a running make target focuses it, never starts a second copy. **Yes.**
5. No config key to disable the Makefile source in v1. **Yes.** Add `makefile: false` only if someone asks.
6. Make-only startup opens the finder immediately. **Yes.**

## Out of scope

- Following `include` and parsing other Makefiles.
- Passing variables or args (`make test PKG=...`).
- Listing targets from `justfile`, `Taskfile.yml`, `package.json` scripts. The parser seam allows them later.
- Group `desc:` and Ctrl+F fuzzy log search (separate tracks).
