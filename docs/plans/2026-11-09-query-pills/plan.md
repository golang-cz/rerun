# Query pills: one bar for filter and search

Status: IN PROGRESS

## Summary
Merge `/` (filter) and `ctrl+f` (find) into one query bar. A query is a list of pills, ANDed.
Each pill either filters (hides non-matching lines) or highlights (keeps all lines, marks hits, `n`/`N` jump).
Pills use a small Loki/Kibana-like syntax whose field names are yq paths; a pill starting with `.` is raw yq.
Saved queries live in `rerun.yml` at the top level, per group, and per target, picked with `@`.

## User flow
- `/` opens the bar (`ctrl+f` and `v` open the same bar). Type, `enter` adds a pill. Empty `backspace` removes the last pill.
- `←`/`→` walk pills (when the input is empty or the text cursor is at 0). On a selected pill: `x`/`backspace` removes, `enter` edits it back into the input, `space` toggles filter ⇄ highlight.
- Highlight pills draw dim/outlined, filter pills solid; the kind is also shown by brackets (`[..]` filter, `⟨..⟩` highlight), never color alone.
- Full-text and field matches of every pill are highlighted in the pane; `n`/`N`/`ctrl+g` jump between hit lines.
- Bar header shows the scope, pill count and `matching / total` lines.
- `@` in the input opens the saved-query list for the current scope (finder style); choosing one adds pill `[@name]`. `→` on `[@name]` expands it into its pills.
- `esc` leaves the bar but keeps the pills (today's rule).
- Invalid pill text: the input turns red with the parse error in the footer; `enter` does not add it.

## Pill syntax
| Pill | Meaning |
|---|---|
| `timeout` | full text, case-insensitive substring of the raw line |
| `"connection refused"` | full-text phrase (quotes allow spaces) |
| `-healthz` | negated full text |
| `level=error` | field equals, case-insensitive for strings, numeric compare for numbers |
| `level=error\|fatal` | field equals any |
| `level!=debug` | field not equal (any of a `\|` list) |
| `http.status>=400` | `>` `>=` `<` `<=` numeric |
| `msg~refused` | field contains, case-insensitive |
| `msg~/time(out\|d out)/` | field matches regex (Go RE2); `msg=~re` accepted as alias |
| `trace_id=*` / `trace_id!=*` | field exists / missing |
| `.expr` | raw yq (today's behavior), truthy result keeps the line |
- Field path: yq path without the leading dot (`http.status` ≡ `.http.status`); keys with special chars via yq quoting `."a-b"`.
- Field name resolution: a column name visible in the current scope's `columns:` resolves to that column's `expr` first, then the yq path.
- A field pill never matches a non-JSON line. Full-text pills match any line.
- Multiple pills in one typed input are allowed: `level=error -healthz` → two pills on `enter` (split on unquoted spaces).

## Config (rerun.yml)
```yaml
queries:                 # global
  errors: level=error|fatal
groups:
  api:
    queries:
      4xx: http.status>=400 http.status<500
targets:
  web:
    queries:
      slow: http.duration_ms>500 -healthz
```
- Key name `queries`. Value is a pill string. Parsed at load; a bad query is a config error naming the file, key and pill.
- Scope: pane scope's target(s), their groups, and global. Name clash → most specific wins.

## API shape (cmd/rerun)
- `pill.go`: `type pill struct{ raw string; kind pillKind /*filter|highlight*/; neg bool; field string; op pillOp; values []string; re *regexp.Regexp; yq *yqlib.ExpressionNode; saved string; sub []pill }`
- `parsePills(input string, cols []Column) ([]pill, error)`; `(p pill) match(line string, obj any) bool`; `(p pill) spans(line string) [][2]int` for highlighting.
- Model: `m.pills []pill` replaces `m.filters` and `m.search`; `searchMatches` derives from highlight-capable pills.
- Config: `Queries map[string]string` on top level, group, target.

## Steps
1. Pill parser + matcher with table tests (every syntax row above, errors, quoting, column alias). Verify: `go test -run Pill`.
2. Model swap: pills replace filters + search; one bar; `ctrl+f`/`v`/`/` open it; filter/highlight toggle; n/N over hits; header count. Verify: tests for each flow bullet.
3. Config `queries:` load + validation + `@` picker + `[@name]` expand. Verify: config tests + picker test.
4. Docs: README, SKILL.md, `?` help. Verify: help fits 80x30 (existing tests).
5. Triple review (`pi-review 3`) + designer DX review; fix round. Verify: full suite green, `make install`.

## Out of scope
- `rerun.local.yml` overrides (later).
- OR across pills, parentheses (use raw yq).
- Persisting ad-hoc pills across restarts.
