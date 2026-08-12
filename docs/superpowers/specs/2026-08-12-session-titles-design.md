# Session titles — design

**Date:** 2026-08-12
**Status:** approved (brainstormed with cat; second daily-use request after v1.1)
**Ships as:** v1.2.0

## Motivation

v1 had a `title` command, dropped in the v2 design under the standing rule
"dropped commands stay dropped until daily use proves them missed". Daily use
has now proved it missed: sessions leave the tmux window and terminal title
unnamed, so parallel workspaces are indistinguishable in the status bar.

The v1 behavior (owner's recollection — the behavioral oracle): on session
start, rename the current tmux window when inside tmux AND set the terminal
title via escape sequence so every emulator benefits; the name was the
workspace name truncated; after the session exited, the tmux auto-name was
restored.

## Decided behaviors

| # | Decision | Choice |
|---|----------|--------|
| 1 | Trigger | Automatic on `launch` and `claude`, immediately before the session child is spawned. No standalone `title` command. |
| 2 | Name | The workspace name (`ws.Name()`), hard-truncated (no ellipsis): 20 chars for the tmux window, 40 for the terminal title. |
| 3 | Terminal title | OSC 0: `\x1b]0;<name>\x07`, written to stdout ONLY when stdout is a TTY. Piped output stays byte-clean (this is also what keeps every existing txtar pin honest — tests have no TTY). |
| 4 | tmux rename | When `$TMUX` is non-empty: run `tmux rename-window <name>` in argv form (no shell). The `\x1bk…` escape is NOT used — it requires `allow-rename`, off by default in modern tmux. |
| 5 | Restore on exit | After the session child exits (success, failure, or absorbed-signal paths): when a tmux rename was done, run `tmux set-option -w -u automatic-rename` — UNSET the window option the rename set, so the window falls back to the user's global setting. No literal-name save/restore; no terminal-title restore (not portably readable; shells reset it at the next prompt). |
| 6 | Failure handling | Best-effort and silent, both directions: a missing tmux binary, a failed rename, a failed restore, or a non-TTY stdout never blocks, fails, or noises up the session. No stderr output. |
| 7 | Env | The tmux subprocess runs with the tool's OWN inherited env (it needs the real `$TMUX`/socket); it is tool plumbing, not a user command — the curated-env doctrine applies to user commands and stays untouched. |
| 8 | Version | v1.2.0 (new user-visible behavior). README v1-divergences appendix updated: `title` leaves the dropped list, replaced by the automatic behavior. |

## Mechanics

A small helper (new file in `internal/cli`, e.g. `session_title.go` — or a
leaf package if cli feels wrong to the implementer; it must not grow the
domain packages) with pure, table-testable parts:

- `truncate(name string, max int) string` — byte-safe on ASCII workspace
  names (names are slugged task ids + descriptions; multi-byte safety may
  simply clamp on runes).
- `oscTitle(name string) string` — returns `\x1b]0;` + name + `\x07`.
- An orchestrating `setSessionTitle(ws)` returning a `restore func()`:
  - stdout TTY → write the OSC sequence (40-char name).
  - `$TMUX` set → exec `tmux rename-window <20-char name>`; remember success.
  - returned restore runs `tmux set-option -w -u automatic-rename` iff the
    rename succeeded; otherwise a no-op.
- `runClaudeSession` (the shared runner both `claude` and `launch` use) calls
  it before spawning and defers the restore — the single call site is what
  keeps the two commands from drifting.

TTY detection: stdlib only — `os.Stdout.Stat()` and `Mode()&os.ModeCharDevice
!= 0`. The ioctl route needs per-OS request constants (TCGETS/TIOCGETA), i.e.
build tags the README promises not to have, or a new x/term module; the char-
device test is cross-platform, and its one false positive (stdout redirected
to /dev/null, a char device) emits escapes nobody sees.

## Testing

- Table tests: truncation (short, exact-limit, over-limit, empty), OSC
  assembly (exact bytes).
- txtar: a `tmux` PATH shim (like git/claude) logging its argv — pin that a
  session inside a fake `$TMUX` calls `rename-window` with the 20-char name
  and, after the session, `set-option -w -u automatic-rename`; pin that
  without `$TMUX` the shim is never invoked. Existing launch/claude txtars
  must pass unchanged (no TTY → no OSC bytes in stdout pins).
- A real `tmux` must never be invocable from tests (same PATH discipline as
  git/claude).

## Out of scope (deliberate)

A `title` command; config knobs (a `title:` template/off-switch and the
truncation lengths were considered and deferred by the owner's standing rule —
options are added on proven need, and the `${}` machinery makes the template
knob cheap whenever that need arrives; backlog:
`docs/superpowers/plans/2026-08-10-m5-deferred-items.md`); terminal-title
restore; titles on non-session commands (`up`, `exec`, …); Windows/ConPTY
anything.
