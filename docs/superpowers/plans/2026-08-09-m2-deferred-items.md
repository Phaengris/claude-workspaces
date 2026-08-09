# M2 deferred items — carried forward from the final whole-branch review

*2026-08-09. Source: SDD ledger triage, final review of feat/m2-lifecycle
(a5e891f..36f9b46).*

## M3 (proc daemons / up-down)
- Bulk git-derivation seam (carried from M1): ls -g / status perf; ProjectStates
  now also calls IsWorkTreeRoot per project — design the prefetch seam here.
- Stale comments: destroy_test.go gitInit doc + ls.go comment still say
  IsWorkTree/--is-inside-work-tree; predicate is now IsWorkTreeRoot.
- new.go undo: a failed WorktreeRemove mid-undo still removes dir + releases,
  leaving registered-but-missing worktree metadata (manual `git worktree prune`
  to recover) — consider stopping the undo stack at a failed worktree undo.
- setup output streaming/progress UX (M2 captures output, reports on failure).

## M4 (adopt/release/gc, claude/launch)
- Registry-as-trust-boundary set: checkout has no containment gate (writes,
  not deletes — lower stakes); adopted-destroy leaves worktrees registered;
  moved-source-repo makes a workspace undestroyable (needs --force escape
  hatch); adopted-destroy teardown half needs real fixtures.
- IsWorkTreeRoot symlink caveat: root reached through a symlink reads as
  "not checked out" (fail-safe direction; one-line EvalSymlinks fix if real).

## M5 (docs/install)
- README env-curation section (spec §6 mandates): SHELL divergence caveat,
  --json scope (query commands only), `export FOO=bar` .env lines become key
  "export FOO" (strip or skip — decide), stderr-line /etc/profile assumption.
- Release-notes item: pre-existing NON-empty dir at a project dest now errors
  loudly (was silent success); empty dir is adopted by worktree add.

## Recorded decisions
- IsWorkTreeRoot (top-of-worktree) is the checked-out predicate everywhere;
  IsWorkTree retained for tests/contrast only.
- destroy: containment gate BEFORE teardown, adopted exempt (legitimately
  outside root); run-remaining teardowns on failure (convergence over strict
  reverse-topo invariant, documented).
- Run-env totality: proc.Run nil env = empty env; wsp.CommandEnv is the sole
  spawn-env composer (moved from proc — import cycle).
- new: transaction undoes worktrees (guarded by IsWorkTreeRoot) then dir then
  allocation; branches never deleted.
