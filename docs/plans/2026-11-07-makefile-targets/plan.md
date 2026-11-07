# Makefile targets

## Summary

A Makefile is one more import source, next to rerun files and compose files.
The Makefile next to `rerun.yml` is imported by default as a `make` group: last in the list, folded.
Makefiles in subfolders come in only through an explicit import, as subgroups of `make`.
Each make target is a normal list row: manual start, runs once, no watch.
The Ctrl+K finder sees them like any other row. Enter on a make target runs it.
With only a Makefile in the cwd, rerun starts with the finder open instead of exiting.

## User-flow impact

- **Repo with `rerun.yml` and a Makefile**: services unchanged on top. A folded `make` group at the bottom. Ctrl+K lists `make/<target>` items.
- **Repo with only a Makefile**: `rerun` opens the TUI with the folded `make` group and the finder already open. Today it exits 1.
- **Repo with neither**: unchanged, exits 1 with the current hint.
- **Running a make target**: Enter in the finder, or the normal start key on its row. The `make` group unfolds to reveal the row, it becomes the solo target, and output streams in the right pane. It ends with `✓ exit 0` or `✗ exit N`. Output stays until the next run.
- **Enter on a running make target**: focus it, never start a second copy.
- **Re-run**: Enter again after it exits, or the restart key on its row.

## Config shape (rerun.yml)

```yaml
imports:
  - path: apps/api/Makefile        # group defaults to make/api
  - path: tools/Makefile
    group: make/tools
    optional: true
    targets: [lint, gen]           # Makefile only: keep these targets

groups:
  make:
    hide: false                    # unfold the default group
```

- The file decides the kind, as today. File name `Makefile`, `makefile`, `GNUmakefile`, or a `.mk` extension means Makefile.
- Default `group` for a Makefile import: `make` for the importing file's own dir, `make/<dir name>` otherwise.
- Makefile groups are folded by default. A `groups:` block entry with `hide: false` unfolds one.
- New filter key `targets:` is valid on Makefile imports only. Unknown keys stay an error.
- **Implicit import**: the Makefile in the `rerun.yml` dir is imported as `optional: true, group: make` without being listed. An explicit import of the same path replaces it; dedupe by absolute path already exists.
- Subfolders are never scanned. A monorepo walk hits `node_modules` and `vendor`, and picks up Makefiles nobody meant to run.

## Makefile parsing (no `make -p`)

Text parse of one file. No subprocess, no `$(shell ...)` side effects.

- A target line: column 0, not a tab or `.`, `name[ name...]:` where the colon is not part of `:=`, `::=`, `?=`.
- Skip names with `%` or `$`. Skip special targets (`.PHONY`, `.DEFAULT`, ...).
- Multiple names on one line become separate targets. Dedupe, keep file order.
- Description, first match wins:
  1. trailing `## text` on the target line (the `make help` convention)
  2. contiguous `#` comment lines directly above the target, joined with a space
- `include` is not followed.
- Makefile edits reload like any imported file today.

## API shape (Go, cmd/rerun)

```go
// makefile.go
type makeTarget struct {
	Name string
	Desc string
}

func isMakefile(path string) bool
func parseMakefile(r io.Reader) ([]makeTarget, error)

// config_imports.go, next to composeTargets
func makefileTargets(spec importSpec, path string) ([]Target, error)
```

- `config.go`: `const makeKind = "make"`. Each target: `Target{Kind: makeKind, Name: <name>, Desc: <desc>, Cmd: "make <name>", Dir: <makefile dir>, Start: startManual}`, no watch globs.
- `importSpec` gains `Targets []string`, validated as Makefile-only.
- `backend.go`: make targets use `processBackend`. Exit is final, no auto-restart.
- `LoadConfig`: with no `rerun.yml` but a Makefile in cwd, return a config holding only the implicit import, instead of `configNotFoundError`.
- `tui_finder.go`: no new candidate code, rows already flow in. `enter` for `makeKind` starts when not running, else focuses. Footer label `⏎ run` when a make row is selected.
- List order: Makefile groups sort last.

## Steps

1. **Parser.** `makefile.go` + `makefile_test.go`.
   Verify: table tests for `##` desc, comment-above desc, multi-name lines, `:=`/`?=` skipped, `%`/`$` skipped, `.PHONY` skipped, recipe lines ignored.
2. **Import source.** `isMakefile`, `makefileTargets`, `targets:` filter, default group names, folded by default, sorted last.
   Verify: config tests for explicit import, `targets:` filter, `targets:` on a compose import errors, default group `make/<dir>`, `hide: false` unfolds.
3. **Implicit import.** Root Makefile imported as `make`; explicit import of the same path wins.
   Verify: tests for (yml + Makefile), (yml + explicit root import), (yml, no Makefile).
4. **Make-only startup.** No `rerun.yml` + Makefile → TUI with the finder open.
   Verify: tests for (Makefile only → finder open on first frame), (neither → existing error).
5. **Run once.** Make targets start manually, exit is final, re-run via Enter or restart.
   Verify: TUI tests for run from finder, focus while running, re-run after exit, exit label, group unfolds to reveal the row.
6. **Docs.** README section, SKILL.md, help overlay.
   Verify: `go test ./...`, `go vet ./...`, `make install`, manual run in this repo (it has a Makefile).

## Decisions (agreed)

1. Makefile is an import source, detected by file name.
2. Root Makefile imported by default as a folded `make` group, last.
3. Subfolder Makefiles need an explicit import. No scanning.
4. Make targets are real list rows: manual start, run once.
5. Text-parse the Makefile, not `make -pRrq`.

## Out of scope

- Following `include`.
- Passing variables or args (`make test PKG=...`).
- `justfile`, `Taskfile.yml`, `package.json` scripts. Same import seam later.
- Group `desc:` and Ctrl+F fuzzy log search (separate tracks).
