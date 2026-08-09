# M1 deferred items — carried forward from the final whole-branch review

*2026-08-03. Source: SDD ledger triage, final review of feat/m1-read-only
(4dacf0b..a51436b). Recorded decisions at the bottom.*

## M2 (checkout/new — first mutating milestone)
- **gitx: neutralize GIT_DIR-family env** (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE,
  GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY) in git() — top-priority gitx item:
  today, running from inside a git hook would answer about the wrong repo.
  Add `--untracked-files=normal` to the dirty check in the same touch.
- **Bulk git-derivation seam**: ls -g runs ProjectStates serially per workspace
  before the bounded StatsFor (duplicated + unbounded reads); status <ws>
  unbounded per-project. Design: ProjectStates accepting prefetched stats, or
  wsp.ProjectStatesFor(cfg, []Workspace) doing one bounded discovery wave.
- **M2 plan constraint**: up/checkout must consult checked_out before
  setup_current — the JSON field is false for not-checked-out projects even
  when a matching stamp exists on disk.
- gitx: git() panics on empty args; errors don't name the dir.
- Config: joined-error ORDER unpinned; decodeStrict drift-guard untested;
  no Load-level multi-error test. AST-level template positions (from M0) —
  revisit with env_allow wiring.
- ui: PrintJSON marshal-error / Table empty-rows untested.
- Output polish batch: ports silent when values empty but workspaces exist;
  ls -g renders `app@` for unborn-HEAD (status says "(branch unknown)");
  loadRoot placement (move to root.go when the mutating preamble appears);
  extract requireProject(cfg, name) at the 3rd consumer.
- Tests: extract shared txtar preamble (WORKROOT sed, git shim, hermetic env)
  as a testscript setup/custom command when the 4th txtar lands; pin
  projects:[] present-but-empty JSON; printStatus unit test incl.
  "projects: none"/[]; status --json exit-code case.

## No action (recorded decisions)
- Unknown project exits 3 (spec §9 "workspace/project not found") — fix-wave
  a51436b flipped env/cd from the unratified exit-1 deviation.
- ProjectStates returns ONLY checked-out projects; status backfills the
  configured rest for display.
- alloc.Block is the single home of the range formula.
- Ambiguous TaskID = plain error exit 1 listing candidates (NOT ErrNotFound).
- which compares paths as written (no EvalSymlinks) — documented in code.
- statusEntry duplicates lsEntry header fields — extract at 3rd consumer.
