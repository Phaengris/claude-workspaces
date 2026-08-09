# M3 deferred items — carried forward from the final whole-branch review

*2026-08-10. Source: SDD ledger triage, final review of feat/m3-services
(8da386d..d85a7c0).*

## Documented v1 divergences (user-facing — must reach the M5 README)
- **stop: ordering**: v1 ran `stop:` commands BEFORE killing daemons; v2 runs
  them AFTER the project's daemons stop. A `stop:` command that talks to a
  running daemon (graceful drain) behaves differently — rewrite as a
  pre-stop hook is not available in v1.0; drain logic belongs in the daemon's
  own TERM handler.
- **"stopped (TERM/KILL)" promises LEADER death only** — a TERM-ignoring
  group member under a TERM-obeying leader can linger (group-emptiness
  polling deliberately absent: zombies would hang the in-process case).
- logs -n reads .log only; stderr-logging daemons (python http.server) show
  output via -f or the new empty-stdout note.

## M4 (adopt/release/gc, claude/launch)
- Bulk git-derivation seam (re-ratified deferral): pairs with gc/adopt's
  multi-workspace walks; ProjectStates is the shared surface.
- proc: SIGKILL-on-pidfile-write-failure path untested; parseStat
  malformed-input table absent.
- new.go undo: failed WorktreeRemove mid-undo still removes dir + releases
  (registered-but-missing worktree metadata; manual prune) — pairs with M4's
  moved-repo/--force escape hatch (carried from M2).
- down.go stop-epilogue gate uses os.Stat existence, not IsWorkTreeRoot —
  deliberate no-git-spawn choice or switch to shared predicate (decide).
- readTail rescans accumulated buffer per chunk (O(size²/chunk) on huge
  logs with sparse newlines) — carry newline count instead.

## M5 (docs/install)
- README env-curation + services sections: the divergences above, SHELL
  divergence caveat, --json scope, export-prefixed .env lines, exec's
  "command named like a project needs explicit path / -- at slot 1" rule,
  browse's no-xdg-open URL-print behavior.

## Recorded decisions
- destroy = down (phase 0, after containment gate) + teardown + remove +
  release; stop failure aborts everything.
- Setsid deliberately absent (daemons share the session; spec-compliant).
- Sniff grammar: exec's args[1] is a project iff configured; unconfigured →
  it IS the command (exit-3-for-unknown-project unreachable by construction).
- `--` is the sniff suppressor ONLY at the slot after the workspace; later
  `--` reaches the exec'd command verbatim.
