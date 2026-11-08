# Bubble Tea v2

Status: IN PROGRESS

## Summary

Move the TUI from Bubble Tea v1 / Lipgloss v1 / Bubbles v1 to v2.
Phase A is a behavior-neutral port: same keys, same look, all tests green.
Phase B uses what v2 unlocks, one small feature at a time.

All three are GA: bubbletea v2.1.1 (2026-10-28), lipgloss v2.0.6, bubbles v2.2.1.
Upgrade guide: https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md

## User-flow impact

- Phase A: none. Any visible change is a bug.
- Phase B:
  - Modifier keys v1 can't see, e.g. Ctrl+Enter, Shift+Enter, Cmd, in terminals with the kitty keyboard protocol (Ghostty).
  - A real terminal cursor in the finder and search inputs.
  - Overlays (finder, help, config error) drawn as lipgloss layers instead of string splicing.

## Surface (today)

- 7 TUI files, ~4,300 lines: `tui.go`, `tui_update.go`, `tui_view.go`, `tui_finder.go`, `tui_bar.go`, `tui_level.go`, `tui_rule.go`.
- Keys: 25 `tea.KeyMsg`, 67 `msg.String()` cases, 2 `msg.Type` switches (`tui_update.go:383`, `tui_finder.go:146`), 6 `tea.KeyRunes`.
- Mouse: one handler (`tui_update.go:523`), 16 `MouseButton*`, 10 `MouseAction*`.
- Program: `tui.go:45`, with `WithAltScreen`, `WithMouseCellMotion`, `WithFPS(30)`, `WithoutSignalHandler`.
- Bubbles: only `viewport`.
- Lipgloss: 13 `AdaptiveColor`, 16 `lipgloss.Color`. `x/ansi` stays (no `/v2`).
- Tests: 13 TUI test files. Messages are built by 3 helpers in `tui_test.go` (`keyMsg`, `press`, `mouse`) plus `keyCmd` in `tui_hardening_test.go`. No golden files.

## API shape (v1 → v2)

| v1 | v2 |
|---|---|
| `github.com/charmbracelet/bubbletea` | `charm.land/bubbletea/v2` |
| `github.com/charmbracelet/lipgloss` | `charm.land/lipgloss/v2` |
| `github.com/charmbracelet/bubbles/viewport` | `charm.land/bubbles/v2/viewport` |
| `View() string` | `View() tea.View`, via `tea.NewView(s)` |
| `WithAltScreen`, `WithMouseCellMotion` | `v.AltScreen = true`, `v.MouseMode = tea.MouseModeCellMotion` |
| `tea.KeyMsg` struct | `tea.KeyPressMsg` (KeyMsg is an interface) |
| `msg.Type` / `msg.Runes` / `msg.Alt` | `msg.Code` / `msg.Text` / `msg.Mod.Contains(tea.ModAlt)` |
| `msg.String() == " "` | `"space"` |
| paste as `KeyRunes` + `Paste` | `tea.PasteMsg{Content}` |
| `tea.MouseMsg` + `Action` | `MouseClickMsg`, `MouseReleaseMsg`, `MouseWheelMsg`, `MouseMotionMsg`; `msg.Mouse()` for X/Y/Button |
| `MouseButtonLeft`, `MouseButtonWheelUp` | `MouseLeft`, `MouseWheelUp` |
| `lipgloss.AdaptiveColor` | `compat.AdaptiveColor` (phase A), `lipgloss.LightDark` (phase B) |

To verify while porting, not covered by the guide: `WithFPS`, `WithoutSignalHandler`, `tea.Tick`, and lipgloss `Width`/`MaxWidth`/`Border` behavior.

## Steps

Phase A doesn't compile halfway, so steps 1–6 are one track and one commit. Each verify is the checkpoint the coder reports.

1. **Deps.** Swap the module paths and bump `x/ansi`. `go mod tidy`.
   Verify: `go.mod` has only v2 charm modules. No v1 `bubbletea`, `lipgloss` or `bubbles` left in `go.sum`.
2. **Program and View.** `View() tea.View`; alt screen and mouse mode move onto the view; keep FPS and signal behavior.
   Verify: `go build ./...` errors only in key and mouse code.
3. **Keys.** `KeyPressMsg` everywhere. `msg.Type` → `msg.Code`, `msg.Runes` → `msg.Text`. Audit every `" "` case for `"space"`. Finder and bar inputs take `tea.PasteMsg`, and keep the control-char filter.
   Verify: `rg 'tea\.KeyMsg\b|msg\.Type|msg\.Runes|KeyRunes' cmd/` returns nothing.
4. **Mouse.** Split by message type, rename buttons.
   Verify: `rg 'MouseButton|MouseAction' cmd/` returns nothing.
5. **Lipgloss and viewport.** Import paths, `compat.AdaptiveColor`, viewport v2 API.
   Verify: `go build ./...` and `go vet ./...` clean.
6. **Tests.** Port the helpers (`keyMsg`, `press`, `mouse`, `keyCmd`). Fix tests that type a space.
   Verify: `go test ./...` green. Test edits are limited to helpers and space keys; no assertion is loosened.
7. **Parity check.** Manual run in this repo and one compose import: keys, wheel, click, finder, help, light and dark terminal.
   Verify: Engineer signs off. `make install`.

Phase B, each its own small commit after A lands:

8. **Overlays as layers.** Finder, help and config-error modal via the lipgloss compositor. Delete the string overlay helpers.
   Verify: finder tests unchanged and green.
9. **Real cursor.** `View.Cursor` in the finder and search bar, replacing the drawn block.
   Verify: test asserts the cursor position.
10. **Light/dark.** `lipgloss.LightDark` from the reported background, drop `compat`.
    Verify: tests for both backgrounds.
11. **Keyboard enhancements.** Request them, then decide on extra bindings, e.g. Ctrl+Enter = restart from the finder.
    Verify: first confirm herdr passes enhanced key reports to the pane. If not, stop and report.

## Decisions (suggested default, flip any)

1. Phase A is behavior-neutral; features only in phase B. **Yes.**
2. `compat.AdaptiveColor` in phase A, real light/dark in phase B. **Yes.**
3. Phase A is one commit, because it doesn't compile halfway. **Yes.**
4. Coder tier: start `code/low`, upgrade to `code/medium` if it struggles. ~4,300 lines of mechanical but wide change. **Yes.**
5. Order: Makefile plan first, this one after. The Makefile work adds little TUI code. **Yes.**

## Out of scope

- New keybindings beyond step 11's decision.
- Visual redesign. Phase A keeps every pixel.
- Replacing `sahilm/fuzzy` or `x/ansi`.
