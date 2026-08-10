# Post-v1.0 backlog — final deferred-items doc (M5 close-out)

*2026-08-10. Source: M5 final whole-branch review (50acebe..76b2628) + the
plan's explicit deferrals. There is no next milestone: this file IS the
backlog. Prioritize on daily-driving feedback.*

## Features (plan-deferred, spec-known)
- Profiles (spec's dev/test start subsets — "many adopted, few hot").
- Docker runtime seam (v1 planning's 05-docker-architecture direction).
- launch --projects flag (reuse-path project addressing without the
  description-slot grammar); project-mismatch detection on reuse.
- Session pid marker so gc -d can refuse under a live claude session
  (today: dirty gate usually saves it; documented caveat).
- Bulk-git render seam for ls -g / status (three-times re-ratified
  deferral; only the render paths pay today).

## Hardening / polish
- sessionEnv: overlay PWD=ws.Dir (or drop the key) — stale $PWD from the
  launcher reaches session children. [taken in M5 close-out if the fix
  landed; else top of this list]
- install layoutFor: honor XDG_CONFIG_HOME/XDG_DATA_HOME (hardcoded
  ~/.config/fish + ~/.local/share today — relocated-XDG users get a
  wrapper fish never autoloads).
- Manifest strategy across layout changes: union-with-surviving-entries or
  layout versioning (replace-not-merge unrecords dropped paths today).
- Manifest-first install ordering (crash before first write leaves
  unrecorded litter; re-install self-heals — ratified, safer variant noted).
- doctor: duplicate-index registry finding (cheap port-collision surface);
  awareness of colon-bearing bare start entries (load-time warning for
  `- echo "run: it"`-style accidental daemons).
- destroy (plain): best-effort worktree prune for drifted-out-of-config
  worktrees (today only --force prunes; stale metadata until then).
- Concurrent-adopt race: loser sees the generic already-allocated error
  instead of the friendly no-op message.
- hook: suppress header/footer when status fails after which succeeded
  (cosmetic race).
- README survival-matrix Clean cell: note unreadable-git-state counts as
  dirty (code pinned; text nuance).
- Test debt: gc batch-flatten deeper pin; adopt --projects pre-flight
  existence check; zsh completions unverified-by-execution on this machine.

## Standing decisions (do not re-litigate without cause)
- Exact-name allowlist + env_allow escape hatch; PATH always sanitized.
- Sessions get sanitized-inherited env; user commands get the curated env.
- stop: runs AFTER daemons (v1 divergence, documented).
- "stopped (TERM/KILL)" promises leader death only.
- gc -d five gates: tool-created, ≥1 checked out, all merged
  (refs/heads-qualified), no live pids-dir record, clean.
- Uninstall removes exactly the manifest; refusals for /, HOME,
  root-or-inside; survivors manifest on failures.
