package wsp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/envx"
	"github.com/Phaengris/claude-workspaces/internal/gitx"
	"github.com/Phaengris/claude-workspaces/internal/proc"
)

// CommandEnv composes the complete curated environment for one spawned
// command: envx.Curated over os.Environ() with the allowlist extended by
// cfg.EnvAllow ∪ the named project's EnvAllow, and ResolvedEnv as the winning
// overlay. An empty or unknown project contributes no extra allowances — the
// global env_allow stands alone.
//
// This is the ONE place spawn environments are composed. It lives here rather
// than in proc because it is built entirely from workspace knowledge
// (ResolvedEnv, the allocation's task id and index) — in proc it forced a
// proc→wsp import that made the ensure-chain (wsp→proc) a cycle. proc stays
// the pure spawn mechanism; wsp owns what the spawned command sees.
func CommandEnv(cfg *config.Config, project, taskID string, index int) []string {
	allow := append([]string(nil), cfg.EnvAllow...)
	if p := cfg.Projects[project]; p != nil {
		allow = append(allow, p.EnvAllow...)
	}
	return envx.Curated(os.Environ(), allow, ResolvedEnv(cfg, taskID, project, index))
}

// Step is the ensure chain's progress reporter: called with a human label
// right before a slow operation runs, it returns the done to call when the
// operation ends (err reports how). A nil Step is silence — every caller
// that has nothing to say passes nil, and EnsureProject never notices.
// Labels name only operations that ACTUALLY run: an already-checked-out
// project or a current setup stamp reports nothing, so idempotent re-runs
// stay as quiet as they always were (spec 2026-08-12 rows 1-2, Mechanics).
type Step func(label string) (done func(err error))

// begin starts a reported step, returning a done that is safe to call on
// every path. It is the nil-Step adapter: with no reporter both halves are
// no-ops.
func begin(step Step, label string) func(error) {
	if step == nil {
		return func(error) {}
	}
	return step(label)
}

// EnsureProject makes one configured project real inside the workspace — the
// checkout ensure-chain. Each step is idempotent, so re-running after any
// failure converges. step reports slow operations as they actually run (a
// human label right before, done(err) right after); nil is silence, and a
// step that is SKIPPED (already checked out, setup stamp current) reports
// nothing at all.
//
//  1. worktree: if ProjectDir is not already the ROOT of a work tree, check
//     out a linked worktree on branch <task id> from the project's base_branch
//     (the gate also covers Task 1's TOCTOU note — a branch checked out
//     elsewhere surfaces as WorktreeAdd's own error, never a half-state). The
//     gate is gitx.IsWorkTreeRoot, not IsWorkTree: "inside a work tree" walks
//     up, so with the workspaces area nested in any enclosing repo a plain
//     directory at dest answered yes, the worktree step was skipped, and steps
//     2-3 wrote .env and ran setup inside the enclosing repo, stamped as done.
//     Asking the narrower question means a stray directory at dest now reaches
//     WorktreeAdd, which either adopts it (git accepts an EMPTY dir) or fails
//     loudly — an honest error the user can act on, never silent wrongness;
//  2. .env: rewritten every time (WriteEnvFile needs the worktree to exist,
//     hence the order);
//  3. setup: skipped when the stamp records the current SetupHash; otherwise
//     every setup command runs via proc.Run — command STRING substituted with
//     RuntimeVars, process env from CommandEnv — and only after ALL succeed is
//     the stamp written. A failed setup leaves no stamp, so the re-run runs
//     setup again (setup commands are expected to be idempotent, spec §3).
//
// Every error is prefixed with the project name: the caller ensures several
// projects and joins the failures, so each line must say whose it is.
// WORKSPACE.md is deliberately NOT refreshed here — the caller does that once,
// after all projects, success or not.
func EnsureProject(cfg *config.Config, ws Workspace, project string, step Step) error {
	fail := func(err error) error { return fmt.Errorf("project %q: %w", project, err) }
	p := cfg.Projects[project]
	if p == nil {
		// The CLI validates names up front; this is defence in depth.
		return fail(fmt.Errorf("not configured"))
	}

	dest := ProjectDir(ws, cfg, project)
	if !gitx.IsWorkTreeRoot(dest) {
		// The branch is named after the FULL workspace name, not the bare
		// task id: `PATED_patternima-editor-fixes` says in `git branch` and
		// in a PR list what `PATED` alone cannot. Consumers never re-derive
		// this — status/WORKSPACE.md/gc all read the worktree's actual
		// branch — so workspaces created when the branch was the task id
		// keep working untouched.
		done := begin(step, fmt.Sprintf("checking out (branch %s)", ws.Name()))
		if err := gitx.WorktreeAdd(p.Repo, dest, ws.Name(), p.BaseBranch); err != nil {
			done(err)
			return fail(err)
		}
		done(nil)
	}

	if err := WriteEnvFile(cfg, ws, project); err != nil {
		return fail(err)
	}

	if setupCurrent(cfg, ws, project) {
		return nil
	}
	vars := RuntimeVars(cfg, ws.Alloc.TaskID, project, ws.Alloc.Index)
	env := CommandEnv(cfg, project, ws.Alloc.TaskID, ws.Alloc.Index)
	for _, cmd := range p.Setup {
		sub := Subst(cmd, vars)
		done := begin(step, "setup: "+sub)
		if err := proc.Run(dest, sub, env); err != nil {
			done(err)
			return fail(err) // proc.Run's message reads "command failed: <reason>"
		}
		done(nil)
	}
	if err := os.MkdirAll(filepath.Join(ws.Dir, stampDirName), 0o755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(stampPath(ws, project), []byte(SetupHash(cfg, ws, project)+"\n"), 0o644); err != nil {
		return fail(err)
	}
	return nil
}
