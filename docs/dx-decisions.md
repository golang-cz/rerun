# DX decisions

This file logs product and developer-experience decisions, each with a short why.

- 2026-11-09 · `c` copies everywhere: the selection or pane cursor line, and the fs events modal. Why: one copy key to learn.
- 2026-11-09 · `t` toggles columns: the columns table moved off `c`, which now copies. Why: each key does one job.
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
