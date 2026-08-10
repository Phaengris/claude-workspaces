# M4 deferred items — carried forward from the final whole-branch review

*2026-08-10. Source: SDD ledger triage, final review of feat/m4-sessions
(96b2ca4..e379f50).*

## M5 (docs/install — the remaining milestone)
- **README requirements (all recorded divergences/decisions land verbatim):**
  history-probe encoding rule (all non-alnum → '-', empirical, safe failure =
  fresh session instead of --continue); .env-dirty decision (repos not
  gitignoring .env read dirty tool-wide → gc -d never collects them);
  gc-under-live-claude-session caveat; release/destroy daemon semantics;
  bundled short flags (-cp) invisible to injection detection; SHELL
  divergence; --json scope; export-prefixed .env lines; exec's project-sniff
  + `--` rule; stop:-after-daemons ordering; browse URL-print fallback.
- doctor additions: list out-of-root allocations loudly (adopted or not);
  bulk-git seam if doctor's walk makes it natural (twice re-ratified).
- down/restart target resolution is config-derived: a daemon renamed out of
  config while running is unstoppable by name (gc reaps its record when
  dead; the pids-dir enumeration exists — give down the same treatment or
  document `kill` as the escape hatch).
- ws.Dir not EvalSymlinks'd before probe; post-`--` injection-influence
  txtar pin; stale PWD in session env; batch-error flatten pin; parseStat
  message nit; Wait4(-1) t.Parallel landmine (in-test flagged).
- launch create-path/reuse asymmetries: --projects pre-flight existence
  check; concurrent-adopt race message.

## Recorded decisions
- release refuses while daemons run (reads pid files; still never writes).
- gc gates on the pids DIRECTORY (unlisted keys count; corrupt = reapable;
  unreadable dir = loud per-workspace error).
- IsMerged: refs/heads-qualified branch, unqualified base (documented
  asymmetry); DefaultBranch = source repo HEAD at gc time (documented).
- Dirty gate: any checked-out project dirty (Stats.Dirty ∪ Err) → survives
  gc -d.
- gc -d requires: tool-created + ≥1 checked-out + all merged + no live pid
  + clean.
