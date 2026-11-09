# DX decisions

This file logs product and developer-experience decisions, each with a short why.

- 2026-11-09 · `c` copies everywhere: the selection or pane cursor line, and the fs events modal. Why: one copy key to learn.
- 2026-11-09 · `t` toggles columns: the columns table moved off `c`, which now copies. Why: each key does one job. (superseded: t removed, see below)
- 2026-11-09 · `→` lands on the last service line: focusing the pane skips rerun's own rules. Why: the newest service output is what the reader wants.
- 2026-11-09 · `b` cycles full, names, hidden: the left list gives its width back to the log. Why: a long log needs the room more than the list.
- 2026-11-09 · expand scrolls minimally: opening a line moves the pane only as far as the block needs. Why: an expansion should not lose the reader's place.
- 2026-11-09 · `ctrl+k` is "go to": the finder jumps to a target as often as it runs one. Why: "find" said only half of it.
- 2026-11-09 · one query bar with filter and highlight pills, and queries in rerun.yml: pills AND together and saved queries persist. Why: one place to filter, and queries survive restarts.
- 2026-11-09 · fs events modal replaces the fs pane, with a left hint `fs events · Ns ago`: the batches get room to read. Why: the pane was too narrow for batch detail.
- 2026-11-09 · click a log line expands it, a plain-line click copies, drag selects: one gesture per line type. Why: a click should never do nothing.
- 2026-11-09 · `ctrl+l` blanks the pane and keeps scrollback: the clear hides old output without deleting it. Why: scrollback stays searchable.
- 2026-11-09 · no `›` gutter glyph: each pane row drops a glyph. Why: the glyph cost bytes on every redraw.
- 2026-11-09 · shift+arrows select lines in the pane: a keyboard selection reuses the drag highlight. Why: copying lines should not need the mouse.
- 2026-11-09 · `auto-hide` lists idle targets only while they run or fail: Makefile imports default on, and an exit 0 hides after 10 minutes. Why: the list keeps what needs attention, and a clean exit stays readable for a while.
- 2026-11-09 · with a rerun.yml, imports are explicit: only `imports:` loads, and `rerun -migrate` writes the entries. Why: no surprise targets appear from a file nobody listed.
- 2026-11-09 · `o` in the pane opens the original lines, already filtered by the pills: raw logs with search. Why: the reader wants the exact text, not columns.
- 2026-11-09 · `c` is the only copy key: no `y` anywhere, and the fs events modal moved from `y` to `c`. Why: one copy key across every modal.
- 2026-11-09 · `t` removed: `→` expands a line's fields and `o` shows the raw lines. Why: a whole-pane raw toggle was redundant.
- 2026-11-09 · modal size defaults (80%, 80–120 cols, 80% height, min 10 rows) live in `cmd/rerun/tui_modal.go`: every modal shares one box. Why: the modals drifted apart in size.
- 2026-11-09 · `v` no longer opens the query bar; `g` opens go to like `ctrl+k`, outside text inputs. Why: `/` already opens the bar, and `g` is a one-key jump.
- 2026-11-09 · go to lands in the pane, and a runnable row shows `▶` with `enter run`: the footer names what enter does. Why: a jump should show the log, and a run should not look like a jump.
- 2026-11-09 · fs events services start one 2-space gap after the longest path, and no `·` separator: the `↻` names the restarts. Why: a right-aligned list drifted with every path length.
