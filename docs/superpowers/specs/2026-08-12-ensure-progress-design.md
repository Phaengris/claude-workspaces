# Ensure-chain progress + launch hint — design

**Date:** 2026-08-12
**Status:** approved (brainstormed with cat; third daily-use request after v1.1)
**Ships as:** v1.3.0

## Motivation

A fresh `workspace launch` sat silent for ~21 seconds. Forensic timeline of
the real case (file mtimes): ~3s `git worktree add`, then ~18s of `setup:`
commands (`bundle install`, `npm ci`, `rails db:prepare`) with output
captured and nothing printed — the tool looked frozen while running the
user's own commands. Separately, launch's `hint: workspace cd <id>` reads as
a pending to-do printed right before Claude takes over the terminal.

## Decided behaviors

| # | Decision | Choice |
|---|----------|--------|
| 1 | What reports progress | The ensure chain's slow operations: the worktree checkout and EACH setup command. Wherever the chain runs with a reporter: `new`, `checkout`, `up`, and `launch` via the shared work functions. |
| 2 | Line format | Two-part, one line per step: `  <project>: checking out (branch <task id>)… ` / `  <project>: setup: <substituted command>… ` printed WITHOUT newline before the step; `ok (2.1s)\n` or `failed\n` appended when it ends. The branch named is the task id — the same value the existing summary line shows. A failed step's error then propagates exactly as today. |
| 3 | Durations | Always shown, `time.Since` rounded to 0.1s (`(0.0s)` is honest for instant steps). Tests pin them with a `\(\d+(\.\d+)?s\)` regex. |
| 4 | Output discipline | Plain text only — no spinners, no control characters, no TTY gating. Piped output stays line-clean because every started line is completed by the same code path. Setup command output remains captured (proc.Run is untouched); the progress line names the command, it does not stream it. |
| 5 | Plumbing | `wsp` gains a small reporter type (e.g. `type Step func(label string) (done func(err error))`) threaded through `EnsureProject`; `nil` reporter = today's silence (all non-CLI callers and tests unchanged). Formatting lives in `internal/cli` — dependency flow stays cli → wsp, and wsp never formats output. |
| 6 | Existing lines | The post-hoc summary lines (`workspace <name> created (index N)`, `  <project>: checked out (branch <b>), setup current`, `started <key> (pid N)`) are unchanged — existing txtar pins must keep passing. Progress lines are additions before them. |
| 7 | Hint split | `new` keeps `hint: workspace cd <id>` verbatim (there it IS the next step). `launch` replaces it on the CREATE path with `tip: in another terminal: workspace cd <id> — work alongside this session`; the reuse path prints no hint (the confusion is a create-path phenomenon; reuse output stays tight). The hint moves out of `newWork` to the command layer so the two commands can differ without duplicating creation logic. |
| 8 | Version | v1.3.0. README updated in the same commits (launch example output, the hint wording where shown). |

## Mechanics

- `internal/wsp/ensure.go`: `EnsureProject` (and its internal setup loop)
  accepts the reporter; it calls `step("checking out (branch <base>)")`
  around the worktree creation ONLY when it actually creates one (an
  already-checked-out project reports nothing — idempotent re-runs stay
  quiet), and `step("setup: " + substitutedCmd)` around each setup command
  ONLY when setup actually runs (current stamp → silence). `done(err)` is
  called on every path, error included.
- `internal/cli`: one printer building the reporter from a `*cobra.Command`
  out-writer and the project name; `newWork`, `checkoutWork`, `upWork` pass
  it; everything else passes nil.
- Reuse-launch and stamp-current paths therefore print exactly what they
  print today: nothing new. The decided-row-6 pins double as the guard.

## Testing

- txtar (new/launch/up flows): a fixture setup command that sleeps briefly
  (bounded) pinned as `  app: setup: sleep 0\.2… ok \(0\.[0-9]s\)` style
  regexes; a failing setup pins the `failed` completion ahead of the error;
  a re-run (stamp current) pins the ABSENCE of progress lines; all
  pre-existing output pins unchanged.
- Table test for the duration formatting if it grows a helper; otherwise the
  txtar regexes carry it.
- `new` txtar: `hint: workspace cd` still pinned. launch txtar: the new tip
  pinned on create, its absence pinned on reuse.

## Out of scope (deliberate)

Streaming setup output; spinners/TTY-dependent rendering; progress for
teardown/destroy (destroys are rare and already chatty); a `--quiet` flag —
add on proven need (backlog: `docs/superpowers/plans/2026-08-10-m5-deferred-items.md`).
