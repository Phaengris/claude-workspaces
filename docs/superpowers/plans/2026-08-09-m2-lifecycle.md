# M2 Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The mutating lifecycle — `new`, `checkout`, `destroy` — with transactional creation, git worktree plumbing, WORKSPACE.md/CLAUDE.md/.env writers, and the curated-env foreground spawn contract (setup/teardown actually run).

**Architecture:** `gitx` gains the write side (worktree add/remove) plus the deferred hardening; new leaf `proc` owns the foreground spawn contract (`$SHELL -lc`, `envx.Curated`); `wsp` gains writers (WORKSPACE.md, CLAUDE.md-once, .env seeding) and ensure-steps (worktree, setup-with-stamp); `alloc` gains locked mutation; `cli` wires three commands. Everything is an idempotent ensure; `new` is the one transaction (undo-on-failure).

**Tech Stack:** No new dependencies. ACCELERATED MODE: plan gives binding contracts + exact behaviors + named test cases; implementers write idiomatic Go against them (reference code only where the logic is subtle). v1's documented behavior remains the oracle; never port Ruby code.

**Plan 3 of 6.** Base: master @ 3fcfe11 (M0+M1 merged). Deferred-items inputs: `docs/superpowers/plans/2026-08-03-m1-deferred-items.md` (gitx hardening, setup_current warning), `2026-07-30-m0-deferred-items.md` (env_allow wiring, alloc.Save doc note).

## Global Constraints

- Module `git.internal/cat/claude-workspaces-go`; CGO_ENABLED=0; POSIX-only; runtime deps stay cobra/goccy/x/sys.
- Exit codes: 0/1/2/3/4 as spec §9; unknown workspace/project → 3; usage → 2; config → 4.
- git argv-form only. All spawned user commands (setup/teardown) run `$SHELL -lc "<cmd>"` (fallback `/bin/sh`) in the project dir with the CURATED environment (spec §6) — never the raw parent env.
- Derived state rules (spec §3): stamp `.workspace/setup-<project>.ok` = SetupHash (already implemented, wsp.SetupHash); **consult CheckedOut before setup_current** (M1 deferred warning).
- Idempotence: `checkout` on an existing worktree is a no-op-then-continue; re-running any failed op converges. `new` alone is transactional (LIFO undo of what THIS invocation created).
- Determinism: sorted output everywhere; `--json` via ui.PrintJSON.
- Doc comments on exported identifiers stating constraints; gofmt/vet/`go test ./...` (+ `-race` on packages with concurrency) clean before every commit; conventional commits + trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`; TDD failing-first per task.
- Clean-room: v1 README/rspec behavior is the oracle; no Ruby code consultation for implementation.
- Reuse M1 txtar patterns (WORKROOT sed + `! grep WORKROOT`, git hermeticity env, `'...'$WORK'...'` quoting for absolute paths, PATH-shim block last). The 4th new txtar triggers the extraction rule: if you add a 4th copy of the shared preamble, extract a testscript setup helper instead.

## Decided behaviors (settle disputes by this table; deviations need a documented reason)

| Topic | Decision |
|---|---|
| Workspace dir name | `<task_id>` when description empty, else `<task_id>_<slug>`; slug = lowercase description, every non-`[a-z0-9]` run → `-`, trimmed of `-`; empty slug after cleaning → task_id alone |
| Task-id validation | must match `^[A-Za-z0-9][A-Za-z0-9._-]*$`, length ≤ 64; violation → usage error (exit 2) |
| Worktree branch | named `<task_id>`, created from the project's `base_branch` (default: repo's current HEAD if `base_branch` empty). Branch already exists → reuse it (`git worktree add <dest> <branch>`); else `git worktree add -b <branch> <dest> <base_branch>` |
| checkout idempotence | dest already a worktree → skip worktree-add, still ensure `.env`, setup stamp, WORKSPACE.md refresh |
| `new` on existing workspace (dir exists or task id allocated) | plain error exit 1 naming the conflict (launch, M4, will layer reuse on top) |
| Setup env | command string substituted with `wsp.RuntimeVars`; process env = `envx.Curated(os.Environ(), envAllow, overlay)` where envAllow = cfg.EnvAllow ∪ project.EnvAllow and overlay = `wsp.ResolvedEnv(...)` for that project |
| Setup failure | error carries project name + first non-empty stderr line; stamp NOT written; checkout of remaining projects continues, errors joined; exit 1 |
| Teardown | same spawn contract, run per checked-out project in REVERSE dependency order; any teardown failure aborts `destroy` before dir removal (re-run converges) |
| destroy on adopted workspace | runs teardown + releases allocation + prints that the dir is left in place (tool never deletes dirs it didn't create) |
| destroy scope | stops nothing (daemons are M3); removes the workspace dir (tool-created only) + releases; worktree branches are NOT deleted (user's work) |
| .env seeding | read `<repo>/.env` if present (parse `K=V` lines, ignore comments/blank; values kept verbatim incl. quotes); overlay `wsp.ResolvedEnv` on top (workspace wins); write sorted `K=V\n` to `<worktree>/.env` (0600) |
| WORKSPACE.md | regenerated wholesale on new/checkout: header (name, task, description, created), values table, per checked-out project: dir, branch, and the project's `instructions` from config verbatim |
| CLAUDE.md | created ONCE at `new` with a single reference line to WORKSPACE.md (spec §5); never rewritten if present |
| Dependency order | `up`-style topological order over checked-out projects using config `depends` (edges to non-checked-out ignored — spec §7); setup runs in topo order, teardown in reverse |

---

### Task 1: gitx — write side + deferred hardening

**Files:** Modify `internal/gitx/gitx.go`, `internal/gitx/gitx_test.go`.

**Produces (binding):**
- `git()` helper hardened: child env = `os.Environ()` minus `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY` (M1 deferred: a caller inside a git hook must not poison `-C` discovery); errors include the dir and git's stderr; empty `args` returns an error instead of panicking.
- Dirty check gains `--untracked-files=normal` (stable definition regardless of host config).
- `gitx.WorktreeAdd(repo, dest, branch, base string) error` — branch exists (`git -C repo rev-parse --verify refs/heads/<branch>`) → `worktree add <dest> <branch>`; else → `worktree add -b <branch> <dest> <base>`; empty base → omit (HEAD).
- `gitx.WorktreeRemove(repo, dest string, force bool) error` — `worktree remove [--force] <dest>`.
- `gitx.BranchExists(repo, branch string) bool`.

**Tests (named, failing-first):** `TestGitEnvNeutralized` (t.Setenv GIT_DIR to a bogus repo; IsWorkTree/Branch on a real repo still answer about the real repo), `TestWorktreeAddNewAndExistingBranch` (add → worktree on new branch `T-1`; remove; add again → reuses existing branch, no `-b` error), `TestWorktreeRemove`, `TestGitEmptyArgs`, dirty-check still green. Keep `-race` green.

**Complexity:** medium-high — **Fable** (git semantics + env hardening).

- [ ] Failing tests → verify fail → implement → `go test -race ./internal/gitx/` + full suite → commit `feat(gitx): worktree write ops; neutralize GIT_DIR-family env`.

---

### Task 2: proc — foreground spawn contract

**Files:** Create `internal/proc/proc.go`, `internal/proc/proc_test.go`.

**Produces (binding):**
- `proc.Run(dir, command string, env []string) error` — executes `$SHELL -lc <command>` (SHELL from the CURRENT process env, fallback `/bin/sh`), `cmd.Dir = dir`, `cmd.Env = env` (caller passes the complete curated slice), captures stdout+stderr separately. Non-zero exit → error whose message contains the first non-empty stderr line (fallback: first stdout line, fallback: exit status). nil on success.
- `proc.CommandEnv(cfg *config.Config, project string, taskID string, index int) []string` — the ONE place composing the spawn env: `envx.Curated(os.Environ(), envAllow, overlay)` with envAllow = cfg.EnvAllow ∪ project's EnvAllow, overlay = `wsp.ResolvedEnv(cfg, taskID, project, index)`. (This wires M0's deferred env_allow merge.)

**Tests:** `TestRunSuccess` (touch a file via the command, assert env visible: run `sh -c 'echo $DB_NAME > out'`-style probe with overlay), `TestRunFailureFirstStderrLine` (command printing two stderr lines then exit 1 → error contains line 1 not line 2), `TestRunEnvIsTotal` (secret var in parent process env NOT visible to child; allowlisted HOME visible; env_allow'd var visible), `TestCommandEnvMergesEnvAllow` (global + project env_allow both honored). Run with `-race`.

**Complexity:** medium-high — **Fable** (this is the tool's reason to exist; the env-totality test is the crown-jewel pin).

- [ ] Failing tests → implement → suite → commit `feat(proc): foreground spawn with curated environment`.

---

### Task 3: wsp — writers (WORKSPACE.md, CLAUDE.md-once, .env)

**Files:** Create `internal/wsp/write.go`, `internal/wsp/write_test.go`.

**Produces (binding):**
- `wsp.WriteWorkspaceMD(cfg, ws) error` — regenerates `<ws.Dir>/WORKSPACE.md` wholesale per the decided-behaviors table (sorted projects; only checked-out projects get sections; values from `alloc.ComputeValues` sorted). Exact layout is the implementer's (golden-tested); MUST contain: workspace name, task id, description, per-project `dir:`/`branch:` lines, project `instructions` verbatim, values as `NAME0=…` lines.
- `wsp.EnsureClaudeMD(ws) error` — creates `<ws.Dir>/CLAUDE.md` containing a one-line pointer to WORKSPACE.md **only if absent**; existing file untouched byte-for-byte (spec §5 — agent notes survive).
- `wsp.WriteEnvFile(cfg, ws, project string) error` — seed per the decided table: parse `<repo .env>` (`K=V` lines; skip blank/`#`; on duplicate key last wins), overlay ResolvedEnv, write sorted to `<projectDir>/.env` mode 0600. Missing source `.env` → just the overlay. Repo path from `cfg.Projects[project].Repo`.

**Tests:** golden file for WORKSPACE.md (fixture cfg + ws, `testdata/workspace_md.golden`); `TestEnsureClaudeMDPreservesExisting` (pre-write custom content + agent note; call; byte-identical); `TestWriteEnvFileSeeding` (source .env with `SECRET=abc`, `DB_NAME=old`, comment, blank; overlay DB_NAME new + PORT-bearing URL; result sorted, SECRET kept, DB_NAME overridden, 0600 perms); `TestWriteEnvFileNoSource`.

**Complexity:** medium — Opus.

- [ ] Failing tests → implement → suite → commit `feat(wsp): workspace writers — WORKSPACE.md, CLAUDE.md-once, .env seeding`.

---

### Task 4: alloc + wsp — locked allocation, dir naming, topo order

**Files:** Modify `internal/alloc/registry.go` (or new `internal/alloc/mutate.go`), `internal/wsp/wsp.go`; tests alongside.

**Produces (binding):**
- `alloc.Allocate(root, dir, taskID, desc string, now string) (Allocation, error)` — under `WithLock`: Load → reject if dir already allocated OR taskID already allocated (error names the existing dir) → NextIndex → Save. Returns the new Allocation. `now` is RFC 3339 passed by caller.
- `alloc.Release(root, dir string) error` — under WithLock; missing entry is a no-op (idempotent).
- `wsp.DirName(taskID, desc string) string` + `wsp.ValidTaskID(id string) bool` per decided table.
- `wsp.TopoOrder(cfg, names []string) ([]string, error)` — topological order of the given (checked-out) project names by `depends`, edges to names outside the set ignored; deterministic (ties sorted); cycle → error (config validation already rejects, defense only).
- Doc note on `alloc.Save` (M0 deferred): may error after a durable rename.

**Tests:** allocate/release round-trip incl. gap reuse; double-allocate rejections (both keys); concurrent `Allocate` from two goroutines with distinct dirs → distinct indices (flock + RMW proof, `-race`); DirName table (`("F-1","Color Buttons!")`→`F-1_color-buttons`, empty desc, symbol-only desc); ValidTaskID table (ok, leading dot, slash, >64); TopoOrder table (chain, tie sorting, out-of-set edge ignored).

**Complexity:** medium — Opus (contracts precise; flock pattern already established).

- [ ] Failing tests → implement → suite → commit `feat(alloc,wsp): locked allocation, dir naming, topo order`.

---

### Task 5: checkout command (the ensure-chain)

**Files:** Create `internal/cli/checkout.go`, `internal/wsp/ensure.go` (+tests), `internal/cli/testdata/checkout.txtar`.

**Produces (binding):**
- `wsp.EnsureProject(cfg, ws, project string) error` — the ensure-chain, each step idempotent: (1) worktree: dest `wsp.ProjectDir`; if not `gitx.IsWorkTree` → `gitx.WorktreeAdd(repo, dest, taskID, base_branch)`; (2) `.env` via `WriteEnvFile`; (3) setup: if stamp current (SetupHash match) → skip; else run each setup command (string substituted via RuntimeVars) through `proc.Run(dest, cmd, proc.CommandEnv(...))`; all succeed → write stamp (hash + "\n", 0644, mkdir `.workspace`).
- `workspace checkout <ws> <project…>` — resolve ws (3), validate projects configured (3), order the *requested* projects by `TopoOrder`, EnsureProject each; failures joined, continue remaining; refresh WORKSPACE.md at the end regardless; exit 1 on any failure. usageArgs ≥2.

**Tests:** txtar with a real source repo (with committed `.env`? no — `.env` is gitignored in real life; put the source `.env` untracked in the fixture repo dir): checkout creates worktree on branch `T-1`, `.env` has seeded+overlay values, stamp written (assert file exists + `workspace status` says `setup current`), WORKSPACE.md lists the project; re-run checkout → idempotent (second run exits 0; setup NOT re-run — pin via a setup command that appends to a log file, assert single line); config-change → stamp stale → re-checkout re-runs setup (append second line); failing setup (`false`) → exit 1, no stamp, error names project; unknown project → exit 3.

**Complexity:** high — **Fable** (orchestration seams; this is the heart of M2).

- [ ] Failing txtar+unit tests → implement → suite → commit `feat(cli,wsp): checkout — worktree, env, stamped setup ensure-chain`.

---

### Task 6: new command (transactional)

**Files:** Create `internal/cli/new.go` (+ helpers in wsp if needed), `internal/cli/testdata/new.txtar`.

**Produces (binding):**
- `workspace new <task_id> <desc> [project…]` — validate task id (2); compute dir `<root>/<DirName>`; conflict checks per decided table (1); then transaction: allocate → mkdir → WORKSPACE.md + CLAUDE.md → checkout listed projects (reuse the Task 5 chain). ANY failure → undo LIFO **what this invocation created**: remove created worktrees (`gitx.WorktreeRemove` force) — but NOT branches — remove created dir, release allocation; report original error + any undo errors joined; exit per original error kind.
- Reference shape for the transaction (the one subtle piece):

```go
type undoStack []func() error
func (u *undoStack) push(fn func() error) { *u = append(*u, fn) }
func (u undoStack) run() error { // LIFO, collect all errors
	var errs []error
	for i := len(u) - 1; i >= 0; i-- {
		if err := u[i](); err != nil { errs = append(errs, err) }
	}
	return errors.Join(errs...)
}
```

- Success output: workspace name + per-project one-liners + hint `workspace cd <task_id>`.

**Tests:** txtar happy path (dir, allocation visible in `ls`, CLAUDE.md exists, worktree branch); duplicate task id → exit 1 naming existing; invalid task id → exit 2; **transaction pin**: `new` with two projects where the second's setup is `false` → exit 1 AND registry has no allocation AND workspace dir gone AND first project's worktree gone from the source repo (`git -C repo worktree list` clean) AND branch `T-1` still exists (not deleted); re-run after fixing config → succeeds (convergence).

**Complexity:** high — **Fable**.

- [ ] Failing tests → implement → suite → commit `feat(cli): new — transactional workspace creation`.

---

### Task 7: destroy command + milestone smoke

**Files:** Create `internal/cli/destroy.go`, `internal/cli/testdata/destroy.txtar`.

**Produces (binding):**
- `workspace destroy <ws>` — resolve (3); teardown per checked-out project in REVERSE TopoOrder via proc.Run (commands substituted, curated env); any teardown failure → abort with joined errors, nothing removed (exit 1, re-run converges); all green → `gitx.WorktreeRemove` (force) each worktree, remove workspace dir, `alloc.Release`. Adopted allocation → teardown + release only, dir kept, say so. usageArgs exactly 1.
- After this task: run the milestone smoke — a throwaway root end-to-end WITH THE REAL BINARY: `new T-9 smoke app` → `ls`/`status`/`env` → `checkout` second project → `destroy T-9` → `ls` empty, source repo `git worktree list` clean, branch `T-9` alive. Script it in the txtar AND run once manually against a scratch root (NOT ~/claude-workspaces); record verbatim output in the report.

**Tests:** txtar covering teardown execution order (two projects with depends, teardown commands append to a shared log — assert reverse order), teardown failure aborts (dir still present, allocation still present), full destroy removes dir + releases + worktrees pruned, branch survives, unknown ws → 3.

**Complexity:** medium-high — **Fable**.

- [ ] Failing tests → implement → suite → smoke → commit `feat(cli): destroy — teardown, worktree removal, release`.

---

## Explicitly deferred (recorded)

- Bulk git-derivation seam (ls -g / status perf) — M3, where proc work touches the same surfaces.
- `setup` output streaming/progress UX — M3 polish; M2 captures output and reports on failure only.
- Confirmation prompt on `destroy` — v1 had prompts on prune; spec is silent; ship without, revisit after daily-driving feedback.
- M1 rides not listed here remain in `2026-08-03-m1-deferred-items.md`.

## Self-review notes

- Spec coverage (M2 slice): §2 new/checkout/destroy rows incl. transactional note; §3 stamps written per rule; §4 .env seeding + runtime substitution in setup/teardown; §5 WORKSPACE.md/CLAUDE.md; §6 curated env at every spawn (proc.Run sole spawn path) + env_allow wiring; §7 spawn contract, topo order, reverse teardown.
- Type consistency: proc.Run/CommandEnv signatures used in Tasks 5/6/7; EnsureProject in 5/6; TopoOrder in 5/7; WorktreeAdd/Remove in 1/5/6/7.
- Deliberate M2 simplifications: no daemons anywhere (M3); `destroy` doesn't stop processes (none exist yet); `checkout` refreshes WORKSPACE.md wholesale (regeneration-safe by §5 design).
