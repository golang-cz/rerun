# rerun — fully complete

Everything deferred in handover.md, in build order. Each track: coder →
reviewer gate → one commit. Copy/self-watch/reconcile already shipped
(fb90399, 6049974); this finishes the backlog.

## T1 — nesting guard

- rerun sets `RERUN=1` on every spawned child (TUI targets and `--` ad-hoc).
- Startup refuses when `RERUN` is already set: one stderr line, exit 1,
  message says `unset RERUN to force`.
- Verify: unit tests for env propagation + guard; existing binary-spawning
  tests still pass.

## T2 — start stagger + restart backoff

- Stagger: at most 4 targets starting at once (gate in the group, not the
  TUI). A slot frees on first output line, exit, or 10s timeout. Queue is
  FIFO in rerun.yml order. Queued rows show `starting`.
- Backoff: for restarts following a run that exited by itself within 30s —
  3+ restarts in a 30s window delay the next start 1→2→4→8→10s (cap).
  Window resets after 60s stable. Row status `backoff Ns`, pane line
  `# backoff 4s (5th restart in 30s)`. Manual stops never back off.
- `statusW` 9→10 so `backoff 10s` fits.
- Verify: gated-start test with fake commands; backoff timing test with a
  fake clock; no gate deadlock under shutdown.

## T3 — ✱ literal + help legend

- ✱ badges any row whose output is not currently in the pane: muted rows,
  rows hidden by another row's solo, and unseen exits. Cleared when the
  row's lines return to the pane (unmute, solo switch, esc).
- Help gains one legend line: `● run  ○ stopped  ✓ ok  ✗ exit  · off  ✱ unseen`.
- Verify: badge on solo-hidden output; cleared on esc; legend in helpView.

## T4 — ringbuf

- Swap `appendCapped` slices for golang-cz/ringbuf (v0.6.0, in go.mod):
  target lines and merged, 10k cap each. `paneLines()` semantics unchanged.
- Verify: cap behavior identical (10k newest kept, O(1) append), suite green.

## T5 — search + filter bar

- `/` opens a search on the pane's bottom line: incremental case-insensitive
  substring, `n` next, `N` prev, jumps freeze the tail like scroll, `esc`
  closes. Counter `12/47` while active. No per-line highlight.
- `v` opens the filter bar: plain text hides non-matching lines; a leading
  `.` evaluates a yq expression (yqlib) per JSON line, e.g.
  `.response.status >= 400`. `esc` clears. Filtered line count shows in the
  bottom line. New lines still collect while filtered; pending unchanged.
- Verify: search jump + wrap + counter tests; filter substring + yq expr
  tests; filter + solo/mute interaction (rebuildMerged path).

## T6 — columns, table mode, dynamic URL

- rerun.yml per target: `columns: [method {.request.method}, status
  {.response.status}]` — name + yq expr. Self-watch reconcile already
  handles config diffs; wire `columns` into it.
- `c` toggles the cursor row's pane between raw lines and a table of the
  latest values parsed from each JSON line. Lines that fail to parse show
  their raw text. No `columns:` → hint instead.
- Dynamic URL: JSON output lines with `url`/`addr`/`listen`/`port` update
  the row's URL (last wins), which relinks the row and title line.
- Verify: table toggle tests, column extraction tests, reconcile picks up
  columns edits, URL capture updates row link.

## Out of scope

- Windows beyond what exists, per-line match highlight, search history,
- multi-cell (character) selection, system clipboard fallback beyond OSC 52.
