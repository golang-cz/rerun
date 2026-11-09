# Auto-hide, explicit imports, open-original modal

Status: DONE

## Summary
- `auto-hide:` on groups and imports lists a target only while it is running, starting, restarting or failed.
- With a `rerun.yml`, Makefile and docker-compose.yml are imported only when listed under `imports:`.
- `o` in the pane opens the original line(s) as printed, filtered by the active pills (in the list, `o` still opens the target's URL).

## User flow

### Auto-hide
- Visible: running, starting, backoff, failed (non-zero exit). Hidden: off, manual, stopped, never run, exited 0 for over 10 minutes.
- Never hidden while the pane shows that target (solo scope), so it cannot vanish under the cursor.
- The group's last row counts hidden members: `(+) 12 hidden`. `→`, `enter` or a click shows them dim; the row becomes `(−) hide 12`; `←`, `enter` or a click hides them again.
- `h` toggles the cursor's group's hidden members from any row of that group.
- A folded header reads `▸ make · 2 running · 12 hidden`.
- `ctrl+k` (go to) still finds hidden targets; running one makes it visible.

### Imports
- No `rerun.yml`: auto-detect Makefile and docker-compose.yml (today's behavior).
- `rerun.yml` present: import only what `imports:` lists.
- `rerun.yml` present, an unlisted Makefile or docker-compose.yml beside it: one startup notice `Makefile not imported · add it under imports:`.
- `rerun -migrate` adds explicit `imports:` entries for files that were auto-imported before.

### Open-original modal
- `o` (pane focused) opens the pane cursor's line exactly as printed: no cut, no columns, no color. Long lines wrap.
- A line selection (`shift+↑`/`↓` or drag) shows those lines as NDJSON.
- `cmd+a` (alt+i) in the pane selects every line the pills kept, so `cmd+a` then `o` gives the raw filtered log.
- `f` toggles raw ⇄ pretty JSON (pretty only when every line is JSON). `c` copies what the modal shows (no `y` anywhere; the fs events modal moves from `y` to `c`). `esc` closes.
- Header: `<scope> · N lines · <active pills>` and `raw`/`pretty` on the right. Same modal style as help and fs events.

## Config
```yaml
imports:
  - path: Makefile        # auto-hide: true is the default for Makefile imports
  - path: docker-compose.yml
groups:
  tools:
    auto-hide: true       # default false outside Makefile imports
```

## Steps
1. Config: `auto-hide` bool on groups and imports, Makefile default true; validation. Verify: config tests.
2. Imports: no auto-import with `rerun.yml`; startup notice; `-migrate` adds entries. Verify: config + migrate tests.
3. List: hide rule incl. 10-minute exit-0 grace and the solo-scope exception; `(+) N hidden` / `(−) hide N` row; `→`/`enter`/click/`h`; header count. Verify: list tests at 80 cols.
4. Open-original modal: `o` in the pane, NDJSON for selections, `cmd+a` select all kept lines, `f`, `c`; fs events modal `y` → `c`. Verify: modal tests incl. wide and non-JSON lines.
5. Docs (README, SKILL.md, help fits 80x30) and `docs/dx-decisions.md` entries.
6. Triple review + designer review; fix round. Verify: full suite green, `make install`.

## Out of scope
- Configurable grace (`auto-hide: 10m`).
- Multi-select in the left list.
