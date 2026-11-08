# Column colors

Status: UNDECIDED

## Summary

Let a table column set its own color, so each attribute reads differently.
Everything else a column needs comes from yq. Only colors are missing.

## What yq already covers

Checked against yq v4.54.1 (`pkg/yqlib/lexer_participle.go`):

- Fallbacks and defaults: `{.message // .error}`, `{.status // "-"}`.
- Time: `format_datetime`, `from_unix`, `tz`, `with_dtf`.
- Strings: `upcase`, `downcase`, `trim`, `sub`, `split`, `join`, `test`, `match`.
- Truncation: string slicing `{.msg | .[0:80]}`. It counts runes, not cells, and adds no `…`.
- Not in yq: rounding, padding, alignment, duration formatting, any color operator.

## Proposed shape

`columns:` also accepts a mapping, in written order. Today's list form keeps working.

```yaml
columns:
  ts:     {value: '{.time | format_datetime("15:04:05")}', color: gray}
  level:  "{.level}"
  msg:    "{(.message // .error) | .[0:80]}"
  status: {value: "{.status}", colors: {"2..": green, "4..": yellow, "5..": red}}
```

1. Short form `name: "{expr}"`: no color, same as today.
2. Object form `name: {value, color, colors}`.
3. `color`: one fixed color. Names, 256-color numbers, or `#hex`.
4. `colors`: color picked by value; `.` matches any one character.
5. `level` keeps its built-in value colors; its own `colors:` replaces them.

## Open decisions

1. Object form only, or also the key form `ts gray: "{expr}"`? Suggested: object form only.
   The key form avoids an object for one color, but adds a third column spelling.
2. Color in a YAML comment (`# color: gray`)? Suggested: no.
   Formatters and copy-paste drop comments silently, and typos cannot be reported.
3. Value patterns in `colors:`: `.` wildcard or full regex? Suggested: `.` wildcard.
4. More built-in special attributes beyond `level` (e.g. `status`)? Suggested: not now.

## Out of scope

- `max`, `align`, `duration`, time shortcuts: yq covers them or they wait for a real need.
- Width-aware truncation with `…`.
