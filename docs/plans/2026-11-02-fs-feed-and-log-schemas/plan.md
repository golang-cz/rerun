# FS change feed and log schemas

Status: DONE

## Summary

Turn rerun's TUI into a small localhost log explorer:
- The fs drawer becomes a time-ordered change feed that names what changed and which targets it restarted.
- The right pane shows only program output. rerun's own lines move to rows, the feed, and a footer.
- Targets that share a JSON log shape share a named `schema` with columns. Selecting a schema row merges its targets' logs.
- Each JSON line can expand in place. From there a click adds a column or a filter; `ctrl+s` saves columns to rerun.yml.

## User-flow impact

- Left pane: target rows grouped under schema rows. The ALL row is gone; a schema row is the merged view for its targets.
- Target rows briefly highlight when a change batch restarts them.
- Right pane: program output only, table layout when the selection has columns, a `(+)` cell at the right edge of each JSON line.
- Footer: rerun notices (config reloaded/failed, watcher and start errors) and the unsaved-changes hint.

## Config shape (rerun.yml)

```yaml
schemas:
  backend:
    columns: [level {.level}, msg {.msg}, status {.status}]

omsx-api:
  schema: backend
  cmd: make -C apps/api run
```

- `schemas` is a reserved top-level key; every other top-level key stays a target.
- A target's own `columns` still works and wins over its schema's columns.
- `schema:` naming an undefined schema is a config error with the usual source excerpt.
- Targets without `schema:` are listed after the schema groups, ungrouped.

## Steps

1. **Right pane cleanup.** The TUI stream renders only `evOutput`. Exit, change, start and backoff info shows on the target row. Notices (`evNotice`, `evReconcile` lines, `evWatcherErr`, `evRunnerErr`) go to a one-line footer: info fades after ~4s, errors stay until the next key. Plain mode is unchanged.
   Verify: TUI tests assert the stream holds only target output; footer tests per notice kind; `make test`.
2. **Schemas in config.** Parse `schemas:` and `schema:`, resolve effective columns per target, error on unknown schema. Add a rerun.yml writer that sets a schema's (or a target's own) `columns` through yaml.v3 nodes, keeping comments, anchors and key order.
   Verify: config tests for parsing, precedence, unknown schema; writer round-trip test keeps comments and anchors byte-for-byte outside the edited node.
3. **FS change feed.** Replace the change tree with a feed:
   - Events from all watchers within 150ms form one batch.
   - One line per batch, newest first: time, GitHub-style compressed path (single-child dir chains collapse), `+N` for more files, `→ target, target` it restarted (`config` for rerun.yml reloads).
   - Batches older than 1 minute drop off; with none left, the latest batch stays, dimmed.
   - Max 6 lines tall, scrollable when focused.
   - `enter` (or click) on a batch lists its files with `+` / `~` / `−` glyphs; `esc` closes.
   - Restarted target rows highlight for ~1s.
   Verify: unit tests for batching, path compression, expiry, target attribution; TUI render test of the feed.
4. **Schema groups in the list.** Schema rows replace ALL; selecting one merges its targets' output; selecting a target solos it. Table layout is the default whenever the selection has columns; `c` toggles raw.
   Verify: TUI tests for grouping, merged selection, table default; `make test`.
5. **Expand, pick, filter, save.**
   - Every JSON line gets a right-aligned `(+)`; click or `enter` expands pretty JSON under the line, `(−)` collapses.
   - In the expanded view, clicking a key adds it as a column to the selection's schema (or target); clicking a value adds a filter `.key == value`.
   - Filters show as removable chips in the pane header; `v` still edits a free yq filter.
   - Column changes mark the session dirty; the footer reads `unsaved columns · ctrl+s save`; `ctrl+s` writes them with the step 2 writer.
   Verify: TUI tests for expand/collapse, key→column, value→filter, chip removal, dirty flag, save round-trip.

## Out of scope

- Full tree view on a feed line (nice to have, later).
- Saving filters to rerun.yml.
- Mouse-driven column reordering.
