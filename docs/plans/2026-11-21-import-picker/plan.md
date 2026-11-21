# Import picker

## Summary

With no `rerun.yml`, rerun no longer loads a Makefile or compose file blindly when there is a choice.
It scans the subtree and opens a tree picker.
The user unchecks what they don't want, then runs it for this session or saves it as `rerun.yml`.
The same picker opens from the TUI with `I`, also when a `rerun.yml` exists.
No CLI flag is added.

## User flow

| Situation | Startup |
|---|---|
| `rerun.yml` exists | unchanged |
| no `rerun.yml`, only root-level Makefile/compose found | unchanged: load them directly (today's `MakeOnly`) |
| no `rerun.yml`, any matching file in a subfolder | picker opens on the first frame |
| `rerun <target>` with no `rerun.yml` | unchanged: no picker |
| nothing found anywhere | unchanged |

`I` in the main view opens the picker at any time.
With a `rerun.yml`, the files it already imports start checked, the rest unchecked.
Without one, every file starts checked.

## Picker

```
┌ No rerun.yml in ~/src/app ── found 7 files ─────────────────────┐
│ filter: _                                                       │
│ [■] ./                                                          │
│ [x] ├── Makefile                               12 targets       │
│ [x] ├── docker-compose.yml                      4 services      │
│ [■] ├── api/                                                    │
│ [x] │   ├── Makefile                            8 targets       │
│ [ ] │   └── compose.yaml                        2 services      │
│ [x] ├── web/                                                    │
│ [x] │   └── rerun.yml                           3 commands      │
│ 4 of 7 files · 27 commands                                      │
├─────────────────────────────────────────────────────────────────┤
│ space toggle · a all/none · enter run · ctrl+s save rerun.yml   │
│ esc close                                                       │
└─────────────────────────────────────────────────────────────────┘
```

- `space` toggles a file, or a folder's whole subtree. `■` = partly checked folder.
- `a` checks all, or unchecks all if all are checked.
- Typing filters rows by path, like the finder.
- `enter` applies the checked files for this session. Nothing is written.
- `ctrl+s` writes `rerun.yml`, then the existing config watcher reloads it.
  - Footer says `save rerun.yml` with no file, `update rerun.yml imports` with one.
  - With an existing file, only the `imports:` key is rewritten. Comments and every other key stay.
- `esc` closes. On the startup picker with no other config, `esc` leaves an empty session, like an empty repo today.
- An imported `rerun.yml` that has its own `imports:` shows a `⚠ nested imports ignored` hint (imports are one level deep).
- Counts come from parsing each file. A file that fails to parse shows the error in place of the count and starts unchecked.

## Scan

- Names: the existing Makefile and compose names (`makefile.go:16-19`) plus `rerun.yml`.
- Respects `.gitignore` unless `-no-gitignore`. Always skips `.git`, `node_modules`, `vendor`.
- A folder with its own `rerun.yml` lists only that file. Its sibling Makefile/compose are covered by it.
- The start folder's own `rerun.yml` is never listed.
- Max depth 4, max 200 files. Past that, a `scan truncated` row.

## Generated config

```yaml
imports:
  - path: Makefile
  - path: docker-compose.yml
  - path: api/Makefile
    group: api
  - path: web/rerun.yml
    group: web
```

- Root files: no `group`. Subfolder files: `group` = folder path relative to the start folder, `/` kept.
- Order: tree order.

## Steps

1. **Scanner.** `scanImportable(root, opts) ([]importCandidate, truncated bool)` in `cmd/rerun/import_scan.go`.
   Verify: table tests for nesting, gitignore, skip dirs, sibling-covered-by-rerun.yml, depth and count caps.
2. **Config synthesis and save.** Build an in-memory `Config` from checked candidates via the existing imports loader. Write/update only the `imports:` node of `rerun.yml` with yaml.v3 nodes; reuse the column picker's save helper if one fits.
   Verify: round-trip test keeps comments and other keys; generated YAML matches the example above.
3. **Startup decision.** In `LoadConfig` / `newConfigTUI` (`config.go:193-204`, `tui.go:643-653`): open the picker when no `rerun.yml` and any candidate is outside the root; keep `MakeOnly` otherwise. Non-TTY: keep today's behavior.
   Verify: tests next to `makefile_startup_test.go` for each row of the user-flow table.
4. **Picker modal.** `cmd/rerun/tui_import.go` on `modalBox`, keys as above, `I` binding in `tui_update.go`, entry in `?` help. `enter` applies through the same path as `reconcile.go` `applyConfig`.
   Verify: `press`-driven tests for toggle, subtree toggle, partial state, filter, enter, ctrl+s, esc.
5. **Docs.** README "Loading" section (`README.md:426-435`) and `SKILL.md`.
   Verify: `make test`, `make lint`, manual run in a repo with nested Makefiles.

## Out of scope

- A CLI flag or non-interactive `rerun.yml` generation.
- Nested imports beyond one level.
- Scanning above the start folder.
